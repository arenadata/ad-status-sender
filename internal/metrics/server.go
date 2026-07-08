package metrics

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverShutdownTimeout   = 5 * time.Second
)

// ServerConfig configures the metrics HTTP endpoint. Basic auth is enabled when
// Username is set; TLS when both CertFile and KeyFile are set.
type ServerConfig struct {
	Listen   string
	Path     string
	Username string
	Password string
	CertFile string
	KeyFile  string
}

// Server serves the metrics endpoint on its own HTTP listener.
type Server struct {
	srv      *http.Server
	certFile string
	keyFile  string
	log      *slog.Logger
}

// NewServer builds the metrics server. The registry supplies the exposed series.
func NewServer(cfg ServerConfig, reg prometheus.Gatherer, log *slog.Logger) *Server {
	path := cfg.Path
	if path == "" {
		path = "/metrics"
	}
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	if cfg.Username != "" {
		handler = basicAuth(handler, cfg.Username, cfg.Password)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	return &Server{
		srv: &http.Server{
			Addr:              cfg.Listen,
			Handler:           mux,
			ReadHeaderTimeout: serverReadHeaderTimeout,
		},
		certFile: cfg.CertFile,
		keyFile:  cfg.KeyFile,
		log:      log,
	}
}

// Start serves in the background until Shutdown; a listener error is logged.
func (s *Server) Start() {
	tlsOn := s.certFile != "" && s.keyFile != ""
	if tlsOn {
		s.srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	s.log.Info("metrics endpoint listening", "addr", s.srv.Addr, "tls", tlsOn)
	go func() {
		var err error
		if tlsOn {
			err = s.srv.ListenAndServeTLS(s.certFile, s.keyFile)
		} else {
			err = s.srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("metrics endpoint stopped", "err", err)
		}
	}()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

func basicAuth(next http.Handler, user, pass string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
