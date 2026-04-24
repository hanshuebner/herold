package observe

import (
	"context"
	"log/slog"
	"strings"
)

// DefaultSecretKeys is the baseline set of attribute keys whose values are
// redacted from log output (REQ-OPS-84). Matching is case-insensitive exact.
// Callers that want to extend the list should append to a copy, not mutate.
var DefaultSecretKeys = []string{
	"password",
	"token",
	"api_key",
	"secret",
	"authorization",
	"cookie",
	"set-cookie",
}

// RedactedValue is the placeholder substituted for any secret attribute value.
const RedactedValue = "<redacted>"

// NewRedactHandler wraps base so that any attribute whose key matches
// secretKeys (case-insensitive, exact) has its value replaced with
// RedactedValue. Recurses into slog.Group attributes.
func NewRedactHandler(base slog.Handler, secretKeys []string) slog.Handler {
	set := make(map[string]struct{}, len(secretKeys))
	for _, k := range secretKeys {
		set[strings.ToLower(k)] = struct{}{}
	}
	return &redactHandler{base: base, secrets: set}
}

type redactHandler struct {
	base    slog.Handler
	secrets map[string]struct{}
}

// Enabled reports whether the underlying handler accepts records at lvl.
func (h *redactHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.base.Enabled(ctx, lvl)
}

// Handle redacts secret attributes in r before forwarding to the wrapped handler.
func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.base.Handle(ctx, nr)
}

// WithAttrs returns a handler with pre-scoped attributes, redacted at add time.
func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.redactAttr(a)
	}
	return &redactHandler{base: h.base.WithAttrs(out), secrets: h.secrets}
}

// WithGroup forwards group nesting to the wrapped handler.
func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{base: h.base.WithGroup(name), secrets: h.secrets}
}

// redactAttr returns a copy of a with any secret leaf values replaced.
func (h *redactHandler) redactAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		children := a.Value.Group()
		out := make([]slog.Attr, len(children))
		for i, c := range children {
			out[i] = h.redactAttr(c)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if _, ok := h.secrets[strings.ToLower(a.Key)]; ok {
		return slog.String(a.Key, RedactedValue)
	}
	return a
}
