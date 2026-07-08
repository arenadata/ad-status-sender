package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/rules"
)

// SetRuleComponentTargetsTx round-trips per-host component targets, and the
// legacy SetRuleComponentsTx path lands on host_id 0.
func TestComponentTargets_RoundTrip(t *testing.T) {
	t.Parallel()
	s, ctx := newTestDB(t)

	mustUpdate(ctx, t, s, func(c context.Context, tx *sql.Tx) error {
		if _, err := EnsureHostTx(c, tx, 7, "node-7"); err != nil {
			return err
		}
		sysID, err := UpsertSystemdRuleTx(c, tx, "adcm/hbase-rest.service", true, "hbase-rest.service", "")
		if err != nil {
			return err
		}
		if setErr := SetRuleComponentTargetsTx(c, tx, sysID, []rules.ComponentTarget{
			{HostID: 0, ComponentID: "101"},
			{HostID: 8, ComponentID: "201"},
			{HostID: 8, ComponentID: "201"}, // duplicate, must be ignored
		}); setErr != nil {
			return setErr
		}
		return SetRuleHostScopeTx(c, tx, sysID, []int{7})
	})

	rr, err := s.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Systemd) != 1 {
		t.Fatalf("systemd rules=%d, want 1", len(rr.Systemd))
	}
	ts := rr.Systemd[0].ComponentTargets
	if len(ts) != 2 {
		t.Fatalf("targets=%v, want 2 (deduped)", ts)
	}
	want := map[rules.ComponentTarget]bool{
		{HostID: 0, ComponentID: "101"}: true,
		{HostID: 8, ComponentID: "201"}: true,
	}
	for _, got := range ts {
		if !want[got] {
			t.Fatalf("unexpected target %v", got)
		}
	}
	// Components projection preserves ids for legacy readers.
	if got := rr.Systemd[0].Components; len(got) != 2 {
		t.Fatalf("Components=%v, want 2 ids", got)
	}
}

// The legacy string API maps to host_id 0, so EffectiveTargets resolves to the
// scoped host at post time.
func TestSetRuleComponents_DefaultsHostZero(t *testing.T) {
	t.Parallel()
	s, ctx := newTestDB(t)

	mustUpdate(ctx, t, s, func(c context.Context, tx *sql.Tx) error {
		if _, err := EnsureHostTx(c, tx, 7, "node-7"); err != nil {
			return err
		}
		id, err := UpsertSystemdRuleTx(c, tx, "nginx", true, "nginx.service", "")
		if err != nil {
			return err
		}
		if setErr := SetRuleComponentsTx(c, tx, id, []string{"501"}); setErr != nil {
			return setErr
		}
		return SetRuleHostScopeTx(c, tx, id, []int{7})
	})

	rr, err := s.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Systemd) != 1 || len(rr.Systemd[0].ComponentTargets) != 1 {
		t.Fatalf("unexpected rules: %+v", rr.Systemd)
	}
	if got := rr.Systemd[0].ComponentTargets[0]; got.HostID != 0 || got.ComponentID != "501" {
		t.Fatalf("target=%v, want {0 501}", got)
	}
}
