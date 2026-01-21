package adcmclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	adcmImageDefault = "arenadata/adcm:2.9.2"
	adcmPort         = "8000/tcp"
	pgImageDefault   = "postgres:14"
	pgPort           = "5432/tcp"
)

func TestADCMContainer_Ready(t *testing.T) {
	ctx := context.Background()
	stack := startADCMWithPostgres(ctx, t)
	defer stack.Terminate(ctx)

	baseURL := containerBaseURL(ctx, t, stack.ADCM)
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v2/hosts/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v2/hosts/: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusUnauthorized &&
		resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
}

func TestClient_GetClusterTopologyByHostName_ADCMContainer(t *testing.T) {
	ctx := context.Background()
	stack := startADCMWithPostgres(ctx, t)
	defer stack.Terminate(ctx)

	if cmd := os.Getenv("ADCM_BOOTSTRAP_CMD"); cmd != "" {
		if err := execInContainer(ctx, stack.ADCM, cmd); err != nil {
			t.Fatalf("bootstrap failed: %v", err)
		}
	}

	token := os.Getenv("ADCM_TOKEN")
	fqdn := os.Getenv("ADCM_HOST_FQDN")
	if token == "" || fqdn == "" {
		t.Skip("set ADCM_TOKEN and ADCM_HOST_FQDN (and optionally ADCM_BOOTSTRAP_CMD) to run")
	}

	baseURL := containerBaseURL(ctx, t, stack.ADCM)
	cli := New(baseURL, token, nil, nil)

	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	got, err := cli.GetClusterTopologyByHostName(callCtx, fqdn)
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

func TestADCM_TokenEntityAndStatus(t *testing.T) {
	ctx := context.Background()
	stack := startADCMWithPostgres(ctx, t)
	defer stack.Terminate(ctx)

	baseURL := containerBaseURL(ctx, t, stack.ADCM)
	token := tokenForTest(ctx, t, baseURL)
	cli := New(baseURL, token, nil, nil)

	hostID := hostIDForTest(ctx, t, cli)
	if err := cli.PostHostStatus(ctx, hostID, 0); err != nil {
		t.Fatalf("post host status: %v", err)
	}

	compID := componentIDForTest(ctx, t, cli, hostID)
	if err := cli.PostComponentStatus(ctx, hostID, compID, 0); err != nil {
		t.Fatalf("post component status: %v", err)
	}
}

func tokenForTest(ctx context.Context, t *testing.T, baseURL string) string {
	t.Helper()
	token := os.Getenv("ADCM_TOKEN")
	if token != "" {
		return token
	}
	user := getenvDefault("ADCM_ADMIN_USER", "admin")
	pass := getenvDefault("ADCM_ADMIN_PASS", "admin")
	cli := New(baseURL, "", nil, nil)
	token, err := cli.ObtainToken(ctx, user, pass)
	if err != nil {
		t.Fatalf("obtain token: %v", err)
	}
	if token == "" {
		t.Fatalf("empty token received")
	}
	return token
}

func hostIDForTest(ctx context.Context, t *testing.T, cli *Client) int {
	t.Helper()
	hostID := envInt("ADCM_HOST_ID")
	if hostID != 0 {
		return hostID
	}
	hostFQDN := os.Getenv("ADCM_HOST_FQDN")
	if hostFQDN == "" {
		t.Skip("set ADCM_HOST_ID or ADCM_HOST_FQDN to run")
	}
	id, err := cli.FindHostID(ctx, hostFQDN)
	if err != nil {
		t.Fatalf("find host: %v", err)
	}
	if id != 0 {
		return id
	}
	if createErr := cli.CreateHost(ctx, hostFQDN); createErr != nil {
		t.Fatalf("create host: %v", createErr)
	}
	id, err = cli.FindHostID(ctx, hostFQDN)
	if err != nil {
		t.Fatalf("find host after create: %v", err)
	}
	if id == 0 {
		t.Fatalf("host %q not found after create", hostFQDN)
	}
	return id
}

func componentIDForTest(ctx context.Context, t *testing.T, cli *Client, hostID int) string {
	t.Helper()
	compID := os.Getenv("ADCM_COMPONENT_ID")
	if compID != "" {
		return compID
	}
	compID, _ = cli.FirstComponentID(ctx, hostID)
	if compID == "" {
		t.Skip("no component id found; set ADCM_COMPONENT_ID to test component status")
	}
	return compID
}

type adcmStack struct {
	ADCM     testcontainers.Container
	Postgres testcontainers.Container
	Network  *testcontainers.DockerNetwork
	Volume   string
}

func (s *adcmStack) Terminate(ctx context.Context) {
	if s.ADCM != nil {
		if s.Volume != "" {
			_ = s.ADCM.Terminate(ctx, testcontainers.RemoveVolumes(s.Volume))
		} else {
			_ = s.ADCM.Terminate(ctx)
		}
	}
	if s.Postgres != nil {
		_ = s.Postgres.Terminate(ctx)
	}
	if s.Network != nil {
		_ = s.Network.Remove(ctx)
	}
}

func startADCMWithPostgres(ctx context.Context, t *testing.T) *adcmStack {
	t.Helper()
	network, err := tcnetwork.New(ctx, tcnetwork.WithAttachable())
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	netName := network.Name

	pgImage := getenvDefault("ADCM_TC_PG_IMAGE", pgImageDefault)
	pgUser := getenvDefault("ADCM_TC_PG_USER", "adcm")
	pgPass := getenvDefault("ADCM_TC_PG_PASS", "adcm")
	pgDB := getenvDefault("ADCM_TC_PG_DB", "adcm")
	pgAlias := "adcm-postgres"

	pgReq := testcontainers.ContainerRequest{
		Image:        pgImage,
		ExposedPorts: []string{pgPort},
		Env: map[string]string{
			"POSTGRES_USER":     pgUser,
			"POSTGRES_PASSWORD": pgPass,
			"POSTGRES_DB":       pgDB,
		},
		Networks:       []string{netName},
		NetworkAliases: map[string][]string{netName: {pgAlias}},
		WaitingFor:     wait.ForListeningPort(pgPort).WithStartupTimeout(2 * time.Minute),
	}

	pg, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: pgReq,
		Started:          true,
	})
	if err != nil {
		_ = network.Remove(ctx)
		t.Fatalf("start postgres container: %v", err)
	}

	volumeName := fmt.Sprintf("adcm-data-%d", time.Now().UnixNano())
	adcm, err := startADCMContainer(ctx, t, netName, pgAlias, pgUser, pgPass, pgDB, volumeName)
	if err != nil {
		_ = pg.Terminate(ctx)
		_ = network.Remove(ctx)
		t.Fatalf("start adc container: %v", err)
	}
	return &adcmStack{ADCM: adcm, Postgres: pg, Network: network, Volume: volumeName}
}

