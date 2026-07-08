package importadcm

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
	"github.com/arenadata/ad-status-sender/internal/rules"
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

// A shared host: original 7 (cluster 10) + duplicate 8 (cluster 20). Both share
// the physical hbase-rest unit; the duplicate's component must be tagged with
// host_id 8 so its status posts under the duplicate id.
func TestFromADCM_SharedHostDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/hosts/7/":
			writeJSON(w, map[string]any{
				"id": 7, "name": "host-7",
				"cluster":    map[string]any{"id": 10, "name": "c1"},
				"components": []map[string]any{{"id": 101, "name": "rest"}, {"id": 102, "name": "master"}},
				"duplicates": []map[string]any{{"id": 8, "cluster": map[string]any{"id": 20, "name": "c2"}}},
			})
		case "/api/v2/hosts/8/":
			writeJSON(w, map[string]any{
				"id": 8, "name": "host-7-dup",
				"cluster":    map[string]any{"id": 20, "name": "c2"},
				"components": []map[string]any{{"id": 201, "name": "rest"}},
			})
		case "/api/v2/clusters/10/services/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 55, "name": "hbase"}}})
		case "/api/v2/clusters/20/services/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 66, "name": "hbase"}}})
		case "/api/v2/clusters/10/services/55/configs/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 100, "isCurrent": true}}})
		case "/api/v2/clusters/20/services/66/configs/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 200, "isCurrent": true}}})
		case "/api/v2/clusters/10/services/55/configs/100/":
			writeJSON(w, map[string]any{"id": 100, "config": map[string]any{"components": map[string]string{
				"rest":   `{"systemd":{"service_name":"hbase-rest"}}`,
				"master": `{"systemd":{"service_name":"hbase-master"}}`,
			}}})
		case "/api/v2/clusters/20/services/66/configs/200/":
			writeJSON(w, map[string]any{"id": 200, "config": map[string]any{"components": map[string]string{
				"rest": `{"systemd":{"service_name":"hbase-rest"}}`,
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rr := importAndLoad(ctx, t, srv, 7)
	targets := map[string][]rules.ComponentTarget{}
	for _, r := range rr.Systemd {
		targets[r.Unit] = r.ComponentTargets
	}
	if !hasTarget(targets["hbase-rest.service"], 0, "101") {
		t.Fatalf("rest missing primary target: %v", targets["hbase-rest.service"])
	}
	if !hasTarget(targets["hbase-rest.service"], 8, "201") {
		t.Fatalf("rest missing duplicate target: %v", targets["hbase-rest.service"])
	}
	if !hasTarget(targets["hbase-master.service"], 0, "102") {
		t.Fatalf("master missing primary target: %v", targets["hbase-master.service"])
	}
	if got := targets["hbase-master.service"]; len(got) != 1 {
		t.Fatalf("master should not carry a duplicate target: %v", got)
	}
}

// sharedHostHandler serves a primary host 7 in cluster 10 (component rest=101,
// unit hbase-rest) plus cluster 20 for its duplicate 8 (component rest=201). The
// dup8 handler controls the GET /hosts/8/ response so tests vary only that.
func sharedHostHandler(dup8 http.HandlerFunc) http.HandlerFunc {
	restCfg := map[string]any{"config": map[string]any{"components": map[string]string{
		"rest": `{"systemd":{"service_name":"hbase-rest"}}`,
	}}}
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/hosts/7/":
			writeJSON(w, map[string]any{
				"id": 7, "name": "host-7",
				"cluster":    map[string]any{"id": 10, "name": "c1"},
				"components": []map[string]any{{"id": 101, "name": "rest"}},
				"duplicates": []map[string]any{{"id": 8, "cluster": map[string]any{"id": 20, "name": "c2"}}},
			})
		case "/api/v2/hosts/8/":
			dup8(w, r)
		case "/api/v2/clusters/10/services/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 55, "name": "hbase"}}})
		case "/api/v2/clusters/20/services/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 66, "name": "hbase"}}})
		case "/api/v2/clusters/10/services/55/configs/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 100, "isCurrent": true}}})
		case "/api/v2/clusters/20/services/66/configs/":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 200, "isCurrent": true}}})
		case "/api/v2/clusters/10/services/55/configs/100/":
			writeJSON(w, restCfg)
		case "/api/v2/clusters/20/services/66/configs/200/":
			writeJSON(w, restCfg)
		default:
			http.NotFound(w, r)
		}
	}
}

