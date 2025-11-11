package checktest

import (
	"os"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/check"
)

func TestDockerChecker_SkipIfNoDaemon(t *testing.T) {
	// Heuristic: if no DOCKER_HOST and default socket likely absent in CI,
	// just try NewDockerChecker and skip on error.
	chk, err := check.NewDockerChecker()
	if err != nil {
		t.Skip("docker daemon not available:", err)
	}
	// Ensure methods are callable (names that likely don't exist).
	defer func() { _ = chk }()

	if os.Getenv("CI") != "" {
		// In CI we avoid actually querying the daemon; the constructor is enough.
		t.Skip("skip runtime docker queries in CI")
	}
	_ = chk.AllRunningNames(t.Context(), []string{"non-existent-container-xyz"})
	_ = chk.AllRunningByLabels(t.Context(), []string{"this=does-not-exist"})
}
