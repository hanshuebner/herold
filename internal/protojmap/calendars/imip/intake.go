package imip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap/calendars/jscalendar"
	"github.com/hanshuebner/herold/internal/store"
)

// jsonRaw is a local alias for json.RawMessage; used purely as a
// readability convenience in the participant / counter-proposal
// helpers below.
type jsonRaw = json.RawMessage

// jsonMarshal / jsonUnmarshal are thin indirections over the standard
// library so the package keeps a single import-site for json.
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// DefaultCursorKey is the row key the iMIP intake worker uses in the
// shared cursors table. Distinct from the FTS, webhook, and DMARC keys
// so the four workers advance independently.
const DefaultCursorKey = "calendars-imip"

// DefaultPollInterval is the change-feed poll cadence. Calendar
// invites are not latency-critical: a one-minute lag matches what
// real-world clients (Google, Outlook) deliver via Push and gives
// operators a generous budget on a low-traffic server.
const DefaultPollInterval = 60 * time.Second

// DefaultBatchSize bounds the per-tick read.
const DefaultBatchSize = 64

// MaxBlobBytes caps how much of a message body the worker pulls into
// memory per change. iMIP attachments are tiny (< 100 KiB at the
// extreme); we cap at 1 MiB to absorb pathological cases without
// letting a single hostile message exhaust the worker.
const MaxBlobBytes = 1 << 20

// Options configures the iMIP intake worker.
type Options struct {
	// Store is the metadata + blob store. Required.
	Store store.Store
	// Logger is the structured logger; falls back to slog.Default.
	Logger *slog.Logger
	// Clock is the time source for poll-interval ticks; falls back
	// to clock.NewReal.
	Clock clock.Clock
	// PollInterval overrides DefaultPollInterval. <= 0 uses the
	// default.
	PollInterval time.Duration
	// BatchSize bounds the per-tick read; <= 0 uses DefaultBatchSize.
	BatchSize int
	// CursorKey overrides DefaultCursorKey. Tests use this to
	// isolate concurrent runs against the same fakestore.
	CursorKey string
}

// Intake watches the global change feed for new EntityKindEmail rows
// and applies the iMIP scheduling METHOD on each text/calendar part
// to the recipient's calendar.
type Intake struct {
	store  store.Store
	logger *slog.Logger
	clock  clock.Clock
	opts   Options

	cursor  atomic.Uint64
	running atomic.Bool
}

// New returns an Intake. The caller is responsible for keeping opts.Store
// alive for the lifetime of the Intake.
func New(opts Options) *Intake {
	if opts.Store == nil {
		panic("imip: New with nil store.Store")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.CursorKey == "" {
		opts.CursorKey = DefaultCursorKey
	}
	return &Intake{
		store:  opts.Store,
		logger: opts.Logger,
		clock:  opts.Clock,
		opts:   opts,
	}
}

// Cursor returns the worker's last persisted resume Seq.
func (i *Intake) Cursor() uint64 { return i.cursor.Load() }

// Run consumes the change feed until ctx is cancelled. Returns nil on
// graceful shutdown; non-nil only on a fatal cursor-table read failure.
// Safe to call once per Intake.
func (i *Intake) Run(ctx context.Context) error {
	if !i.running.CompareAndSwap(false, true) {
		return errors.New("imip: Intake already running")
	}
	defer i.running.Store(false)

	if seq, err := i.store.Meta().GetFTSCursor(ctx, i.opts.CursorKey); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("imip: load cursor: %w", err)
	} else {
		i.cursor.Store(seq)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		changes, err := i.store.FTS().ReadChangeFeedForFTS(ctx, i.cursor.Load(), i.opts.BatchSize)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("imip: read change feed: %w", err)
		}
		if len(changes) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-i.clock.After(i.opts.PollInterval):
			}
			continue
		}
		var maxSeq uint64
		for _, c := range changes {
			if err := ctx.Err(); err != nil {
				return nil
			}
			if c.Seq > maxSeq {
				maxSeq = c.Seq
			}
			if c.Kind != store.EntityKindEmail || c.Op != store.ChangeOpCreated {
				continue
			}
			i.processChange(ctx, c)
		}
		if maxSeq > 0 {
			i.cursor.Store(maxSeq)
			if err := i.store.Meta().SetFTSCursor(ctx, i.opts.CursorKey, maxSeq); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				i.logger.WarnContext(ctx, "imip: persist cursor",
					slog.String("key", i.opts.CursorKey),
					slog.Uint64("seq", maxSeq),
					slog.Any("err", err),
				)
			}
		}
	}
}

