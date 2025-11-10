package runner

import (
	"testing"
	"time"
)

func TestShouldSendCache(t *testing.T) {
	r := &Runner{
		cache:      make(map[string]lastSend),
		forceAfter: 200 * time.Millisecond,
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

	// wait ≥ forceAfter → send even if unchanged
	time.Sleep(r.forceAfter + 30*time.Millisecond)
	if !r.shouldSend(key, 1, r.forceAfter) {
		t.Fatalf("force resend must trigger after forceAfter")
	}
}
