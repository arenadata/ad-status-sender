package runner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
	"github.com/arenadata/ad-status-sender/internal/check"
	"github.com/arenadata/ad-status-sender/internal/config"
	"github.com/arenadata/ad-status-sender/internal/rules"
	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
	"github.com/fsnotify/fsnotify"
)

const (
	jobQueueSize         = 2048
	httpMaxIdle          = 100
	httpMaxIdlePerHost   = 100
	httpIdleTimeout      = 90 * time.Second
	shutdownDrainTimeout = 15 * time.Second

	defaultInterval     = 5 * time.Second
	defaultHTTPTimeout  = 5 * time.Second
	defaultForceSend    = 120 * time.Second
	defaultRulesRefresh = 60 * time.Second
	legacyDebounceDelay = 150 * time.Millisecond

	// rulesRefreshJitterFrac spreads rule-sync ticks by up to interval/frac so
	// daemons started together don't hit ADCM in a synchronized burst.
	rulesRefreshJitterFrac = 4

	rulesSourceYAML   = "yaml"
	rulesSourceLegacy = "legacy"
	rulesSourceADCM   = "adcm"
)

type Runner struct {
	cfgPath string
	log     *slog.Logger

	mu     sync.RWMutex
	cfg    config.Config
	client *http.Client
	db     *sqlite.Store
	dbPath string
	adcm   *adcmclient.Client

	ruleStore rules.Store
	stopWatch chan struct{}

	tickerMu    sync.Mutex
	ticker      Ticker
	tickerReset chan struct{}
	jobs        chan func()
	scanCancel  context.CancelFunc
	postCancel  context.CancelFunc
	workerWg    sync.WaitGroup
	rulesSyncMu sync.Mutex

	sd   check.Systemd
	dck  check.Docker
	post Poster
	clk  Clock

	cacheMu    sync.Mutex
	cache      map[string]lastSend // key -> last
	forceAfter time.Duration

	postKeys keyedMutex
}

type lastSend struct {
	status   int
	lastTime time.Time
}

// keyedMutex hands out a mutex per string key so posts for the same key
// serialize while different keys stay concurrent.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[string]*sync.Mutex)
	}
	km, ok := k.m[key]
	if !ok {
		km = &sync.Mutex{}
		k.m[key] = km
	}
	k.mu.Unlock()
	km.Lock()
	return km.Unlock
}

func NewWithLogger(cfgPath string, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return NewWithDeps(cfgPath, logger, nil, nil, nil, nil)
}

func New(cfgPath string) *Runner { return NewWithLogger(cfgPath, slog.Default()) }

func NewWithDeps(
	cfgPath string,
	logger *slog.Logger,
	sd check.Systemd,
	dck check.Docker,
	post Poster,
	clk Clock,
) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = realClock{}
	}
	return &Runner{
		cfgPath: cfgPath,
		log:     logger,
		sd:      sd,
		dck:     dck,
		post:    post,
		clk:     clk,
	}
}

func (r *Runner) Start() error {
	if err := r.reload(); err != nil {
		return err
	}
	if err := r.syncRules(context.Background()); err != nil {
		r.log.Warn("rules initial load", "err", err)
	}

	// scanCtx stops scheduling on shutdown; postCtx keeps in-flight checks and
	// posts alive until they drain, so SIGTERM does not abort a status POST.
	scanCtx, scanCancel := context.WithCancel(context.Background())
	postCtx, postCancel := context.WithCancel(context.Background())
	r.scanCancel = scanCancel
	r.postCancel = postCancel
	r.initRuntime()

	r.startWorkers()
	r.startTickerLoop(scanCtx, postCtx)
	r.startRulesWatcher()
	r.startRulesSyncer(scanCtx)
	r.startLegacyWatcher(scanCtx)
	r.startSignalHandler()

	return nil
}

func (r *Runner) Stop() {
	if r.scanCancel != nil {
		r.scanCancel() // stop scheduling; loop closes r.jobs after the current scan
	}
	r.closeStopWatch()
	// Drain in-flight and queued posts before cancelling their context.
	done := make(chan struct{})
	go func() {
		r.workerWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownDrainTimeout):
		r.log.Warn("shutdown drain timed out; cancelling in-flight posts")
	}
	if r.postCancel != nil {
		r.postCancel()
	}
}

