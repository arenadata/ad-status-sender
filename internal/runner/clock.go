package runner

import "time"

type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTicker(d time.Duration) Ticker {
	t := time.NewTicker(d)
	return tickerWrap{t}
}

type tickerWrap struct{ *time.Ticker }

func (t tickerWrap) C() <-chan time.Time { return t.Ticker.C }
