package runner

import (
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time                   { return f.now }
func (f *fakeClock) NewTicker(_ time.Duration) Ticker { return nil }
func (f *fakeClock) advance(d time.Duration)          { f.now = f.now.Add(d) }

func TestShouldSendCache(t *testing.T) {
	clk := &fakeClock{now: time.Unix(0, 0)}
	r := &Runner{
		cache:      make(map[string]lastSend),
		forceAfter: 200 * time.Millisecond,
		clk:        clk,
	}
	key := "comp:1:42"

	// first send: no cache → should send
	if !r.shouldSend(key, 0, r.forceAfter) {
		t.Fatalf("first send should be allowed")
	}
	r.markSent(key, 0)

	// same status, before forceAfter → no send
	if r.shouldSend(key, 0, r.forceAfter) {
		t.Fatalf("should NOT send before forceAfter if status unchanged")
	}

	// status changed → send
	if !r.shouldSend(key, 1, r.forceAfter) {
		t.Fatalf("status change must trigger send")
	}
	r.markSent(key, 1)

	// force resend after timeout
	clk.advance(210 * time.Millisecond)
	if !r.shouldSend(key, 1, r.forceAfter) {
		t.Fatalf("force resend must trigger after forceAfter")
	}
}
