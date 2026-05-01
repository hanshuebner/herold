// Package observe wires log/slog, prometheus/client_golang, and go.opentelemetry.io/otel.
// Exposes the Clock and RandSource abstractions used by the rest of the server for
// deterministic testing.
//
// # OTLP log exporter (REQ-OPS-205)
//
// [NewOTLPLogProvider] constructs an OTLP/HTTP log provider that mirrors the
// trace exporter setup. When the endpoint is empty it returns a noop provider.
// Resource attributes deployment.environment and service.instance.id are set
// at provider construction time; service.name and service.version (build SHA)
// are set per-event via the instrumentation scope so that herold-suite and
// herold-admin records are distinguishable in the collector.
//
// # Client-log emitter (REQ-OPS-204, REQ-OPS-205)
//
// [ClientEmitter] fans an enriched [ClientEvent] into slog and OTLP:
//
//   - slog: one record per event with source=client plus the mandatory
//     activity attribute (audit/user/internal depending on kind and auth).
//   - OTLP: one log record with per-record attributes defined in
//     architecture/10-client-log-pipeline.md §OTLP shape.
//
// Anonymous events (Endpoint=="public") skip OTLP unless the emitter's
// PublicOTLPEgress flag is true (REQ-OPS-205, REQ-OPS-217 default false).
//
// # Multi-sink logging (REQ-OPS-80..86)
//
// The public API is:
//
//   - [NewLogger] — constructs a [Logger] from an [ObservabilityConfig] that
//     holds a []LogSinkConfig slice. Each sink has its own target, format, level,
//     per-module overrides, and activity filter. The returned *Logger embeds
//     *slog.Logger and can be used directly.
//
//   - [Logger.Reload] — atomically swaps the active sink set on SIGHUP without
//     losing records (REQ-OPS-85). The swap uses atomic.Pointer; in-flight
//     Handle calls against the previous fanoutHandler finish safely.
//
//   - [NewLoggerTo] — legacy single-sink test seam; accepts the old
//     ObservabilityConfig.LogFormat/LogLevel/LogModules fields. New code
//     populates ObservabilityConfig.Sinks instead.
//
// # Activity tagging (REQ-OPS-86a)
//
// Every log record emitted from a wire-protocol layer (protosmtp, protoimap,
// protojmap, protomanagesieve, protoadmin, protosend, protowebhook), the
// queue/delivery path, the plugin supervisor, and the auth/directory layer MUST
// carry an "activity" attribute from the closed enum defined by the Activity*
// constants in this package.
//
// The preferred pattern is a pre-scoped logger so activity is set uniformly:
//
//	log := parentLog.With("subsystem", "protojmap", "activity", observe.ActivityUser)
//
// # Activity lint (REQ-OPS-86a)
//
// [AssertActivityTagged] is a test helper exported from this package. Wire-protocol
// tests use it to verify that every record emitted during an exercise carries a
// valid "activity" attribute. Usage:
//
//	func TestMyHandler(t *testing.T) {
//	    observe.AssertActivityTagged(t, func(log *slog.Logger) {
//	        handler := protojmap.NewHandler(log, ...)
//	        handler.HandleRequest(...)
//	    })
//	}
//
// # Console format (REQ-OPS-81a)
//
// [NewConsoleHandler] and [NewConsoleHandlerWithClock] produce human-readable
// one-line output: HH:MM:SS.mmm LEVL subsystem|module message key=value ...
// Color is emitted only when the target is a TTY and NO_COLOR is not set.
// Multi-line attribute values indent continuation lines with "  | ".
//
// Ownership: ops-observability-implementor.
package observe
