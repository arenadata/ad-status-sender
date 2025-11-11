package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/config"
)

func TestHTTPPoster_HostAndComponent(t *testing.T) {
	var cnt int32
	var lastURL, lastAuth, lastBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&cnt, 1)
		lastURL = r.URL.Path
		lastAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		_ = r.Body.Close()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Config{
		ADCMURL: "http://" + strings.TrimPrefix(srv.URL, "http://"),
		HostID:  7,
	}
	httpc := makeHTTPClient(cfg)

	p := &httpPoster{
		log:       slog.Default(),
		c:         httpc,
		adcmURL:   cfg.ADCMURL,
		hostID:    cfg.HostID,
		token:     "TokenX",
		logBodies: true,
	}

	if err := p.PostHost(context.Background(), 0); err != nil {
		t.Fatalf("PostHost err: %v", err)
	}
	if lastURL != "/status/api/v1/host/7/" || lastAuth != "Token TokenX" {
		t.Fatalf("bad host req: url=%s auth=%s", lastURL, lastAuth)
	}
	var m map[string]int
	if err := json.Unmarshal([]byte(lastBody), &m); err != nil || m["status"] != 0 {
		t.Fatalf("bad host body: %s err=%v", lastBody, err)
	}

	p.token = "ZZ"
	if err := p.PostComponent(context.Background(), "42", 1); err != nil {
		t.Fatalf("PostComponent err: %v", err)
	}
	if lastURL != "/status/api/v1/host/7/component/42/" || lastAuth != "Token ZZ" {
		t.Fatalf("bad comp req: url=%s auth=%s", lastURL, lastAuth)
	}
	if err := json.Unmarshal([]byte(lastBody), &m); err != nil || m["status"] != 1 {
		t.Fatalf("bad comp body: %s err=%v", lastBody, err)
	}

	if atomic.LoadInt32(&cnt) != 2 {
		t.Fatalf("server got %d requests, want 2", cnt)
	}
}
