package runner

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/arenadata/ad-status-sender/internal/check"
	"github.com/arenadata/ad-status-sender/internal/config"
	"github.com/arenadata/ad-status-sender/internal/rules"
)

const (
	jobQueueSize       = 2048
	httpMaxIdle        = 100
	httpMaxIdlePerHost = 100
	httpIdleTimeout    = 90 * time.Second

	defaultInterval    = 5 * time.Second
	defaultHTTPTimeout = 5 * time.Second
	defaultForceSend   = 120 * time.Second
)

type httpPoster struct {
	log       *slog.Logger
	c         *http.Client
	adcmURL   string
	hostID    int
	token     string
	logBodies bool
}

func (p *httpPoster) PostHost(ctx context.Context, status int) error {
	url := fmt.Sprintf("%s/status/api/v1/host/%d/", strings.TrimRight(p.adcmURL, "/"), p.hostID)
	body, _ := json.Marshal(map[string]int{"status": status})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Token "+p.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if p.logBodies || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(resp.Body)
		p.log.InfoContext(
			ctx,
			"host post",
			"url", url,
			"code", resp.StatusCode,
			"body", strings.TrimSpace(string(data)),
		)
	}
	return nil
}

func (p *httpPoster) PostComponent(ctx context.Context, compID string, status int) error {
	url := fmt.Sprintf(
		"%s/status/api/v1/host/%d/component/%s/",
		strings.TrimRight(p.adcmURL, "/"),
		p.hostID,
		compID,
	)
	body, _ := json.Marshal(map[string]int{"status": status})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Token "+p.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if p.logBodies || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(resp.Body)
		p.log.InfoContext(
			ctx,
			"status post",
			"url", url,
			"code", resp.StatusCode,
			"body", strings.TrimSpace(string(data)),
		)
	}
	return nil
}

type Runner struct {
	cfgPath string
	log     *slog.Logger

	mu     sync.RWMutex
	cfg    config.Config
	token  string
	client *http.Client

	ruleStore rules.Store
	stopWatch chan struct{}

	tickerMu sync.Mutex
	ticker   Ticker
	jobs     chan func()
	cancel   context.CancelFunc

	sd   check.Systemd
	dck  check.Docker
	post Poster
	clk  Clock

	cacheMu    sync.Mutex
	cache      map[string]lastSend // key -> last
	forceAfter time.Duration
}

type lastSend struct {
	status   int
	lastTime time.Time
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
	if err := r.loadRulesOnce(); err != nil {
		r.log.Warn("rules initial load", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.initRuntime()

	r.startWorkers(ctx)
	r.startTickerLoop(ctx)
	r.startRulesWatcher()
	r.startSignalHandler()

	return nil
}

func (r *Runner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Runner) initRuntime() {
	r.jobs = make(chan func(), jobQueueSize)
	r.cache = make(map[string]lastSend)
}

func (r *Runner) startWorkers(ctx context.Context) {
	n := r.cfg.Concurrency
	for range n {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case fn, ok := <-r.jobs:
					if !ok {
						return
					}
					fn()
				}
			}
		}()
	}
}

func (r *Runner) startTickerLoop(ctx context.Context) {
	r.resetTicker(config.MustDuration(r.cfg.Interval, defaultInterval))
	go r.loop(ctx)
}

func (r *Runner) startRulesWatcher() {
	r.stopWatch = make(chan struct{})
	go func() {
		err := rules.Watch(r.stopWatch, r.cfg.RulesPath, func(rr rules.Rules) {
			r.ruleStore.Set(rr)
			r.log.Info("rules reloaded", "systemd", len(rr.Systemd), "docker", len(rr.Docker))
		})
		if err != nil {
			r.log.Error("rules watch", "err", err)
		}
	}()
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
				if err := r.loadRulesOnce(); err != nil {
					r.log.Error("reload rules", "err", err)
				}
			default:
				r.Stop()
				close(r.stopWatch)
				return
			}
		}
	}()
}

func (r *Runner) reload() error {
	c, loadErr := config.Load(r.cfgPath)
	if loadErr != nil {
		return loadErr
	}
	tok, tokenErr := config.LoadToken(&c)
	if tokenErr != nil {
		return tokenErr
	}

	if r.dck == nil {
		if d, err := check.NewDockerChecker(); err == nil {
			r.dck = d
		} else {
			r.log.Warn("docker init failed", "err", err)
		}
	}
	if r.sd == nil {
		if cli, err := check.NewSystemdClient(context.Background()); err == nil {
			r.sd = cli
		} else {
			r.log.Warn("systemd dbus init failed", "err", err)
		}
	}

	httpc := makeHTTPClient(c)

	if r.post == nil {
		r.post = &httpPoster{
			log:       r.log,
			c:         httpc,
			adcmURL:   c.ADCMURL,
			hostID:    c.HostID,
			token:     tok,
			logBodies: c.LogBodies,
		}
	} else if hp, ok := r.post.(*httpPoster); ok {
		hp.c = httpc
		hp.adcmURL = c.ADCMURL
		hp.hostID = c.HostID
		hp.token = tok
		hp.logBodies = c.LogBodies
	}

	r.mu.Lock()
	r.cfg = c
	r.token = tok
	r.client = httpc
	r.forceAfter = config.MustDuration(c.ForceSendAfter, defaultForceSend)
	r.mu.Unlock()

	r.resetTicker(config.MustDuration(c.Interval, defaultInterval))
	return nil
}