func (r *Runner) initRuntime() {
	r.jobs = make(chan func(), jobQueueSize)
	r.cache = make(map[string]lastSend)
	r.tickerReset = make(chan struct{}, 1)
}

func (r *Runner) startWorkers() {
	n := r.cfg.Concurrency
	for range n {
		r.workerWg.Add(1)
		go func() {
			defer r.workerWg.Done()
			for fn := range r.jobs { // exits once loop closes r.jobs and the queue drains
				fn()
			}
		}()
	}
}

func (r *Runner) startTickerLoop(scanCtx, postCtx context.Context) {
	r.resetTicker(config.MustDuration(r.cfg.Interval, defaultInterval))
	go r.loop(scanCtx, postCtx)
}

func (r *Runner) startRulesWatcher() {
	r.stopWatch = make(chan struct{})
	go func() {
		r.mu.RLock()
		src := r.cfg.RulesSource
		path := r.cfg.RulesPath
		r.mu.RUnlock()
		if src != rulesSourceYAML {
			return
		}
		err := rules.Watch(r.stopWatch, path, func(rr rules.Rules) {
			r.mu.RLock()
			cur := r.cfg.RulesSource
			r.mu.RUnlock()
			if cur != rulesSourceYAML {
				return // source switched via reload; ignore stale yaml watcher
			}
			if syncErr := r.syncRulesWithImporter(context.Background(), yamlRulesImporter{rr: rr}); syncErr != nil {
				r.log.Error("rules import", "err", syncErr)
				return
			}
			r.log.Info("rules reloaded", "systemd", len(rr.Systemd), "docker", len(rr.Docker))
		}, func(werr error) {
			r.log.Error("rules watch", "err", werr)
		})
		if err != nil {
			r.log.Error("rules watch", "err", err)
		}
	}()
}

// jitteredInterval adds up to interval/rulesRefreshJitterFrac random delay to decorrelate ticks.
func jitteredInterval(interval time.Duration) time.Duration {
	span := int64(interval) / rulesRefreshJitterFrac
	if span <= 0 {
		return interval
	}
	return interval + time.Duration(rand.Int64N(span)) //nolint:gosec // G404: jitter, not security-sensitive
}

func (r *Runner) startRulesSyncer(ctx context.Context) {
	go func() {
		for {
			r.mu.RLock()
			interval := config.MustDuration(r.cfg.RulesRefresh, defaultRulesRefresh)
			r.mu.RUnlock()

			timer := time.NewTimer(jitteredInterval(interval))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			timer.Stop()

			// Periodic re-sync for all sources: the primary trigger for yaml/legacy
			// is fsnotify, but a lost watch (dir replaced, inotify limit) would
			// otherwise leave rules stale forever. syncRules re-reads the source.
			if err := r.syncRules(ctx); err != nil {
				r.log.ErrorContext(ctx, "rules sync", "err", err)
			}
		}
	}()
}

func (r *Runner) startLegacyWatcher(ctx context.Context) {
	go func() {
		if err := r.runLegacyWatcher(ctx); err != nil {
			r.log.ErrorContext(ctx, "legacy watch", "err", err)
		}
	}()
}

type legacyPaths struct {
	root     string
	services string
	docker   string
	hosts    string
}

func (r *Runner) legacyConfig() (string, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.RulesSource, r.cfg.LegacyDir
}

func (r *Runner) runLegacyWatcher(ctx context.Context) error {
	src, legacyDir := r.legacyConfig()
	if src != rulesSourceLegacy {
		return nil
	}
	if strings.TrimSpace(legacyDir) == "" {
		return errors.New("legacy_dir is empty")
	}
	w, paths, err := r.newLegacyWatcher(legacyDir)
	if err != nil {
		return err
	}
	defer w.Close()
	r.legacyWatchLoop(ctx, w, paths)
	return nil
}

