package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/arenadata/ad-status-sender/internal/config"
	"github.com/arenadata/ad-status-sender/internal/metrics"
	"github.com/arenadata/ad-status-sender/internal/runner"
	sd "github.com/coreos/go-systemd/v22/daemon"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config",
		"/etc/ad-status-sender/config.yaml", "path to config")
	flag.Parse()

	// pre-load config only to get logging settings
	cfg, err := config.Load(cfgPath)
	if err != nil {
		// fallback logger if config can't be read
		fallback := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
		fallback.Error("failed to load config for logger", "err", err)
		os.Exit(1)
	}

	level := config.ParseSlogLevel(cfg.LogLevel)
	logOut := logWriter(cfg)

	var handler slog.Handler
	switch cfg.LogFormat {
	case "json":
		handler = slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: level})
	default:
		handler = slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(handler)

	logger.Info("ad-status-sender starting",
		"host_id", cfg.HostID,
		"rules_source", cfg.RulesSource,
		"adcm_url", cfg.ADCMURL,
	)

	r := runner.NewWithLogger(cfgPath, logger)

	metricsSrv := startMetrics(cfg, r, logger)

	if rErr := r.Start(); rErr != nil {
		logger.Error("start failed", "err", rErr)
		if metricsSrv != nil {
			metricsSrv.Shutdown()
		}
		os.Exit(1)
	}
	_, _ = sd.SdNotify(false, sd.SdNotifyReady)
	logger.Info("ad-status-sender started")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	r.Stop()
	if metricsSrv != nil {
		metricsSrv.Shutdown()
	}
}

// startMetrics wires the Prometheus sink into the runner and starts the endpoint
// when metrics.listen is set. Returns nil when metrics are disabled.
func startMetrics(cfg config.Config, r *runner.Runner, logger *slog.Logger) *metrics.Server {
	if !cfg.MetricsEnabled() {
		return nil
	}
	m := metrics.New()
	m.SetUp(true)
	r.SetMetrics(m)

	sc := metrics.ServerConfig{
		Listen:   cfg.Metrics.Listen,
		Path:     cfg.Metrics.Path,
		Username: cfg.Metrics.BasicAuth.Username,
		CertFile: cfg.Metrics.TLS.CertFile,
		KeyFile:  cfg.Metrics.TLS.KeyFile,
	}
	if sc.Username != "" {
		pass, err := config.LoadMetricsPassword(&cfg)
		if err != nil {
			logger.Error("metrics basic auth configured but password missing", "err", err)
			os.Exit(1)
		}
		sc.Password = pass
	}
	srv := metrics.NewServer(sc, m.Registry(), logger)
	srv.Start()
	return srv
}

// logWriter returns stdout, or a size-rotating file writer when log_file is set.
func logWriter(cfg config.Config) io.Writer {
	if cfg.LogFile == "" {
		return os.Stdout
	}
	return &lumberjack.Logger{
		Filename:   cfg.LogFile,
		MaxSize:    cfg.LogMaxSizeMB,
		MaxBackups: cfg.LogMaxBackups,
		MaxAge:     cfg.LogMaxAgeDays,
		Compress:   cfg.LogCompressEnabled(),
	}
}
