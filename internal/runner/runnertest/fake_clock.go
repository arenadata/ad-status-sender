package runnertest

import (
	"time"

	"github.com/arenadata/ad-status-sender/internal/runner"
)

const tickerBuf = 16

type FakeClock struct {
	now time.Time
	ch  chan time.Time
}

func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start, ch: make(chan time.Time, tickerBuf)}
}

func (c *FakeClock) Now() time.Time { return c.now }

func (c *FakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func (c *FakeClock) Tick() { c.ch <- c.now }

type fakeTicker struct{ ch <-chan time.Time }

func (t fakeTicker) C() <-chan time.Time { return t.ch }
func (t fakeTicker) Stop()               {}

func (c *FakeClock) NewTicker(_ time.Duration) runner.Ticker {
	return fakeTicker{c.ch}
}
