package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// PayloadCapBytes is the per-field byte cap for free-text payload
// fields per REQ-PROTO-125 (subject, preview, chat text). The cap is
// expressed in bytes (not runes) so the encoded payload size is
// bounded; we use UTF-8-safe truncation (truncateUTF8) so we never
// emit a partial code point.
const PayloadCapBytes = 80

// buildPayloadResult is the (bytes, coalesce-tag) pair the dispatcher
// hands forward. The coalesce-tag is empty in 3.8b — Wave 3.8c will
// populate it with a stable per-thread / per-conversation key so
// REQ-PROTO-124 coalescing can replace stacked notifications.
type buildPayloadResult struct {
	JSON         []byte
	CoalesceTag  string
	OriginatorID uint64
}

// BuildPayload constructs the privacy-capped JSON payload the
// dispatcher will encrypt and POST to the push endpoint, plus a
// coalesce-tag the dispatcher uses to replace stacked notifications
// per REQ-PROTO-124. Per REQ-PROTO-125 the payload never carries full
// message bodies — subjects + 80-char previews for mail, 80-char
// excerpts (or [image] / [reaction] markers) for chat, structured
// fields only for calendar.
//
// The dispatcher consults notificationRules (rules.go) before calling
// BuildPayload; events the rule denies never reach this path.
func BuildPayload(ctx context.Context, st store.Store, ev store.StateChange) (buildPayloadResult, error) {
	switch ev.Kind {
	case store.EntityKindEmail:
		return buildEmailPayload(ctx, st, ev)
	case store.EntityKindChatMessage:
		return buildChatPayload(ctx, st, ev)
	case store.EntityKindCalendarEvent:
		return buildCalendarEventPayload(ctx, st, ev)
	default:
		return buildPayloadResult{}, errUnsupportedKind
	}
}

// errUnsupportedKind is returned when the change-feed event's Kind is
// outside the set of push-eligible types. The dispatcher treats this
// as "skip" — not every state-change kind warrants a push (mailbox
// renames, identity edits, address-book churn). Only Email, ChatMessage
// and CalendarEvent fan out to push subscribers in 3.8b.
var errUnsupportedKind = errors.New("webpush: event kind not push-eligible")

type emailPayload struct {
	Type    string `json:"type"`
	From    string `json:"from,omitempty"`
	Subject string `json:"subject,omitempty"`
	Preview string `json:"preview,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
	MsgID   string `json:"msgid,omitempty"`
}

func buildEmailPayload(ctx context.Context, st store.Store, ev store.StateChange) (buildPayloadResult, error) {
	if ev.Op != store.ChangeOpCreated && ev.Op != store.ChangeOpUpdated {
		return buildPayloadResult{}, errUnsupportedKind
	}
	msgID := store.MessageID(ev.EntityID)
	msg, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		return buildPayloadResult{}, fmt.Errorf("webpush: get email %d: %w", msgID, err)
	}
	mbox, err := st.Meta().GetMailboxByID(ctx, msg.MailboxID)
	if err != nil {
		// A missing mailbox is not fatal — the message row carries
		// enough envelope data on its own.
		mbox = store.Mailbox{Name: ""}
	}
	out := emailPayload{
		Type:    "email",
		From:    truncateUTF8(msg.Envelope.From, PayloadCapBytes),
		Subject: truncateUTF8(msg.Envelope.Subject, PayloadCapBytes),
		Mailbox: mbox.Name,
		MsgID:   fmt.Sprintf("%d", msg.ID),
	}
	// REQ-PROTO-125 / Wave 3.8c spec resolution: build the 80-byte
	// preview by re-walking the blob inline via mailparse. This is the
	// cheap path now that the FTS extractor doubles as the preview
	// source. We tolerate every error class (blob fetch miss, parse
	// failure, no plain-text origin) by silently omitting `preview`
	// from the payload — push delivery never blocks on preview
	// extraction.
	if preview := emailPreview(ctx, st, msg); preview != "" {
		out.Preview = truncateUTF8(preview, PayloadCapBytes)
	}

	js, err := json.Marshal(out)
	if err != nil {
		return buildPayloadResult{}, fmt.Errorf("webpush: marshal email payload: %w", err)
	}
	// Coalesce-tag: stable per-thread key so 3.8c can replace
	// stacked notifications for the same thread. We compute it now
	// but leave the dispatcher's HTTP `Topic` header empty in 3.8b;
	// the caller threads the tag through to 3.8c untouched.
	tag := ""
	if msg.ThreadID != 0 {
		tag = fmt.Sprintf("email/%d", msg.ThreadID)
	}
	return buildPayloadResult{JSON: js, CoalesceTag: tag, OriginatorID: uint64(msg.ID)}, nil
}

type chatPayload struct {
	Type           string `json:"type"`
	From           string `json:"from,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Text           string `json:"text,omitempty"`
}

