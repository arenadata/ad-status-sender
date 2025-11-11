package check

import "context"

type Systemd interface {
	SystemdStatus(ctx context.Context, unit string) int
	ExpandUnitsByGlob(ctx context.Context, glob string) []string
}

type Docker interface {
	AllRunningNames(ctx context.Context, names []string) int
	AllRunningByLabels(ctx context.Context, labels []string) int
}
