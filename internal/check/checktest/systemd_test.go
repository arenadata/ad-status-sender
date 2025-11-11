package checktest

import (
	"runtime"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/check"
)

func TestSystemdHelpers_SkipIfUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd tests only on linux")
	}

	cli, err := check.NewSystemdClient(t.Context())
	if err != nil {
		t.Skipf("systemd dbus not available: %v", err)
	}
	defer func() { _ = cli.Close() }()

	_ = cli.SystemdStatus(t.Context(), "unknown.service")
	_ = cli.ExpandUnitsByGlob(t.Context(), "ssh*.service")
}