func dup8OK(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"id": 8, "name": "host-7-dup",
		"cluster":    map[string]any{"id": 20, "name": "c2"},
		"components": []map[string]any{{"id": 201, "name": "rest"}},
	})
}

// A permanent duplicate failure (404 gone / 403 no-RBAC) is skipped and the
// primary still imports; a transient one (5xx) aborts the whole import so the
// clear-and-replace tx rolls back instead of wiping still-valid targets.
func TestFromADCM_DuplicateErrorClass(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		code    int
		wantErr bool
	}{
		{"forbidden_skipped", http.StatusForbidden, false},
		{"notfound_skipped", http.StatusNotFound, false},
		{"server_error_fatal", http.StatusInternalServerError, true},
		{"bad_gateway_fatal", http.StatusBadGateway, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			srv := httptest.NewServer(sharedHostHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()

			store := openStore(t)
			client := adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)
			err := store.UpdateRules(ctx, func(tx *sql.Tx) error { return FromADCM(ctx, tx, client, 7, nil) })
			if tc.wantErr {
				if err == nil {
					t.Fatalf("transient dup %d should abort import", tc.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("permanent dup %d should skip, got: %v", tc.code, err)
			}
			rr := mustLoad(ctx, t, store, 7)
			if len(rr.Systemd) != 1 || !hasTarget(rr.Systemd[0].ComponentTargets, 0, "101") {
				t.Fatalf("primary rule missing after skip: %+v", rr.Systemd)
			}
			if hasTarget(rr.Systemd[0].ComponentTargets, 8, "201") {
				t.Fatalf("skipped duplicate must not persist targets: %+v", rr.Systemd[0].ComponentTargets)
			}
		})
	}
}

// A duplicate with no cluster carries nothing to import and is skipped.
func TestFromADCM_DuplicateNoCluster_Skipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := httptest.NewServer(sharedHostHandler(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": 8, "name": "dup", "cluster": nil, "components": []any{}})
	}))
	defer srv.Close()

	store := openStore(t)
	client := adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)
	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error { return FromADCM(ctx, tx, client, 7, nil) }); err != nil {
		t.Fatalf("import: %v", err)
	}
	rr := mustLoad(ctx, t, store, 7)
	if len(rr.Systemd) != 1 || hasTarget(rr.Systemd[0].ComponentTargets, 8, "201") {
		t.Fatalf("unclustered duplicate must be skipped: %+v", rr.Systemd)
	}
}