func buildChatPayload(ctx context.Context, st store.Store, ev store.StateChange) (buildPayloadResult, error) {
	if ev.Op != store.ChangeOpCreated && ev.Op != store.ChangeOpUpdated {
		return buildPayloadResult{}, errUnsupportedKind
	}
	id := store.ChatMessageID(ev.EntityID)
	msg, err := st.Meta().GetChatMessage(ctx, id)
	if err != nil {
		return buildPayloadResult{}, fmt.Errorf("webpush: get chat msg %d: %w", id, err)
	}
	if msg.IsSystem {
		// System messages (call.started, joiner notes) do not push.
		return buildPayloadResult{}, errUnsupportedKind
	}
	out := chatPayload{
		Type:           "chat",
		ConversationID: fmt.Sprintf("%d", msg.ConversationID),
	}
	if msg.SenderPrincipalID != nil {
		if p, err := st.Meta().GetPrincipalByID(ctx, *msg.SenderPrincipalID); err == nil {
			from := p.DisplayName
			if from == "" {
				from = p.CanonicalEmail
			}
			out.From = truncateUTF8(from, PayloadCapBytes)
		}
	}
	switch {
	case strings.TrimSpace(msg.BodyText) != "":
		out.Text = truncateUTF8(msg.BodyText, PayloadCapBytes)
	case len(msg.AttachmentsJSON) > 0:
		out.Text = "[image]"
	case len(msg.ReactionsJSON) > 0:
		out.Text = "[reaction]"
	default:
		out.Text = ""
	}
	js, err := json.Marshal(out)
	if err != nil {
		return buildPayloadResult{}, fmt.Errorf("webpush: marshal chat payload: %w", err)
	}
	tag := fmt.Sprintf("chat/%d", msg.ConversationID)
	return buildPayloadResult{JSON: js, CoalesceTag: tag, OriginatorID: uint64(msg.ID)}, nil
}

