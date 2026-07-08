package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
)

func TestADCMPPoster_HostAndComponent(t *testing.T) {
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

	httpc := srv.Client()
	client := adcmclient.New(srv.URL, "TokenX", httpc, slog.Default())
	p := &adcmPoster{
		log:    slog.Default(),
		client: client,
		hostID: 7,
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

	client2 := adcmclient.New(srv.URL, "ZZ", httpc, slog.Default())
	p2 := &adcmPoster{
		log:    slog.Default(),
		client: client2,
		hostID: 7,
	}
	if err := p2.PostComponent(context.Background(), 7, "42", 1); err != nil {
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
