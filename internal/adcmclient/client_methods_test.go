package adcmclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_ObtainToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/token/":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"username":"admin"`) ||
				!strings.Contains(string(body), `"password":"admin"`) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "abc"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(srv.URL, "", srv.Client(), nil)
	token, err := cli.ObtainToken(context.Background(), "admin", "admin")
	if err != nil {
		t.Fatalf("ObtainToken err: %v", err)
	}
	if token != "abc" {
		t.Fatalf("unexpected token: %q", token)
	}
}

func TestClient_ObtainToken_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/token/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("oops"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(srv.URL, "", srv.Client(), nil)
	_, err := cli.ObtainToken(context.Background(), "admin", "admin")
	if err == nil || !strings.Contains(err.Error(), "token decode error") {
		t.Fatalf("expected token decode error, got: %v", err)
	}
}

func TestClient_RetryWithTokenProvider(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Token NEW" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"id": 7, "name": "h1"},
			},
		})
	}))
	defer srv.Close()

	var providerCalls int
	provider := func(_ context.Context) (string, error) {
		providerCalls++
		return "NEW", nil
	}

	cli := NewWithTokenProvider(srv.URL, "OLD", provider, srv.Client(), nil)
	id, err := cli.FindHostID(context.Background(), "h1")
	if err != nil {
		t.Fatalf("FindHostID err: %v", err)
	}
	if id != 7 {
		t.Fatalf("unexpected host id: %d", id)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if providerCalls != 1 {
		t.Fatalf("expected provider to be called once, got %d", providerCalls)
	}
}

func TestClient_NoRetryOnForbidden(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	var providerCalls int
	provider := func(_ context.Context) (string, error) {
		providerCalls++
		return "NEW", nil
	}

	cli := NewWithTokenProvider(srv.URL, "OLD", provider, srv.Client(), nil)
	_, err := cli.FindHostID(context.Background(), "h1")
	if err == nil {
		t.Fatalf("expected error on forbidden response")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if providerCalls != 0 {
		t.Fatalf("expected provider to not be called, got %d", providerCalls)
	}
}

func TestClient_FindHostAndCreateHost(t *testing.T) {
	const wantToken = "TT"
	var createCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v2/hosts/":
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"results": []map[string]any{
						{"id": 7, "name": "h1"},
					},
				})
				return
			}
			if r.Method == http.MethodPost {
				b, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(b), `"name":"new-host"`) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				createCalled = true
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli := New(srv.URL, wantToken, srv.Client(), nil)
	id, err := cli.FindHostID(context.Background(), "h1")
	if err != nil {
		t.Fatalf("FindHostID err: %v", err)
	}
	if id != 7 {
		t.Fatalf("unexpected host id: %d", id)
	}
	err = cli.CreateHost(context.Background(), "new-host")
	if err != nil {
		t.Fatalf("CreateHost err: %v", err)
	}
	if !createCalled {
		t.Fatalf("expected create host call")
	}
}

func TestClient_FirstComponentID(t *testing.T) {
	const wantToken = "TT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v2/hosts/7/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"components": []map[string]any{
				{"id": 55},
			},
		})
	}))
	defer srv.Close()

	cli := New(srv.URL, wantToken, srv.Client(), nil)
	id, err := cli.FirstComponentID(context.Background(), 7)
	if err != nil {
		t.Fatalf("FirstComponentID err: %v", err)
	}
	if id != "55" {
		t.Fatalf("unexpected component id: %q", id)
	}
}

func TestClient_PostStatuses(t *testing.T) {
	const wantToken = "TT"
	var hostOK, compOK bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/status/api/v1/host/7/":
			if string(body) != `{"status":0}` {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			hostOK = true
		case "/status/api/v1/host/7/component/42/":
			if string(body) != `{"status":1}` {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			compOK = true
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := New(srv.URL, wantToken, srv.Client(), nil)
	if err := cli.PostHostStatus(context.Background(), 7, 0); err != nil {
		t.Fatalf("PostHostStatus err: %v", err)
	}
	if err := cli.PostComponentStatus(context.Background(), 7, "42", 1); err != nil {
		t.Fatalf("PostComponentStatus err: %v", err)
	}
	if !hostOK || !compOK {
		t.Fatalf("expected host and component calls to succeed")
	}
}
