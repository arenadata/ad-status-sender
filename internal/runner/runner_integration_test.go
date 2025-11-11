package runner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arenadata/ad-status-sender/internal/check/checktest"
	"github.com/arenadata/ad-status-sender/internal/config"
	"github.com/arenadata/ad-status-sender/internal/rules"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time                   { return c.now }
func (c *testClock) NewTicker(_ time.Duration) Ticker { return nil }
func (c *testClock) advance(d time.Duration)          { c.now = c.now.Add(d) }

type sentEvent struct {
	IsHost bool
	CompID string
	Status int
}
type testPoster struct {
	mu   sync.Mutex
	list []sentEvent
}

func (p *testPoster) PostHost(_ context.Context, status int) error {
	p.mu.Lock()
	p.list = append(p.list, sentEvent{IsHost: true, Status: status})
	p.mu.Unlock()
	return nil
}
func (p *testPoster) PostComponent(_ context.Context, compID string, status int) error {
	p.mu.Lock()
	p.list = append(p.list, sentEvent{IsHost: false, CompID: compID, Status: status})
	p.mu.Unlock()
	return nil
}
func (p *testPoster) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.list)
}
func (p *testPoster) Reset() {
	p.mu.Lock()
	p.list = nil
	p.mu.Unlock()
}

func waitUntil(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %v", timeout)
	}
}

func TestRunner_SendsStatuses_WithCacheAndForceResend(t *testing.T) {
	sd := &checktest.FakeSystemd{
		Units: map[string]bool{
			"nginx.service": true,
			"redis.service": false,
		},
		Globs: map[string][]string{
			"app@*.service": {"app@1.service", "app@2.service"},
		},
	}
	dck := &checktest.FakeDocker{
		Names:       map[string]bool{"db": true, "cache": true},
		LabelGroups: map[string][]bool{"app=etl,stage=prod": {true, true}},
	}
	post := &testPoster{}
	clk := &testClock{now: time.Unix(0, 0)}

	r := NewWithDeps("unused.yaml", nil, sd, dck, post, clk)

	r.mu.Lock()
	r.cfg = config.Config{
		ADCMURL:        "http://example",
		HostID:         7,
		ForceSendAfter: "120s",
		Concurrency:    0,
	}
	r.forceAfter = 120 * time.Second
	r.cache = make(map[string]lastSend)
	r.jobs = make(chan func(), 1)
	r.jobs <- func() {}
	r.mu.Unlock()

	r.ruleStore.Set(rules.Rules{
		Systemd: []rules.RuleSystemd{
			{Unit: "nginx.service", Components: []string{"501"}},
			{Unit: "redis.service", Components: []string{"502"}},
		},
		Docker: []rules.RuleDocker{
			{
				Name:       "core",
				Components: []string{"601"},
				Containers: rules.DockerSelector{Names: []string{"db", "cache"}},
			},
			{
				Name:       "etl",
				Components: []string{"701"},
				Containers: rules.DockerSelector{Labels: []string{"app=etl", "stage=prod"}},
			},
		},
	})

	ctx := context.Background()

	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 5 }, 500*time.Millisecond)

	post.Reset()
	r.scanOnce(ctx)
	time.Sleep(20 * time.Millisecond)
	if got := post.Count(); got != 0 {
		t.Fatalf("cache not working, got %d", got)
	}

	clk.advance(2 * time.Minute)
	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() > 0 }, 500*time.Millisecond)
}

func (p *testPoster) Snapshot() []sentEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]sentEvent, len(p.list))
	copy(out, p.list)
	return out
}

