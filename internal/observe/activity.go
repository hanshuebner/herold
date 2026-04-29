package observe

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Activity is the closed enum for the activity tag (REQ-OPS-86).
// Every log record emitted from a wire-protocol layer, the queue/delivery
// path, the plugin supervisor, and the auth/directory layer must carry an
// "activity" attribute whose value is one of these constants.
type Activity = string

const (
	// ActivityUser is caller-initiated state changes or information retrieval
	// the operator sees in a normal day's log.
	ActivityUser Activity = "user"
	// ActivityAudit is security-relevant events: login attempts, permission
	// denials, ACL decisions, key rotations.
	ActivityAudit Activity = "audit"
	// ActivitySystem is server-initiated work: delivery attempts, queue
	// retries, ACME renewal, DNS publication, schema migrations.
	ActivitySystem Activity = "system"
	// ActivityPoll is recurring no-op heartbeats: IMAP IDLE keep-alives,
	// push reconnects, periodic reconciliation reads.
	ActivityPoll Activity = "poll"
	// ActivityAccess is per-request echo lines: HTTP request log, IMAP
	// command trace, SMTP command echo. Forensic-only; debug level.
	ActivityAccess Activity = "access"
	// ActivityInternal is diagnostic, framework-level, or library events
	// with no caller-facing semantics.
	ActivityInternal Activity = "internal"
)

// validActivityValues is the closed set; used by activityFilter and tests.
var validActivityValues = []string{
	ActivityUser, ActivityAudit, ActivitySystem,
	ActivityPoll, ActivityAccess, ActivityInternal,
}

// IsValidActivity reports whether s is a member of the activity enum.
func IsValidActivity(s string) bool {
	return slices.Contains(validActivityValues, s)
}

// activityFilter is an slog.Handler wrapper that implements REQ-OPS-86b:
// allow/deny filtering based on the "activity" attribute. It inspects both
// pre-scoped attrs (set via WithAttrs) and per-record attrs.
type activityFilter struct {
	base slog.Handler
	// allow is non-nil when mode is allowlist; the record passes only if
	// its activity is in the set.
	allow map[string]struct{}
	// deny is non-nil when mode is denylist; the record passes unless its
	// activity is in the set.
	deny map[string]struct{}
	// preActivity is the activity pre-scoped via WithAttrs ("" = not set).
	preActivity string
}

func newActivityFilter(base slog.Handler, cfg ActivityFilterConfig) slog.Handler {
	af := &activityFilter{base: base}
	if len(cfg.Allow) > 0 {
		af.allow = make(map[string]struct{}, len(cfg.Allow))
		for _, a := range cfg.Allow {
			af.allow[a] = struct{}{}
		}
	}
	if len(cfg.Deny) > 0 {
		af.deny = make(map[string]struct{}, len(cfg.Deny))
		for _, a := range cfg.Deny {
			af.deny[a] = struct{}{}
		}
	}
	return af
}

func (f *activityFilter) Enabled(ctx context.Context, lvl slog.Level) bool {
	if !f.base.Enabled(ctx, lvl) {
		return false
	}
	// If activity is pre-scoped we can decide immediately.
	if f.preActivity != "" {
		return f.activityPasses(f.preActivity)
	}
	// Without a pre-scoped activity we can't know yet; return true and let
	// Handle make the final decision after inspecting the record.
	return true
}

func (f *activityFilter) Handle(ctx context.Context, r slog.Record) error {
	activity := f.preActivity
	if activity == "" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "activity" {
				activity = a.Value.String()
				return false
			}
			return true
		})
	}
	if !f.activityPasses(activity) {
		return nil
	}
	return f.base.Handle(ctx, r)
}

func (f *activityFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	act := f.preActivity
	for _, a := range attrs {
		if a.Key == "activity" {
			act = strings.ToLower(a.Value.String())
		}
	}
	return &activityFilter{
		base:        f.base.WithAttrs(attrs),
		allow:       f.allow,
		deny:        f.deny,
		preActivity: act,
	}
}

func (f *activityFilter) WithGroup(name string) slog.Handler {
	return &activityFilter{
		base:        f.base.WithGroup(name),
		allow:       f.allow,
		deny:        f.deny,
		preActivity: f.preActivity,
	}
}

// activityPasses reports whether a record with the given activity value should
// pass this filter. An empty activity value is treated as passing when no
// filter is configured, and as failing when either allow or deny is set
// (because an untagged record from a layer that must tag is suspicious;
// strict mode surfaces it).
func (f *activityFilter) activityPasses(activity string) bool {
	if f.allow != nil {
		if activity == "" {
			return false
		}
		_, ok := f.allow[activity]
		return ok
	}
	if f.deny != nil {
		if activity == "" {
			return true // untagged records always pass a denylist
		}
		_, blocked := f.deny[activity]
		return !blocked
	}
	return true // no filter configured
}

// --- AssertActivityTagged test helper (REQ-OPS-86a) ---

// recordingHandler captures every log record's attrs for post-test assertion.
type recordingHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	message string
	attrs   map[string]string // flattened key→value (string only for ease)
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	cr := capturedRecord{
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		cr.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, cr)
	h.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Returns a child handler with pre-scoped attrs merged in.
	pre := make(map[string]string, len(attrs))
	for _, a := range attrs {
		pre[a.Key] = a.Value.String()
	}
	return &recordingChildHandler{parent: h, pre: pre}
}

func (h *recordingHandler) WithGroup(_ string) slog.Handler { return h }

// recordingChildHandler is a WithAttrs child of recordingHandler.
type recordingChildHandler struct {
	parent *recordingHandler
	pre    map[string]string
}

func (c *recordingChildHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *recordingChildHandler) Handle(_ context.Context, r slog.Record) error {
	cr := capturedRecord{
		message: r.Message,
		attrs:   make(map[string]string, len(c.pre)),
	}
	for k, v := range c.pre {
		cr.attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		cr.attrs[a.Key] = a.Value.String()
		return true
	})
	c.parent.mu.Lock()
	c.parent.records = append(c.parent.records, cr)
	c.parent.mu.Unlock()
	return nil
}

func (c *recordingChildHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(c.pre)+len(attrs))
	for k, v := range c.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return &recordingChildHandler{parent: c.parent, pre: merged}
}

func (c *recordingChildHandler) WithGroup(_ string) slog.Handler { return c }

// AssertActivityTagged runs fn with a *slog.Logger backed by a recording
// handler, then asserts that (a) every emitted record has an "activity"
// attribute, and (b) its value is in the closed enum {user, audit, system,
// poll, access, internal} (REQ-OPS-86a).
//
// Usage in a test that exercises a wire-protocol package:
//
//	observe.AssertActivityTagged(t, func(log *slog.Logger) {
//	    handler := protosmtp.NewHandler(log, ...)
//	    handler.ProcessSomeRequest(...)
//	})
func AssertActivityTagged(t testing.TB, fn func(log *slog.Logger)) {
	t.Helper()
	rec := &recordingHandler{}
	log := slog.New(rec)
	fn(log)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, r := range rec.records {
		act, ok := r.attrs["activity"]
		if !ok {
			t.Errorf("log record %q missing \"activity\" attribute (REQ-OPS-86a)", r.message)
			continue
		}
		if !IsValidActivity(act) {
			t.Errorf("log record %q has invalid activity %q; want one of %v (REQ-OPS-86a)",
				r.message, act, validActivityValues)
		}
	}
}
