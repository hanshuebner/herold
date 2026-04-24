package observe

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the process-wide Prometheus collector registry (REQ-OPS-90).
// It is intentionally not prometheus.DefaultRegisterer: the default registry
// auto-registers Go runtime and process collectors, which we opt into
// explicitly at server start via MustRegister.
var Registry = prometheus.NewRegistry()

// MustRegister registers cs with the server-wide Registry, panicking on
// duplicate or otherwise invalid collectors (a programmer bug).
func MustRegister(cs ...prometheus.Collector) {
	Registry.MustRegister(cs...)
}

// MetricsHandler returns an http.Handler serving the Prometheus text
// exposition format for Registry on /metrics (REQ-OPS-90, REQ-OPS-92).
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}
