package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Closed-enum event-type vocabulary used by the rules engine. The dispatcher
// maps each store.StateChange to one of these tokens before consulting the
// per-type allow/deny map. Adding a new value here is a closed-set extension
// and requires a coordinated suite change (REQ-PROTO-127 + REQ-PUSH-81).
const (
	EventTypeMail             = "mail"
	EventTypeChatDM           = "chat_dm"
	EventTypeChatSpace        = "chat_space"
	EventTypeCalendarInvite   = "calendar_invite"
	EventTypeCalendarReminder = "calendar_reminder"
	EventTypeCallIncoming     = "call_incoming"
	EventTypeCallMissed       = "call_missed"
	// EventTypeReaction is reserved per REQ-PROTO-127's closed enum.
	// Reaction detection on the change feed lands in Wave 3.9; today
	// reactions surface as ordinary EntityKindEmail updates and are
	// classified as EventTypeMail. The constant is kept so persisted
	// rules referencing "reaction" round-trip cleanly.
	EventTypeReaction = "reaction"
)

// allEventTypes is the closed-enum set the parser validates per-event-type
// keys against. Order matches the documented vocabulary.
var allEventTypes = []string{
	EventTypeMail,
	EventTypeChatDM,
	EventTypeChatSpace,
	EventTypeCalendarInvite,
	EventTypeCalendarReminder,
	EventTypeCallIncoming,
	EventTypeCallMissed,
	EventTypeReaction,
}

// isKnownEventType reports whether t is in the closed enum.
func isKnownEventType(t string) bool {
	for _, v := range allEventTypes {
		if v == t {
			return true
		}
	}
	return false
}

// Rules is the parsed shape of a PushSubscription.notificationRules JSON
// blob. The wire grammar mirrors the suite's REQ-PUSH-80..83 surface; unknown
// top-level fields are preserved verbatim in Unknown so future suite
// versions can extend the rules without herold needing schema changes
// (REQ-PROTO-127).
type Rules struct {
	// Master is REQ-PUSH-80's master enable/disable switch. When false,
	// every event denies. Default true.
	Master bool
	// PerEventType maps a closed-enum event-type token to true/false.
	// Default values come from REQ-PUSH-81: mail/chat_dm/chat_space/
	// calendar_invite/call_incoming/call_missed/reaction = true. Keys
	// outside the closed set are ignored on read and warn-logged on
	// parse via WarnUnknownEventTypes.
	PerEventType map[string]bool
	// MailCategoryAllowlist is the set of mail categories that pass the
	// rule. Defaults to ["primary"] per REQ-PUSH-81. The categoriser
	// (REQ-FILT-200) tags messages with "$category-<name>" keywords;
	// the dispatcher reads the bare category name out of the keyword
	// list when evaluating this allowlist.
	MailCategoryAllowlist []string
	// QuietHoursStartLocal is the inclusive start hour [0..23] of the
	// quiet-hours window in QuietHoursTZ. nil disables quiet hours.
	QuietHoursStartLocal *int
	// QuietHoursEndLocal is the exclusive end hour [0..23] of the
	// quiet-hours window. nil disables quiet hours.
	QuietHoursEndLocal *int
	// QuietHoursTZ is the IANA timezone name (e.g. "Europe/Berlin").
	// Empty when quiet hours are disabled.
	QuietHoursTZ string
	// QuietHoursOverridePerType, when true for an event-type token,
	// allows that event to break through quiet hours. REQ-PUSH-82
	// notes "incoming video call" should override by default; we
	// default true for EventTypeCallIncoming.
	QuietHoursOverridePerType map[string]bool
	// SenderVIPs is REQ-PUSH-83's VIP list. Per the spec the list is
	// stored client-local, so the field is accepted in JSON for
	// forward-compat / round-trip but the server never consults it.
	SenderVIPs []string
	// Unknown captures any top-level JSON fields outside the closed
	// vocabulary above so REQ-PROTO-127's "preserve unknown fields"
	// guarantee survives a save / load round-trip.
	Unknown map[string]json.RawMessage
	// WarnUnknownEventTypes lists per-event-type keys that did NOT
	// match the closed enum. ParseRules surfaces these so callers can
	// warn-log them once at parse time; the values themselves are
	// dropped from PerEventType (an unknown key cannot deny because it
	// names no event the dispatcher knows about).
	WarnUnknownEventTypes []string
}