func (r *Runner) newLegacyWatcher(legacyDir string) (*fsnotify.Watcher, legacyPaths, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, legacyPaths{}, err
	}
	paths := legacyPaths{
		root:     legacyDir,
		services: filepath.Join(legacyDir, "services"),
		docker:   filepath.Join(legacyDir, "docker"),
		hosts:    filepath.Join(legacyDir, "hosts"),
	}
	r.addLegacyWatch(w, paths.root)
	r.addLegacyWatch(w, paths.services)
	r.addLegacyWatch(w, paths.docker)
	r.addLegacyWatch(w, paths.hosts)
	return w, paths, nil
}

func (r *Runner) addLegacyWatch(w *fsnotify.Watcher, path string) {
	if st, stErr := os.Stat(path); stErr == nil && st.IsDir() {
		if addErr := w.Add(path); addErr != nil {
			r.log.Warn("legacy watch add failed", "path", path, "err", addErr)
		}
	}
}

func (r *Runner) legacyWatchLoop(ctx context.Context, w *fsnotify.Watcher, paths legacyPaths) {
	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	fire := func() { debounce.Reset(legacyDebounceDelay) }

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-w.Events:
			if r.legacyEventTriggersSync(w, paths, ev) {
				fire()
			}
		case <-debounce.C:
			if syncErr := r.syncRules(context.Background()); syncErr != nil {
				r.log.ErrorContext(ctx, "legacy rules sync", "err", syncErr)
			}
		case werr := <-w.Errors:
			r.log.WarnContext(ctx, "legacy watch error", "err", werr)
		}
	}
}

func (r *Runner) legacyEventTriggersSync(w *fsnotify.Watcher, paths legacyPaths, ev fsnotify.Event) bool {
	if ev.Has(fsnotify.Create) {
		if st, stErr := os.Stat(ev.Name); stErr == nil && st.IsDir() {
			_ = w.Add(ev.Name)
		}
	}
	if !isLegacyWriteEvent(ev) {
		return false
	}
	return isUnder(paths.services, ev.Name) || isUnder(paths.docker, ev.Name) || isUnder(paths.hosts, ev.Name)
}

func isLegacyWriteEvent(ev fsnotify.Event) bool {
	return ev.Has(fsnotify.Write) || ev.Has(fsnotify.Create) ||
		ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
}

func (r *Runner) startSignalHandler() {
	go func() {
		const sigBuf = 2
		sigCh := make(chan os.Signal, sigBuf)
		signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
		for s := range sigCh {
			switch s {
			case syscall.SIGHUP:
				if err := r.reload(); err != nil {
					r.log.Error("reload config", "err", err)
				}
				if err := r.syncRules(context.Background()); err != nil {
					r.log.Error("reload rules", "err", err)
				}
			default:
				r.Stop()
				return
			}
		}
	}()
}

func (r *Runner) closeStopWatch() {
	r.mu.Lock()
	ch := r.stopWatch
	r.stopWatch = nil
	r.mu.Unlock()
	if ch == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	close(ch)
}

type authConfig struct {
	cfg     config.Config
	token   string
	user    string
	pass    string
	credErr error
}

func (r *Runner) reload() error {
	auth, err := loadAuthConfig(r.cfgPath)
	if err != nil {
		return err
	}

	sd, dck := r.ensureCheckers()

	httpc := makeHTTPClient(auth.cfg, r.log)
	tok, err := r.ensureToken(auth, httpc)
	if err != nil {
		return err
	}
	adcm := r.buildADCMClient(auth, tok, httpc)
	adcm.SetLogBodies(auth.cfg.LogBodies)
	poster := &adcmPoster{
		log:       r.log,
		client:    adcm,
		hostID:    auth.cfg.HostID,
		logBodies: auth.cfg.LogBodies,
	}

	// Hold rulesSyncMu so a db swap cannot close a store an in-flight sync is using.
	r.rulesSyncMu.Lock()
	defer r.rulesSyncMu.Unlock()

	db, dsn, err := r.reopenDB(auth.cfg.RulesDB)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.cfg = auth.cfg
	r.client = httpc
	r.adcm = adcm
	r.post = poster
	r.sd = sd
	r.dck = dck
	r.db = db
	r.dbPath = dsn
	r.forceAfter = config.MustDuration(auth.cfg.ForceSendAfter, defaultForceSend)
	r.mu.Unlock()

	r.resetTicker(config.MustDuration(auth.cfg.Interval, defaultInterval))
	return nil
}

