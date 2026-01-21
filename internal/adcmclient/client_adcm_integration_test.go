package adcmclient

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestClient_GetClusterTopologyByHostName_ADCM(t *testing.T) {
	baseURL := os.Getenv("ADCM_BASE_URL")
	token := os.Getenv("ADCM_TOKEN")
	fqdn := os.Getenv("ADCM_HOST_FQDN")
	if baseURL == "" || token == "" || fqdn == "" {
		t.Skip("set ADCM_BASE_URL, ADCM_TOKEN, ADCM_HOST_FQDN to run")
	}

	cli := New(baseURL, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	got, err := cli.GetClusterTopologyByHostName(ctx, fqdn)
	if err != nil {
		t.Fatalf("GetClusterTopologyByHostName err: %v", err)
	}
	if got.ClusterID == 0 || got.ClusterName == "" {
		t.Fatalf("unexpected cluster data: %+v", got)
	}
	if len(got.Hosts) == 0 {
		t.Fatalf("expected at least one host in topology")
	}
}
