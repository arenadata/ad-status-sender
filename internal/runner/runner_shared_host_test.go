package runner

import (
	"context"
	"testing"
	"time"

	"github.com/arenadata/ad-status-sender/internal/check/checktest"
	"github.com/arenadata/ad-status-sender/internal/config"
	"github.com/arenadata/ad-status-sender/internal/rules"
)

// One systemd unit shared by an original (host_id 0 → configured 7) and a
// duplicate (host_id 8) posts one component status per (host, component), plus
// the host heartbeat, and the cache is keyed per target.
func TestRunner_SharedHost_PostsPerDuplicate(t *testing.T) {
	sd := &checktest.FakeSystemd{Units: map[string]bool{"nginx.service": true}}
	post := &testPoster{}
	clk := &testClock{now: time.Unix(0, 0)}

	r := NewWithDeps("unused.yaml", nil, sd, nil, post, clk)
	r.mu.Lock()
	r.cfg = config.Config{HostID: 7, ForceSendAfter: "120s"}
	r.forceAfter = 120 * time.Second
	r.cache = make(map[string]lastSend)
	r.jobs = make(chan func(), 1)
	r.jobs <- func() {}
	r.mu.Unlock()

	r.ruleStore.Set(rules.Rules{
		Systemd: []rules.RuleSystemd{{
			Unit: "nginx.service",
			ComponentTargets: []rules.ComponentTarget{
				{HostID: 0, ComponentID: "501"},
				{HostID: 8, ComponentID: "801"},
			},
		}},
	})

	ctx := context.Background()
	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 3 }, 500*time.Millisecond)

	var primary, dup, host bool
	for _, e := range post.Snapshot() {
		switch {
		case e.IsHost && e.HostID == 0:
			host = true
		case e.HostID == 7 && e.CompID == "501":
			primary = true
		case e.HostID == 8 && e.CompID == "801":
			dup = true
		}
	}
	if !primary || !dup || !host {
		t.Fatalf("missing posts: primary=%v dup=%v host=%v (%+v)", primary, dup, host, post.Snapshot())
	}

	// Second scan: everything unchanged, cache suppresses all posts.
	post.Reset()
	r.scanOnce(ctx)
	time.Sleep(20 * time.Millisecond)
	if got := post.Count(); got != 0 {
		t.Fatalf("cache not per-target, got %d posts", got)
	}
}

// Same component id on the primary (host 0->7) and a duplicate (host 8) must not
// collide in the post cache. A host-agnostic cache key would suppress the second
// target and drop the scan from 3 posts to 2.
func TestRunner_SharedHost_CacheKeyedPerHost(t *testing.T) {
	sd := &checktest.FakeSystemd{Units: map[string]bool{"nginx.service": true}}
	post := &testPoster{}
	clk := &testClock{now: time.Unix(0, 0)}

	r := NewWithDeps("unused.yaml", nil, sd, nil, post, clk)
	r.mu.Lock()
	r.cfg = config.Config{HostID: 7, ForceSendAfter: "120s"}
	r.forceAfter = 120 * time.Second
	r.cache = make(map[string]lastSend)
	r.jobs = make(chan func(), 1)
	r.jobs <- func() {}
	r.mu.Unlock()

	r.ruleStore.Set(rules.Rules{
		Systemd: []rules.RuleSystemd{{
			Unit: "nginx.service",
			ComponentTargets: []rules.ComponentTarget{
				{HostID: 0, ComponentID: "900"},
				{HostID: 8, ComponentID: "900"},
			},
		}},
	})

	ctx := context.Background()
	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 3 }, 500*time.Millisecond)

	var onPrimary, onDup bool
	for _, e := range post.Snapshot() {
		if e.IsHost || e.CompID != "900" {
			continue
		}
		switch e.HostID {
		case 7:
			onPrimary = true
		case 8:
			onDup = true
		}
	}
	if !onPrimary || !onDup {
		t.Fatalf("same compid must post per host: primary=%v dup=%v (%+v)", onPrimary, onDup, post.Snapshot())
	}
}