func startADCMContainer(
	ctx context.Context,
	t *testing.T,
	networkName string,
	dbHost, dbUser, dbPass, dbName string,
	volumeName string,
) (testcontainers.Container, error) {
	t.Helper()
	startupTimeout := 5 * time.Minute
	if raw := os.Getenv("ADCM_TC_STARTUP_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			startupTimeout = d
		} else {
			t.Fatalf("invalid ADCM_TC_STARTUP_TIMEOUT: %v", err)
		}
	}
	env := envFromPrefix("ADCM_TC_ENV_")
	if env == nil {
		env = map[string]string{}
	}
	if path := os.Getenv("ADCM_TC_ENV_FILE"); path != "" {
		fileEnv, err := readEnvFile(path)
		if err != nil {
			t.Fatalf("read ADCM_TC_ENV_FILE: %v", err)
		}
		for k, v := range fileEnv {
			env[k] = v
		}
	}
	setIfMissing(env, "DB_HOST", dbHost)
	setIfMissing(env, "DB_PORT", "5432")
	setIfMissing(env, "DB_USER", dbUser)
	setIfMissing(env, "DB_PASS", dbPass)
	setIfMissing(env, "DB_NAME", dbName)
	setIfMissing(env, "DB_OPTIONS", "{}")

	image := getenvDefault("ADCM_TC_IMAGE", adcmImageDefault)
	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{adcmPort},
		WaitingFor: wait.ForHTTP("/api/v2/hosts/").
			WithPort(adcmPort).
			WithStatusCodeMatcher(func(code int) bool {
				return code == http.StatusOK ||
					code == http.StatusUnauthorized ||
					code == http.StatusForbidden
			}).
			WithStartupTimeout(startupTimeout),
		Env:        env,
		Privileged: os.Getenv("ADCM_TC_PRIVILEGED") == "1",
		Networks:   []string{networkName},
	}
	if volumeName != "" {
		req.Mounts = testcontainers.Mounts(
			testcontainers.VolumeMount(volumeName, "/adcm/data"),
		)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          false,
	})
	if err != nil {
		return nil, err
	}
	if startErr := c.Start(ctx); startErr != nil {
		dumpContainerLogs(ctx, t, c)
		_ = c.Terminate(ctx)
		return nil, startErr
	}
	return c, nil
}

func containerBaseURL(ctx context.Context, t *testing.T, c testcontainers.Container) string {
	t.Helper()
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, adcmPort)
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	return "http://" + net.JoinHostPort(host, port.Port())
}

func execInContainer(ctx context.Context, c testcontainers.Container, cmd string) error {
	rc, _, err := c.Exec(ctx, []string{"sh", "-lc", cmd})
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("command exited with status %d", rc)
	}
	return nil
}

func envFromPrefix(prefix string) map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if !strings.HasPrefix(parts[0], prefix) {
			continue
		}
		out[strings.TrimPrefix(parts[0], prefix)] = parts[1]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	lines := strings.Split(string(data), "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		parts := strings.SplitN(ln, "=", 2)
		if len(parts) != 2 {
			continue
		}
		out[parts[0]] = parts[1]
	}
	return out, nil
}

func dumpContainerLogs(ctx context.Context, t *testing.T, c testcontainers.Container) {
	t.Helper()
	r, err := c.Logs(ctx)
	if err != nil {
		t.Logf("container logs error: %v", err)
		return
	}
	defer func() { _ = r.Close() }()
	data, rdErr := io.ReadAll(r)
	if rdErr != nil {
		t.Logf("container logs read error: %v", rdErr)
		return
	}
	if len(data) == 0 {
		t.Log("container logs empty")
		return
	}
	t.Logf("container logs:\n%s", string(data))
}

func setIfMissing(m map[string]string, key, value string) {
	if m == nil {
		return
	}
	if _, ok := m[key]; ok {
		return
	}
	m[key] = value
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// token/host/status helpers moved to internal/adcmclient/helpers.go

func envInt(key string) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0
	}
	var out int
	_, _ = fmt.Sscanf(v, "%d", &out)
	return out
}