func loadAuthConfig(path string) (authConfig, error) {
	cfg, loadErr := config.Load(path)
	if loadErr != nil {
		return authConfig{}, loadErr
	}
	if cfg.HostID == 0 && cfg.RulesSource == rulesSourceLegacy {
		hostIDs, err := legacyHostIDs(filepath.Join(cfg.LegacyDir, "hosts"))
		if err != nil {
			return authConfig{}, err
		}
		switch len(hostIDs) {
		case 0:
			return authConfig{}, errors.New("host_id is required or legacy/hosts is empty")
		case 1:
			cfg.HostID = hostIDs[0]
		default:
			return authConfig{}, errors.New("multiple host_id entries in legacy/hosts")
		}
	}
	tok, tokenErr := config.LoadToken(&cfg)
	user, pass, credErr := config.LoadUserPass(&cfg)
	if tokenErr != nil && credErr != nil {
		return authConfig{}, tokenErr
	}
	return authConfig{
		cfg:     cfg,
		token:   tok,
		user:    user,
		pass:    pass,
		credErr: credErr,
	}, nil
}

// ensureCheckers returns the systemd/docker checkers, reusing existing ones and
// creating any that are still nil (e.g. dbus/docker not ready at boot). Callers
// publish the result under r.mu so a nil checker self-heals on a later scan
// instead of latching every component DOWN forever.
func (r *Runner) ensureCheckers() (check.Systemd, check.Docker) {
	r.mu.RLock()
	sd, dck := r.sd, r.dck
	r.mu.RUnlock()
	if dck == nil {
		if d, err := check.NewDockerChecker(); err == nil {
			dck = d
		} else {
			r.log.Warn("docker init failed", "err", err)
		}
	}
	if sd == nil {
		if cli, err := check.NewSystemdClient(context.Background()); err == nil {
			sd = cli
		} else {
			r.log.Warn("systemd dbus init failed", "err", err)
		}
	}
	return sd, dck
}

// refreshCheckers self-heals nil checkers and publishes them for workers.
func (r *Runner) refreshCheckers() (check.Systemd, check.Docker) {
	sd, dck := r.ensureCheckers()
	r.mu.Lock()
	r.sd, r.dck = sd, dck
	r.mu.Unlock()
	return sd, dck
}

func (r *Runner) ensureToken(auth authConfig, httpc *http.Client) (string, error) {
	tok := strings.TrimSpace(auth.token)
	if tok != "" {
		return tok, nil
	}
	if auth.credErr != nil {
		return "", auth.credErr
	}
	tmp := adcmclient.New(auth.cfg.ADCMURL, "", httpc, r.log)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		config.MustDuration(auth.cfg.HTTPTimeout, defaultHTTPTimeout),
	)
	defer cancel()
	return tmp.ObtainToken(ctx, auth.user, auth.pass)
}

func (r *Runner) buildADCMClient(auth authConfig, token string, httpc *http.Client) *adcmclient.Client {
	var client *adcmclient.Client
	var tokenMu sync.Mutex
	lastToken := strings.TrimSpace(token)
	tokenProvider := func(ctx context.Context) (string, error) {
		tokenMu.Lock()
		defer tokenMu.Unlock()
		tok2, _ := config.LoadToken(&auth.cfg)
		tok2 = strings.TrimSpace(tok2)
		if tok2 != "" && tok2 != lastToken {
			lastToken = tok2
			return tok2, nil
		}
		if auth.credErr != nil {
			if tok2 != "" {
				lastToken = tok2
				return tok2, nil
			}
			return "", auth.credErr
		}
		if client == nil {
			return "", errors.New("adcm client not initialized")
		}
		newTok, err := client.ObtainToken(ctx, auth.user, auth.pass)
		if err == nil {
			lastToken = strings.TrimSpace(newTok)
			return lastToken, nil
		}
		if tok2 != "" {
			lastToken = tok2
			return tok2, nil
		}
		return "", err
	}
	client = adcmclient.NewWithTokenProvider(auth.cfg.ADCMURL, token, tokenProvider, httpc, r.log)
	// adcm mode needs both rbac (rule import) and the status secret (POSTs); fetch
	// the latter from the status-checker-token endpoint. Other sources deploy the
	// secret as token, so status POSTs fall back to it with no provider.
	if auth.cfg.RulesSource == rulesSourceADCM {
		client.SetStatusTokenProvider(client.ObtainStatusToken)
	}
	return client
}

