package calendars

import (
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2). Calendar and
// CalendarEvent ids are stringified store ids; clients echo them back
// unchanged on subsequent calls.
type jmapID = string

// calendarIDFromJMAP parses a wire-form id into a store.CalendarID.
// Empty / unparseable / zero values return (0, false); callers translate
// to a "notFound" SetError per the JMAP-Calendars binding draft.
func calendarIDFromJMAP(id jmapID) (store.CalendarID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.CalendarID(v), true
}

// jmapIDFromCalendar renders a CalendarID as the wire id form.
func jmapIDFromCalendar(id store.CalendarID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// eventIDFromJMAP parses a wire-form id into a store.CalendarEventID.
func eventIDFromJMAP(id jmapID) (store.CalendarEventID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.CalendarEventID(v), true
}

// jmapIDFromEvent renders a CalendarEventID as the wire id form.
func jmapIDFromEvent(id store.CalendarEventID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// calMyRights is the per-Calendar capability mask returned in
// Calendar/get and inherited by CalendarEvent/get (per the
// JMAP-Calendars binding draft).
type calMyRights struct {
	MayReadFreeBusy       bool `json:"mayReadFreeBusy"`
	MayReadItems          bool `json:"mayReadItems"`
	MayAddItems           bool `json:"mayAddItems"`
	MayUpdatePrivate      bool `json:"mayUpdatePrivate"`
	MayUpdateOwn          bool `json:"mayUpdateOwn"`
	MayUpdateAll          bool `json:"mayUpdateAll"`
	MayRemoveOwn          bool `json:"mayRemoveOwn"`
	MayRemoveAll          bool `json:"mayRemoveAll"`
	MayAdmin              bool `json:"mayAdmin"`
	MayDelete             bool `json:"mayDelete"`
	MayShareWithOthers    bool `json:"mayShareWithOthers"`
	MayChangeFreeBusyType bool `json:"mayChangeFreeBusyType"`
}

// rightsForCalendarOwner is the mask the owning principal sees on its
// own calendars — every JMAP mutation is permitted.
func rightsForCalendarOwner() calMyRights {
	return calMyRights{
		MayReadFreeBusy:       true,
		MayReadItems:          true,
		MayAddItems:           true,
		MayUpdatePrivate:      true,
		MayUpdateOwn:          true,
		MayUpdateAll:          true,
		MayRemoveOwn:          true,
		MayRemoveAll:          true,
		MayAdmin:              true,
		MayDelete:             true,
		MayShareWithOthers:    true,
		MayChangeFreeBusyType: true,
	}
}

// jmapCalendar is the wire-form Calendar object per the JMAP-Calendars
// binding draft.
type jmapCalendar struct {
	ID           jmapID      `json:"id"`
	Name         string      `json:"name"`
	Description  *string     `json:"description"`
	Color        *string     `json:"color"`
	SortOrder    int         `json:"sortOrder"`
	IsSubscribed bool        `json:"isSubscribed"`
	IsDefault    bool        `json:"isDefault"`
	IsVisible    bool        `json:"isVisible"`
	TimeZone     *string     `json:"timeZone"`
	MyRights     calMyRights `json:"myRights"`
}

// setError is the JMAP /set per-key error envelope (RFC 8620 §5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// comparator is the JMAP /query sort-comparator wire shape (RFC 8620
// §5.5).
type comparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation,omitempty"`
}
