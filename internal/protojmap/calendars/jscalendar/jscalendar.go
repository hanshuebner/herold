package jscalendar

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Event is the JSCalendar Event object per RFC 8984 §5. The hybrid
// model: frequently-queried fields are typed Go-natively so the
// JMAP/store layer can populate denormalised columns without re-
// parsing the blob; rare or forward-compat fields round-trip through
// RawJSON so unknown keys survive a get/set cycle without loss.
//
// MarshalJSON merges the typed fields back into RawJSON before
// serialising — typed mutations win over the raw map, so callers that
// mutate Event.Title and re-marshal see the new title regardless of
// whether the raw "title" key was present.
type Event struct {
	// Type MUST be "Event" per RFC 8984 §5.1. Validate enforces it.
	Type string
	// UID is the unique identifier per RFC 5545; carried verbatim from
	// any inbound iMIP source so REPLY/CANCEL flows match.
	UID string
	// ProdID identifies the product that wrote the event; populated on
	// outbound serialisations to "herold/<version>".
	ProdID string
	// Created and Updated are UTCDateTime values (RFC 8984 §1.4.4).
	Created time.Time
	Updated time.Time

	// Title is the human-visible event subject (RFC 8984 §5.1.6).
	Title string
	// Description is the long-form body (§5.1.7).
	Description string
	// Status is "confirmed" | "cancelled" | "tentative" (§5.1.13).
	// Empty defaults to "confirmed" via the Status() helper; we do not
	// auto-fill the field on Marshal so callers can distinguish "client
	// did not specify" from an explicit "confirmed".
	StatusValue string
	// Sequence is the iTIP sequence number (§5.1.14) — bumped on every
	// organiser-side material change.
	Sequence int
	// Color is a CSS colour string (§5.1.10).
	Color string
	// Categories and Keywords are open-vocabulary tag sets (§5.1.11,
	// §5.1.12). Modeled as a set (map[string]bool) per the RFC's "JSON
	// object whose member names are values and whose values are true".
	Categories map[string]bool
	Keywords   map[string]bool

	// Start is the event's start instant. The wire format is a
	// LocalDateTime (no zone suffix); the zone is in TimeZone. We
	// resolve to UTC at Unmarshal time so storage and queries can
	// compare instants directly. ShowWithoutTime carries the all-day
	// distinction.
	Start time.Time
	// Duration is the event length per RFC 8984 §5.2.2 (ISO 8601).
	Duration Duration
	// TimeZone is the IANA zone the master start was specified in
	// (§5.2.4). For floating events this is the empty string.
	TimeZone string
	// ShowWithoutTime is true for all-day events (§5.2.5).
	ShowWithoutTime bool
	// Locations is the locations map (§5.2.6).
	Locations map[string]Location

	// Participants is the participants map (§5.4.3). The organiser is
	// stored here too, marked by Roles["owner"] == true; OrganizerEmail
	// looks them up.
	Participants map[string]Participant

	// RecurrenceRules is the master series recurrence rule set
	// (§5.3.1). Most events have a single rule; the slice is for the
	// rare multi-rule case Apple Calendar emits.
	RecurrenceRules []RecurrenceRule
	// RecurrenceOverrides keys an override block by the original
	// occurrence start. Standard JSCalendar convention: a single key
	// "@removed":true marks the occurrence as excluded (RFC 8984
	// §5.3.4 calls it "excluded": true; we accept either spelling).
	RecurrenceOverrides map[time.Time]map[string]json.RawMessage
	// ExcludedRecurrenceRules removes occurrences that the inclusion
	// rules would otherwise produce (§5.3.2).
	ExcludedRecurrenceRules []RecurrenceRule

	// Alerts is the alarms map (§5.5.1). v1 preserves the structure
	// for round-trip; the outbound-side iCalendar VALARM serialiser is
	// Phase 3.
	Alerts map[string]Alert

	// RawJSON carries every field as the client/server sent it,
	// including the typed fields (kept in lock-step by Marshal /
	// Unmarshal). Unknown keys flow through unchanged so a future
	// JSCalendar errata does not require re-parsing on the wire.
	RawJSON map[string]json.RawMessage
}

