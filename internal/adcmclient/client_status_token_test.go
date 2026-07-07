package adcmclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_ObtainStatusToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != statusCheckerTokenPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Token RBAC" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "SECRET"})
	}))
	defer srv.Close()

	cli := New(srv.URL, "RBAC", srv.Client(), nil)
	tok, err := cli.ObtainStatusToken(context.Background())
	if err != nil {
		t.Fatalf("ObtainStatusToken err: %v", err)
	}
	if tok != "SECRET" {
		t.Fatalf("unexpected token: %q", tok)
	}
}

func TestClient_ObtainStatusToken_MissingField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"other": "x"})
	}))
	defer srv.Close()

	cli := New(srv.URL, "RBAC", srv.Client(), nil)
	_, err := cli.ObtainStatusToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing token") {
		t.Fatalf("expected missing token error, got: %v", err)
	}
}

// Status POSTs fetch the secret from the endpoint, then reuse the cached value.
func TestClient_StatusPost_FetchesAndCaches(t *testing.T) {
	var endpointCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case statusCheckerTokenPath:
			endpointCalls++
			if auth != "Token RBAC" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "SECRET"})
		case "/status/api/v1/host/7/":
			if auth != "Token SECRET" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(srv.URL, "RBAC", srv.Client(), nil)
	cli.SetStatusTokenProvider(cli.ObtainStatusToken)

	for range 2 {
		if err := cli.PostHostStatus(context.Background(), 7, 0); err != nil {
			t.Fatalf("PostHostStatus err: %v", err)
		}
	}
	if endpointCalls != 1 {
		t.Fatalf("expected endpoint fetched once (cached), got %d", endpointCalls)
	}
}

// Missing endpoint (404 on an old image) falls back to the configured token and
// enters cooldown so the missing route is not probed on every scan.
func TestClient_StatusPost_FallbackWhenEndpointMissing(t *testing.T) {
	var endpointCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case statusCheckerTokenPath:
			endpointCalls++
			w.WriteHeader(http.StatusNotFound)
		case "/status/api/v1/host/7/":
			if auth != "Token RBAC" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(srv.URL, "RBAC", srv.Client(), nil)
	cli.SetStatusTokenProvider(cli.ObtainStatusToken)

	for range 2 {
		if err := cli.PostHostStatus(context.Background(), 7, 0); err != nil {
			t.Fatalf("PostHostStatus err: %v", err)
		}
	}
	if endpointCalls != 1 {
		t.Fatalf("expected endpoint probed once then cooled down, got %d", endpointCalls)
	}
}

// A 401 from the status server triggers a single refetch (rotated secret).
func TestClient_StatusPost_RefetchOn401(t *testing.T) {
	var endpointCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case statusCheckerTokenPath:
			endpointCalls++
			tok := "STALE"
			if endpointCalls > 1 {
				tok = "GOOD"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": tok})
		case "/status/api/v1/host/7/":
			if auth != "Token GOOD" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(srv.URL, "RBAC", srv.Client(), nil)
	cli.SetStatusTokenProvider(cli.ObtainStatusToken)

	if err := cli.PostHostStatus(context.Background(), 7, 0); err != nil {
		t.Fatalf("PostHostStatus err: %v", err)
	}
	if endpointCalls != 2 {
		t.Fatalf("expected fetch then refetch, got %d", endpointCalls)
	}
}
