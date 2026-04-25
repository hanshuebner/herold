package protojmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// stateChangeEvent is the SSE payload defined by RFC 8620 §7.1
// (StateChange object). The "@type" discriminator and the "changed"
// map (accountId -> typeStates) form the spec-mandated shape; we never
// elide either because clients dispatch on the @type tag.
type stateChangeEvent struct {
	Type    string                   `json:"@type"`
	Changed map[Id]map[string]string `json:"changed"`
}

// changeFeedPoller decouples the EventSource handler from the change
// feed. Production wiring uses store.Metadata.ReadChangeFeed; tests
// substitute a poller that fires whenever a ChangeFeedSignal channel
// is closed/sent on, so they do not need to drive a real polling
// loop.
type changeFeedPoller func(ctx context.Context, pid store.PrincipalID, fromSeq store.ChangeSeq, max int) ([]store.StateChange, error)

// handleEventSource is GET /jmap/eventsource (RFC 8620 §7.3). The
// server emits SSE events of "@type": "StateChange" whenever a change
// in one of the requested types lands on the per-principal feed.
//
// Query params:
//
//	types=Mailbox,Email   — comma-separated JMAP type names; empty/"*"
//	                         means "every registered type"
//	closeafter=state      — close the connection after the first
//	                         StateChange event
//	ping=N                — heartbeat interval in seconds (default
//	                         from Options.PushPingInterval)
//
// Concurrency: one goroutine per session feeds the SSE writer; both
// the HTTP request context and the server's Close() cancel it. We
// register no per-server background loop — every push session is
// owned by the request goroutine.
func (s *Server) handleEventSource(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		WriteJMAPError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteJMAPError(w, http.StatusInternalServerError,
			"serverFail", "streaming unsupported")
		return
	}
	q := r.URL.Query()
	types := parseEventSourceTypes(q.Get("types"))
	closeAfterState := q.Get("closeafter") == "state"
	ping := s.opts.PushPingInterval
	if v := q.Get("ping"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ping = time.Duration(n) * time.Second
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := changeFeedPoller(s.store.Meta().ReadChangeFeed)
	if err := s.runEventSource(r.Context(), w, flusher, p, types, closeAfterState, ping, poll); err != nil {
		s.log.Debug("protojmap.eventsource.exit", "err", err, "pid", p.ID)
	}
}

// runEventSource is the core push loop. Split out from handleEventSource
// so unit tests drive it with a synthetic ResponseWriter and a mock
// poller. The loop terminates when ctx is done, the underlying writer
// errors, or closeAfterState fires.
func (s *Server) runEventSource(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	p store.Principal,
	types map[string]struct{},
	closeAfterState bool,
	ping time.Duration,
	poll changeFeedPoller,
) error {
	// pollInterval is how often we ask the change feed for new
	// entries when no signal source is wired. The fakestore is in-
	// memory so this is the only available cadence for now; the
	// production storesqlite/storepg implementation can layer a
	// LISTEN/NOTIFY-style trigger over the same reader.
	const pollInterval = 100 * time.Millisecond

	var cursor store.ChangeSeq
	pendingChanged := false
	flushTimer := s.clk.After(s.opts.PushCoalesceWindow)
	// Reset flushTimer to a never-firing channel until we have a
	// pending change. Using a nil channel makes select skip it; a
	// real timer is created on the first matched change.
	flushTimer = nil

	pollTimer := s.clk.After(pollInterval)
	pingTimer := s.clk.After(ping)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pollTimer:
			pollTimer = s.clk.After(pollInterval)
			changes, err := poll(ctx, p.ID, cursor, 256)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				s.log.Warn("protojmap.eventsource.read_failed", "err", err, "pid", p.ID)
				continue
			}
			matched := false
			for _, c := range changes {
				if c.Seq > cursor {
					cursor = c.Seq
				}
				if matchesEventSourceType(types, c.Kind) {
					matched = true
				}
			}
			if matched && !pendingChanged {
				pendingChanged = true
				flushTimer = s.clk.After(s.opts.PushCoalesceWindow)
			}
		case <-flushTimer:
			flushTimer = nil
			if !pendingChanged {
				continue
			}
			pendingChanged = false
			ev, err := s.buildStateChange(ctx, p, types)
			if err != nil {
				s.log.Warn("protojmap.eventsource.state_failed",
					"err", err, "pid", p.ID)
				continue
			}
			if err := writeSSEStateChange(w, ev); err != nil {
				return err
			}
			flusher.Flush()
			if closeAfterState {
				return nil
			}
		case <-pingTimer:
			pingTimer = s.clk.After(ping)
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return err
			}
			flusher.Flush()
		}
	}
}