// processChange fetches the message indicated by c, walks the MIME
// tree for text/calendar parts, and dispatches each through the iMIP
// state machine. Errors are logged at warn and absorbed.
func (i *Intake) processChange(ctx context.Context, c store.FTSChange) {
	msgID := store.MessageID(c.EntityID)
	msg, err := i.store.Meta().GetMessage(ctx, msgID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			i.logger.WarnContext(ctx, "imip: get message",
				slog.Uint64("message_id", uint64(msgID)),
				slog.Any("err", err))
		}
		return
	}
	body, err := i.readBody(ctx, msg.Blob.Hash)
	if err != nil {
		i.logger.WarnContext(ctx, "imip: read body",
			slog.Uint64("message_id", uint64(msgID)),
			slog.String("blob_hash", msg.Blob.Hash),
			slog.Any("err", err))
		return
	}
	parts, err := extractCalendarParts(body)
	if err != nil {
		i.logger.WarnContext(ctx, "imip: extract calendar parts",
			slog.Uint64("message_id", uint64(msgID)),
			slog.Any("err", err))
		return
	}
	if len(parts) == 0 {
		return
	}
	pid := c.PrincipalID
	for _, p := range parts {
		cal, perr := jscalendar.ParseICS(bytes.NewReader(p))
		if perr != nil {
			i.logger.WarnContext(ctx, "imip: parse ics",
				slog.Uint64("message_id", uint64(msgID)),
				slog.Any("err", perr))
			continue
		}
		method := strings.ToUpper(cal.Method)
		for _, vev := range cal.Events {
			jev, jerr := vev.ToJSCalendarEvent(method)
			if jerr != nil {
				i.logger.WarnContext(ctx, "imip: bridge ics->jscalendar",
					slog.Uint64("message_id", uint64(msgID)),
					slog.Any("err", jerr))
				continue
			}
			if err := i.applyIMIP(ctx, pid, method, &jev); err != nil {
				i.logger.WarnContext(ctx, "imip: apply method",
					slog.Uint64("message_id", uint64(msgID)),
					slog.String("method", method),
					slog.Any("err", err))
			}
		}
	}
}

// applyIMIP dispatches one parsed VCALENDAR object on its scheduling
// METHOD to the recipient's calendar. RFC 5546 §3.2. Method is the
// upper-cased iCalendar METHOD string ("REQUEST", "CANCEL", ...).
func (i *Intake) applyIMIP(
	ctx context.Context,
	pid store.PrincipalID,
	method string,
	jev *jscalendar.Event,
) error {
	if jev == nil || jev.UID == "" {
		return errors.New("imip: missing UID")
	}
	switch method {
	case "REQUEST":
		return i.applyRequest(ctx, pid, jev)
	case "CANCEL":
		return i.applyCancel(ctx, pid, jev)
	case "REPLY":
		return i.applyReply(ctx, pid, jev, false)
	case "COUNTER":
		return i.applyReply(ctx, pid, jev, true)
	case "REFRESH":
		i.logger.DebugContext(ctx, "imip: refresh ignored (phase 3)",
			slog.String("uid", jev.UID))
		return nil
	default:
		i.logger.DebugContext(ctx, "imip: unknown method",
			slog.String("uid", jev.UID),
			slog.String("method", method))
		return nil
	}
}