// Location is one row in Event.Locations (RFC 8984 §5.2.6).
type Location struct {
	Name          string          `json:"name,omitempty"`
	Description   string          `json:"description,omitempty"`
	LocationTypes map[string]bool `json:"locationTypes,omitempty"`
	RelativeTo    string          `json:"relativeTo,omitempty"`
	TimeZone      string          `json:"timeZone,omitempty"`
	Coordinates   string          `json:"coordinates,omitempty"`
	LinkIDs       map[string]bool `json:"linkIds,omitempty"`
}

// Participant is one row in Event.Participants (RFC 8984 §5.4.3).
type Participant struct {
	// Email is the canonical address. RFC 9533 carries it inside a
	// SendTo map (e.g. {"imip": "mailto:foo@bar"}); we project the
	// mailto: value out for the typed field. The full SendTo map
	// survives through RawJSON.
	Email string
	// Name is the display name (§5.4.3.2).
	Name string
	// Kind is "individual"|"group"|"resource"|"location" (§5.4.3.3).
	Kind string
	// Roles is the role set; "owner" identifies the organiser
	// (§5.4.3.4).
	Roles map[string]bool
	// ParticipationStatus — "needs-action"|"accepted"|"declined"|
	// "tentative"|"delegated" (§5.4.3.5).
	ParticipationStatus string
	// ExpectReply is the iTIP RSVP flag (§5.4.3.7).
	ExpectReply bool
	// RawJSON carries unknown keys (sendTo map, links, scheduleSequence,
	// etc.) so a participant survives a round-trip without lossy
	// normalisation.
	RawJSON map[string]json.RawMessage
}

// Alert is one row in Event.Alerts (RFC 8984 §5.5). v1 preserves the
// structure verbatim; the typed fields cover the load-bearing keys for
// future outbound VALARM emission.
type Alert struct {
	Trigger map[string]json.RawMessage `json:"trigger,omitempty"`
	Action  string                     `json:"action,omitempty"`
	RawJSON map[string]json.RawMessage `json:"-"`
}

// Duration is an ISO 8601 duration as it appears on the JSCalendar
// wire. We store the canonical string and the resolved time.Duration;
// both round-trip across Marshal/Unmarshal.
type Duration struct {
	Text  string
	Value time.Duration
}

// MarshalJSON renders a Duration as the canonical ISO 8601 string.
func (d Duration) MarshalJSON() ([]byte, error) {
	if d.Text == "" && d.Value == 0 {
		return []byte(`null`), nil
	}
	s := d.Text
	if s == "" {
		s = formatISO8601Duration(d.Value)
	}
	return json.Marshal(s)
}

// UnmarshalJSON parses an ISO 8601 duration. Empty string and JSON
// null are both treated as zero so the field is optional in practice.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		*d = Duration{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("jscalendar: duration: %w", err)
	}
	if s == "" {
		*d = Duration{}
		return nil
	}
	v, err := parseISO8601Duration(s)
	if err != nil {
		return fmt.Errorf("jscalendar: duration: %w", err)
	}
	d.Text = s
	d.Value = v
	return nil
}

