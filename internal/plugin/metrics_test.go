package plugin

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/observe"
)

// TestPluginMetrics_RecordCallSetsCounter exercises recordPluginCall
// directly: a successful invocation should advance both the calls
// counter and the duration histogram. Bypasses the plugin process
// machinery (covered in supervisor_integration_test) so the metric
// wiring can be asserted without spawning a child binary.
func TestPluginMetrics_RecordCallSetsCounter(t *testing.T) {
	observe.RegisterPluginMetrics()
	const name = "test-spam"
	const method = "spam.classify"

	beforeOK := testutil.ToFloat64(observe.PluginCallsTotal.WithLabelValues(name, method, "ok"))
	beforeFail := testutil.ToFloat64(observe.PluginCallsTotal.WithLabelValues(name, method, "error"))

	now := time.Now()
	recordPluginCall(name, method, now, now.Add(5*time.Millisecond), nil)
	recordPluginCall(name, method, now, now.Add(5*time.Millisecond), &Error{Code: -32099, Message: "boom"})

	afterOK := testutil.ToFloat64(observe.PluginCallsTotal.WithLabelValues(name, method, "ok"))
	afterFail := testutil.ToFloat64(observe.PluginCallsTotal.WithLabelValues(name, method, "error"))

	if afterOK <= beforeOK {
		t.Errorf("herold_plugin_calls_total{outcome=ok}: before=%v after=%v", beforeOK, afterOK)
	}
	if afterFail <= beforeFail {
		t.Errorf("herold_plugin_calls_total{outcome=error}: before=%v after=%v", beforeFail, afterFail)
	}

	// Histogram count should land at least the two observations we just
	// drove. CollectAndCount reports the number of label streams with
	// at least one observation; we expect a non-zero count here.
	if got := testutil.CollectAndCount(observe.PluginCallDuration); got == 0 {
		t.Fatalf("herold_plugin_call_duration_seconds: no metric streams after observations")
	}
}

// TestPluginMetrics_SetStateUpdatesGauge confirms the plugin_up gauge
// flips between 0 and 1 as the lifecycle state changes — operators
// alert on plugin_up == 0 for any plugin in their config.
func TestPluginMetrics_SetStateUpdatesGauge(t *testing.T) {
	observe.RegisterPluginMetrics()
	mgr := NewManager(ManagerOptions{ServerVersion: "test"})
	p := newPlugin(mgr, Spec{Name: "p-metrics-test"})

	p.setState(StateHealthy)
	if got := testutil.ToFloat64(observe.PluginUp.WithLabelValues("p-metrics-test")); got != 1 {
		t.Fatalf("plugin_up{name=p-metrics-test}: want 1 after StateHealthy, got %v", got)
	}
	p.setState(StateUnhealthy)
	if got := testutil.ToFloat64(observe.PluginUp.WithLabelValues("p-metrics-test")); got != 0 {
		t.Fatalf("plugin_up{name=p-metrics-test}: want 0 after StateUnhealthy, got %v", got)
	}
}
