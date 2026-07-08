package runnertest

import (
	"context"
	"sync"
)

type Sent struct {
	URL    string
	HostID int
	CompID string
	Status int
	IsHost bool
}

type FakePoster struct {
	mu   sync.Mutex
	Sent []Sent
}

func (f *FakePoster) PostHost(_ context.Context, status int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, Sent{IsHost: true, Status: status})
	return nil
}
func (f *FakePoster) PostComponent(_ context.Context, hostID int, compID string, status int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, Sent{IsHost: false, HostID: hostID, CompID: compID, Status: status})
	return nil
}
