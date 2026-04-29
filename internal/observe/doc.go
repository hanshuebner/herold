// Package observe wires log/slog, prometheus/client_golang, and go.opentelemetry.io/otel.
// Exposes the Clock and RandSource abstractions used by the rest of the server for
// deterministic testing.
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
