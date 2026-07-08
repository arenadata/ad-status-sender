package adcmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type TokenProvider func(context.Context) (string, error)

// HTTPStatusError is returned when ADCM answers with a non-2xx status, so
// callers can distinguish permanent (404/403) from transient (5xx) failures.
type HTTPStatusError struct {
	Code int
	Body string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("http status %d: %s", e.Code, e.Body)
}

// HTTPStatusCode reports the ADCM status code carried by err, if any. A missing
// code (network/transport error) returns ok=false.
func HTTPStatusCode(err error) (int, bool) {
	var e *HTTPStatusError
	if errors.As(err, &e) {
		return e.Code, true
	}
	return 0, false
}

const (
	defaultHTTPTimeout = 10 * time.Second
	statusClassSuccess = 2

	headerAccept      = "Accept"
	headerContentType = "Content-Type"
	mimeJSON          = "application/json"

	//nolint:gosec // G101: URL path, not a credential
	statusCheckerTokenPath = "/api/v2/adcm/status-checker-token/"
	// statusProbeCooldown throttles status-token endpoint retries after a failure
	// (e.g. 404 on an ADCM image without the endpoint).
	statusProbeCooldown = 60 * time.Second
)

type Client struct {
	baseURL       string
	token         string
	tokenProvider TokenProvider
	tokenMu       sync.RWMutex
	http          *http.Client
	log           *slog.Logger
	logBodies     atomic.Bool

	// Status POSTs use a shared secret, a different auth domain from the rbac token
	// used for /api/v2/*. statusTokenProvider fetches it in rules_source=adcm;
	// statusToken caches it. Unset/unavailable provider falls back to token.
	statusToken         string
	statusTokenProvider TokenProvider
	statusProbeAfter    time.Time
	statusFetchMu       sync.Mutex
}

func New(baseURL, token string, httpClient *http.Client, logger *slog.Logger) *Client {
	return NewWithTokenProvider(baseURL, token, nil, httpClient, logger)
}

func NewWithTokenProvider(
	baseURL, token string,
	tokenProvider TokenProvider,
	httpClient *http.Client,
	logger *slog.Logger,
) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		token:         strings.TrimSpace(token),
		tokenProvider: tokenProvider,
		http:          httpClient,
		log:           logger,
	}
}

type HostTopology struct {
	ClusterID   int            `json:"cluster_id"`
	ClusterName string         `json:"cluster_name"`
	Hosts       []TopologyHost `json:"hosts"`
}

type TopologyHost struct {
	ID         int                     `json:"id"`
	Name       string                  `json:"name"`
	Components []TopologyHostComponent `json:"components"`
}

type TopologyHostComponent struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type hostListResponse struct {
	Count   int          `json:"count"`
	Results []HostObject `json:"results"`
}

type HostObject struct {
	ID         int              `json:"id"`
	Name       string           `json:"name"`
	Cluster    *clusterRef      `json:"cluster"`
	Components []ComponentShort `json:"components"`
	// Duplicates lists the shared-host duplicate rows (ids only) of an original
	// host. Present on GET /hosts/<id>/; each duplicate carries its own cluster
	// and components fetched via a separate GetHost.
	Duplicates []HostDuplicate `json:"duplicates"`
}

type HostDuplicate struct {
	ID      int         `json:"id"`
	Cluster *clusterRef `json:"cluster"`
}

type clusterRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ComponentShort struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type ServiceObject struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type serviceConfigItem struct {
	ID        int  `json:"id"`
	IsCurrent bool `json:"isCurrent"`
}

type serviceConfigResponse struct {
	ID     int `json:"id"`
	Config struct {
		Components map[string]string `json:"components"`
	} `json:"config"`
}

func (c *Client) tokenValue() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

func (c *Client) setToken(token string) {
	c.tokenMu.Lock()
	c.token = strings.TrimSpace(token)
	c.tokenMu.Unlock()
}

// SetStatusTokenProvider sets the status-secret source. Call before sharing the client.
func (c *Client) SetStatusTokenProvider(p TokenProvider) {
	c.statusTokenProvider = p
}

func (c *Client) cachedStatusToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.statusToken
}

func (c *Client) setCachedStatusToken(token string) {
	c.tokenMu.Lock()
	c.statusToken = strings.TrimSpace(token)
	c.statusProbeAfter = time.Time{}
	c.tokenMu.Unlock()
}

func (c *Client) clearCachedStatusToken() {
	c.tokenMu.Lock()
	c.statusToken = ""
	c.tokenMu.Unlock()
}

func (c *Client) startProbeCooldown() {
	c.tokenMu.Lock()
	c.statusProbeAfter = time.Now().Add(statusProbeCooldown)
	c.tokenMu.Unlock()
}

