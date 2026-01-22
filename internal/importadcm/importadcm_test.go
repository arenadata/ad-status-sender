package importadcm

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
)

func TestFromADCM_SystemdMapping(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	hostID := 7
	clusterID := 10
	serviceID := 55
	configID := 100

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/hosts/7/":
			writeJSON(w, map[string]any{
				"id":   hostID,
				"name": "host-7",
				"cluster": map[string]any{
					"id":   clusterID,
					"name": "cluster",
				},
				"components": []map[string]any{
					{"id": 101, "name": "rest", "displayName": "rest"},
					{"id": 102, "name": "master", "displayName": "master"},
				},
			})
		case "/api/v2/clusters/10/services/":
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{"id": serviceID, "name": "hbase"},
				},
			})
		case "/api/v2/clusters/10/services/55/configs/":
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{"id": configID, "isCurrent": true},
				},
			})
		case "/api/v2/clusters/10/services/55/configs/100/":
			components := map[string]string{
				"rest":   `{"systemd":{"service_name":"hbase-rest"}}`,
				"master": `{"systemd":{"service_name":"hbase-master.service"}}`,
				"ghost":  `{"systemd":{"service_name":"ghost"}}`,
				"bad":    `not-json`,
			}
			writeJSON(w, map[string]any{
				"id": configID,
				"config": map[string]any{
					"components": components,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dbDir := t.TempDir()
	store, err := sqlite.Open("file:" + filepath.Join(dbDir, "rules.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	client := adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)
	if updErr := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return FromADCM(ctx, tx, client, hostID, nil)
	}); updErr != nil {
		t.Fatalf("import: %v", updErr)
	}

	rr, err := store.LoadRulesForHost(ctx, hostID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Systemd) != 2 {
		t.Fatalf("systemd rules=%d, want 2", len(rr.Systemd))
	}

	got := map[string][]string{}
	for _, r := range rr.Systemd {
		got[r.Unit] = r.Components
	}
	if len(got["hbase-rest.service"]) != 1 || got["hbase-rest.service"][0] != "101" {
		t.Fatalf("rest rule mismatch: %v", got["hbase-rest.service"])
	}
	if len(got["hbase-master.service"]) != 1 || got["hbase-master.service"][0] != "102" {
		t.Fatalf("master rule mismatch: %v", got["hbase-master.service"])
	}
}

func TestFromADCM_NoCurrentConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	hostID := 7
	clusterID := 10
	serviceID := 55

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/hosts/7/":
			writeJSON(w, map[string]any{
				"id":   hostID,
				"name": "host-7",
				"cluster": map[string]any{
					"id":   clusterID,
					"name": "cluster",
				},
				"components": []map[string]any{
					{"id": 101, "name": "rest", "displayName": "rest"},
				},
			})
		case "/api/v2/clusters/10/services/":
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{"id": serviceID, "name": "hbase"},
				},
			})
		case "/api/v2/clusters/10/services/55/configs/":
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{"id": 1, "isCurrent": false},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dbDir := t.TempDir()
	store, err := sqlite.Open("file:" + filepath.Join(dbDir, "rules.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	client := adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)
	if updErr := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return FromADCM(ctx, tx, client, hostID, nil)
	}); updErr != nil {
		t.Fatalf("import: %v", updErr)
	}

	rr, err := store.LoadRulesForHost(ctx, hostID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Systemd) != 0 {
		t.Fatalf("systemd rules=%d, want 0", len(rr.Systemd))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
