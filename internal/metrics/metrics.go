// Package metrics exposes Prometheus counters and gauges for the agent and an
// optional HTTP endpoint (with basic auth and TLS) to scrape them. All observer
// methods are nil-safe so callers can hold a nil *Metrics when metrics are
// disabled and instrument unconditionally.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "ad_status_sender"

const (
	labelKind   = "kind"
	labelTarget = "target"
	labelResult = "result"
	labelID     = "id"

	resultOK    = "ok"
	resultError = "error"
)

// Metrics holds the collectors and the registry they are registered in.
type Metrics struct {
	reg *prometheus.Registry

	up               prometheus.Gauge
	sendTotal        *prometheus.CounterVec
	sendDuration     *prometheus.HistogramVec
	entityStatus     *prometheus.GaugeVec
	rules            *prometheus.GaugeVec
	rulesRefresh     *prometheus.CounterVec
	rulesLastRefresh prometheus.Gauge
	tokenFetch       *prometheus.CounterVec
}

// New builds the collectors in a private registry (isolated from the global
// default so tests don't collide and the endpoint exposes only our series).
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "1 while the agent is running.",
		}),
		sendTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "send_total",
			Help:      "Status POSTs by target (host/component) and result (ok/error).",
		}, []string{labelTarget, labelResult}),
		sendDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "send_duration_seconds",
			Help:      "Latency of status POSTs by target.",
			Buckets:   prometheus.DefBuckets,
		}, []string{labelTarget}),
		entityStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "entity_status",
			Help:      "Last posted status per entity (0 up, 1 down).",
		}, []string{labelKind, labelID}),
		rules: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "rules",
			Help:      "Number of loaded rules by kind (systemd/docker).",
		}, []string{labelKind}),
		rulesRefresh: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "rules_refresh_total",
			Help:      "Rule imports by result (ok/error).",
		}, []string{labelResult}),
		rulesLastRefresh: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "rules_last_refresh_timestamp_seconds",
			Help:      "Unix time of the last successful rule import.",
		}),
		tokenFetch: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "token_fetch_total",
			Help:      "Token fetches by kind (rbac/status) and result (ok/error).",
		}, []string{labelKind, labelResult}),
	}
	m.reg.MustRegister(
		m.up, m.sendTotal, m.sendDuration, m.entityStatus,
		m.rules, m.rulesRefresh, m.rulesLastRefresh, m.tokenFetch,
	)
	return m
}

// Registry returns the registry the collectors are registered in.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// SetUp marks the agent live (1) or not (0).
func (m *Metrics) SetUp(up bool) {
	if m == nil {
		return
	}
	m.up.Set(boolToFloat(up))
}

func result(err error) string {
	if err != nil {
		return resultError
	}
	return resultOK
}

// ObserveSend records one status POST: its outcome and latency.
func (m *Metrics) ObserveSend(target string, dur time.Duration, err error) {
	if m == nil {
		return
	}
	m.sendTotal.WithLabelValues(target, result(err)).Inc()
	m.sendDuration.WithLabelValues(target).Observe(dur.Seconds())
}

// SetHostStatus records the last status posted for a host.
func (m *Metrics) SetHostStatus(hostID, status int) {
	if m == nil {
		return
	}
	m.entityStatus.WithLabelValues("host", strconv.Itoa(hostID)).Set(float64(status))
}

// SetComponentStatus records the last status posted for a component on a host.
func (m *Metrics) SetComponentStatus(hostID int, compID string, status int) {
	if m == nil {
		return
	}
	id := strconv.Itoa(hostID) + ":" + compID
	m.entityStatus.WithLabelValues("component", id).Set(float64(status))
}

// SetRules records the loaded rule counts by kind.
func (m *Metrics) SetRules(systemd, docker int) {
	if m == nil {
		return
	}
	m.rules.WithLabelValues("systemd").Set(float64(systemd))
	m.rules.WithLabelValues("docker").Set(float64(docker))
}

// ObserveRefresh records a rule import outcome; on success it stamps the refresh time.
func (m *Metrics) ObserveRefresh(err error) {
	if m == nil {
		return
	}
	m.rulesRefresh.WithLabelValues(result(err)).Inc()
	if err == nil {
		m.rulesLastRefresh.Set(float64(time.Now().Unix()))
	}
}

// ObserveTokenFetch records a token fetch by kind (rbac/status) and outcome.
func (m *Metrics) ObserveTokenFetch(kind string, err error) {
	if m == nil {
		return
	}
	m.tokenFetch.WithLabelValues(kind, result(err)).Inc()
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
