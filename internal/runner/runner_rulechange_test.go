package runner

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/rules"
)

func TestLogRuleChanges(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	r := &Runner{log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	base := rules.Rules{Systemd: []rules.RuleSystemd{{Unit: "a.service"}, {Unit: "b.service"}}}

	// First load: summary line, not a per-unit diff.
	r.logRuleChanges(rules.Rules{}, base)
	if s := buf.String(); !strings.Contains(s, "rules loaded") || !strings.Contains(s, "systemd=2") {
		t.Fatalf("first load: %q", s)
	}

	// Service appeared (c) and removed (b).
	buf.Reset()
	next := rules.Rules{Systemd: []rules.RuleSystemd{{Unit: "a.service"}, {Unit: "c.service"}}}
	r.logRuleChanges(base, next)
	s := buf.String()
	if !strings.Contains(s, "rules changed") {
		t.Fatalf("want rules changed: %q", s)
	}
	if !strings.Contains(s, "added=[c.service]") || !strings.Contains(s, "removed=[b.service]") {
		t.Fatalf("bad diff: %q", s)
	}

	// No change: nothing logged.
	buf.Reset()
	r.logRuleChanges(next, next)
	if buf.Len() != 0 {
		t.Fatalf("unchanged reload should log nothing: %q", buf.String())
	}
}

func TestRuleUnitSet_DockerAndGlob(t *testing.T) {
	t.Parallel()
	rr := rules.Rules{
		Systemd: []rules.RuleSystemd{{UnitGlob: "hbase@*.service"}},
		Docker:  []rules.RuleDocker{{Name: "webstack"}},
	}
	got := ruleUnitSet(rr)
	if _, ok := got["hbase@*.service"]; !ok {
		t.Fatalf("glob missing: %v", got)
	}
	if _, ok := got["docker:webstack"]; !ok {
		t.Fatalf("docker group missing: %v", got)
	}
}