// applyRequest is the core iMIP-as-create / iMIP-as-update flow. It
// looks up the existing event by UID; absent → InsertCalendarEvent in
// the recipient's default calendar (lazily creating one if absent).
// Present → UpdateCalendarEvent if and only if the inbound SEQUENCE
// is >= the stored sequence, per RFC 5546 §2.1.5.
func (i *Intake) applyRequest(ctx context.Context, pid store.PrincipalID, jev *jscalendar.Event) error {
	cal, err := i.ensureDefaultCalendar(ctx, pid)
	if err != nil {
		return fmt.Errorf("ensure default calendar: %w", err)
	}
	existing, err := i.findEventByUID(ctx, pid, jev.UID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("find event: %w", err)
	}
	body, mErr := jev.MarshalJSON()
	if mErr != nil {
		return fmt.Errorf("marshal event: %w", mErr)
	}
	startT, endT := startEndTimes(jev)
	row := store.CalendarEvent{
		PrincipalID:    pid,
		CalendarID:     cal.ID,
		UID:            jev.UID,
		JSCalendarJSON: body,
		Summary:        jev.Title,
		Start:          startT,
		End:            endT,
		IsRecurring:    jev.IsRecurring(),
		RRuleJSON:      rruleJSON(jev),
		OrganizerEmail: jev.OrganizerEmail(),
		Status:         jev.Status(),
	}
	if errors.Is(err, store.ErrNotFound) || existing.ID == 0 {
		if _, err := i.store.Meta().InsertCalendarEvent(ctx, row); err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
		if _, err := i.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
			return fmt.Errorf("bump event state: %w", err)
		}
		return nil
	}
	// Sequence lives inside the JSCalendar body, not on the store row.
	// Compare against the stored body's sequence so an out-of-order
	// REQUEST does not regress the calendar.
	storedSeq := storedSequence(existing.JSCalendarJSON)
	if jev.Sequence < storedSeq {
		i.logger.DebugContext(ctx, "imip: drop stale REQUEST",
			slog.String("uid", jev.UID),
			slog.Int("incoming_seq", jev.Sequence),
			slog.Int("stored_seq", storedSeq))
		return nil
	}
	row.ID = existing.ID
	row.CalendarID = existing.CalendarID
	if err := i.store.Meta().UpdateCalendarEvent(ctx, row); err != nil {
		return fmt.Errorf("update event: %w", err)
	}
	if _, err := i.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
		return fmt.Errorf("bump event state: %w", err)
	}
	return nil
}

// applyCancel marks the matching event status=cancelled but does not
// delete it: clients want history.
func (i *Intake) applyCancel(ctx context.Context, pid store.PrincipalID, jev *jscalendar.Event) error {
	existing, err := i.findEventByUID(ctx, pid, jev.UID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.logger.DebugContext(ctx, "imip: CANCEL for unknown uid",
				slog.String("uid", jev.UID))
			return nil
		}
		return fmt.Errorf("find event: %w", err)
	}
	// Mutate the stored JSCalendar body's status to cancelled.
	var stored jscalendar.Event
	if err := stored.UnmarshalJSON(existing.JSCalendarJSON); err != nil {
		return fmt.Errorf("parse stored event: %w", err)
	}
	stored.StatusValue = "cancelled"
	if jev.Sequence > stored.Sequence {
		stored.Sequence = jev.Sequence
	}
	body, mErr := stored.MarshalJSON()
	if mErr != nil {
		return fmt.Errorf("marshal event: %w", mErr)
	}
	existing.JSCalendarJSON = body
	existing.Status = "cancelled"
	if err := i.store.Meta().UpdateCalendarEvent(ctx, existing); err != nil {
		return fmt.Errorf("update event: %w", err)
	}
	if _, err := i.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
		return fmt.Errorf("bump event state: %w", err)
	}
	return nil
}