// buildStateChange assembles a single StateChange payload by reading
// the requesting principal's per-kind state row. The result includes
// only the requested types, so a client subscribed to "Email" does not
// see Mailbox state churn.
func (s *Server) buildStateChange(ctx context.Context, p store.Principal, types map[string]struct{}) (stateChangeEvent, error) {
	st, err := s.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return stateChangeEvent{}, fmt.Errorf("get jmap states: %w", err)
	}
	row := stateRowToMap(st)
	out := make(map[string]string, len(row))
	for typ, state := range row {
		if !matchesEventSourceTypeName(types, typ) {
			continue
		}
		out[typ] = state
	}
	return stateChangeEvent{
		Type: "StateChange",
		Changed: map[Id]map[string]string{
			AccountIDForPrincipal(p.ID): out,
		},
	}, nil
}

// stateRowToMap projects a JMAPStates row to the JMAP type name keyed
// state-string map. The state strings are the int64 counters
// stringified; opaque to clients per RFC 8620 §1.5 ("opaque string").
func stateRowToMap(st store.JMAPStates) map[string]string {
	return map[string]string{
		"Mailbox":          strconv.FormatInt(st.Mailbox, 10),
		"Email":            strconv.FormatInt(st.Email, 10),
		"Thread":           strconv.FormatInt(st.Thread, 10),
		"Identity":         strconv.FormatInt(st.Identity, 10),
		"EmailSubmission":  strconv.FormatInt(st.EmailSubmission, 10),
		"VacationResponse": strconv.FormatInt(st.VacationResponse, 10),
	}
}

// writeSSEStateChange emits a single SSE event in the wire form
// mandated by RFC 8620 §7.3:
//
//	event: state
//	data: <json>
//	id: <opaque>
//
// followed by a blank line. We omit the id field; clients that
// reconnect supply the Last-Event-ID header which we ignore in v1
// (we treat each connection as a fresh subscription).
func writeSSEStateChange(w http.ResponseWriter, ev stateChangeEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal state change: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", body); err != nil {
		return err
	}
	return nil
}

// parseEventSourceTypes parses the ?types= query parameter. An empty
// or "*" value means "every type"; other values are comma-separated
// JMAP type names. Unknown names are accepted and just never match.
func parseEventSourceTypes(raw string) map[string]struct{} {
	out := make(map[string]struct{})
	if raw == "" || raw == "*" {
		return nil // nil means "all types"
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out[part] = struct{}{}
	}
	return out
}

// matchesEventSourceType maps a store EntityKind to a JMAP type name
// and reports whether the client subscribed to it.
func matchesEventSourceType(types map[string]struct{}, kind store.EntityKind) bool {
	return matchesEventSourceTypeName(types, entityKindToJMAPType(kind))
}

func matchesEventSourceTypeName(types map[string]struct{}, name string) bool {
	if types == nil { // all types
		return name != ""
	}
	if name == "" {
		return false
	}
	_, ok := types[name]
	return ok
}

// entityKindToJMAPType maps the change-feed EntityKind enum to the
// corresponding JMAP type name. Unknown kinds map to "" so they never
// match any subscription.
func entityKindToJMAPType(k store.EntityKind) string {
	switch k {
	case store.EntityKindMailbox:
		return "Mailbox"
	case store.EntityKindEmail:
		return "Email"
	case store.EntityKindEmailSubmission:
		return "EmailSubmission"
	case store.EntityKindIdentity:
		return "Identity"
	case store.EntityKindVacationResponse:
		return "VacationResponse"
	default:
		return ""
	}
}