func makeHTTPClient(c config.Config, log *slog.Logger) *http.Client {
	tr := buildTransport(c, log)
	httpTimeout := config.MustDuration(c.HTTPTimeout, defaultHTTPTimeout)
	return &http.Client{Timeout: httpTimeout, Transport: tr}
}

func buildTransport(c config.Config, log *slog.Logger) *http.Transport {
	tr := &http.Transport{
		MaxIdleConns:        httpMaxIdle,
		MaxIdleConnsPerHost: httpMaxIdlePerHost,
		IdleConnTimeout:     httpIdleTimeout,
	}
	if !strings.HasPrefix(strings.ToLower(c.ADCMURL), "https://") {
		return tr
	}
	tr.TLSClientConfig = buildTLSConfig(c, log)
	return tr
}

func buildTLSConfig(c config.Config, log *slog.Logger) *tls.Config {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	roots, sysErr := x509.SystemCertPool()
	if sysErr != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if strings.TrimSpace(c.TLS.CAFile) != "" {
		if pem, rdErr := os.ReadFile(c.TLS.CAFile); rdErr != nil {
			log.Error("tls ca_file read failed", "file", c.TLS.CAFile, "err", rdErr)
		} else if !roots.AppendCertsFromPEM(pem) {
			log.Error("tls ca_file has no valid certificates", "file", c.TLS.CAFile)
		}
	}
	tlsConf.RootCAs = roots

	if c.TLS.CertFile != "" && c.TLS.KeyFile != "" {
		if cert, ckErr := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile); ckErr != nil {
			log.Error("tls client cert load failed", "cert", c.TLS.CertFile, "key", c.TLS.KeyFile, "err", ckErr)
		} else {
			tlsConf.Certificates = []tls.Certificate{cert}
		}
	}
	if c.TLS.ServerName != "" {
		tlsConf.ServerName = c.TLS.ServerName
	}
	if c.TLS.InsecureSkipVerify {
		tlsConf.InsecureSkipVerify = true
		log.Warn("tls insecure_skip_verify enabled: server certificate is not verified")
	}
	return tlsConf
}

// reopenDB opens the store for path, reusing the current one when the DSN is
// unchanged. The caller holds rulesSyncMu so closing the old store cannot race
// an in-flight rule sync.
func (r *Runner) reopenDB(path string) (*sqlite.Store, string, error) {
	dsn := rulesDBDSN(path)
	r.mu.RLock()
	cur, curPath := r.db, r.dbPath
	r.mu.RUnlock()
	if cur != nil && curPath == dsn {
		return cur, dsn, nil
	}
	if dir := filepath.Dir(strings.TrimPrefix(path, "file:")); dir != "" && dir != "." {
		if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
			return nil, "", fmt.Errorf("create rules_db dir %q: %w", dir, mkErr)
		}
	}
	store, err := sqlite.Open(dsn)
	if err != nil {
		return nil, "", err
	}
	if cur != nil {
		_ = cur.Close()
	}
	return store, dsn, nil
}

func rulesDBDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	return "file:" + filepath.Clean(path)
}

func (r *Runner) syncRules(ctx context.Context) error {
	imp, err := r.selectImporter()
	if err != nil {
		return err
	}
	return r.syncRulesWithImporter(ctx, imp)
}

func (r *Runner) syncRulesWithImporter(ctx context.Context, imp rulesImporter) error {
	r.rulesSyncMu.Lock()
	defer r.rulesSyncMu.Unlock()
	r.mu.RLock()
	db := r.db
	r.mu.RUnlock()
	if db == nil {
		return errors.New("rules db not initialized")
	}
	if imp == nil {
		return errors.New("rules importer is nil")
	}
	if updErr := db.UpdateRules(ctx, func(tx *sql.Tx) error {
		if clearErr := sqlite.ClearRulesTx(ctx, tx); clearErr != nil {
			return clearErr
		}
		return imp.Import(ctx, tx)
	}); updErr != nil {
		return updErr
	}
	return r.loadRulesOnce(ctx)
}

