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

	r := runner.NewWithLogger(cfgPath, logger)
	if rErr := r.Start(); rErr != nil {
		logger.Error("start failed", "err", rErr)
		os.Exit(1)
	}
	_, _ = sd.SdNotify(false, sd.SdNotifyReady)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	r.Stop()
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
