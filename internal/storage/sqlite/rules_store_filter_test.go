package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

/* ---------------- helpers ---------------- */

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rules.db")

	s, openErr := Open(dsn)
	if openErr != nil {
		t.Fatalf("Open error: %v", openErr)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return s, ctx
}

func seedSystemdForHost(ctx context.Context, t *testing.T, s *Store, hostID int, unit string, comps []string) {
	t.Helper()
	updErr := s.UpdateRules(ctx, func(tx *sql.Tx) error {
		if _, hostErr := EnsureHostTx(ctx, tx, hostID, ""); hostErr != nil {
			return hostErr
		}
		ruleID, upErr := UpsertSystemdRuleTx(ctx, tx, unit+"-rule", true, unit, "")
		if upErr != nil {
			return upErr
		}
		if compErr := SetRuleComponentsTx(ctx, tx, ruleID, comps); compErr != nil {
			return compErr
		}
		return SetRuleHostScopeTx(ctx, tx, ruleID, []int{hostID})
	})
	if updErr != nil {
		t.Fatalf("seedSystemdForHost: %v", updErr)
	}
}

func seedDockerGlobal(ctx context.Context, t *testing.T, s *Store, name string, comps []string) {
	t.Helper()
	updErr := s.UpdateRules(ctx, func(tx *sql.Tx) error {
		ruleID, upErr := UpsertDockerRuleTx(ctx, tx, name+"-rule", true, []string{name}, nil)
		if upErr != nil {
			return upErr
		}
		return SetRuleComponentsTx(ctx, tx, ruleID, comps)
	})
	if updErr != nil {
		t.Fatalf("seedDockerGlobal: %v", updErr)
	}
}

/* ---------------- tests ---------------- */

func TestDeleteRulesSimple_Systemd(t *testing.T) {
	t.Parallel()
	s, ctx := openTestStore(t)

	seedSystemdForHost(ctx, t, s, 7, "nginx.service", []string{"501"})

	rr, loadErr := s.LoadRulesForHost(ctx, 7)
	if loadErr != nil {
		t.Fatalf("LoadRulesForHost: %v", loadErr)
	}
	if len(rr.Systemd) != 1 {
		t.Fatalf("sanity: systemd=%d want 1", len(rr.Systemd))
	}

	deleted, delErr := s.DeleteRulesSimple(ctx, 7, "501", "nginx.service", "")
	if delErr != nil {
		t.Fatalf("DeleteRulesSimple(systemd): %v", delErr)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted)
	}

	rr2, loadErr2 := s.LoadRulesForHost(ctx, 7)
	if loadErr2 != nil {
		t.Fatalf("LoadRulesForHost(2): %v", loadErr2)
	}
	if len(rr2.Systemd) != 0 {
		t.Fatalf("after delete: systemd=%d want 0", len(rr2.Systemd))
	}
}

func TestDeleteRulesSimple_Docker(t *testing.T) {
	t.Parallel()
	s, ctx := openTestStore(t)

	seedDockerGlobal(ctx, t, s, "db", []string{"601"})

	rr, loadErr := s.LoadRulesForHost(ctx, 7)
	if loadErr != nil {
		t.Fatalf("LoadRulesForHost: %v", loadErr)
	}
	if len(rr.Docker) != 1 {
		t.Fatalf("sanity: docker=%d want 1", len(rr.Docker))
	}

	deleted, delErr := s.DeleteRulesSimple(ctx, 7, "601", "", "db")
	if delErr != nil {
		t.Fatalf("DeleteRulesSimple(docker): %v", delErr)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted)
	}

	rr2, loadErr2 := s.LoadRulesForHost(ctx, 7)
	if loadErr2 != nil {
		t.Fatalf("LoadRulesForHost(2): %v", loadErr2)
	}
	if len(rr2.Docker) != 0 {
		t.Fatalf("after delete: docker=%d want 0", len(rr2.Docker))
	}
}

func TestDeleteRulesSimple_AllKindsWhenNoFilters(t *testing.T) {
	t.Parallel()
	s, ctx := openTestStore(t)

	seedSystemdForHost(ctx, t, s, 9, "svc.service", []string{"777"})
	seedDockerGlobal(ctx, t, s, "dock", []string{"777"})

	deleted, delErr := s.DeleteRulesSimple(ctx, 9, "777", "", "")
	if delErr != nil {
		t.Fatalf("DeleteRulesSimple(all): %v", delErr)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d, want 2", deleted)
	}

	rr, loadErr := s.LoadRulesForHost(ctx, 9)
	if loadErr != nil {
		t.Fatalf("LoadRulesForHost: %v", loadErr)
	}
	if len(rr.Systemd) != 0 || len(rr.Docker) != 0 {
		t.Fatalf("after delete all: systemd=%d docker=%d", len(rr.Systemd), len(rr.Docker))
	}
}