// applyReply applies a REPLY (and COUNTER) on the matching event:
// finds the responding attendee by email and updates their
// participationStatus to the iMIP REPLY's PARTSTAT. When counter is
// true, the proposed alternative is appended to the
// `counterProposals` extension array on the stored body so a future
// client surface can render the alternative for the organiser.
func (i *Intake) applyReply(
	ctx context.Context,
	pid store.PrincipalID,
	jev *jscalendar.Event,
	counter bool,
) error {
	existing, err := i.findEventByUID(ctx, pid, jev.UID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			i.logger.DebugContext(ctx, "imip: REPLY for unknown uid",
				slog.String("uid", jev.UID))
			return nil
		}
		return fmt.Errorf("find event: %w", err)
	}
	var stored jscalendar.Event
	if err := stored.UnmarshalJSON(existing.JSCalendarJSON); err != nil {
		return fmt.Errorf("parse stored event: %w", err)
	}
	if changed := mergeReplyParticipants(&stored, jev); !changed {
		i.logger.DebugContext(ctx, "imip: REPLY made no change",
			slog.String("uid", jev.UID))
	}
	if counter {
		appendCounterProposal(&stored, jev)
	}
	body, mErr := stored.MarshalJSON()
	if mErr != nil {
		return fmt.Errorf("marshal event: %w", mErr)
	}
	existing.JSCalendarJSON = body
	if err := i.store.Meta().UpdateCalendarEvent(ctx, existing); err != nil {
		return fmt.Errorf("update event: %w", err)
	}
	if _, err := i.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
		return fmt.Errorf("bump event state: %w", err)
	}
	return nil
}

// ensureDefaultCalendar returns the principal's default calendar,
// lazily creating one when the principal has never owned a calendar
// (e.g. an account whose first calendar interaction is an inbound
// invite).
func (i *Intake) ensureDefaultCalendar(ctx context.Context, pid store.PrincipalID) (store.Calendar, error) {
	c, err := i.store.Meta().DefaultCalendar(ctx, pid)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Calendar{}, err
	}
	row := store.Calendar{
		PrincipalID:  pid,
		Name:         "Calendar",
		IsSubscribed: true,
		IsVisible:    true,
		IsDefault:    true,
	}
	id, ierr := i.store.Meta().InsertCalendar(ctx, row)
	if ierr != nil {
		return store.Calendar{}, fmt.Errorf("insert default calendar: %w", ierr)
	}
	if _, ierr := i.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendar); ierr != nil {
		return store.Calendar{}, fmt.Errorf("bump calendar state: %w", ierr)
	}
	return i.store.Meta().GetCalendar(ctx, id)
}

// findEventByUID returns the principal-owned CalendarEvent with the
// given UID, or ErrNotFound. The store's ListCalendarEvents filter
// supports UID; we use it.
func (i *Intake) findEventByUID(ctx context.Context, pid store.PrincipalID, uid string) (store.CalendarEvent, error) {
	rows, err := i.store.Meta().ListCalendarEvents(ctx, store.CalendarEventFilter{
		PrincipalID: &pid,
		UID:         &uid,
		Limit:       1,
	})
	if err != nil {
		return store.CalendarEvent{}, err
	}
	if len(rows) == 0 {
		return store.CalendarEvent{}, store.ErrNotFound
	}
	return rows[0], nil
}

// readBody slurps the message blob into memory, capped at MaxBlobBytes.
func (i *Intake) readBody(ctx context.Context, hash string) ([]byte, error) {
	if hash == "" {
		return nil, errors.New("empty blob hash")
	}
	r, err := i.store.Blobs().Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, MaxBlobBytes))
}

// extractCalendarParts walks an RFC 5322 message body and returns
// every text/calendar part's bytes. Recurses into multipart parts
// once; iMIP envelopes are usually multipart/mixed of
// {text/plain or text/html, text/calendar} or
// multipart/alternative with a text/calendar inside, plus an
// optional application/ics attachment we also accept.
func extractCalendarParts(raw []byte) ([][]byte, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		return nil, nil
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil, fmt.Errorf("parse content-type: %w", err)
	}
	budget := newByteBudget(MaxBlobBytes)
	body, err := io.ReadAll(io.LimitReader(msg.Body, budget.remaining()))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	budget.consume(int64(len(body)))
	return collectCalendar(mt, params, body, 0, budget)
}