// DefaultRules returns the REQ-PUSH-81 defaults: every event-type allowed,
// mail filtered to the "primary" category, no quiet hours, call_incoming
// breaks quiet hours when they are configured.
func DefaultRules() Rules {
	per := make(map[string]bool, len(allEventTypes))
	for _, k := range allEventTypes {
		per[k] = true
	}
	override := map[string]bool{
		EventTypeCallIncoming: true,
	}
	return Rules{
		Master:                    true,
		PerEventType:              per,
		MailCategoryAllowlist:     []string{"primary"},
		QuietHoursOverridePerType: override,
	}
}

// rulesWire is the JSON wire grammar; it mirrors the loose suite shape
// described in the suite/docs/notes/server-contract.md plus the per-event-type
// toggle map and quiet-hours fields settings exposes (REQ-PUSH-80..83).
//
// Top-level keys we recognise:
//
//	master              bool
//	perEventType        object<string,bool>
//	mailCategories      []string  // REQ-PUSH-81 mail-by-category
//	quietHours          { startHourLocal, endHourLocal, tz }
//	quietHoursOverride  object<string,bool>
//	senderVIPs          []string  // forward-compat; ignored server-side
//
// Anything else is captured into Unknown verbatim per REQ-PROTO-127.
type rulesWire struct {
	Master             *bool           `json:"master,omitempty"`
	PerEventType       map[string]bool `json:"perEventType,omitempty"`
	MailCategories     []string        `json:"mailCategories,omitempty"`
	QuietHours         *quietHoursWire `json:"quietHours,omitempty"`
	QuietHoursOverride map[string]bool `json:"quietHoursOverride,omitempty"`
	SenderVIPs         []string        `json:"senderVIPs,omitempty"`
}

type quietHoursWire struct {
	StartHourLocal *int   `json:"startHourLocal,omitempty"`
	EndHourLocal   *int   `json:"endHourLocal,omitempty"`
	TZ             string `json:"tz,omitempty"`
}

// knownTopLevelKeys is the set of fields rulesWire recognises; anything
// outside this set lands in Rules.Unknown.
var knownTopLevelKeys = map[string]struct{}{
	"master":             {},
	"perEventType":       {},
	"mailCategories":     {},
	"quietHours":         {},
	"quietHoursOverride": {},
	"senderVIPs":         {},
}

