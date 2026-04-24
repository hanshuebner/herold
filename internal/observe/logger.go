package observe

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// ObservabilityConfig is the slice of sysconfig.ObservabilityConfig observe
// cares about. Kept narrow to avoid importing sysconfig from observe (layering).
type ObservabilityConfig struct {
	LogFormat    string
	LogLevel     string
	MetricsBind  string
	OTLPEndpoint string

	// SecretKeys, if non-nil, overrides the default list of log attribute keys
	// whose values are redacted. Matching is case-insensitive, substring.
	SecretKeys []string
}

// NewLogger returns a *slog.Logger configured from cfg, writing to os.Stderr.
// The logger is wrapped in a secret-stripping handler (REQ-OPS-84).
func NewLogger(cfg ObservabilityConfig) *slog.Logger {
	return NewLoggerTo(os.Stderr, cfg)
}

// NewLoggerTo is like NewLogger but writes to w. Test seam.
func NewLoggerTo(w io.Writer, cfg ObservabilityConfig) *slog.Logger {
	lvl := parseLogLevel(cfg.LogLevel)
	var base slog.Handler
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(cfg.LogFormat) {
	case "", "json":
		base = slog.NewJSONHandler(w, opts)
	case "text":
		base = slog.NewTextHandler(w, opts)
	default:
		base = slog.NewJSONHandler(w, opts)
	}
	keys := cfg.SecretKeys
	if keys == nil {
		keys = DefaultSecretKeys
	}
	return slog.New(NewRedactHandler(base, keys))
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "", "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