// byteBudget tracks the running total of bytes a single intake pass is
// permitted to read across all nested multipart parts. The outer body
// is bounded by MaxBlobBytes; deeper parts share that budget so a
// hostile sender cannot amplify a 1 MiB outer cap into many MiB of
// inner reads via multipart fan-out.
type byteBudget struct {
	left int64
}

func newByteBudget(limit int64) *byteBudget { return &byteBudget{left: limit} }

func (b *byteBudget) remaining() int64 {
	if b == nil || b.left < 0 {
		return 0
	}
	return b.left
}

func (b *byteBudget) consume(n int64) {
	if b == nil {
		return
	}
	b.left -= n
	if b.left < 0 {
		b.left = 0
	}
}

// collectCalendar walks one MIME node. depth caps recursion at 4 so a
// hostile sender cannot blow the stack with deeply-nested multiparts.
// budget caps total bytes read across the whole walk so deeply-nested
// multipart fan-outs cannot amplify the outer MaxBlobBytes cap.
func collectCalendar(mt string, params map[string]string, body []byte, depth int, budget *byteBudget) ([][]byte, error) {
	if depth > 4 {
		return nil, nil
	}
	switch {
	case mt == "text/calendar" || mt == "application/ics":
		return [][]byte{body}, nil
	case strings.HasPrefix(mt, "multipart/"):
		bound := params["boundary"]
		if bound == "" {
			return nil, nil
		}
		mr := multipart.NewReader(bytes.NewReader(body), bound)
		var out [][]byte
		for {
			if budget.remaining() <= 0 {
				return out, nil
			}
			p, err := mr.NextPart()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return out, nil
				}
				return out, nil
			}
			pCT := p.Header.Get("Content-Type")
			if pCT == "" {
				_ = p.Close()
				continue
			}
			pmt, pparams, perr := mime.ParseMediaType(pCT)
			if perr != nil {
				_ = p.Close()
				continue
			}
			pb, rerr := io.ReadAll(io.LimitReader(p, budget.remaining()))
			_ = p.Close()
			budget.consume(int64(len(pb)))
			if rerr != nil {
				continue
			}
			sub, _ := collectCalendar(pmt, pparams, pb, depth+1, budget)
			out = append(out, sub...)
		}
	}
	return nil, nil
}

// startEndTimes derives the UTC start / end time.Time stamps the
// store's denormalised columns require. Mirrors the helper in the
// parent calendars package; we duplicate rather than export from
// there to keep the worker independent of the JMAP handler types.
func startEndTimes(e *jscalendar.Event) (time.Time, time.Time) {
	if e.Start.IsZero() {
		return time.Time{}, time.Time{}
	}
	start := e.Start.UTC()
	end := start
	if d := e.Duration.Value; d > 0 {
		end = start.Add(d)
	}
	return start, end
}

// storedSequence reads the iTIP SEQUENCE counter from a stored
// JSCalendar body without re-deriving every field. RFC 5546 §2.1.5
// uses it for organiser-vs-receiver collision arbitration; the worker
// accepts only updates whose SEQUENCE >= the stored value.
func storedSequence(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	var probe struct {
		Sequence int `json:"sequence"`
	}
	_ = jsonUnmarshal(body, &probe)
	return probe.Sequence
}

// rruleJSON renders the master recurrence rules as JSON for the
// store's denormalised rrule_json column. Returns nil for
// non-recurring events.
func rruleJSON(e *jscalendar.Event) []byte {
	if !e.IsRecurring() {
		return nil
	}
	body, err := encodeJSON(e.RecurrenceRules)
	if err != nil {
		return nil
	}
	return body
}

