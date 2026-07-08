//go:build adcm_integration

// Live-ADCM integration test for the shared-host (host duplicate) contract.
//
// Boots a real ADCM (arenadata/adcm image) + Postgres via testcontainers,
// uploads the ADO cluster bundle + SSH host-provider bundle from testdata,
// provisions a shared host (original in cluster #1, duplicate in cluster #2,
// each mapped to the adpg component), then asserts — using the PRODUCTION
// client methods — every ADCM response shape the shared-host import depends on:
//   - GET /hosts/<original>/ exposes duplicates[] with the duplicate id+cluster
//   - GET /hosts/<duplicate>/ carries the duplicate's own cluster + components
//   - the service config's components map decodes as map[string]string and its
//     value is the systemd JSON the importer parses ({"systemd":{"service_name"}})
//
// FromADCM's aggregation/host-tagging over these inputs is deterministic and is
// covered by the httptest unit tests; this test proves the inputs are real.
//
// Run: go test -tags adcm_integration -run TestSharedHost ./internal/adcmclient/
// Requires Docker. Slow (boots ADCM). Excluded from the default build.
package adcmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	adoClusterBundle  = "testdata/adcm_cluster_ado_v2.11.1_arenadata1_b1-1_community.tgz"
	sshProviderBundle = "testdata/adcm_host_ssh_v3.1.0-1_community.tgz"
	adpgServiceName   = "adpg"
	adpgComponentName = "adpg"
	adpgExpectedUnit  = "adpg16" // components.adpg -> {"systemd":{"service_name":"adpg16"}}
)

func TestSharedHost_ImportContract_ADCMContainer(t *testing.T) {
	ctx := context.Background()
	stack := startADCMWithPostgres(ctx, t)
	defer stack.Terminate(ctx)

	baseURL := containerBaseURL(ctx, t, stack.ADCM)
	token := waitForToken(ctx, t, baseURL)
	rc := &restClient{base: strings.TrimRight(baseURL, "/"), token: token, hc: newHTTPClient()}

	sh := provisionSharedHost(ctx, t, rc)
	t.Logf("provisioned: original=%d duplicate=%d cluster1=%d cluster2=%d comp1=%d comp2=%d",
		sh.originalHostID, sh.duplicateHostID, sh.cluster1ID, sh.cluster2ID, sh.component1ID, sh.component2ID)

	cli := New(baseURL, token, nil, nil)

	// 1. The original exposes the duplicate.
	orig, err := cli.GetHost(ctx, sh.originalHostID)
	if err != nil {
		t.Fatalf("GetHost(original): %v", err)
	}
	if !hasDuplicate(orig.Duplicates, sh.duplicateHostID, sh.cluster2ID) {
		t.Fatalf("original host %d missing duplicate %d in cluster %d: %+v",
			sh.originalHostID, sh.duplicateHostID, sh.cluster2ID, orig.Duplicates)
	}

	// 2. The duplicate carries its own cluster + components.
	dup, err := cli.GetHost(ctx, sh.duplicateHostID)
	if err != nil {
		t.Fatalf("GetHost(duplicate): %v", err)
	}
	if dup.Cluster == nil || dup.Cluster.ID != sh.cluster2ID {
		t.Fatalf("duplicate cluster = %+v, want id %d", dup.Cluster, sh.cluster2ID)
	}
	if !hasComponentName(dup.Components, adpgComponentName) {
		t.Fatalf("duplicate components missing %q: %+v", adpgComponentName, dup.Components)
	}

	// 3. The crux: the service config's components map decodes as map[string]string
	// and its value is the systemd JSON the importer parses — on BOTH clusters.
	assertSystemdConfig(ctx, t, cli, sh.cluster1ID, sh.service1ID)
	assertSystemdConfig(ctx, t, cli, sh.cluster2ID, sh.service2ID)
}

