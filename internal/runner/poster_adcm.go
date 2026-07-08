package runner

import (
	"context"
	"errors"
	"log/slog"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
)

type adcmPoster struct {
	log       *slog.Logger
	client    *adcmclient.Client
	hostID    int
	logBodies bool
}

func (p *adcmPoster) PostHost(ctx context.Context, status int) error {
	if p.client == nil {
		return errors.New("adcm client is nil")
	}
	return p.client.PostHostStatus(ctx, p.hostID, status)
}

func (p *adcmPoster) PostComponent(ctx context.Context, hostID int, compID string, status int) error {
	if p.client == nil {
		return errors.New("adcm client is nil")
	}
	return p.client.PostComponentStatus(ctx, hostID, compID, status)
}