// statusProviderReady: provider set and past any failure cooldown.
func (c *Client) statusProviderReady() bool {
	if c.statusTokenProvider == nil {
		return false
	}
	c.tokenMu.RLock()
	after := c.statusProbeAfter
	c.tokenMu.RUnlock()
	return !time.Now().Before(after)
}

// resolveStatusToken returns the status POST token: cached secret, a fresh fetch,
// or token as fallback when no provider is set or the endpoint is unavailable.
func (c *Client) resolveStatusToken(ctx context.Context) string {
	if t := c.cachedStatusToken(); t != "" {
		return t
	}
	if !c.statusProviderReady() {
		return c.tokenValue()
	}
	c.statusFetchMu.Lock()
	defer c.statusFetchMu.Unlock()
	if t := c.cachedStatusToken(); t != "" {
		return t
	}
	tok, err := c.statusTokenProvider(ctx)
	if err != nil || strings.TrimSpace(tok) == "" {
		c.log.WarnContext(ctx, "status token endpoint unavailable, using configured token", "err", err)
		c.startProbeCooldown()
		return c.tokenValue()
	}
	c.setCachedStatusToken(tok)
	return strings.TrimSpace(tok)
}

// refetchStatusToken forces a fresh fetch after a 401 (rotated secret).
func (c *Client) refetchStatusToken(ctx context.Context) (string, bool) {
	c.statusFetchMu.Lock()
	defer c.statusFetchMu.Unlock()
	c.clearCachedStatusToken()
	tok, err := c.statusTokenProvider(ctx)
	if err != nil || strings.TrimSpace(tok) == "" {
		c.startProbeCooldown()
		return "", false
	}
	c.setCachedStatusToken(tok)
	return strings.TrimSpace(tok), true
}

// ObtainStatusToken fetches the status secret from the ADCM status-checker-token
// endpoint using the rbac token.
func (c *Client) ObtainStatusToken(ctx context.Context) (string, error) {
	headers := map[string]string{headerAccept: mimeJSON}
	resp, err := c.doWithAuthRetry(ctx, http.MethodPost, c.baseURL+statusCheckerTokenPath, nil, headers)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status checker token %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Token string `json:"token"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return "", decodeErr
	}
	if body.Token == "" {
		return "", errors.New("status checker token response missing token")
	}
	return body.Token, nil
}

// doStatusPost posts to the status server with the status secret, refetching once on 401.
func (c *Client) doStatusPost(
	ctx context.Context,
	url string,
	body []byte,
	headers map[string]string,
) (*http.Response, error) {
	tok := c.resolveStatusToken(ctx)
	resp, err := c.doOnce(ctx, http.MethodPost, url, body, headers, tok)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || c.statusTokenProvider == nil {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if ctx.Err() != nil {
		return resp, nil //nolint:nilerr // ctx canceled: return the response, not a fetch error
	}
	tok2, ok := c.refetchStatusToken(ctx)
	if !ok {
		return c.doOnce(ctx, http.MethodPost, url, body, headers, c.tokenValue())
	}
	return c.doOnce(ctx, http.MethodPost, url, body, headers, tok2)
}

func (c *Client) SetLogBodies(enabled bool) {
	c.logBodies.Store(enabled)
}

func (c *Client) doOnce(
	ctx context.Context,
	method, url string,
	body []byte,
	headers map[string]string,
	token string,
) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	//nolint:gosec // G704: url = operator config baseURL + fixed path + url.PathEscape'd segments
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Token "+token)
	}
	return c.http.Do(req) //nolint:gosec // G704: url = operator config baseURL + escaped path segments
}

func (c *Client) doWithAuthRetry(
	ctx context.Context,
	method, url string,
	body []byte,
	headers map[string]string,
) (*http.Response, error) {
	token := c.tokenValue()
	resp, err := c.doOnce(ctx, method, url, body, headers, token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	if c.tokenProvider == nil {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if ctx.Err() != nil {
		return resp, nil
	}
	providerCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPTimeout)
	defer cancel()
	newToken, err := c.tokenProvider(providerCtx)
	if err != nil {
		return nil, err
	}
	newToken = strings.TrimSpace(newToken)
	if newToken == "" {
		return nil, errors.New("token provider returned empty token")
	}
	c.setToken(newToken)
	return c.doOnce(ctx, method, url, body, headers, newToken)
}

func (c *Client) GetClusterTopologyByHostName(ctx context.Context, fqdn string) (*HostTopology, error) {
	hosts, err := c.listAllHosts(ctx)
	if err != nil {
		return nil, err
	}

	var target *HostObject
	for i := range hosts {
		if hosts[i].Name == fqdn {
			target = &hosts[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("host %q not found in ADCM", fqdn)
	}
	if target.Cluster == nil {
		return nil, fmt.Errorf("host %q has no cluster assigned", fqdn)
	}

	clusterID := target.Cluster.ID
	clusterName := target.Cluster.Name

	var topoHosts []TopologyHost
	for _, h := range hosts {
		if h.Cluster == nil || h.Cluster.ID != clusterID {
			continue
		}
		th := TopologyHost{
			ID:   h.ID,
			Name: h.Name,
		}
		for _, comp := range h.Components {
			th.Components = append(th.Components, TopologyHostComponent(comp))
		}
		topoHosts = append(topoHosts, th)
	}

	return &HostTopology{
		ClusterID:   clusterID,
		ClusterName: clusterName,
		Hosts:       topoHosts,
	}, nil
}

func (c *Client) listAllHosts(ctx context.Context) ([]HostObject, error) {
	var out []HostObject
	nextURL := c.baseURL + "/api/v2/hosts/"

	for nextURL != "" {
		page, next, err := c.fetchHostPage(ctx, nextURL)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		nextURL = next
	}

	return out, nil
}

func (c *Client) fetchHostPage(ctx context.Context, fullURL string) ([]HostObject, string, error) {
	headers := map[string]string{headerAccept: mimeJSON}
	resp, err := c.doWithAuthRetry(ctx, http.MethodGet, fullURL, nil, headers)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("ADCM hosts request failed: %s", resp.Status)
	}

	var body struct {
		Count    int          `json:"count"`
		Next     *string      `json:"next"`
		Previous *string      `json:"previous"`
		Results  []HostObject `json:"results"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return nil, "", decodeErr
	}

	next := ""
	if body.Next != nil && *body.Next != "" {
		u, parseErr := url.Parse(*body.Next)
		switch {
		case parseErr != nil:
			c.log.WarnContext(ctx, "ignoring malformed pagination next", "next", *body.Next)
		case !u.IsAbs():
			next = c.baseURL + *body.Next
		case c.sameOrigin(u):
			next = *body.Next
		default:
			// Never follow a cross-origin next: it would leak the Authorization token.
			c.log.WarnContext(ctx, "dropping cross-origin pagination next", "next", *body.Next)
		}
	}

	return body.Results, next, nil
}