func makeHTTPClient(c config.Config) *http.Client {
	tr := buildTransport(c)
	httpTimeout := config.MustDuration(c.HTTPTimeout, defaultHTTPTimeout)
	return &http.Client{Timeout: httpTimeout, Transport: tr}
}

func buildTransport(c config.Config) *http.Transport {
	tr := &http.Transport{
		MaxIdleConns:        httpMaxIdle,
		MaxIdleConnsPerHost: httpMaxIdlePerHost,
		IdleConnTimeout:     httpIdleTimeout,
	}
	if !strings.HasPrefix(strings.ToLower(c.ADCMURL), "https://") {
		return tr
	}
	tr.TLSClientConfig = buildTLSConfig(c)
	return tr
}

func buildTLSConfig(c config.Config) *tls.Config {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	roots, sysErr := x509.SystemCertPool()
	if sysErr != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if strings.TrimSpace(c.TLS.CAFile) != "" {
		if pem, rdErr := os.ReadFile(c.TLS.CAFile); rdErr == nil {
			_ = roots.AppendCertsFromPEM(pem)
		}
	}
	tlsConf.RootCAs = roots

	if c.TLS.CertFile != "" && c.TLS.KeyFile != "" {
		if cert, ckErr := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile); ckErr == nil {
			tlsConf.Certificates = []tls.Certificate{cert}
		}
	}
	if c.TLS.ServerName != "" {
		tlsConf.ServerName = c.TLS.ServerName
	}
	if c.TLS.InsecureSkipVerify {
		tlsConf.InsecureSkipVerify = true
	}
	return tlsConf
}

func (r *Runner) loadRulesOnce() error {
	rr, err := rules.Load(r.cfg.RulesPath)
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
}

func (r *Runner) loop(ctx context.Context) {
	r.scanOnce(ctx)
	for {
		r.tickerMu.Lock()
		c := r.ticker.C()
		r.tickerMu.Unlock()
		select {
		case <-ctx.Done():
			close(r.jobs)
			return
		case <-c:
			r.scanOnce(ctx)
		}
	}
}

func (r *Runner) scanOnce(ctx context.Context) {
	cfg, token, force := r.snapshot()
	_ = token
	r.scanSystemd(ctx, cfg, force)
	r.scanDocker(ctx, cfg, force)
	r.sendHeartbeat(ctx, cfg, force)
}

func (r *Runner) snapshot() (config.Config, string, time.Duration) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg, r.token, r.forceAfter
}

func (r *Runner) scanSystemd(ctx context.Context, cfg config.Config, forceAfter time.Duration) {
	rr := r.ruleStore.Get()
	for _, rule := range rr.Systemd {
		comps := append([]string(nil), rule.Components...)
		var units []string
		if rule.Unit != "" {
			units = append(units, rule.Unit)
		}
		if rule.UnitGlob != "" && r.sd != nil {
			units = append(units, r.sd.ExpandUnitsByGlob(ctx, rule.UnitGlob)...)
		}
		for _, unit := range units {
			unitLocal := unit
			r.enqueue(func() {
				st := 1
				if r.sd != nil {
					st = r.sd.SystemdStatus(ctx, unitLocal)
				}
				for _, comp := range comps {
					r.maybePostComponent(ctx, cfg, comp, st, forceAfter)
				}
			})
		}
	}
}

func (r *Runner) scanDocker(ctx context.Context, cfg config.Config, forceAfter time.Duration) {
	rr := r.ruleStore.Get()
	for _, d := range rr.Docker {
		comps := append([]string(nil), d.Components...)
		sel := d.Containers
		r.enqueue(func() {
			status := 1
			if r.dck != nil {
				if len(sel.Names) > 0 {
					status = r.dck.AllRunningNames(context.Background(), sel.Names)
				} else {
					status = r.dck.AllRunningByLabels(context.Background(), sel.Labels)
				}
			}
			for _, comp := range comps {
				r.maybePostComponent(ctx, cfg, comp, status, forceAfter)
			}
		})
	}
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
	compID string,
	status int,
	forceAfter time.Duration,
) {
	key := fmt.Sprintf("comp:%d:%s", cfg.HostID, compID)
	if !r.shouldSend(key, status, forceAfter) {
		return
	}
	if r.post != nil {
		if err := r.post.PostComponent(ctx, compID, status); err != nil {
			r.log.WarnContext(ctx, "post component failed", "comp", compID, "err", err)
			return
		}
	}
	r.markSent(key, status)
}

func (r *Runner) maybePostHost(ctx context.Context, cfg config.Config, status int, forceAfter time.Duration) {
	key := fmt.Sprintf("host:%d", cfg.HostID)
	if !r.shouldSend(key, status, forceAfter) {
		return
	}
	if r.post != nil {
		if err := r.post.PostHost(ctx, status); err != nil {
			r.log.WarnContext(ctx, "post host failed", "host", cfg.HostID, "err", err)
			return
		}
	}
	r.markSent(key, status)
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