// encodeJSON is a small indirection over encoding/json; isolates the
// import surface so the package's test files don't need to reach for
// json directly.
func encodeJSON(v any) ([]byte, error) {
	return jsonMarshal(v)
}

// mergeReplyParticipants applies the responding attendee's PARTSTAT on
// the stored event's participants map. Returns true if a participant
// row was actually mutated. The matching key is the participant's
// SendTo[].mailto: address (or, equivalently for v1, the email field
// the bridge populated).
func mergeReplyParticipants(stored, reply *jscalendar.Event) bool {
	if stored == nil || reply == nil {
		return false
	}
	if stored.Participants == nil {
		stored.Participants = map[string]jscalendar.Participant{}
	}
	changed := false
	for _, rp := range reply.Participants {
		// Match by email (the bridge stores mailto addresses verbatim).
		key := participantKey(rp)
		if key == "" {
			continue
		}
		// Find the matching stored participant or insert one.
		matchKey := ""
		for sk, sp := range stored.Participants {
			if participantKey(sp) == key {
				matchKey = sk
				break
			}
		}
		if matchKey == "" {
			matchKey = key
			stored.Participants[matchKey] = rp
			changed = true
			continue
		}
		sp := stored.Participants[matchKey]
		if sp.ParticipationStatus != rp.ParticipationStatus && rp.ParticipationStatus != "" {
			sp.ParticipationStatus = rp.ParticipationStatus
			stored.Participants[matchKey] = sp
			changed = true
		}
	}
	return changed
}

// participantKey returns the canonical lowercased lookup key for a
// participant: the bridge-projected Email if present, otherwise an
// "imip" entry in the sendTo RawJSON. Empty when the participant
// carries no usable address (an unparsed REPLY shape we silently
// drop).
func participantKey(p jscalendar.Participant) string {
	if p.Email != "" {
		return strings.ToLower(p.Email)
	}
	if raw, ok := p.RawJSON["sendTo"]; ok {
		var send map[string]string
		if jsonUnmarshal(raw, &send) == nil {
			if v, ok := send["imip"]; ok {
				return strings.ToLower(strings.TrimPrefix(v, "mailto:"))
			}
			for _, v := range send {
				return strings.ToLower(strings.TrimPrefix(v, "mailto:"))
			}
		}
	}
	return ""
}

// appendCounterProposal records the COUNTER's proposed alternative on
// the stored event's RawJSON under the "counterProposals" extension
// key. The shape is one JSON object per proposal carrying the
// proposer's email plus the proposed start / duration / location.
// Clients render the array and present accept/reject UI; the v1
// server itself takes no further action.
func appendCounterProposal(stored, counter *jscalendar.Event) {
	if stored == nil || counter == nil {
		return
	}
	if stored.RawJSON == nil {
		stored.RawJSON = map[string]jsonRaw{}
	}
	prop := map[string]any{
		"proposer": counterProposer(counter),
	}
	if !counter.Start.IsZero() {
		prop["start"] = counter.Start.UTC().Format(time.RFC3339)
	}
	if d := counter.Duration.Value; d > 0 {
		prop["duration"] = d.String()
	}
	body, err := jsonMarshal(prop)
	if err != nil {
		return
	}
	// Append to the existing array, or start a fresh one.
	var existing []jsonRaw
	if raw, ok := stored.RawJSON["counterProposals"]; ok {
		_ = jsonUnmarshal(raw, &existing)
	}
	existing = append(existing, body)
	merged, err := jsonMarshal(existing)
	if err != nil {
		return
	}
	stored.RawJSON["counterProposals"] = merged
}

// counterProposer returns the email address of the participant who
// sent the COUNTER. Used to attribute the proposal in the stored
// extension array.
func counterProposer(e *jscalendar.Event) string {
	for _, p := range e.Participants {
		if k := participantKey(p); k != "" {
			return k
		}
	}
	return ""
}
