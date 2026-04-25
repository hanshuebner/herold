package store

import "time"

// This file declares the Phase-2 Wave 2.7 entities backing JMAP for
// Calendars (REQ-PROTO-54, RFC 8984 JSCalendar + the JMAP-Calendars
// binding draft). The schema-side commentary lives in
// internal/storesqlite/migrations/0011_calendars.sql; this file is the
// Go-side companion.
//
// Storage strategy. RFC 8984 specifies a deeply-nested JSCalendar Event
// object that we persist verbatim as JSON in the
// calendar_events.jscalendar_json BLOB. A small set of denormalised
// columns (Start, End, IsRecurring, RRuleJSON, Summary, OrganizerEmail,
// Status) carries the values JMAP queries filter and sort on so the
// read path does not have to JSON-parse every row. The JMAP serializer
// (parallel agent B in Wave 2.7) is the sole producer of those
// columns: it parses the JSCalendar, derives the denormalised fields,
// and hands the store a fully-populated CalendarEvent. New JSCalendar
// fields land additively in the JSON blob; only when a field needs to
// be filterable does it earn a column.

// CalendarID identifies one row in the calendars table.
type CalendarID uint64

// Calendar is one JMAP Calendar owned by a principal. Mirrors the
// container shape of Mailbox / AddressBook: per-principal, with name +
// colour + is_subscribed + is_visible + RFC 4314-style rights mask.
// RightsMask reuses the existing ACLRights bitfield; the JMAP-Calendars
// myRights vocabulary maps cleanly onto Lookup/Read/Write/Insert/
// DeleteMessage/Admin for v1 — the JSCalendar-specific extras
// (mayReadFreeBusy, mayRSVP, …) come later when REQ-PROTO-54 deems them
// necessary.
type Calendar struct {
	// ID is the assigned primary key.
	ID CalendarID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Name is the display name.
	Name string
	// Description is an optional free-text description; empty when the
	// caller did not provide one.
	Description string
	// Color is the optional "#RRGGBB" colour hex, mirroring Mailbox.Color
	// and AddressBook.Color. nil means "unset"; clients render their
	// own default.
	Color *string
	// SortOrder is the JMAP sort hint (smaller sorts first); 0 by
	// default.
	SortOrder int
	// IsSubscribed mirrors the IMAP subscribed bit / JMAP isSubscribed.
	// True on insert by default.
	IsSubscribed bool
	// IsDefault marks at most one default calendar per principal. The
	// store fences "at most one" with a partial unique index; the
	// metadata layer auto-flips the previous default when a new row
	// arrives with IsDefault = true.
	IsDefault bool
	// IsVisible toggles whether the calendar participates in the
	// principal's aggregated default view (e.g. busy-time projection).
	// True on insert by default; mirrors RFC 8984 §6.2 isVisible.
	IsVisible bool
	// TimeZoneID is the IANA tz name the calendar's events default to
	// when none is set on the event itself; "" means "treat as UTC".
	// Examples: "Europe/Berlin", "America/Los_Angeles".
	TimeZoneID string
	// RightsMask packs the JMAP myRights flags for v1 using the existing
	// ACLRights vocabulary. 0 means "no extra rights beyond ownership".
	RightsMask ACLRights
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
	// ModSeq is the per-row monotonic counter used for /changes
	// pagination at the JMAP layer (orthogonal to the per-principal
	// state-change feed).
	ModSeq ModSeq
}

// CalendarFilter narrows a ListCalendars read.
type CalendarFilter struct {
	// PrincipalID, when non-nil, restricts to calendars owned by that
	// principal.
	PrincipalID *PrincipalID
	// AfterModSeq, when non-zero, returns rows whose ModSeq > the
	// supplied value (used by Calendar/changes).
	AfterModSeq ModSeq
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor; rows whose ID > AfterID. Zero
	// starts at the first row.
	AfterID CalendarID
}

// CalendarEventID identifies one row in the calendar_events table.
type CalendarEventID uint64

// CalendarEvent is one JSCalendar Event object owned by a calendar.
// The full RFC 8984 shape lives in JSCalendarJSON; the denormalised
// columns the store filters and sorts on are populated by the JMAP
// serializer on every Insert / Update.
type CalendarEvent struct {
	// ID is the assigned primary key.
	ID CalendarEventID
	// CalendarID is the containing calendar.
	CalendarID CalendarID
	// PrincipalID is the owning principal (denormalised so /query can
	// filter without joining calendars).
	PrincipalID PrincipalID
	// UID is the RFC 5545 / 8984 uid: client-supplied or server-minted;
	// unique per calendar.
	UID string
	// JSCalendarJSON is the full JSCalendar Event object serialised as
	// JSON. The store treats the bytes as opaque; the protojmap
	// serializer parses on read and re-serialises on write.
	JSCalendarJSON []byte
	// Start / End are the denormalised UTC start and end of the event
	// (or, for a recurring series, the master DTSTART / DTEND). Used by
	// the /query window filter and the calendar-day projection.
	Start time.Time
	End   time.Time
	// IsRecurring is true when the event carries a recurrence rule.
	// Pairs with RRuleJSON for the occurrence-expansion path.
	IsRecurring bool
	// RRuleJSON is the denormalised recurrence rule serialised as JSON
	// (the JSCalendar @type:RecurrenceRule object). nil when the event
	// is not recurring.
	RRuleJSON []byte
	// Summary is the JSCalendar title denormalised for substring
	// filters and sort.
	Summary string
	// OrganizerEmail is the marked-organizer's email from the
	// JSCalendar participants map, lower-cased; empty when no organizer
	// is set. Indexed for the iMIP RSVP path lookup.
	OrganizerEmail string
	// Status is the JSCalendar status: "confirmed", "cancelled", or
	// "tentative". Empty defaults to "confirmed" at the JMAP layer.
	Status string
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
	// ModSeq is the per-row monotonic counter used by
	// CalendarEvent/changes.
	ModSeq ModSeq
}

// CalendarEventFilter narrows a ListCalendarEvents read. Zero values
// mean "no constraint". Limit caps at 1000 server-side.
type CalendarEventFilter struct {
	// CalendarID, when non-nil, restricts to one calendar.
	CalendarID *CalendarID
	// PrincipalID, when non-nil, restricts to one principal (useful
	// when the caller wants every event across all calendars they own).
	PrincipalID *PrincipalID
	// UID, when non-nil, restricts to a specific uid (rarely used
	// directly; CalendarID + UID is the natural key — see
	// GetCalendarEventByUID for the iMIP lookup path).
	UID *string
	// Text is a case-insensitive substring matched against Summary.
	Text string
	// StartAfter, when non-nil, restricts to events whose Start >=
	// *StartAfter.
	StartAfter *time.Time
	// StartBefore, when non-nil, restricts to events whose Start <
	// *StartBefore. Together with StartAfter forms the time-window
	// filter the calendar-day view drives.
	StartBefore *time.Time
	// Status, when non-nil, restricts to events whose Status equals
	// *Status (exact match on the canonical lower-case form).
	Status *string
	// AfterModSeq, when non-zero, returns rows whose ModSeq > the
	// supplied value (used by CalendarEvent/changes).
	AfterModSeq ModSeq
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor; rows whose ID > AfterID.
	AfterID CalendarEventID
}
