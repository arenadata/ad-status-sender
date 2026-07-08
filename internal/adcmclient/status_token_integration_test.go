//go:build adcm_integration

package adcmclient

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestStatusCheckerToken_ADCMContainer(t *testing.T) {
	if os.Getenv("ADCM_TC_IMAGE") == "" {
		t.Skip("set ADCM_TC_IMAGE to an image carrying the status-checker-token endpoint")
	}
	ctx := context.Background()
	stack := startADCMWithPostgres(ctx, t)
	defer stack.Terminate(ctx)

	baseURL := containerBaseURL(ctx, t, stack.ADCM)
	rbac := waitForToken(ctx, t, baseURL)

	cli := New(baseURL, rbac, nil, nil)
	statusTok, err := cli.ObtainStatusToken(ctx)
	if err != nil {
		t.Fatalf("ObtainStatusToken: %v", err)
	}
	if statusTok == "" {
		t.Fatal("status-checker token is empty")
	}
	// The status secret is a different auth domain from the rbac token.
	if statusTok == rbac {
		t.Fatalf("status token equals rbac token (%q) — endpoint returned the wrong secret", statusTok)
	}
	t.Logf("obtained status-checker token (len=%d), distinct from rbac token", len(statusTok))
}

// TestStatusTwoDomainAuth exercises the full two-domain flow against a real
// ADCM: the daemon fetches the status secret from the endpoint and posts host +
// component status with it (accepted), while the rbac token — the wrong auth
// domain for the status server — is rejected.
func TestStatusTwoDomainAuth_ADCMContainer(t *testing.T) {
	if os.Getenv("ADCM_TC_IMAGE") == "" {
		t.Skip("set ADCM_TC_IMAGE to an image carrying the status-checker-token endpoint (feature/status-token-api)")
	}
	ctx := context.Background()
	stack := startADCMWithPostgres(ctx, t)
	defer stack.Terminate(ctx)

	baseURL := containerBaseURL(ctx, t, stack.ADCM)
	rbac := waitForToken(ctx, t, baseURL)
	rc := &restClient{base: strings.TrimRight(baseURL, "/"), token: rbac, hc: newHTTPClient()}
	sh := provisionSharedHost(ctx, t, rc)

	// Positive: the daemon's real flow — fetch the status secret from the
	// endpoint and post with it. Host and component status must be accepted.
	good := New(baseURL, rbac, nil, nil)
	good.SetStatusTokenProvider(good.ObtainStatusToken)
	if err := good.PostHostStatus(ctx, sh.originalHostID, 0); err != nil {
		t.Fatalf("PostHostStatus with fetched status secret should succeed: %v", err)
	}
	if err := good.PostComponentStatus(ctx, sh.originalHostID, strconv.Itoa(sh.component1ID), 0); err != nil {
		t.Fatalf("PostComponentStatus with fetched status secret should succeed: %v", err)
	}

	// Negative control: a client with only the rbac token (no status provider)
	// posts with the rbac token, which the status server must reject.
	bad := New(baseURL, rbac, nil, nil)
	err := bad.PostHostStatus(ctx, sh.originalHostID, 0)
	if err == nil {
		t.Fatal("status server must reject the rbac token (two-domain auth)")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected an auth rejection (401/403) from status server, got: %v", err)
	}
	t.Logf("two-domain auth confirmed: status secret accepted, rbac token rejected (%v)", err)
}
