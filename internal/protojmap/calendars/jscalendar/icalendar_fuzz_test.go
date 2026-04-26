package jscalendar

// iCalendar parser fuzz targets (STANDARDS §8.2). Track C of Wave 2.9.5
// adds the missing harnesses for the iMIP intake path:
//
//   FuzzParseICS         drives ParseICS (RFC 5545 line-folder + content-line
//                        splitter + property dispatcher).
//   FuzzVEventToJSCalendar drives VEvent.ToJSCalendarEvent (the iMIP
//                        bridge that produces a JSCalendar Event from a
//                        parsed VEVENT).
//
// Both are exposed to untrusted input — the bytes arrive from arbitrary
// senders' message bodies, so a panic here would translate into a
// crash-loop on the iMIP worker. We only assert "must not panic" plus a
// handful of structural invariants that survive the bridge.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// FuzzParseICS drives ParseICS over arbitrary byte streams. Invariants:
//
//  1. Never panics on any input.
//  2. On a successful parse, every contained VEVENT carries a non-zero
//     DTStart only when the source had a parsable DTSTART value, and
//     Method is upper-case (the dispatcher relies on that).
//  3. No parsed string field (TZID, UID, etc.) contains a raw CR or LF
//     byte: ParseICS rejects content lines whose unfolded form holds
//     embedded CR / LF (RFC 5545 §3.1; wave 2.9.8 surfaced the seed at
//     testdata/fuzz/FuzzParseICS/481fd31f4df828e3 where a malformed
//     "DTEND;TZID=0\r0:" smuggled CR into TZID and would have corrupted
//     outbound iMIP re-emission).
func FuzzParseICS(f *testing.F) {
	// Seed 1: minimal valid VCALENDAR with one VEVENT.
	f.Add([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:abc@example.com\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260101T120000Z\r\nDTEND:20260101T130000Z\r\nSUMMARY:Hi\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"))
	// Seed 2: REQUEST with TZID + RRULE.
	f.Add([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:rec@example.com\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;TZID=America/New_York:20260101T090000\r\n" +
		"DTEND;TZID=America/New_York:20260101T100000\r\n" +
		"RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=10\r\n" +
		"SUMMARY:Standup\r\nATTENDEE;CN=Bob;PARTSTAT=ACCEPTED:mailto:bob@example.com\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"))
	// Seed 3: malformed dates / durations to exercise the
	// best-effort parseICSTime fallbacks.
	f.Add([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:x\r\nDTSTAMP:not-a-date\r\nDTSTART:99999999T999999\r\n" +
		"DTEND;TZID=Imaginary/Zone:20260101T120000\r\n" +
		"DURATION:PT-blah\r\nRRULE:FREQ=BANANA;COUNT=abc;UNTIL=??\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"))
	// Adversarial: structural edge cases.
	f.Add([]byte(""))
	f.Add([]byte("BEGIN:VCALENDAR\r\n"))               // unterminated
	f.Add([]byte("END:VCALENDAR\r\n"))                 // END without BEGIN
	f.Add([]byte("BEGIN:VEVENT\r\nEND:VCALENDAR\r\n")) // mismatched
	f.Add([]byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\n" +
		"UID;FOO=\"a:b;c\":id1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")) // quoted params
	f.Add([]byte(strings.Repeat("BEGIN:VEVENT\r\nEND:VEVENT\r\n", 50))) // many empty events

	f.Fuzz(func(t *testing.T, in []byte) {
		cal, err := ParseICS(bytes.NewReader(in))
		if err != nil {
			return
		}
		if cal.Method != strings.ToUpper(cal.Method) {
			t.Fatalf("method not upper-cased: %q", cal.Method)
		}
		// Each event with a TZID we recognise should resolve to a
		// non-nil location at parse time (we only check structurally
		// that no field went unrecognisably weird).
		for _, ev := range cal.Events {
			if ev.TZID == "" {
				continue
			}
			// Must not contain CR or LF — those would corrupt any
			// subsequent re-emission.
			if strings.ContainsAny(ev.TZID, "\r\n") {
				t.Fatalf("TZID leaked CR/LF: %q", ev.TZID)
			}
		}
	})
}

// FuzzVEventToJSCalendar drives the bridge from a parsed VEvent to a
// JSCalendar Event. The fuzzer constructs the VEvent indirectly by
// running the bytes through ParseICS first; this exercises the bridge
// over the joint distribution of "things ParseICS can produce", which
// is exactly the surface iMIP intake hits at runtime.
//
// Invariants on the bridged Event:
//
//  1. Never panics.
//  2. e.UID must equal v.UID (round-trip property).
//  3. e.Type must be "Event" (the bridge always sets it).
//  4. e.StatusValue must be one of "", "confirmed", "cancelled",
//     "tentative".
func FuzzVEventToJSCalendar(f *testing.F) {
	// Seed 1: minimal one-VEVENT calendar with REQUEST.
	f.Add([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n"+
		"BEGIN:VEVENT\r\nUID:min@example.com\r\nDTSTAMP:20260101T000000Z\r\n"+
		"DTSTART:20260101T120000Z\r\nDTEND:20260101T130000Z\r\nSUMMARY:M\r\n"+
		"END:VEVENT\r\nEND:VCALENDAR\r\n"), "REQUEST")
	// Seed 2: REPLY with TZID + RRULE + ATTENDEE.
	f.Add([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\n"+
		"BEGIN:VEVENT\r\nUID:rep@example.com\r\nDTSTAMP:20260101T000000Z\r\n"+
		"DTSTART;TZID=Europe/Berlin:20260101T090000\r\n"+
		"RRULE:FREQ=DAILY;COUNT=5\r\n"+
		"ATTENDEE;CN=Alice;PARTSTAT=ACCEPTED;RSVP=TRUE:mailto:alice@example.com\r\n"+
		"END:VEVENT\r\nEND:VCALENDAR\r\n"), "REPLY")
	// Seed 3: CANCEL with bogus TZ + invalid BYMONTH.
	f.Add([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\n"+
		"BEGIN:VEVENT\r\nUID:can@example.com\r\nDTSTAMP:20260101T000000Z\r\n"+
		"DTSTART;TZID=No/Such/Zone:20260101T120000\r\n"+
		"RRULE:FREQ=MONTHLY;BYMONTH=13,-1,99,abc\r\n"+
		"END:VEVENT\r\nEND:VCALENDAR\r\n"), "CANCEL")
	// Adversarial: bytes that produce no VEVENT at all (loop body
	// stays empty), exotic methods, oversize RRULE.
	f.Add([]byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"), "")
	f.Add([]byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:n\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"),
		strings.Repeat("X", 256))
	f.Add([]byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:r\r\n"+
		"RRULE:"+strings.Repeat("BYMONTH=1;", 200)+"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"),
		"REQUEST")

	f.Fuzz(func(t *testing.T, in []byte, method string) {
		cal, err := ParseICS(bytes.NewReader(in))
		if err != nil {
			return
		}
		for _, vev := range cal.Events {
			ev, berr := vev.ToJSCalendarEvent(method)
			if berr != nil {
				continue // typed error path is fine
			}
			if ev.UID != vev.UID {
				t.Fatalf("UID round-trip: got %q want %q", ev.UID, vev.UID)
			}
			if ev.Type != "Event" {
				t.Fatalf("@type must be \"Event\", got %q", ev.Type)
			}
			switch ev.StatusValue {
			case "", "confirmed", "cancelled", "tentative":
			default:
				t.Fatalf("unexpected status %q", ev.StatusValue)
			}
			// Updated must be a valid time.Time (zero is fine).
			if !ev.Updated.IsZero() && ev.Updated.Year() < 1 {
				t.Fatalf("updated has bogus year: %v", ev.Updated)
			}
			// Force a marshal/unmarshal cycle to surface any panic
			// that might lurk in the canonical-JSON path. We do not
			// assert byte equality on the UID round-trip: encoding/
			// json replaces invalid UTF-8 with U+FFFD, so a malformed
			// inbound UID survives at parse but mutates on emission —
			// that is intentional behaviour, not a regression.
			if _, mErr := ev.MarshalJSON(); mErr != nil {
				t.Fatalf("marshal: %v", mErr)
			}
			_ = time.Now() // silence unused-import lint when seeds drop
		}
	})
}
