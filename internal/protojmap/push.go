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
	// flushTimer starts as a nil channel so select skips it; a real
	// timer is created on the first matched change via s.clk.After
	// with PushCoalesceWindow.
	var flushTimer <-chan time.Time

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

// buildStateChange assembles a single StateChange payload using the
// same per-type state-derivation rules each JMAP datatype applies for
// its own /get and /changes responses. For Email / Mailbox / Thread
// that means the maximum change-feed seq for the corresponding entity
// kind (so a delivery into the inbox bumps the state without anybody
// having to call IncrementJMAPState); for Identity / EmailSubmission /
// VacationResponse it is still the per-kind counter on the
// jmap_states row, since those datatypes mutate exclusively through
// JMAP /set and the row is the authoritative source.
//
// Mismatching the data side here is the canonical "new mail arrives
// but the inbox does not refresh" failure: clients early-return when
// the pushed state string equals the one they already cached.
func (s *Server) buildStateChange(ctx context.Context, p store.Principal, types map[string]struct{}) (stateChangeEvent, error) {
	row, err := s.collectStateMap(ctx, p, types)
	if err != nil {
		return stateChangeEvent{}, err
	}
	return stateChangeEvent{
		Type: "StateChange",
		Changed: map[Id]map[string]string{
			AccountIDForPrincipal(p.ID): row,
		},
	}, nil
}

// collectStateMap returns the typeName -> state-string map for the
// types the EventSource subscription requested. Types not in the
// subscription are skipped; types with no derivation rule are also
// skipped (rather than reporting "0" and lying that nothing changed).
func (s *Server) collectStateMap(ctx context.Context, p store.Principal, types map[string]struct{}) (map[string]string, error) {
	out := make(map[string]string, 6)

	// Change-feed-derived types. We fetch only the ones the
	// subscription cares about; the SQL store implements
	// GetMaxChangeSeqForKind as an indexed MAX() so each call is
	// cheap.
	feedKinds := []struct {
		typeName string
		kind     store.EntityKind
	}{
		{"Mailbox", store.EntityKindMailbox},
		{"Email", store.EntityKindEmail},
		{"Thread", store.EntityKindEmail}, // Threads are derived from Email mutations.
	}
	for _, fk := range feedKinds {
		if !matchesEventSourceTypeName(types, fk.typeName) {
			continue
		}
		if _, ok := out[fk.typeName]; ok {
			continue // de-dupe: Email and Thread share the same kind.
		}
		seq, err := s.store.Meta().GetMaxChangeSeqForKind(ctx, p.ID, fk.kind)
		if err != nil {
			return nil, fmt.Errorf("max change seq %s: %w", fk.typeName, err)
		}
		out[fk.typeName] = strconv.FormatUint(uint64(seq), 10)
	}

	// jmap_states-derived types — only fetch the row when at least one
	// row-based subscriber needs it.
	rowTypes := []string{"Identity", "EmailSubmission", "VacationResponse"}
	rowNeeded := false
	for _, t := range rowTypes {
		if matchesEventSourceTypeName(types, t) {
			rowNeeded = true
			break
		}
	}
	if rowNeeded {
		st, err := s.store.Meta().GetJMAPStates(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("get jmap states: %w", err)
		}
		if matchesEventSourceTypeName(types, "Identity") {
			out["Identity"] = strconv.FormatInt(st.Identity, 10)
		}
		if matchesEventSourceTypeName(types, "EmailSubmission") {
			out["EmailSubmission"] = strconv.FormatInt(st.EmailSubmission, 10)
		}
		if matchesEventSourceTypeName(types, "VacationResponse") {
			out["VacationResponse"] = strconv.FormatInt(st.VacationResponse, 10)
		}
	}
	return out, nil
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
