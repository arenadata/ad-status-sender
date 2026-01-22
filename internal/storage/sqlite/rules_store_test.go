package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/arenadata/ad-status-sender/internal/rules"
)

func newTestDB(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rules.db")

	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return s, ctx
}

func mustUpdate(ctx context.Context, t *testing.T, s *Store, fn func(ctx context.Context, tx *sql.Tx) error) {
	t.Helper()
	if err := s.UpdateRules(ctx, func(tx *sql.Tx) error { return fn(ctx, tx) }); err != nil {
		t.Fatalf("UpdateRules: %v", err)
	}
}

func seedInitialRules(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()
	mustUpdate(ctx, t, s, func(c context.Context, tx *sql.Tx) error {
		if _, err := EnsureHostTx(c, tx, 7, "node-7"); err != nil {
			return err
		}
		sysID, err1 := UpsertSystemdRuleTx(c, tx, "nginx-active", true, "nginx.service", "")
		if err1 != nil {
			return err1
		}
		if err2 := SetRuleComponentsTx(c, tx, sysID, []string{"501"}); err2 != nil {
			return err2
		}
		if err3 := SetRuleHostScopeTx(c, tx, sysID, []int{7}); err3 != nil {
			return err3
		}

		dID, err4 := UpsertDockerRuleTx(c, tx, "core", true,
			[]string{"db", "cache"}, []string{"app=etl", "stage=prod"})
		if err4 != nil {
			return err4
		}
		if err5 := SetRuleComponentsTx(c, tx, dID, []string{"601"}); err5 != nil {
			return err5
		}
		return nil
	})
}

func deleteSystemdRuleByName(ctx context.Context, t *testing.T, s *Store, name string) {
	t.Helper()
	mustUpdate(ctx, t, s, func(c context.Context, tx *sql.Tx) error {
		var id int64
		row := tx.QueryRowContext(c, `SELECT id FROM rule WHERE kind='systemd' AND name=?`, name)
		if scanErr := row.Scan(&id); scanErr != nil {
			return scanErr
		}
		return DeleteRuleTx(c, tx, id)
	})
}

func assertRulesForHost(t *testing.T, rr rules.Rules) {
	t.Helper()
	if len(rr.Systemd) != 1 {
		t.Fatalf("Systemd rules = %d, want 1", len(rr.Systemd))
	}
	sys := rr.Systemd[0]
	if sys.Unit != "nginx.service" {
		t.Fatalf("systemd.Unit=%q, want nginx.service", sys.Unit)
	}
	if len(sys.Components) != 1 || sys.Components[0] != "501" {
		t.Fatalf("systemd.Components=%v, want [501]", sys.Components)
	}

	if len(rr.Docker) != 1 {
		t.Fatalf("Docker rules = %d, want 1", len(rr.Docker))
	}
	d := rr.Docker[0]
	if len(d.Components) != 1 || d.Components[0] != "601" {
		t.Fatalf("docker.Components=%v, want [601]", d.Components)
	}
	if len(d.Containers.Names) != 2 || len(d.Containers.Labels) != 2 {
		t.Fatalf("docker selectors = %+v, want 2 names + 2 labels", d.Containers)
	}
}

func TestRulesRevisionIncrements(t *testing.T) {
	t.Parallel()

	s, ctx := newTestDB(t)

	initialRev, err := s.GetRulesRevision(ctx)
	if err != nil {
		t.Fatalf("GetRulesRevision: %v", err)
	}
	if initialRev < 1 {
		t.Fatalf("initial revision=%d, want >=1", initialRev)
	}

	mustUpdate(ctx, t, s, func(_ context.Context, _ *sql.Tx) error { return nil })

	rNoop, err := s.GetRulesRevision(ctx)
	if err != nil {
		t.Fatalf("GetRulesRevision(noop): %v", err)
	}
	if rNoop != initialRev {
		t.Fatalf("revision after noop=%d, want %d", rNoop, initialRev)
	}

	mustUpdate(ctx, t, s, func(ctx context.Context, tx *sql.Tx) error {
		rid, upErr := UpsertSystemdRuleTx(ctx, tx, "rev-test", true, "dummy.service", "")
		if upErr != nil {
			return upErr
		}
		if setErr := SetRuleComponentsTx(ctx, tx, rid, []string{"9999"}); setErr != nil {
			return setErr
		}
		return nil
	})

	r2, err := s.GetRulesRevision(ctx)
	if err != nil {
		t.Fatalf("GetRulesRevision(2): %v", err)
	}
	if r2 != initialRev+1 {
		t.Fatalf("revision=%d, want %d", r2, initialRev+1)
	}
}

func TestLoadRulesForHost_BasicCRUD(t *testing.T) {
	t.Parallel()

	s, ctx := newTestDB(t)

	seedInitialRules(ctx, t, s)

	rr, err := s.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("LoadRulesForHost: %v", err)
	}
	assertRulesForHost(t, rr)

	deleteSystemdRuleByName(ctx, t, s, "nginx-active")

	rr2, err := s.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("LoadRulesForHost(2): %v", err)
	}
	if len(rr2.Systemd) != 0 {
		t.Fatalf("Systemd rules = %d, want 0", len(rr2.Systemd))
	}
	if len(rr2.Docker) != 1 {
		t.Fatalf("Docker rules = %d, want 1", len(rr2.Docker))
	}
}
