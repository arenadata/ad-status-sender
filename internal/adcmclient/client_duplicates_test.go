package adcmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GetHost decodes the duplicates[] of a shared original host.
func TestClient_GetHost_DecodesDuplicates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/hosts/7/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"id": 7, "name": "host-7",
			"cluster": {"id": 10, "name": "c1"},
			"components": [{"id": 101, "name": "rest"}],
			"duplicates": [{"id": 8, "cluster": {"id": 20, "name": "c2"}}]
		}`))
	}))
	defer srv.Close()

	cli := New(srv.URL, "TokenX", srv.Client(), nil)
	host, err := cli.GetHost(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetHost err: %v", err)
	}
	if len(host.Duplicates) != 1 {
		t.Fatalf("duplicates=%d, want 1", len(host.Duplicates))
	}
	if d := host.Duplicates[0]; d.ID != 8 || d.Cluster == nil || d.Cluster.ID != 20 {
		t.Fatalf("bad duplicate: %+v", d)
	}
}

// GetHost surfaces the ADCM status code as a typed error so importadcm can
// distinguish permanent (404/403) from transient (5xx) duplicate failures.
func TestClient_GetHost_TypedStatusError(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		cli := New(srv.URL, "TokenX", srv.Client(), nil)
		_, err := cli.GetHost(context.Background(), 8)
		srv.Close()
		if err == nil {
			t.Fatalf("code %d: expected error", code)
		}
		got, ok := HTTPStatusCode(err)
		if !ok || got != code {
			t.Fatalf("HTTPStatusCode(%v) = %d,%v; want %d,true", err, got, ok, code)
		}
	}
}