// ParseRules decodes raw as a notificationRules JSON blob and returns the
// parsed Rules struct. Empty / nil input returns DefaultRules with no
// error. Malformed JSON returns an error wrapping the json.Unmarshal
// failure. Unknown top-level keys are preserved verbatim per
// REQ-PROTO-127; per-event-type keys outside the closed enum are dropped
// from the in-memory map but surfaced via WarnUnknownEventTypes for the
// caller to warn-log once.
func ParseRules(raw []byte) (Rules, error) {
	if len(raw) == 0 {
		return DefaultRules(), nil
	}
	// First pass: capture every top-level field as RawMessage so we can
	// preserve unknown keys verbatim.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		return Rules{}, fmt.Errorf("webpush: parse notificationRules: %w", err)
	}
	// Second pass: decode the known fields via rulesWire so we get the
	// stdlib's type checking on each.
	var wire rulesWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Rules{}, fmt.Errorf("webpush: parse notificationRules typed: %w", err)
	}
	out := DefaultRules()
	if wire.Master != nil {
		out.Master = *wire.Master
	}
	if wire.PerEventType != nil {
		// Start fresh: the wire map is authoritative for the keys it
		// names. Keys outside the closed enum are dropped + reported.
		out.PerEventType = make(map[string]bool, len(wire.PerEventType))
		for k, v := range wire.PerEventType {
			if !isKnownEventType(k) {
				out.WarnUnknownEventTypes = append(out.WarnUnknownEventTypes, k)
				continue
			}
			out.PerEventType[k] = v
		}
		// Defaults still apply for any closed-enum key the client did
		// not explicitly mention; merge in DefaultRules' values so the
		// dispatcher does not have to special-case "key absent" later.
		for _, k := range allEventTypes {
			if _, ok := out.PerEventType[k]; !ok {
				out.PerEventType[k] = true
			}
		}
	}
	if wire.MailCategories != nil {
		// An explicit empty list means "no categories pass" — that is a
		// legal user configuration (mail off via category filter
		// instead of perEventType). We honour the empty case rather
		// than collapsing to DefaultRules.
		out.MailCategoryAllowlist = append([]string(nil), wire.MailCategories...)
	}
	if wire.QuietHours != nil {
		if wire.QuietHours.StartHourLocal != nil {
			h := *wire.QuietHours.StartHourLocal
			if h < 0 || h > 23 {
				return Rules{}, fmt.Errorf("webpush: quietHours.startHourLocal %d out of range [0..23]", h)
			}
			out.QuietHoursStartLocal = &h
		}
		if wire.QuietHours.EndHourLocal != nil {
			h := *wire.QuietHours.EndHourLocal
			if h < 0 || h > 23 {
				return Rules{}, fmt.Errorf("webpush: quietHours.endHourLocal %d out of range [0..23]", h)
			}
			out.QuietHoursEndLocal = &h
		}
		out.QuietHoursTZ = wire.QuietHours.TZ
		if out.QuietHoursTZ != "" {
			if _, err := time.LoadLocation(out.QuietHoursTZ); err != nil {
				return Rules{}, fmt.Errorf("webpush: quietHours.tz %q: %w", out.QuietHoursTZ, err)
			}
		}
	}
	if wire.QuietHoursOverride != nil {
		// Replace the default map; defaults remerged for unknown keys.
		out.QuietHoursOverridePerType = make(map[string]bool, len(wire.QuietHoursOverride))
		for k, v := range wire.QuietHoursOverride {
			if !isKnownEventType(k) {
				// Mark unknown overrides via the same warn channel —
				// the caller will warn-log once.
				out.WarnUnknownEventTypes = append(out.WarnUnknownEventTypes, k)
				continue
			}
			out.QuietHoursOverridePerType[k] = v
		}
		// Re-merge default override for call_incoming if not specified
		// explicitly.
		if _, ok := out.QuietHoursOverridePerType[EventTypeCallIncoming]; !ok {
			out.QuietHoursOverridePerType[EventTypeCallIncoming] = true
		}
	}
	if wire.SenderVIPs != nil {
		out.SenderVIPs = append([]string(nil), wire.SenderVIPs...)
	}
	// Capture unknown top-level fields verbatim.
	for k, v := range generic {
		if _, ok := knownTopLevelKeys[k]; ok {
			continue
		}
		if out.Unknown == nil {
			out.Unknown = make(map[string]json.RawMessage)
		}
		out.Unknown[k] = append(json.RawMessage(nil), v...)
	}
	return out, nil
}

// MarshalJSON re-emits Rules in the wire grammar ParseRules consumes. It
// preserves unknown top-level fields verbatim so a save / load round-trip
// is byte-stable for the unknown subset (and semantically stable for the
// known subset).
func (r Rules) MarshalJSON() ([]byte, error) {
	wire := rulesWire{}
	master := r.Master
	wire.Master = &master
	if len(r.PerEventType) > 0 {
		wire.PerEventType = r.PerEventType
	}
	if r.MailCategoryAllowlist != nil {
		wire.MailCategories = r.MailCategoryAllowlist
	}
	if r.QuietHoursStartLocal != nil || r.QuietHoursEndLocal != nil || r.QuietHoursTZ != "" {
		wire.QuietHours = &quietHoursWire{
			StartHourLocal: r.QuietHoursStartLocal,
			EndHourLocal:   r.QuietHoursEndLocal,
			TZ:             r.QuietHoursTZ,
		}
	}
	if len(r.QuietHoursOverridePerType) > 0 {
		wire.QuietHoursOverride = r.QuietHoursOverridePerType
	}
	if len(r.SenderVIPs) > 0 {
		wire.SenderVIPs = r.SenderVIPs
	}
	primary, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("webpush: marshal notificationRules: %w", err)
	}
	if len(r.Unknown) == 0 {
		return primary, nil
	}
	// Merge primary + Unknown into a single object. Decoding to a map
	// here is safe: the wire shape is a JSON object, and the field set
	// is closed.
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(primary, &merged); err != nil {
		return nil, fmt.Errorf("webpush: re-decode marshalled rules: %w", err)
	}
	for k, v := range r.Unknown {
		if _, ok := knownTopLevelKeys[k]; ok {
			continue
		}
		merged[k] = v
	}
	return json.Marshal(merged)
}