// assertSystemdConfig fetches the current service config via the production
// client and asserts components[adpg] is the systemd JSON string.
func assertSystemdConfig(ctx context.Context, t *testing.T, cli *Client, clusterID, serviceID int) {
	t.Helper()
	cfgID, err := cli.CurrentServiceConfigID(ctx, clusterID, serviceID)
	if err != nil {
		t.Fatalf("CurrentServiceConfigID(c=%d,s=%d): %v", clusterID, serviceID, err)
	}
	if cfgID == 0 {
		t.Fatalf("no current config for cluster=%d service=%d", clusterID, serviceID)
	}
	cfg, err := cli.GetServiceConfig(ctx, clusterID, serviceID, cfgID)
	if err != nil {
		// A decode error here means ADCM returned the json-type field as an
		// object, not a string — the map[string]string contract is broken.
		t.Fatalf("GetServiceConfig(c=%d,s=%d,cfg=%d): %v", clusterID, serviceID, cfgID, err)
	}
	raw, ok := cfg.Components[adpgComponentName]
	if !ok {
		t.Fatalf("service config components missing %q: %+v", adpgComponentName, cfg.Components)
	}
	var doc struct {
		Systemd struct {
			ServiceName string `json:"service_name"`
		} `json:"systemd"`
	}
	if jErr := json.Unmarshal([]byte(raw), &doc); jErr != nil {
		t.Fatalf("components[%s] not a systemd JSON string (got %q): %v", adpgComponentName, raw, jErr)
	}
	if doc.Systemd.ServiceName != adpgExpectedUnit {
		t.Fatalf("service_name = %q, want %q", doc.Systemd.ServiceName, adpgExpectedUnit)
	}
}

type sharedHost struct {
	originalHostID  int
	duplicateHostID int
	cluster1ID      int
	cluster2ID      int
	service1ID      int
	service2ID      int
	component1ID    int
	component2ID    int
}

// provisionSharedHost runs the confirmed ADCM v2 REST sequence to create a
// shared host: two clusters from the ADO bundle, a host from the SSH provider
// bundle mapped to adpg in cluster #1, and a duplicate mapped to adpg in
// cluster #2. Community bundles => no license acceptance.
func provisionSharedHost(ctx context.Context, t *testing.T, rc *restClient) sharedHost {
	t.Helper()

	clusterBundle := rc.uploadBundle(ctx, t, adoClusterBundle)
	providerBundle := rc.uploadBundle(ctx, t, sshProviderBundle)
	clusterProtoID := jInt(t, clusterBundle, "mainPrototype", "id")
	providerProtoID := jInt(t, providerBundle, "mainPrototype", "id")
	clusterBundleID := jInt(t, clusterBundle, "id")

	// Even "community" bundles may carry a license (EULA) that must be accepted
	// before the prototype can be instantiated.
	svcProtoID := rc.findPrototypeID(ctx, t, clusterBundleID, "service", adpgServiceName)
	rc.acceptLicenseIfNeeded(ctx, t, clusterProtoID)
	rc.acceptLicenseIfNeeded(ctx, t, svcProtoID)
	rc.acceptLicenseIfNeeded(ctx, t, providerProtoID)

	c1 := jInt(t, rc.post(ctx, t, "/api/v2/clusters/",
		map[string]any{"prototypeId": clusterProtoID, "name": "sh-cluster-1"}), "id")
	c2 := jInt(t, rc.post(ctx, t, "/api/v2/clusters/",
		map[string]any{"prototypeId": clusterProtoID, "name": "sh-cluster-2"}), "id")

	providerID := jInt(t, rc.post(ctx, t, "/api/v2/hostproviders/",
		map[string]any{"prototypeId": providerProtoID, "name": "sh-provider"}), "id")

	fqdn := "sh-host-1"
	hostID := jInt(t, rc.post(ctx, t, "/api/v2/hosts/",
		map[string]any{"name": fqdn, "hostproviderId": providerID}), "id")
	rc.post(ctx, t, fmt.Sprintf("/api/v2/clusters/%d/hosts/", c1),
		map[string]any{"hostId": hostID})

	s1 := jInt(t, rc.post(ctx, t, fmt.Sprintf("/api/v2/clusters/%d/services/", c1),
		map[string]any{"prototypeId": svcProtoID}), "id")
	s2 := jInt(t, rc.post(ctx, t, fmt.Sprintf("/api/v2/clusters/%d/services/", c2),
		map[string]any{"prototypeId": svcProtoID}), "id")

	comp1 := rc.componentID(ctx, t, c1, s1, adpgComponentName)
	comp2 := rc.componentID(ctx, t, c2, s2, adpgComponentName)

	rc.postList(ctx, t, fmt.Sprintf("/api/v2/clusters/%d/mapping/", c1),
		[]map[string]any{{"hostId": hostID, "componentId": comp1}})

	dup := rc.post(ctx, t, fmt.Sprintf("/api/v2/hosts/%d/duplicates/", hostID),
		map[string]any{"name": "sh-host-1-dup", "clusterId": c2})
	dupID := jInt(t, dup, "id")

	rc.postList(ctx, t, fmt.Sprintf("/api/v2/clusters/%d/mapping/", c2),
		[]map[string]any{{"hostId": dupID, "componentId": comp2}})

	return sharedHost{
		originalHostID: hostID, duplicateHostID: dupID,
		cluster1ID: c1, cluster2ID: c2,
		service1ID: s1, service2ID: s2,
		component1ID: comp1, component2ID: comp2,
	}
}