type calendarPayload struct {
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	From     string `json:"from,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	Location string `json:"location,omitempty"`
	UID      string `json:"uid,omitempty"`
}

func buildCalendarEventPayload(ctx context.Context, st store.Store, ev store.StateChange) (buildPayloadResult, error) {
	if ev.Op != store.ChangeOpCreated && ev.Op != store.ChangeOpUpdated {
		return buildPayloadResult{}, errUnsupportedKind
	}
	id := store.CalendarEventID(ev.EntityID)
	cev, err := st.Meta().GetCalendarEvent(ctx, id)
	if err != nil {
		return buildPayloadResult{}, fmt.Errorf("webpush: get calendar event %d: %w", id, err)
	}
	out := calendarPayload{
		Type:  "calendar",
		Title: truncateUTF8(cev.Summary, PayloadCapBytes),
		UID:   cev.UID,
		From:  cev.OrganizerEmail,
	}
	if !cev.Start.IsZero() {
		out.Start = cev.Start.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !cev.End.IsZero() {
		out.End = cev.End.UTC().Format("2006-01-02T15:04:05Z")
	}
	// Location is in JSCalendarJSON; per REQ-PROTO-125 we surface
	// "structured fields only", so a best-effort lookup of the
	// JSCalendar `locations[*].name` is permitted but we keep the
	// implementation tiny — extracting a single string out of the
	// raw JSON without an extra dependency is fine, but the field is
	// optional and we do not synthesize a fallback. 3.8c may add a
	// dedicated locations parser when the rules engine consults
	// per-event filtering.
	if loc := extractCalendarLocation(cev.JSCalendarJSON); loc != "" {
		out.Location = truncateUTF8(loc, PayloadCapBytes)
	}
	js, err := json.Marshal(out)
	if err != nil {
		return buildPayloadResult{}, fmt.Errorf("webpush: marshal calendar payload: %w", err)
	}
	tag := fmt.Sprintf("calendar/%s", cev.UID)
	return buildPayloadResult{JSON: js, CoalesceTag: tag, OriginatorID: uint64(cev.ID)}, nil
}

// extractCalendarLocation pulls locations[*].name out of a JSCalendar
// JSON blob. Returns "" when the blob is empty, malformed, or has no
// locations — none of those is an error condition for the push
// builder, so we silently fall through to no-location.
func extractCalendarLocation(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	var generic struct {
		Locations map[string]struct {
			Name string `json:"name"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(blob, &generic); err != nil {
		return ""
	}
	for _, loc := range generic.Locations {
		if loc.Name != "" {
			return loc.Name
		}
	}
	return ""
}

// emailPreview re-walks msg.Blob through mailparse and returns the
// best-effort plain-text body (REQ-PROTO-125 + Wave 3.8c). Failures —
// blob miss, parse error, no plain-text origin — are debug-logged and
// collapse to "" so the push payload omits the field entirely; we never
// fail a push because the preview could not be built.
//
// The walk is inline (no cache) per the 3.8b reconcile decision; the FTS
// indexer already exercises the same parser on the same bytes, so the
// cost is bounded by message size. The cap on push payloads (REQ-PROTO-125)
// keeps the bytes that actually leave herold to <=80 plus subject /
// envelope chrome.
func emailPreview(ctx context.Context, st store.Store, msg store.Message) string {
	if msg.Blob.Hash == "" {
		return ""
	}
	rc, err := st.Blobs().Get(ctx, msg.Blob.Hash)
	if err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelDebug,
			"webpush: preview blob fetch failed",
			slog.Uint64("message", uint64(msg.ID)),
			slog.String("err", err.Error()))
		return ""
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelDebug,
			"webpush: preview blob read failed",
			slog.Uint64("message", uint64(msg.ID)),
			slog.String("err", err.Error()))
		return ""
	}
	parsed, err := mailparse.Parse(strings.NewReader(string(body)),
		mailparse.ParseOptions{StrictBoundary: false})
	if err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelDebug,
			"webpush: preview parse failed",
			slog.Uint64("message", uint64(msg.ID)),
			slog.String("err", err.Error()))
		return ""
	}
	text, origin := mailparse.ExtractBodyText(parsed)
	if origin == mailparse.BodyTextOriginNone || text == "" {
		return ""
	}
	// Collapse runs of whitespace so the 80-byte preview is dense
	// readable text rather than CRLF-padded headers. ExtractBodyText
	// already strips HTML; here we just normalise whitespace so the
	// truncation cap maximises information density.
	return previewCollapseWhitespace(text)
}

// previewCollapseWhitespace folds runs of whitespace (including
// newlines) into a single space and trims leading/trailing whitespace.
// Mirrors the mail/searchsnippet helper — duplicated here to avoid an
// upward webpush -> protojmap import dependency.
func previewCollapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

// truncateUTF8 returns s truncated to at most maxBytes bytes, never
// splitting a UTF-8 code point. When s is already short enough the
// original string is returned unchanged.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk back from maxBytes until we land on a UTF-8 boundary
	// (utf8.RuneStart) so we never emit a partial code point.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
