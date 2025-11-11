package runner

import (
	"context"
)

type Poster interface {
	PostHost(ctx context.Context, status int) error
	PostComponent(ctx context.Context, compID string, status int) error
}