// UnmarshalJSON parses a JSCalendar Event. The whole object lands in
// RawJSON, then the typed projections are overlaid. Per-field
// validation is deferred to Validate so callers can pick a relaxed-vs-
// strict posture; we only fail here on non-object input.
func (e *Event) UnmarshalJSON(b []byte) error {
	if len(bytes.TrimSpace(b)) == 0 || string(bytes.TrimSpace(b)) == "null" {
		*e = Event{}
		return nil
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("jscalendar: parse event: %w", err)
	}
	e.RawJSON = raw
	e.Type = projectString(raw, "@type")
	e.UID = projectString(raw, "uid")
	e.ProdID = projectString(raw, "prodId")
	e.Title = projectString(raw, "title")
	e.Description = projectString(raw, "description")
	e.StatusValue = projectString(raw, "status")
	e.Color = projectString(raw, "color")
	e.TimeZone = projectString(raw, "timeZone")

	if v, ok := raw["sequence"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.Sequence)
	}
	if v, ok := raw["showWithoutTime"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.ShowWithoutTime)
	}
	if v, ok := raw["created"]; ok && len(v) > 0 && string(v) != "null" {
		e.Created = parseTimeField(v)
	}
	if v, ok := raw["updated"]; ok && len(v) > 0 && string(v) != "null" {
		e.Updated = parseTimeField(v)
	}
	if v, ok := raw["start"]; ok && len(v) > 0 && string(v) != "null" {
		e.Start = parseLocalDateTime(v, e.TimeZone)
	}
	if v, ok := raw["duration"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.Duration)
	}
	if v, ok := raw["categories"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.Categories)
	}
	if v, ok := raw["keywords"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.Keywords)
	}
	if v, ok := raw["locations"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.Locations)
	}
	if v, ok := raw["participants"]; ok && len(v) > 0 && string(v) != "null" {
		e.Participants = parseParticipants(v)
	}
	if v, ok := raw["recurrenceRules"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.RecurrenceRules)
	}
	if v, ok := raw["excludedRecurrenceRules"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.ExcludedRecurrenceRules)
	}
	if v, ok := raw["recurrenceOverrides"]; ok && len(v) > 0 && string(v) != "null" {
		e.RecurrenceOverrides = parseRecurrenceOverrides(v, e.TimeZone)
	}
	if v, ok := raw["alerts"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &e.Alerts)
	}
	return nil
}