func (r *Runner) selectImporter() (rulesImporter, error) {
	r.mu.RLock()
	cfg, adcm := r.cfg, r.adcm
	r.mu.RUnlock()
	switch cfg.RulesSource {
	case rulesSourceYAML:
		return yamlFileImporter{path: cfg.RulesPath}, nil
	case rulesSourceLegacy:
		return legacyImporter{legacyDir: cfg.LegacyDir, hostID: cfg.HostID}, nil
	case rulesSourceADCM:
		return adcmImporter{client: adcm, hostID: cfg.HostID}, nil
	default:
		return nil, fmt.Errorf("unsupported rules_source %q", cfg.RulesSource)
	}
}

func (r *Runner) loadRulesOnce(ctx context.Context) error {
	r.mu.RLock()
	db, hostID := r.db, r.cfg.HostID
	r.mu.RUnlock()
	if db == nil {
		return errors.New("rules db not initialized")
	}
	rr, err := db.LoadRulesForHost(ctx, hostID)
	if err != nil {
		return err
	}
	r.ruleStore.Set(rr)
	return nil
}

func (r *Runner) resetTicker(d time.Duration) {
	r.tickerMu.Lock()
	defer r.tickerMu.Unlock()
	if r.ticker != nil {
		r.ticker.Stop()
	}
	if r.clk == nil {
		r.clk = realClock{}
	}
	r.ticker = r.clk.NewTicker(d)
	// Wake loop() so it re-reads the new ticker channel; the old one never fires again.
	select {
	case r.tickerReset <- struct{}{}:
	default:
	}
}

func (r *Runner) loop(scanCtx, postCtx context.Context) {
	r.scanOnce(postCtx)
	for {
		r.tickerMu.Lock()
		c := r.ticker.C()
		r.tickerMu.Unlock()
		select {
		case <-scanCtx.Done():
			close(r.jobs)
			return
		case <-c:
			r.scanOnce(postCtx)
		case <-r.tickerReset:
		}
	}
}

func (r *Runner) scanOnce(ctx context.Context) {
	cfg, force := r.snapshot()
	sd, dck := r.refreshCheckers()
	// One job per component, aggregating every rule that references it, so a
	// component listed under both a systemd and a docker rule posts a single
	// status per scan instead of two rules fighting (0/1 flap).
	for id, chk := range r.buildScanPlan(ctx, sd) {
		key, checks := id, chk
		r.enqueue(func() {
			r.evalAndPostComponent(ctx, cfg, key, checks, sd, dck, force)
		})
	}
	r.sendHeartbeat(ctx, cfg, force)
}

func (r *Runner) snapshot() (config.Config, time.Duration) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg, r.forceAfter
}

// compKey identifies a status target: a component on a specific host. HostID 0
// means the configured (original) host; a non-zero id is a shared-host duplicate.
type compKey struct {
	hostID int
	compID string
}

// compChecks is the set of checks a single component depends on, gathered
// across all rules that reference it.
type compChecks struct {
	units      []string
	dockerSels []rules.DockerSelector
}

// buildScanPlan maps each (host, component) target to every check that
// determines its status. Systemd globs are expanded once here so the per-target
// job can aggregate.
func (r *Runner) buildScanPlan(ctx context.Context, sd check.Systemd) map[compKey]*compChecks {
	rr := r.ruleStore.Get()
	plan := make(map[compKey]*compChecks)
	get := func(k compKey) *compChecks {
		cc, ok := plan[k]
		if !ok {
			cc = &compChecks{}
			plan[k] = cc
		}
		return cc
	}
	for _, rule := range rr.Systemd {
		var units []string
		if rule.Unit != "" {
			units = append(units, rule.Unit)
		}
		if rule.UnitGlob != "" && sd != nil {
			units = append(units, sd.ExpandUnitsByGlob(ctx, rule.UnitGlob)...)
		}
		for _, t := range rule.Targets() {
			cc := get(compKey{t.HostID, t.ComponentID})
			cc.units = append(cc.units, units...)
		}
	}
	for _, d := range rr.Docker {
		for _, t := range d.Targets() {
			cc := get(compKey{t.HostID, t.ComponentID})
			cc.dockerSels = append(cc.dockerSels, d.Containers)
		}
	}
	return plan
}

