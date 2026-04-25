package jscalendar

// JSCalendar Event JSON-decoder fuzz target (STANDARDS section 8.2).
// Track C of Wave 2.9.5: untrusted JSON arrives via JMAP Calendar/set
// plus the iMIP intake bridge after a sender's iCalendar payload has
// been rendered to JSCalendar by ToJSCalendarEvent + MarshalJSON.
// Either path can introduce malformed strings; UnmarshalJSON must
// absorb the hostile shapes without panicking.
//
// Invariants:
//
//   1. UnmarshalJSON never panics on any input.
//   2. On a successful parse, e.Type, e.UID, e.TimeZone are valid
//      UTF-8.
//   3. A subsequent MarshalJSON / UnmarshalJSON cycle preserves the
//      typed projections (UID, Type).

import (
	"testing"
	"unicode/utf8"
)

// FuzzEventUnmarshalJSON drives Event.UnmarshalJSON over arbitrary
// bytes.
func FuzzEventUnmarshalJSON(f *testing.F) {
	// Seed 1: minimal Event.
	f.Add([]byte(`{"@type":"Event","uid":"u1","title":"Minimal","start":"2026-01-01T12:00:00"}`))
	// Seed 2: complex with participants + recurrenceOverrides.
	f.Add([]byte(`{"@type":"Event","uid":"u2","title":"Complex","start":"2026-01-01T09:00:00","timeZone":"Europe/Berlin","duration":"PT1H","sequence":3,"participants":{"organizer":{"@type":"Participant","sendTo":{"imip":"mailto:alice@example.com"},"roles":{"owner":true,"attendee":true},"participationStatus":"accepted"},"a1":{"@type":"Participant","sendTo":{"imip":"mailto:bob@example.com"},"roles":{"attendee":true},"participationStatus":"needs-action","expectReply":true}},"recurrenceRules":[{"@type":"RecurrenceRule","frequency":"weekly","count":4}],"recurrenceOverrides":{"2026-01-08T09:00:00":{"@removed":true},"2026-01-15T09:00:00":{"title":"Moved"}}}`))
	// Seed 3: malformed values across dates / durations / freq.
	f.Add([]byte(`{"@type":"Event","uid":"u3","start":"not-a-date","duration":"PNotADuration","timeZone":"Imaginary/Zone","recurrenceRules":[{"frequency":"banana","count":"abc"}],"recurrenceOverrides":{"badkey":{"@removed":true}}}`))
	// Adversarial: non-object, deeply nested arrays, wrong @type.
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`"just-a-string"`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`{"@type":"Event","uid":"x","start":"2026-01-01T00:00:00"}`))
	f.Add([]byte(`{"@type":"NotEvent","uid":"y"}`))

	f.Fuzz(func(t *testing.T, in []byte) {
		var e Event
		if err := e.UnmarshalJSON(in); err != nil {
			return
		}
		if e.Type != "" && !utf8.ValidString(e.Type) {
			t.Fatalf("Type not utf8: %q", e.Type)
		}
		if e.UID != "" && !utf8.ValidString(e.UID) {
			t.Fatalf("UID not utf8: %q", e.UID)
		}
		if e.TimeZone != "" && !utf8.ValidString(e.TimeZone) {
			t.Fatalf("TimeZone not utf8: %q", e.TimeZone)
		}
		// Marshal/Unmarshal cycle must not panic. We do not assert
		// byte equality on string fields: encoding/json replaces any
		// non-UTF-8 bytes with U+FFFD on emission, so a UID that
		// arrived on the wire as already-invalid UTF-8 (rare but
		// possible via clients that bypass strict JSON) mutates on
		// re-emission. Only the parse-then-re-parse error-freeness
		// is the load-bearing invariant.
		body, err := e.MarshalJSON()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var round Event
		if err := round.UnmarshalJSON(body); err != nil {
			t.Fatalf("re-unmarshal failed on canonical body: %v", err)
		}
		// When the inputs are valid UTF-8, the typed projections must
		// survive the cycle.
		if utf8.ValidString(e.UID) && round.UID != e.UID {
			t.Fatalf("UID lost across cycle: %q -> %q", e.UID, round.UID)
		}
		if utf8.ValidString(e.Type) && round.Type != e.Type {
			t.Fatalf("Type lost across cycle: %q -> %q", e.Type, round.Type)
		}
	})
}