func TestRunner_StatusChangeTriggersSend(t *testing.T) {
	sd := &checktest.FakeSystemd{
		Units: map[string]bool{"nginx.service": true},
	}
	dck := &checktest.FakeDocker{}
	post := &testPoster{}
	clk := &testClock{now: time.Unix(0, 0)}

	r := NewWithDeps("unused.yaml", nil, sd, dck, post, clk)
	r.mu.Lock()
	r.cfg = config.Config{ADCMURL: "http://example", HostID: 7, ForceSendAfter: "120s", Concurrency: 0}
	r.forceAfter = 120 * time.Second
	r.cache = make(map[string]lastSend)
	r.jobs = make(chan func(), 1)
	r.jobs <- func() {}
	r.mu.Unlock()

	r.ruleStore.Set(rules.Rules{
		Systemd: []rules.RuleSystemd{
			{Unit: "nginx.service", Components: []string{"501"}},
		},
	})

	ctx := context.Background()
	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 2 }, 300*time.Millisecond)

	post.Reset()
	sd.Units["nginx.service"] = false
	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 1 }, 300*time.Millisecond)

	ss := post.Snapshot()
	if len(ss) != 1 || ss[0].IsHost || ss[0].CompID != "501" || ss[0].Status != 1 {
		t.Fatalf("want one comp event 501=1, got: %+v", ss)
	}
}

func TestRunner_DockerLabelsOnly(t *testing.T) {
	sd := &checktest.FakeSystemd{}
	dck := &checktest.FakeDocker{
		LabelGroups: map[string][]bool{"app=etl,stage=prod": {true, true}},
	}
	post := &testPoster{}
	clk := &testClock{now: time.Unix(0, 0)}

	r := NewWithDeps("unused.yaml", nil, sd, dck, post, clk)
	r.mu.Lock()
	r.cfg = config.Config{ADCMURL: "http://example", HostID: 7, ForceSendAfter: "120s", Concurrency: 0}
	r.forceAfter = 120 * time.Second
	r.cache = make(map[string]lastSend)
	r.jobs = make(chan func(), 1)
	r.jobs <- func() {}
	r.mu.Unlock()

	r.ruleStore.Set(rules.Rules{
		Docker: []rules.RuleDocker{
			{
				Name:       "etl",
				Components: []string{"701"},
				Containers: rules.DockerSelector{Labels: []string{"app=etl", "stage=prod"}},
			},
		},
	})

	ctx := context.Background()
	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 2 }, 300*time.Millisecond)

	ss := post.Snapshot()
	var hostOK, compOK bool
	for _, e := range ss {
		if e.IsHost && e.Status == 0 {
			hostOK = true
		}
		if !e.IsHost && e.CompID == "701" && e.Status == 0 {
			compOK = true
		}
	}
	if !hostOK || !compOK {
		t.Fatalf("unexpected events: %+v", ss)
	}
}

func TestRunner_RulesReloadAddsComponent(t *testing.T) {
	sd := &checktest.FakeSystemd{
		Units: map[string]bool{
			"nginx.service": true,  // 0
			"redis.service": false, // 1
		},
	}
	dck := &checktest.FakeDocker{}
	post := &testPoster{}
	clk := &testClock{now: time.Unix(0, 0)}

	r := NewWithDeps("unused.yaml", nil, sd, dck, post, clk)
	r.mu.Lock()
	r.cfg = config.Config{ADCMURL: "http://example", HostID: 7, ForceSendAfter: "120s", Concurrency: 0}
	r.forceAfter = 120 * time.Second
	r.cache = make(map[string]lastSend)
	r.jobs = make(chan func(), 1)
	r.jobs <- func() {}
	r.mu.Unlock()

	r.ruleStore.Set(rules.Rules{
		Systemd: []rules.RuleSystemd{
			{Unit: "nginx.service", Components: []string{"501"}},
		},
	})

	ctx := context.Background()
	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 2 }, 300*time.Millisecond) // host + 501=0

	post.Reset()
	r.ruleStore.Set(rules.Rules{
		Systemd: []rules.RuleSystemd{
			{Unit: "nginx.service", Components: []string{"501"}},
			{Unit: "redis.service", Components: []string{"502"}},
		},
	})

	r.scanOnce(ctx)
	waitUntil(t, func() bool { return post.Count() == 1 }, 300*time.Millisecond)

	ss := post.Snapshot()
	if len(ss) != 1 || ss[0].IsHost || ss[0].CompID != "502" || ss[0].Status != 1 {
		t.Fatalf("want one comp event 502=1 after rules update, got: %+v", ss)
	}
}