// evalAndPostComponent runs all of a component's checks and posts one aggregated
// status: down if any determinable check is down, up if all are up. If no check
// could be evaluated (checker unavailable / infra error), nothing is posted so a
// transient outage does not flap the component DOWN.
func (r *Runner) evalAndPostComponent(
	ctx context.Context,
	cfg config.Config,
	key compKey,
	chk *compChecks,
	sd check.Systemd,
	dck check.Docker,
	forceAfter time.Duration,
) {
	status := 0
	determined := false
	if sd != nil {
		for _, unit := range chk.units {
			st := sd.SystemdStatus(ctx, unit)
			if st == check.UnexpectedExitCode {
				continue // infra error (dbus timeout/broken), not a real DOWN
			}
			determined = true
			if st != 0 {
				status = 1
			}
		}
	}
	if dck != nil {
		for _, sel := range chk.dockerSels {
			determined = true
			if dockerStatus(ctx, dck, sel) != 0 {
				status = 1
			}
		}
	}
	if !determined {
		return
	}
	r.maybePostComponent(ctx, cfg, key, status, forceAfter)
}

func dockerStatus(ctx context.Context, dck check.Docker, sel rules.DockerSelector) int {
	if len(sel.Names) > 0 {
		return dck.AllRunningNames(ctx, sel.Names)
	}
	return dck.AllRunningByLabels(ctx, sel.Labels)
}

func (r *Runner) sendHeartbeat(ctx context.Context, cfg config.Config, forceAfter time.Duration) {
	r.enqueue(func() {
		const ok = 0
		r.maybePostHost(ctx, cfg, ok, forceAfter)
	})
}

func (r *Runner) enqueue(fn func()) {
	select {
	case r.jobs <- fn:
	default:
		go fn()
	}
}

func (r *Runner) maybePostComponent(
	ctx context.Context,
	cfg config.Config,
	target compKey,
	status int,
	forceAfter time.Duration,
) {
	hostID := target.hostID
	if hostID == 0 {
		hostID = cfg.HostID
	}
	key := fmt.Sprintf("comp:%d:%s", hostID, target.compID)
	// Serialize per key so concurrent scans of a flapping unit cannot post
	// out of order or race shouldSend/markSent.
	unlock := r.postKeys.lock(key)
	defer unlock()
	if !r.shouldSend(key, status, forceAfter) {
		return
	}
	if post := r.poster(); post != nil {
		if err := post.PostComponent(ctx, hostID, target.compID, status); err != nil {
			r.log.WarnContext(ctx, "post component failed", "host", hostID, "comp", target.compID, "err", err)
			return
		}
	}
	r.markSent(key, status)
}

func (r *Runner) maybePostHost(ctx context.Context, cfg config.Config, status int, forceAfter time.Duration) {
	key := fmt.Sprintf("host:%d", cfg.HostID)
	unlock := r.postKeys.lock(key)
	defer unlock()
	if !r.shouldSend(key, status, forceAfter) {
		return
	}
	if post := r.poster(); post != nil {
		if err := post.PostHost(ctx, status); err != nil {
			r.log.WarnContext(ctx, "post host failed", "host", cfg.HostID, "err", err)
			return
		}
	}
	r.markSent(key, status)
}

func (r *Runner) poster() Poster {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.post
}

func (r *Runner) shouldSend(key string, status int, forceAfter time.Duration) bool {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	prev, ok := r.cache[key]
	now := r.clk.Now()
	if !ok {
		return true
	}
	if prev.status != status {
		return true
	}
	return now.Sub(prev.lastTime) >= forceAfter
}

func (r *Runner) markSent(key string, status int) {
	r.cacheMu.Lock()
	r.cache[key] = lastSend{status: status, lastTime: r.clk.Now()}
	r.cacheMu.Unlock()
}

func legacyHostIDs(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []int
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		id, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			return nil, fmt.Errorf("invalid host id %q in %s", e.Name(), dir)
		}
		out = append(out, id)
	}
	return out, nil
}

func isUnder(parent, path string) bool {
	if parent == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..")
}
