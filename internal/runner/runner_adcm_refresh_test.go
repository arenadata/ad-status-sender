package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
	"github.com/arenadata/ad-status-sender/internal/config"
)

type adcmTestServer struct {
	mu   sync.Mutex
	unit string
}

func (s *adcmTestServer) setUnit(name string) {
	s.mu.Lock()
	s.unit = name
	s.mu.Unlock()
}

func (s *adcmTestServer) getUnit() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unit
}

func TestRunner_ADCMRefreshSyncer(t *testing.T) {
	t.Parallel()

	state := &adcmTestServer{unit: "hbase-rest"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/hosts/7/":
			writeJSON(w, map[string]any{
				"id":   7,
				"name": "host-7",
				"cluster": map[string]any{
					"id":   10,
					"name": "cluster",
				},
				"components": []map[string]any{
					{"id": 101, "name": "rest", "displayName": "rest"},
				},
			})
		case "/api/v2/clusters/10/services/":
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{"id": 55, "name": "hbase"},
				},
			})
		case "/api/v2/clusters/10/services/55/configs/":
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{"id": 100, "isCurrent": true},
				},
			})
		case "/api/v2/clusters/10/services/55/components/":
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{"id": 101, "name": "rest"},
				},
			})
		case "/api/v2/clusters/10/services/55/configs/100/":
			unit := state.getUnit()
			comp := map[string]string{
				"rest": `{"systemd":{"service_name":"` + unit + `"}}`,
			}
			writeJSON(w, map[string]any{
				"id": 100,
				"config": map[string]any{
					"components": comp,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rulesDB := filepath.Join(t.TempDir(), "rules.db")
	r := NewWithDeps("unused.yaml", nil, nil, nil, nil, nil)
	r.mu.Lock()
	r.cfg = config.Config{
		HostID:       7,
		RulesSource:  "adcm",
		RulesDB:      rulesDB,
		RulesRefresh: "50ms",
	}
	r.mu.Unlock()
	db, dsn, err := r.reopenDB(rulesDB)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	r.mu.Lock()
	r.db = db
	r.dbPath = dsn
	r.mu.Unlock()
	t.Cleanup(func() {
		if r.db != nil {
			_ = r.db.Close()
		}
	})
	r.adcm = adcmclient.New(srv.URL, "TokenX", srv.Client(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.startRulesSyncer(ctx)

	waitUntil(t, func() bool {
		rr := r.ruleStore.Get()
		return len(rr.Systemd) == 1 && rr.Systemd[0].Unit == "hbase-rest.service"
	}, 2*time.Second)

	state.setUnit("hbase-rest-2")
	waitUntil(t, func() bool {
		rr := r.ruleStore.Get()
		return len(rr.Systemd) == 1 && rr.Systemd[0].Unit == "hbase-rest-2.service"
	}, 2*time.Second)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