func (c *Client) sameOrigin(u *url.URL) bool {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host)
}

// ObtainToken posts to the ADCM token endpoint and returns a token.
func (c *Client) ObtainToken(ctx context.Context, user, pass string) (string, error) {
	token, err := c.tryTokenEndpoint(ctx, "/api/v2/token/", user, pass)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", errors.New("token endpoint returned empty token")
	}
	return token, nil
}

func (c *Client) tryTokenEndpoint(ctx context.Context, path, user, pass string) (string, error) {
	payload, err := json.Marshal(map[string]string{"username": user, "password": pass})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set(headerContentType, mimeJSON)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if unmarshalErr := json.Unmarshal(raw, &m); unmarshalErr != nil {
		return "", fmt.Errorf("token decode error: %w: %s", unmarshalErr, strings.TrimSpace(string(raw)))
	}
	if v, ok := m["token"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("token response missing token field")
}

func (c *Client) FindHostID(ctx context.Context, fqdn string) (int, error) {
	resp, err := c.doWithAuthRetry(ctx, http.MethodGet, c.baseURL+"/api/v2/hosts/", nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("list hosts status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Results []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return 0, decodeErr
	}
	for _, h := range body.Results {
		if h.Name == fqdn {
			return h.ID, nil
		}
	}
	return 0, nil
}

func (c *Client) CreateHost(ctx context.Context, fqdn string) error {
	body := []byte(fmt.Sprintf(`{"name":%q}`, fqdn))
	headers := map[string]string{headerContentType: mimeJSON}
	resp, err := c.doWithAuthRetry(ctx, http.MethodPost, c.baseURL+"/api/v2/hosts/", body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create host status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) FirstComponentID(ctx context.Context, hostID int) (string, error) {
	resp, err := c.doWithAuthRetry(ctx, http.MethodGet, fmt.Sprintf("%s/api/v2/hosts/%d/", c.baseURL, hostID), nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		return "", fmt.Errorf("get host status %d", resp.StatusCode)
	}
	var body struct {
		Components []struct {
			ID int `json:"id"`
		} `json:"components"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return "", decodeErr
	}
	if len(body.Components) == 0 {
		return "", nil
	}
	return strconv.Itoa(body.Components[0].ID), nil
}

func (c *Client) PostHostStatus(ctx context.Context, hostID int, status int) error {
	body := []byte(fmt.Sprintf(`{"status":%d}`, status))
	url := fmt.Sprintf("%s/status/api/v1/host/%d/", strings.TrimRight(c.baseURL, "/"), hostID)
	headers := map[string]string{headerContentType: mimeJSON}
	resp, err := c.doStatusPost(ctx, url, body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	needBody := c.logBodies.Load() || resp.StatusCode/100 != statusClassSuccess
	var b []byte
	if needBody {
		b, _ = io.ReadAll(resp.Body)
	}
	if c.logBodies.Load() {
		c.log.InfoContext(
			ctx,
			"host post",
			"url", url,
			"code", resp.StatusCode,
			"sent_status", status,
			"body", strings.TrimSpace(string(b)),
		)
	}
	if resp.StatusCode/100 != statusClassSuccess {
		return fmt.Errorf("post host status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) PostComponentStatus(ctx context.Context, hostID int, compID string, status int) error {
	body := []byte(fmt.Sprintf(`{"status":%d}`, status))
	reqURL := fmt.Sprintf(
		"%s/status/api/v1/host/%d/component/%s/",
		strings.TrimRight(c.baseURL, "/"), hostID, url.PathEscape(compID),
	)
	headers := map[string]string{headerContentType: mimeJSON}
	resp, err := c.doStatusPost(ctx, reqURL, body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	needBody := c.logBodies.Load() || resp.StatusCode/100 != statusClassSuccess
	var b []byte
	if needBody {
		b, _ = io.ReadAll(resp.Body)
	}
	if c.logBodies.Load() {
		c.log.InfoContext(
			ctx,
			"status post",
			"url", reqURL,
			"comp", compID,
			"code", resp.StatusCode,
			"sent_status", status,
			"body", strings.TrimSpace(string(b)),
		)
	}
	if resp.StatusCode/100 != statusClassSuccess {
		return fmt.Errorf("post component status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) GetHost(ctx context.Context, hostID int) (*HostObject, error) {
	resp, err := c.doWithAuthRetry(ctx, http.MethodGet, fmt.Sprintf("%s/api/v2/hosts/%d/", c.baseURL, hostID), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return nil, &HTTPStatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	var body HostObject
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return nil, decodeErr
	}
	return &body, nil
}

func (c *Client) ListClusterServices(ctx context.Context, clusterID int) ([]ServiceObject, error) {
	var out []ServiceObject
	nextURL := fmt.Sprintf("%s/api/v2/clusters/%d/services/", c.baseURL, clusterID)
	for nextURL != "" {
		page, next, err := c.fetchServicePage(ctx, nextURL)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		nextURL = next
	}
	return out, nil
}

func (c *Client) fetchServicePage(ctx context.Context, fullURL string) ([]ServiceObject, string, error) {
	headers := map[string]string{headerAccept: mimeJSON}
	resp, err := c.doWithAuthRetry(ctx, http.MethodGet, fullURL, nil, headers)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("list services status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Next    *string         `json:"next"`
		Results []ServiceObject `json:"results"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return nil, "", decodeErr
	}
	next := ""
	if body.Next != nil && *body.Next != "" {
		u, parseErr := url.Parse(*body.Next)
		if parseErr == nil && !u.IsAbs() {
			next = c.baseURL + *body.Next
		} else {
			next = *body.Next
		}
	}
	return body.Results, next, nil
}

func (c *Client) CurrentServiceConfigID(ctx context.Context, clusterID, serviceID int) (int, error) {
	items, err := c.listServiceConfigs(ctx, clusterID, serviceID)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		if it.IsCurrent {
			return it.ID, nil
		}
	}
	return 0, nil
}

func (c *Client) listServiceConfigs(ctx context.Context, clusterID, serviceID int) ([]serviceConfigItem, error) {
	url := fmt.Sprintf("%s/api/v2/clusters/%d/services/%d/configs/", c.baseURL, clusterID, serviceID)
	headers := map[string]string{headerAccept: mimeJSON}
	resp, err := c.doWithAuthRetry(ctx, http.MethodGet, url, nil, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list configs status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Results []serviceConfigItem `json:"results"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return nil, decodeErr
	}
	return body.Results, nil
}

type ServiceConfig struct {
	ID         int
	Components map[string]string
}

func (c *Client) GetServiceConfig(ctx context.Context, clusterID, serviceID, configID int) (ServiceConfig, error) {
	url := fmt.Sprintf("%s/api/v2/clusters/%d/services/%d/configs/%d/", c.baseURL, clusterID, serviceID, configID)
	headers := map[string]string{headerAccept: mimeJSON}
	resp, err := c.doWithAuthRetry(ctx, http.MethodGet, url, nil, headers)
	if err != nil {
		return ServiceConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != statusClassSuccess {
		b, _ := io.ReadAll(resp.Body)
		return ServiceConfig{}, fmt.Errorf("get config status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body serviceConfigResponse
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return ServiceConfig{}, decodeErr
	}
	return ServiceConfig{ID: body.ID, Components: body.Config.Components}, nil
}
