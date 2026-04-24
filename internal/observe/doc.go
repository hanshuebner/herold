// Package observe wires log/slog, prometheus/client_golang, and go.opentelemetry.io/otel. Exposes the Clock and RandSource abstractions used by the rest of the server for deterministic testing.
//
// Ownership: ops-observability-implementor.
package observe