// Two duplicates in different clusters both contribute host-tagged targets to
// the shared unit.
func TestFromADCM_TwoDuplicates_Aggregated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	restCfg := map[string]any{"config": map[string]any{"components": map[string]string{
		"rest": `{"systemd":{"service_name":"hbase-rest"}}`,
	}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/hosts/7/":
			writeJSON(w, map[string]any{
				"id": 7, "name": "host-7",
				"cluster":    map[string]any{"id": 10, "name": "c1"},
				"components": []map[string]any{{"id": 101, "name": "rest"}},
				"duplicates": []map[string]any{
					{"id": 8, "cluster": map[string]any{"id": 20, "name": "c2"}},
					{"id": 9, "cluster": map[string]any{"id": 30, "name": "c3"}},
				},
			})
		case "/api/v2/hosts/8/":
			writeJSON(w, map[string]any{
				"id": 8, "name": "d8",
				"cluster":    map[string]any{"id": 20, "name": "c2"},
				"components": []map[string]any{{"id": 201, "name": "rest"}},
			})
		case "/api/v2/hosts/9/":
			writeJSON(w, map[string]any{
				"id": 9, "name": "d9",
				"cluster":    map[string]any{"id": 30, "name": "c3"},
				"components": []map[string]any{{"id": 301, "name": "rest"}},
			})
		default:
			// Every cluster (10/20/30) serves the same hbase service 55, config
			// 100, and rest systemd config.
			switch {
			case strings.HasSuffix(r.URL.Path, "/configs/100/"):
				writeJSON(w, restCfg)
			case strings.HasSuffix(r.URL.Path, "/configs/"):
				writeJSON(w, map[string]any{"results": []map[string]any{{"id": 100, "isCurrent": true}}})
			case strings.HasSuffix(r.URL.Path, "/services/"):
				writeJSON(w, map[string]any{"results": []map[string]any{{"id": 55, "name": "hbase"}}})
			default:
				http.NotFound(w, r)
			}
		}
	}))
	defer srv.Close()

	store := openStore(t)
	client := adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)
	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error { return FromADCM(ctx, tx, client, 7, nil) }); err != nil {
		t.Fatalf("import: %v", err)
	}
	rr := mustLoad(ctx, t, store, 7)
	if len(rr.Systemd) != 1 {
		t.Fatalf("systemd rules=%d, want 1", len(rr.Systemd))
	}
	ts := rr.Systemd[0].ComponentTargets
	for _, want := range []rules.ComponentTarget{{HostID: 0, ComponentID: "101"}, {HostID: 8, ComponentID: "201"}, {HostID: 9, ComponentID: "301"}} {
		if !hasTarget(ts, want.HostID, want.ComponentID) {
			t.Fatalf("missing target %+v in %v", want, ts)
		}
	}
}

// A transient duplicate failure aborts the import, so the clear-and-replace tx
// rolls back and the previous sync's duplicate targets survive.
func TestFromADCM_DuplicateTransient_RollsBackPreservingPrior(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var dupFail atomic.Bool
	srv := httptest.NewServer(sharedHostHandler(func(w http.ResponseWriter, r *http.Request) {
		if dupFail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		dup8OK(w, r)
	}))
	defer srv.Close()

	store := openStore(t)
	client := adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)
	sync := func() error {
		return store.UpdateRules(ctx, func(tx *sql.Tx) error { return FromADCM(ctx, tx, client, 7, nil) })
	}

	// Sync N: duplicate healthy -> its target is persisted.
	if err := sync(); err != nil {
		t.Fatalf("sync N: %v", err)
	}
	if rr := mustLoad(ctx, t, store, 7); !hasTarget(rr.Systemd[0].ComponentTargets, 8, "201") {
		t.Fatalf("sync N should persist duplicate target: %+v", rr.Systemd)
	}

	// Sync N+1: duplicate flakes -> import aborts, tx rolls back.
	dupFail.Store(true)
	if err := sync(); err == nil {
		t.Fatal("sync N+1 with transient dup error should return error")
	}
	rr := mustLoad(ctx, t, store, 7)
	if len(rr.Systemd) != 1 || !hasTarget(rr.Systemd[0].ComponentTargets, 8, "201") {
		t.Fatalf("rollback should preserve prior duplicate target: %+v", rr.Systemd)
	}
}

func openStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open("file:" + filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustLoad(ctx context.Context, t *testing.T, store *sqlite.Store, hostID int) rules.Rules {
	t.Helper()
	rr, err := store.LoadRulesForHost(ctx, hostID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return rr
}

func importAndLoad(ctx context.Context, t *testing.T, srv *httptest.Server, hostID int) rules.Rules {
	t.Helper()
	store := openStore(t)
	client := adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)
	if updErr := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return FromADCM(ctx, tx, client, hostID, nil)
	}); updErr != nil {
		t.Fatalf("import: %v", updErr)
	}
	return mustLoad(ctx, t, store, hostID)
}

func hasTarget(ts []rules.ComponentTarget, hostID int, compID string) bool {
	for _, t := range ts {
		if t.HostID == hostID && t.ComponentID == compID {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
