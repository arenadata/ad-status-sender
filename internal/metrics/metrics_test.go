package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func gaugeValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	return sampleValue(t, reg, name, labels)
}

func sampleValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, labels) {
				return metricValue(m)
			}
		}
	}
	t.Fatalf("metric %s%v not found", name, labels)
	return 0
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func metricValue(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	case m.GetHistogram() != nil:
		return float64(m.GetHistogram().GetSampleCount())
	}
	return 0
}

func TestNilSafe(t *testing.T) {
	t.Parallel()
	var m *Metrics // disabled
	// None of these must panic.
	m.SetUp(true)
	m.ObserveSend("host", time.Second, nil)
	m.SetHostStatus(1, 0)
	m.SetComponentStatus(1, "2", 1)
	m.SetRules(3, 4)
	m.ObserveRefresh(nil)
	m.ObserveTokenFetch("rbac", nil)
	if m.Registry() != nil {
		t.Fatal("nil metrics registry should be nil")
	}
}

func TestObservers(t *testing.T) {
	t.Parallel()
	m := New()
	reg := m.Registry()

	m.SetUp(true)
	if v := gaugeValue(t, reg, "ad_status_sender_up", nil); v != 1 {
		t.Fatalf("up=%v", v)
	}

	m.ObserveSend("component", 10*time.Millisecond, nil)
	m.ObserveSend("component", 10*time.Millisecond, errors.New("boom"))
	if v := sampleValue(
		t,
		reg,
		"ad_status_sender_send_total",
		map[string]string{"target": "component", "result": "ok"},
	); v != 1 {
		t.Fatalf("send ok=%v", v)
	}
	if v := sampleValue(
		t,
		reg,
		"ad_status_sender_send_total",
		map[string]string{"target": "component", "result": "error"},
	); v != 1 {
		t.Fatalf("send error=%v", v)
	}
	if v := sampleValue(
		t,
		reg,
		"ad_status_sender_send_duration_seconds",
		map[string]string{"target": "component"},
	); v != 2 {
		t.Fatalf("duration count=%v", v)
	}

	m.SetComponentStatus(7, "42", 1)
	if v := gaugeValue(
		t,
		reg,
		"ad_status_sender_entity_status",
		map[string]string{"kind": "component", "id": "7:42"},
	); v != 1 {
		t.Fatalf("entity status=%v", v)
	}

	m.SetRules(5, 2)
	if v := gaugeValue(t, reg, "ad_status_sender_rules", map[string]string{"kind": "systemd"}); v != 5 {
		t.Fatalf("rules systemd=%v", v)
	}

	m.ObserveRefresh(nil)
	if v := sampleValue(t, reg, "ad_status_sender_rules_refresh_total", map[string]string{"result": "ok"}); v != 1 {
		t.Fatalf("refresh ok=%v", v)
	}
	if v := gaugeValue(t, reg, "ad_status_sender_rules_last_refresh_timestamp_seconds", nil); v <= 0 {
		t.Fatalf("last refresh ts=%v", v)
	}

	m.ObserveTokenFetch("status", errors.New("x"))
	if v := sampleValue(
		t,
		reg,
		"ad_status_sender_token_fetch_total",
		map[string]string{"kind": "status", "result": "error"},
	); v != 1 {
		t.Fatalf("token fetch=%v", v)
	}
}
