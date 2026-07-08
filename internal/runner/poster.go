package runner

import (
	"context"
)

type Poster interface {
	PostHost(ctx context.Context, status int) error
	PostComponent(ctx context.Context, hostID int, compID string, status int) error
}