// --- raw REST helper (test-only provisioning; the production client stays read+status) ---

type restClient struct {
	base  string
	token string
	hc    *http.Client
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

func (r *restClient) do(ctx context.Context, method, path string, body io.Reader, contentType string) ([]byte, int) {
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, body)
	if err != nil {
		return nil, 0
	}
	req.Header.Set("Authorization", "Token "+r.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return []byte(err.Error()), 0
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode
}

func (r *restClient) post(ctx context.Context, t *testing.T, path string, body any) map[string]any {
	t.Helper()
	return r.decodeObj(t, path, r.send(ctx, t, http.MethodPost, path, body))
}

func (r *restClient) postList(ctx context.Context, t *testing.T, path string, body any) {
	t.Helper()
	r.send(ctx, t, http.MethodPost, path, body)
}

func (r *restClient) send(ctx context.Context, t *testing.T, method, path string, body any) []byte {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	data, code := r.do(ctx, method, path, bytes.NewReader(buf), "application/json")
	if code/100 != 2 {
		t.Fatalf("%s %s -> %d: %s", method, path, code, strings.TrimSpace(string(data)))
	}
	return data
}

func (r *restClient) decodeObj(t *testing.T, path string, data []byte) map[string]any {
	t.Helper()
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("decode object from %s (%s): %v", path, truncate(data), err)
	}
	return obj
}

func (r *restClient) uploadBundle(ctx context.Context, t *testing.T, relPath string) map[string]any {
	t.Helper()
	abs, err := filepath.Abs(relPath)
	if err != nil {
		t.Fatalf("abs %s: %v", relPath, err)
	}
	f, err := os.Open(abs)
	if err != nil {
		t.Fatalf("open bundle %s: %v", abs, err)
	}
	defer func() { _ = f.Close() }()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(abs))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, cpErr := io.Copy(part, f); cpErr != nil {
		t.Fatalf("copy bundle: %v", cpErr)
	}
	if clErr := mw.Close(); clErr != nil {
		t.Fatalf("close multipart: %v", clErr)
	}
	data, code := r.do(ctx, http.MethodPost, "/api/v2/bundles/", &buf, mw.FormDataContentType())
	if code/100 != 2 {
		t.Fatalf("upload %s -> %d: %s", filepath.Base(abs), code, strings.TrimSpace(string(data)))
	}
	return r.decodeObj(t, "/api/v2/bundles/", data)
}

