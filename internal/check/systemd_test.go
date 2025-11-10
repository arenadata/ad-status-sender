package check

import (
	"runtime"
	"testing"
)

func TestSystemdHelpers_SkipIfUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd tests only on linux")
	}

	cli, err := NewSystemdClient(t.Context())
	if err != nil {
		t.Skipf("systemd dbus not available: %v", err)
	}
	defer func() { _ = cli.Close() }()

	_ = cli.SystemdStatus(t.Context(), "unknown.service")
	_ = cli.ExpandUnitsByGlob(t.Context(), "ssh*.service")
}