// RuleDecision is the (allow, reason) pair the dispatcher consumes when
// deciding whether to fan out a single event to a single subscription.
// Reason is a closed-enum token suitable for log + metric labels.
type RuleDecision struct {
	Allow     bool
	Reason    string
	EventType string
}

// Decision reasons. Closed enum; do not extend without coordinating with
// the metrics label vocabulary in observe/metrics_subsystems.go.
const (
	ReasonMutedMaster      = "muted_master"
	ReasonMutedEventType   = "muted_event_type"
	ReasonCategoryFiltered = "category_filtered"
	ReasonQuietHours       = "quiet_hours"
	ReasonDefaultAllow     = "default_allow"
	ReasonUnsupportedEvent = "unsupported_event"
)

// errUnknownConversation is returned by classifyEvent when a chat event
// references a conversation row the metadata side cannot find. The
// dispatcher treats this as a non-fatal "skip" — the row may have been
// destroyed between the change-feed read and the rule evaluation.
var errUnknownConversation = errors.New("webpush: chat conversation not found")

// classifyEvent maps a store.StateChange to one of the closed-enum event
// tokens above. Returns "" + error when the event references a missing
// row (the dispatcher logs + skips); returns ("", nil) for events outside
// the push-eligible set (defensive — the dispatcher gates on isPushable
// before calling, but a future caller might not).
//
// Mapping rationale:
//
//   - EntityKindEmail              -> EventTypeMail. Reaction detection is
//     deferred to Wave 3.9 (REQ-PROTO-127 reserves the "reaction" key in
//     the closed enum but the change-feed shape that distinguishes a
//     reaction from a regular Email update lands later).
//   - EntityKindChatMessage        -> EventTypeChatDM if the conversation
//     kind is "dm", else EventTypeChatSpace.
//   - EntityKindCalendarEvent     -> EventTypeCalendarInvite. Reminders
//     are tracked separately (calendar alarms surface via a different
//     path); we always classify a state-change on the event row itself
//     as an invite for today.
//
// Calls (REQ-PROTO-126) flow through the chat envelope as system messages
// today; classifyEvent treats them as their underlying chat event type.
// A future wave that emits a dedicated EntityKind for calls will extend
// this dispatch table.
func classifyEvent(ctx context.Context, st store.Store, ev store.StateChange) (string, error) {
	switch ev.Kind {
	case store.EntityKindEmail:
		return EventTypeMail, nil
	case store.EntityKindChatMessage:
		msg, err := st.Meta().GetChatMessage(ctx, store.ChatMessageID(ev.EntityID))
		if err != nil {
			return "", fmt.Errorf("webpush: classify chat message %d: %w", ev.EntityID, err)
		}
		conv, err := st.Meta().GetChatConversation(ctx, msg.ConversationID)
		if err != nil {
			return "", fmt.Errorf("webpush: classify chat conversation %d: %w", msg.ConversationID, errUnknownConversation)
		}
		if conv.Kind == store.ChatConversationKindDM {
			return EventTypeChatDM, nil
		}
		return EventTypeChatSpace, nil
	case store.EntityKindCalendarEvent:
		return EventTypeCalendarInvite, nil
	}
	return "", nil
}

// categoryFromMessage extracts the bare category name carried by the
// first "$category-*" keyword on m, or "" when none is present. Mirrors
// internal/categorise.CategoryFromKeywords; we duplicate the constant
// here to avoid a webpush -> categorise import dependency.
func categoryFromMessage(m store.Message) string {
	const prefix = "$category-"
	for _, k := range m.Keywords {
		if strings.HasPrefix(k, prefix) {
			return strings.TrimPrefix(k, prefix)
		}
	}
	return ""
}