func (r *restClient) acceptLicenseIfNeeded(ctx context.Context, t *testing.T, prototypeID int) {
	t.Helper()
	path := fmt.Sprintf("/api/v2/prototypes/%d/", prototypeID)
	data, code := r.do(ctx, http.MethodGet, path, nil, "")
	if code/100 != 2 {
		t.Fatalf("GET %s -> %d: %s", path, code, strings.TrimSpace(string(data)))
	}
	obj := r.decodeObj(t, path, data)
	lic, _ := obj["license"].(map[string]any)
	if lic == nil {
		return
	}
	if s, _ := lic["status"].(string); s != "unaccepted" {
		return
	}
	accept := fmt.Sprintf("/api/v2/prototypes/%d/license/accept/", prototypeID)
	if body, ac := r.do(ctx, http.MethodPost, accept, nil, "application/json"); ac/100 != 2 {
		t.Fatalf("POST %s -> %d: %s", accept, ac, strings.TrimSpace(string(body)))
	}
}

func (r *restClient) findPrototypeID(ctx context.Context, t *testing.T, bundleID int, protoType, name string) int {
	t.Helper()
	path := fmt.Sprintf("/api/v2/prototypes/?bundleId=%d&type=%s", bundleID, protoType)
	data, code := r.do(ctx, http.MethodGet, path, nil, "")
	if code/100 != 2 {
		t.Fatalf("GET %s -> %d: %s", path, code, strings.TrimSpace(string(data)))
	}
	for _, it := range decodeResults(t, data) {
		if s, _ := it["name"].(string); s == name {
			return toInt(it["id"])
		}
	}
	t.Fatalf("prototype type=%s name=%s not found in bundle %d: %s", protoType, name, bundleID, truncate(data))
	return 0
}

func (r *restClient) componentID(ctx context.Context, t *testing.T, clusterID, serviceID int, name string) int {
	t.Helper()
	path := fmt.Sprintf("/api/v2/clusters/%d/services/%d/components/", clusterID, serviceID)
	data, code := r.do(ctx, http.MethodGet, path, nil, "")
	if code/100 != 2 {
		t.Fatalf("GET %s -> %d: %s", path, code, strings.TrimSpace(string(data)))
	}
	for _, it := range decodeResults(t, data) {
		if s, _ := it["name"].(string); s == name {
			return toInt(it["id"])
		}
	}
	t.Fatalf("component %s not found in cluster=%d service=%d: %s", name, clusterID, serviceID, truncate(data))
	return 0
}

// --- decode helpers ---

func decodeResults(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	// v2 list endpoints may be paginated ({results:[...]}) or a bare array.
	var paged struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(data, &paged); err == nil && paged.Results != nil {
		return paged.Results
	}
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr
	}
	t.Fatalf("cannot decode list: %s", truncate(data))
	return nil
}

func jInt(t *testing.T, obj map[string]any, keys ...string) int {
	t.Helper()
	var cur any = obj
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object", keys, k)
		}
		cur = m[k]
	}
	return toInt(cur)
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func hasDuplicate(ds []HostDuplicate, id, clusterID int) bool {
	for _, d := range ds {
		if d.ID == id && d.Cluster != nil && d.Cluster.ID == clusterID {
			return true
		}
	}
	return false
}

func hasComponentName(cs []ComponentShort, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

func truncate(b []byte) string {
	const limit = 400
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

// waitForToken polls the token endpoint until ADCM finishes booting (admin user
// created, migrations applied).
func waitForToken(ctx context.Context, t *testing.T, baseURL string) string {
	t.Helper()
	user := getenvDefault("ADCM_ADMIN_USER", "admin")
	pass := getenvDefault("ADCM_ADMIN_PASS", "admin")
	cli := New(baseURL, "", nil, nil)
	deadline := time.Now().Add(3 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		tok, err := cli.ObtainToken(ctx, user, pass)
		if err == nil && tok != "" {
			return tok
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("ADCM token not obtainable within timeout: %v", lastErr)
	return ""
}
