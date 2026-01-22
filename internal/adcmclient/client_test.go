package adcmclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClient_GetClusterTopologyByHostName_PaginationAndJoin(t *testing.T) {
	var calls int32
	page1 := hostListResponse{
		Count: 2,
		Results: []HostObject{
			{
				ID:   1,
				Name: "h1.example",
				Cluster: &clusterRef{
					ID:   10,
					Name: "cl",
				},
				Components: []ComponentShort{{ID: 101, Name: "n1", DisplayName: "N1"}},
			},
		},
	}
	page2 := hostListResponse{
		Count: 2,
		Results: []HostObject{
			{
				ID:   2,
				Name: "h2.example",
				Cluster: &clusterRef{
					ID:   10,
					Name: "cl",
				},
				Components: []ComponentShort{{ID: 102, Name: "n2", DisplayName: "N2"}},
			},
		},
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/hosts/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch atomic.AddInt32(&calls, 1) {
		case 1:
			_ = json.NewEncoder(w).Encode(struct {
				Count    int          `json:"count"`
				Next     *string      `json:"next"`
				Previous *string      `json:"previous"`
				Results  []HostObject `json:"results"`
			}{Count: page1.Count, Next: strPtr(srv.URL + "/api/v2/hosts/?page=2"), Results: page1.Results})
		case 2:
			_ = json.NewEncoder(w).Encode(struct {
				Count    int          `json:"count"`
				Next     *string      `json:"next"`
				Previous *string      `json:"previous"`
				Results  []HostObject `json:"results"`
			}{Count: page2.Count, Next: nil, Results: page2.Results})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	cli := New(srv.URL, "", srv.Client(), nil)
	ctx := context.Background()

	got, err := cli.GetClusterTopologyByHostName(ctx, "h1.example")
	if err != nil {
		t.Fatalf("GetClusterTopologyByHostName err: %v", err)
	}
	if got.ClusterID != 10 || got.ClusterName != "cl" {
		t.Fatalf("bad cluster: %+v", got)
	}
	if len(got.Hosts) != 2 {
		t.Fatalf("want 2 hosts, got %d", len(got.Hosts))
	}
}

func TestClient_GetClusterTopologyByHostName_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(struct {
			Count   int          `json:"count"`
			Next    *string      `json:"next"`
			Results []HostObject `json:"results"`
		}{Count: 0, Results: nil})
	}))
	defer srv.Close()

	cli := New(srv.URL, "", srv.Client(), nil)
	_, err := cli.GetClusterTopologyByHostName(context.Background(), "missing")
	if err == nil {
		t.Fatalf("expected error for missing host")
	}
}

func strPtr(s string) *string { return &s }