// MarshalJSON serialises an Event. The typed fields override the
// matching RawJSON entries so helper-driven mutations win; everything
// else flows through unchanged. Object-key ordering is canonical
// (lexical) so the round-trip tests can byte-compare.
func (e Event) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(e.RawJSON)+16)
	for k, v := range e.RawJSON {
		out[k] = v
	}
	if e.Type != "" {
		out["@type"] = mustMarshal(e.Type)
	}
	if e.UID != "" {
		out["uid"] = mustMarshal(e.UID)
	}
	if e.ProdID != "" {
		out["prodId"] = mustMarshal(e.ProdID)
	}
	if !e.Created.IsZero() {
		out["created"] = mustMarshal(e.Created.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if !e.Updated.IsZero() {
		out["updated"] = mustMarshal(e.Updated.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if e.Title != "" {
		out["title"] = mustMarshal(e.Title)
	}
	if e.Description != "" {
		out["description"] = mustMarshal(e.Description)
	}
	if e.StatusValue != "" {
		out["status"] = mustMarshal(e.StatusValue)
	}
	if e.Sequence != 0 {
		out["sequence"] = mustMarshal(e.Sequence)
	}
	if e.Color != "" {
		out["color"] = mustMarshal(e.Color)
	}
	if e.Categories != nil {
		out["categories"] = mustMarshal(e.Categories)
	}
	if e.Keywords != nil {
		out["keywords"] = mustMarshal(e.Keywords)
	}
	if !e.Start.IsZero() {
		out["start"] = mustMarshal(formatLocalDateTime(e.Start, e.TimeZone))
	}
	if e.Duration.Text != "" || e.Duration.Value != 0 {
		out["duration"] = mustMarshal(e.Duration)
	}
	if e.TimeZone != "" {
		out["timeZone"] = mustMarshal(e.TimeZone)
	}
	if e.ShowWithoutTime {
		out["showWithoutTime"] = mustMarshal(true)
	}
	if e.Locations != nil {
		out["locations"] = mustMarshal(e.Locations)
	}
	if e.Participants != nil {
		out["participants"] = marshalParticipants(e.Participants)
	}
	if e.RecurrenceRules != nil {
		out["recurrenceRules"] = mustMarshal(e.RecurrenceRules)
	}
	if e.ExcludedRecurrenceRules != nil {
		out["excludedRecurrenceRules"] = mustMarshal(e.ExcludedRecurrenceRules)
	}
	if e.RecurrenceOverrides != nil {
		out["recurrenceOverrides"] = marshalRecurrenceOverrides(e.RecurrenceOverrides, e.TimeZone)
	}
	if e.Alerts != nil {
		out["alerts"] = mustMarshal(e.Alerts)
	}
	return canonicalJSONObject(out), nil
}

// Validate enforces RFC 8984 §5 hard rules on an Event: @type must be
// "Event" (we explicitly do not accept "Task" or "Group" in v1; see
// doc.go), uid is present, start is non-zero. The rest of the model is
// relaxed so a forward-compatible client payload survives.
func (e *Event) Validate() error {
	if e == nil {
		return errors.New("jscalendar: nil event")
	}
	if e.Type != "Event" {
		if e.Type == "Task" || e.Type == "Group" {
			return fmt.Errorf("jscalendar: @type %q is RFC 8984 but unsupported in v1 (Event-only)", e.Type)
		}
		return fmt.Errorf("jscalendar: @type must be \"Event\", got %q", e.Type)
	}
	if e.UID == "" {
		return errors.New("jscalendar: uid is required")
	}
	if e.Start.IsZero() {
		return errors.New("jscalendar: start is required")
	}
	return nil
}

// Status returns the effective status string, defaulting to
// "confirmed" per RFC 8984 §5.1.13 when the event has no explicit
// value.
func (e *Event) Status() string {
	if e == nil || e.StatusValue == "" {
		return "confirmed"
	}
	return e.StatusValue
}

// IsRecurring reports whether the event has any inclusion rule. A
// rules-free event with overrides only is not "recurring" for
// expansion purposes — the overrides have nothing to override.
func (e *Event) IsRecurring() bool {
	return e != nil && len(e.RecurrenceRules) > 0
}

// OrganizerEmail returns the email of the first participant whose
// roles map carries "owner": true. Returns "" when no organiser is
// present (a self-only event).
func (e *Event) OrganizerEmail() string {
	if e == nil {
		return ""
	}
	for _, k := range sortedKeys(e.Participants) {
		p := e.Participants[k]
		if p.Roles["owner"] {
			return p.Email
		}
	}
	return ""
}

// MarshalJSON for Participant projects the typed fields and merges
// RawJSON. The wire shape uses sendTo: {"imip": "mailto:..."} for the
// address per RFC 9533; we always emit an "imip" channel when Email
// is non-empty so a typed-only construction round-trips into a valid
// JSCalendar object.
func (p Participant) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(p.RawJSON)+8)
	for k, v := range p.RawJSON {
		out[k] = v
	}
	if p.Email != "" {
		// Preserve any existing sendTo channels but ensure imip is set.
		// Round-trip-safe: only emit a top-level "email" key when one
		// was present on input (some senders abbreviate that way) so a
		// canonical sendTo-only payload survives byte-for-byte.
		if existing, ok := out["sendTo"]; ok && len(existing) > 0 && string(existing) != "null" {
			st := map[string]string{}
			if err := json.Unmarshal(existing, &st); err == nil {
				st["imip"] = "mailto:" + p.Email
				out["sendTo"] = mustMarshal(st)
			} else {
				out["sendTo"] = mustMarshal(map[string]string{"imip": "mailto:" + p.Email})
			}
		} else {
			out["sendTo"] = mustMarshal(map[string]string{"imip": "mailto:" + p.Email})
		}
		if _, hadEmail := p.RawJSON["email"]; hadEmail {
			out["email"] = mustMarshal(p.Email)
		}
	}
	if p.Name != "" {
		out["name"] = mustMarshal(p.Name)
	}
	if p.Kind != "" {
		out["kind"] = mustMarshal(p.Kind)
	}
	if p.Roles != nil {
		out["roles"] = mustMarshal(p.Roles)
	}
	if p.ParticipationStatus != "" {
		out["participationStatus"] = mustMarshal(p.ParticipationStatus)
	}
	if p.ExpectReply {
		out["expectReply"] = mustMarshal(true)
	}
	return canonicalJSONObject(out), nil
}

// UnmarshalJSON projects the typed fields from a Participant object,
// keeping the rest in RawJSON. The Email projection prefers the
// sendTo.imip channel (RFC 9533 canonical form) but falls back to a
// top-level "email" key when senders abbreviate.
func (p *Participant) UnmarshalJSON(b []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("jscalendar: participant: %w", err)
	}
	p.RawJSON = raw
	p.Name = projectString(raw, "name")
	p.Kind = projectString(raw, "kind")
	p.ParticipationStatus = projectString(raw, "participationStatus")
	p.Email = projectString(raw, "email")
	if p.Email == "" {
		if v, ok := raw["sendTo"]; ok && len(v) > 0 && string(v) != "null" {
			st := map[string]string{}
			if err := json.Unmarshal(v, &st); err == nil {
				if mail, ok := st["imip"]; ok {
					p.Email = strings.TrimPrefix(mail, "mailto:")
				}
			}
		}
	}
	if v, ok := raw["roles"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &p.Roles)
	}
	if v, ok := raw["expectReply"]; ok && len(v) > 0 && string(v) != "null" {
		_ = json.Unmarshal(v, &p.ExpectReply)
	}
	return nil
}

