package metrics

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerBasicAuth(t *testing.T) {
	t.Parallel()
	m := New()
	m.SetUp(true)
	srv := NewServer(ServerConfig{
		Listen:   ":0",
		Path:     "/metrics",
		Username: "prom",
		Password: "s3cret",
	}, m.Registry(), slog.New(slog.DiscardHandler))

	// No credentials -> 401.
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: want 401, got %d", rec.Code)
	}

	// Wrong password -> 401.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.SetBasicAuth("prom", "wrong")
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pass: want 401, got %d", rec.Code)
	}

	// Correct credentials -> 200 with our series.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.SetBasicAuth("prom", "s3cret")
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("good auth: want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ad_status_sender_up 1") {
		t.Fatalf("missing up metric: %q", rec.Body.String())
	}
}

func TestServerNoAuth(t *testing.T) {
	t.Parallel()
	m := New()
	srv := NewServer(ServerConfig{Listen: ":0"}, m.Registry(),
		slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no-auth endpoint: want 200, got %d", rec.Code)
	}
}