// inAllowlist reports whether category appears in allow.
func inAllowlist(allow []string, category string) bool {
	for _, a := range allow {
		if a == category {
			return true
		}
	}
	return false
}

// withinQuietHours reports whether instant t (in tz) lies within the
// half-open hour-of-day window [start, end). Handles wrap-around midnight
// (e.g. start=22 end=7 means 22:00..23:59 plus 00:00..06:59). When start
// and end are equal the window is empty (matches the expected user
// model: "no quiet hours" rather than "always quiet").
func withinQuietHours(t time.Time, tz string, start, end int) bool {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// A bad tz at evaluation time is non-fatal; treat as "not in
		// quiet hours" so a pre-validated rule that has somehow lost
		// its tz between save and read does not silently mute every
		// notification.
		return false
	}
	hour := t.In(loc).Hour()
	if start == end {
		return false
	}
	if start < end {
		return hour >= start && hour < end
	}
	// Wrap-around: e.g. 22..7 covers [22..23] and [0..6].
	return hour >= start || hour < end
}

// Evaluate applies rules to ev for principal and returns the decision.
// st is consulted only for the per-event classification (chat
// conversation kind) and the mail-category lookup; the rules logic
// itself is otherwise pure. The clock is used for the quiet-hours hour
// extraction.
//
// Decision precedence (REQ-PROTO-127 + REQ-PUSH-80..83):
//
//  1. Master == false               -> deny "muted_master"
//  2. Event maps to no closed type  -> deny "unsupported_event"
//  3. PerEventType[type] == false   -> deny "muted_event_type"
//  4. Mail + category not allowed   -> deny "category_filtered"
//  5. Quiet hours active and the
//     per-type override is false    -> deny "quiet_hours"
//  6. otherwise                     -> allow "default_allow"
//
// VIP handling (REQ-PUSH-83): VIPs are client-local; the field is
// accepted in the JSON for forward-compat but Evaluate never consults
// it. The closed reason set therefore omits a VIP outcome.
func Evaluate(
	ctx context.Context,
	rules Rules,
	st store.Store,
	ev store.StateChange,
	now time.Time,
) RuleDecision {
	if !rules.Master {
		return RuleDecision{Allow: false, Reason: ReasonMutedMaster}
	}
	eventType, err := classifyEvent(ctx, st, ev)
	if err != nil || eventType == "" {
		return RuleDecision{Allow: false, Reason: ReasonUnsupportedEvent}
	}
	if allowed, ok := rules.PerEventType[eventType]; ok && !allowed {
		return RuleDecision{Allow: false, Reason: ReasonMutedEventType, EventType: eventType}
	}
	if eventType == EventTypeMail {
		// Look up the message's category keyword; deny when it is not
		// in the allowlist (and the allowlist is non-nil — a nil
		// allowlist behaves like "no category filter", treating every
		// mail event as allowed regardless of category).
		if rules.MailCategoryAllowlist != nil {
			msg, mErr := st.Meta().GetMessage(ctx, store.MessageID(ev.EntityID))
			if mErr == nil {
				cat := categoryFromMessage(msg)
				// An uncategorised message (no $category-* keyword) is
				// treated as "primary" — operators that have not
				// configured the categoriser see all mail under the
				// default allowlist. This matches REQ-FILT-200's
				// graceful-fallback contract.
				if cat == "" {
					cat = "primary"
				}
				if !inAllowlist(rules.MailCategoryAllowlist, cat) {
					return RuleDecision{Allow: false, Reason: ReasonCategoryFiltered, EventType: eventType}
				}
			}
		}
	}
	if rules.QuietHoursStartLocal != nil && rules.QuietHoursEndLocal != nil && rules.QuietHoursTZ != "" {
		if withinQuietHours(now, rules.QuietHoursTZ, *rules.QuietHoursStartLocal, *rules.QuietHoursEndLocal) {
			if !rules.QuietHoursOverridePerType[eventType] {
				return RuleDecision{Allow: false, Reason: ReasonQuietHours, EventType: eventType}
			}
		}
	}
	return RuleDecision{Allow: true, Reason: ReasonDefaultAllow, EventType: eventType}
}