// parseParticipants is the explicit shape used by Event.UnmarshalJSON
// because the raw message is a map and we want each value to flow
// through Participant.UnmarshalJSON (so its RawJSON survives).
func parseParticipants(v json.RawMessage) map[string]Participant {
	out := map[string]Participant{}
	if err := json.Unmarshal(v, &out); err != nil {
		return nil
	}
	return out
}

// marshalParticipants renders a participants map with sorted keys so
// the canonical-byte assertion in Event round-trip tests holds.
func marshalParticipants(m map[string]Participant) json.RawMessage {
	keys := sortedKeys(m)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		v, _ := json.Marshal(m[k])
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// parseRecurrenceOverrides turns the wire-shape map (LocalDateTime
// keys -> override blocks) into the typed map keyed by time.Time. The
// timezone for resolution is the master's TimeZone.
func parseRecurrenceOverrides(v json.RawMessage, tz string) map[time.Time]map[string]json.RawMessage {
	raw := map[string]map[string]json.RawMessage{}
	if err := json.Unmarshal(v, &raw); err != nil {
		return nil
	}
	out := make(map[time.Time]map[string]json.RawMessage, len(raw))
	for k, block := range raw {
		t, err := resolveLocalDateTimeString(k, tz)
		if err != nil {
			continue
		}
		out[t] = block
	}
	return out
}

// marshalRecurrenceOverrides renders the typed map back to the wire
// LocalDateTime-keyed shape, in sorted key order for byte-stable
// output.
func marshalRecurrenceOverrides(m map[time.Time]map[string]json.RawMessage, tz string) json.RawMessage {
	type kv struct {
		Key   string
		Value json.RawMessage
	}
	pairs := make([]kv, 0, len(m))
	for t, block := range m {
		key := formatLocalDateTime(t, tz)
		bl, _ := json.Marshal(block)
		pairs = append(pairs, kv{Key: key, Value: bl})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(p.Key)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(p.Value)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// parseTimeField parses a JSON UTCDateTime string. Returns a zero time
// on parse errors so the field stays best-effort and Validate is the
// explicit gate.
func parseTimeField(v json.RawMessage) time.Time {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return time.Time{}
	}
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// parseLocalDateTime parses a LocalDateTime ("2020-01-15T13:00:00")
// using the supplied IANA TimeZone (or UTC when empty / unknown). The
// returned value is normalised to UTC so storage comparisons are
// straightforward.
func parseLocalDateTime(v json.RawMessage, tz string) time.Time {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return time.Time{}
	}
	t, err := resolveLocalDateTimeString(s, tz)
	if err != nil {
		return time.Time{}
	}
	return t
}

// resolveLocalDateTimeString is the lower-level parser shared by
// recurrenceOverrides and the master Start projection.
func resolveLocalDateTimeString(s, tz string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.000",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC(), nil
		}
	}
	// Permissive fallback: accept a UTC suffix so iCalendar bridges
	// that already produced UTC don't silently zero out.
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unparsable LocalDateTime %q", s)
}

// formatLocalDateTime renders a UTC time.Time back into a
// LocalDateTime in the supplied TimeZone. Empty zone falls back to
// UTC, which yields a no-suffix instant — the JSCalendar wire format
// distinguishes via the sibling timeZone field.
func formatLocalDateTime(t time.Time, tz string) string {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	return t.In(loc).Format("2006-01-02T15:04:05")
}

// projectString unwraps a JSON string from the raw map. Returns "" on
// missing or non-string values; never returns an error so the typed
// fields stay best-effort.
func projectString(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// sortedKeys returns the map's keys in deterministic ascending order.
// Mirror of the helper in internal/protojmap/contacts; replicated to
// keep this package self-contained.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mustMarshal panics on a json.Marshal error; used only for value
// types whose encoding cannot fail (strings, primitive maps, structs
// defined in this package).
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("jscalendar: marshal: %v", err))
	}
	return b
}

// canonicalJSONObject renders a map as a JSON object with keys sorted
// lexically. The round-trip tests rely on byte equality.
func canonicalJSONObject(m map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		v := m[k]
		if len(v) == 0 {
			buf.WriteString("null")
		} else {
			buf.Write(v)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// parseISO8601Duration parses the RFC 8984 §1.4.5 / ISO 8601 duration
// shape that JSCalendar uses: optional minus, "P", optional weeks,
// optional days, then optional "T" with hours/minutes/seconds. We do
// not accept months/years here — JSCalendar Duration is fixed-length
// per the RFC's clarification that calendar durations live elsewhere.
func parseISO8601Duration(s string) (time.Duration, error) {
	orig := s
	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("duration %q: missing P prefix", orig)
	}
	s = s[1:]
	var d time.Duration
	parseField := func(rest, suffix string, unit time.Duration) (string, error) {
		idx := strings.Index(rest, suffix)
		if idx < 0 {
			return rest, nil
		}
		// digits must be all that precede the suffix in this segment
		num := rest[:idx]
		if num == "" {
			return rest, fmt.Errorf("duration %q: empty number before %s", orig, suffix)
		}
		var n int
		for _, c := range num {
			if c < '0' || c > '9' {
				return rest, fmt.Errorf("duration %q: non-digit before %s", orig, suffix)
			}
			n = n*10 + int(c-'0')
		}
		d += time.Duration(n) * unit
		return rest[idx+1:], nil
	}
	var err error
	// Date portion: weeks (W), days (D). Stop at "T" or end.
	dateRest := s
	tIdx := strings.Index(s, "T")
	if tIdx >= 0 {
		dateRest = s[:tIdx]
	}
	if dateRest, err = parseField(dateRest, "W", 7*24*time.Hour); err != nil {
		return 0, err
	}
	if dateRest, err = parseField(dateRest, "D", 24*time.Hour); err != nil {
		return 0, err
	}
	if dateRest != "" {
		return 0, fmt.Errorf("duration %q: trailing date chars %q", orig, dateRest)
	}
	if tIdx >= 0 {
		timeRest := s[tIdx+1:]
		if timeRest, err = parseField(timeRest, "H", time.Hour); err != nil {
			return 0, err
		}
		if timeRest, err = parseField(timeRest, "M", time.Minute); err != nil {
			return 0, err
		}
		if timeRest, err = parseField(timeRest, "S", time.Second); err != nil {
			return 0, err
		}
		if timeRest != "" {
			return 0, fmt.Errorf("duration %q: trailing time chars %q", orig, timeRest)
		}
	}
	if negative {
		d = -d
	}
	return d, nil
}

// formatISO8601Duration renders a time.Duration in the canonical
// JSCalendar shape used by Marshal (PnDTnHnMnS, omitting empty
// fields). Negative durations get a leading minus.
func formatISO8601Duration(d time.Duration) string {
	if d == 0 {
		return "PT0S"
	}
	negative := d < 0
	if negative {
		d = -d
	}
	days := int64(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int64(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	minutes := int64(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute
	seconds := int64(d / time.Second)
	var b strings.Builder
	if negative {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	if days > 0 {
		fmt.Fprintf(&b, "%dD", days)
	}
	if hours > 0 || minutes > 0 || seconds > 0 {
		b.WriteByte('T')
		if hours > 0 {
			fmt.Fprintf(&b, "%dH", hours)
		}
		if minutes > 0 {
			fmt.Fprintf(&b, "%dM", minutes)
		}
		if seconds > 0 {
			fmt.Fprintf(&b, "%dS", seconds)
		}
	}
	if b.Len() == 1 || (negative && b.Len() == 2) {
		// Only "P" or "-P" so far -> no fields fired. Append T0S.
		b.WriteString("T0S")
	}
	return b.String()
}
