package jscalendar_test

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap/calendars/jscalendar"
)

// CRLF-terminated iCalendar lines per RFC 5545 §3.1. Tests that depend
// on line-folding still work with LF-only fixtures because the parser
// tolerates either, but the canonical wire form is CRLF and the
// fixtures here mirror that.
func crlf(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	return strings.Join(lines, "\r\n") + "\r\n"
}

func TestParseICS_GoogleStyleRequest(t *testing.T) {
	body := crlf(`
BEGIN:VCALENDAR
PRODID:-//Google Inc//Google Calendar 70.9054//EN
VERSION:2.0
METHOD:REQUEST
BEGIN:VEVENT
UID:google-event-1@google.com
SUMMARY:Project sync
DTSTAMP:20250420T093000Z
DTSTART:20250424T140000Z
DTEND:20250424T150000Z
SEQUENCE:0
ORGANIZER;CN=Ada Lovelace:mailto:ada@example.test
ATTENDEE;CN=Bob Smith;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.test
ATTENDEE;CN=Carol;ROLE=OPT-PARTICIPANT;PARTSTAT=ACCEPTED:mailto:carol@example.test
RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=TH;COUNT=10
END:VEVENT
END:VCALENDAR
`)
	cal, err := jscalendar.ParseICS(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseICS: %v", err)
	}
	if cal.Method != "REQUEST" {
		t.Fatalf("Method = %q, want REQUEST", cal.Method)
	}
	if len(cal.Events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(cal.Events))
	}
	ev := cal.Events[0]
	if ev.UID != "google-event-1@google.com" {
		t.Fatalf("UID = %q", ev.UID)
	}
	if ev.Summary != "Project sync" {
		t.Fatalf("Summary = %q", ev.Summary)
	}
	if ev.Organizer.CalAddress != "mailto:ada@example.test" {
		t.Fatalf("Organizer = %q", ev.Organizer.CalAddress)
	}
	if ev.Organizer.CN != "Ada Lovelace" {
		t.Fatalf("Organizer.CN = %q", ev.Organizer.CN)
	}
	if len(ev.Attendees) != 2 {
		t.Fatalf("attendees = %d, want 2", len(ev.Attendees))
	}
	if ev.RRule == "" {
		t.Fatalf("RRULE missing")
	}
	if ev.DTStart.IsZero() || ev.DTEnd.IsZero() {
		t.Fatalf("DTSTART/DTEND not parsed")
	}
}

func TestParseICS_MicrosoftWindowsTZID(t *testing.T) {
	body := crlf(`
BEGIN:VCALENDAR
PRODID:-//Microsoft Corporation//Outlook 16.0 MIMEDIR//EN
VERSION:2.0
METHOD:REQUEST
BEGIN:VEVENT
UID:msft-event-1@example.com
SUMMARY:Standup
DTSTAMP:20250420T093000Z
DTSTART;TZID=Pacific Standard Time:20250424T090000
DTEND;TZID=Pacific Standard Time:20250424T093000
ORGANIZER;CN=Org:mailto:org@example.test
ATTENDEE;CN=A;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:a@example.test
END:VEVENT
END:VCALENDAR
`)
	cal, err := jscalendar.ParseICS(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseICS: %v", err)
	}
	if len(cal.Events) != 1 {
		t.Fatalf("events = %d", len(cal.Events))
	}
	ev := cal.Events[0]
	if ev.TZID != "Pacific Standard Time" {
		t.Fatalf("TZID = %q", ev.TZID)
	}
	jsev, err := ev.ToJSCalendarEvent("REQUEST")
	if err != nil {
		t.Fatalf("ToJSCalendarEvent: %v", err)
	}
	if jsev.TimeZone != "America/Los_Angeles" {
		t.Fatalf("TimeZone = %q, want America/Los_Angeles", jsev.TimeZone)
	}
}

func TestParseICS_Reply(t *testing.T) {
	body := crlf(`
BEGIN:VCALENDAR
PRODID:-//Apple Inc.//iOS 17.4//EN
VERSION:2.0
METHOD:REPLY
BEGIN:VEVENT
UID:google-event-1@google.com
SUMMARY:Project sync
DTSTAMP:20250421T093000Z
DTSTART:20250424T140000Z
DTEND:20250424T150000Z
ORGANIZER:mailto:ada@example.test
ATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@example.test
SEQUENCE:0
END:VEVENT
END:VCALENDAR
`)
	cal, err := jscalendar.ParseICS(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseICS: %v", err)
	}
	if cal.Method != "REPLY" {
		t.Fatalf("Method = %q, want REPLY", cal.Method)
	}
	if len(cal.Events) != 1 || len(cal.Events[0].Attendees) != 1 {
		t.Fatalf("unexpected event/attendee shape: %+v", cal.Events)
	}
	if cal.Events[0].Attendees[0].PartStat != "ACCEPTED" {
		t.Fatalf("PartStat = %q, want ACCEPTED", cal.Events[0].Attendees[0].PartStat)
	}
	jsev, err := cal.Events[0].ToJSCalendarEvent("REPLY")
	if err != nil {
		t.Fatalf("ToJSCalendarEvent: %v", err)
	}
	// The replying attendee's status should be projected onto the JS
	// participant.
	gotAccepted := false
	for _, p := range jsev.Participants {
		if p.Email == "bob@example.test" && p.ParticipationStatus == "accepted" {
			gotAccepted = true
		}
	}
	if !gotAccepted {
		t.Fatalf("REPLY did not produce accepted status: %+v", jsev.Participants)
	}
}

func TestParseICS_Cancel(t *testing.T) {
	body := crlf(`
BEGIN:VCALENDAR
PRODID:-//Google Inc//Google Calendar 70.9054//EN
VERSION:2.0
METHOD:CANCEL
BEGIN:VEVENT
UID:google-event-1@google.com
SUMMARY:Project sync
STATUS:CANCELLED
SEQUENCE:1
DTSTAMP:20250423T093000Z
DTSTART:20250424T140000Z
DTEND:20250424T150000Z
ORGANIZER:mailto:ada@example.test
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:bob@example.test
END:VEVENT
END:VCALENDAR
`)
	cal, err := jscalendar.ParseICS(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseICS: %v", err)
	}
	if cal.Method != "CANCEL" {
		t.Fatalf("Method = %q, want CANCEL", cal.Method)
	}
	if cal.Events[0].Status != "CANCELLED" {
		t.Fatalf("Status = %q, want CANCELLED", cal.Events[0].Status)
	}
	jsev, err := cal.Events[0].ToJSCalendarEvent("CANCEL")
	if err != nil {
		t.Fatalf("ToJSCalendarEvent: %v", err)
	}
	if jsev.StatusValue != "cancelled" {
		t.Fatalf("StatusValue = %q, want cancelled", jsev.StatusValue)
	}
	if jsev.Sequence != 1 {
		t.Fatalf("Sequence = %d, want 1", jsev.Sequence)
	}
}

func TestParseICS_LineFolding(t *testing.T) {
	// A folded DESCRIPTION line: the second physical line begins with
	// SPACE, which the parser must concatenate (without the SPACE) onto
	// the first.
	// RFC 5545 §3.1: the line break + single leading whitespace are
	// removed during unfolding. To preserve a space in the data, the
	// fixture must include it explicitly before the fold (here:
	// "Hello " ... " world").
	body := "BEGIN:VCALENDAR\r\nPRODID:-//x//x\r\nVERSION:2.0\r\nMETHOD:PUBLISH\r\n" +
		"BEGIN:VEVENT\r\nUID:fold-1\r\n" +
		"DESCRIPTION:Hello \r\n world this is a long line\r\n" +
		"DTSTAMP:20250420T093000Z\r\n" +
		"DTSTART:20250424T140000Z\r\nDTEND:20250424T150000Z\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, err := jscalendar.ParseICS(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseICS: %v", err)
	}
	if cal.Events[0].Description != "Hello world this is a long line" {
		t.Fatalf("Description = %q", cal.Events[0].Description)
	}
}

func TestParseICS_RoundTripAllMethods(t *testing.T) {
	for _, method := range []string{"REQUEST", "REPLY", "CANCEL"} {
		t.Run(method, func(t *testing.T) {
			body := crlf(`
BEGIN:VCALENDAR
PRODID:-//Test//Test
VERSION:2.0
METHOD:` + method + `
BEGIN:VEVENT
UID:rt-1
SUMMARY:Roundtrip
DTSTAMP:20250420T093000Z
DTSTART:20250424T140000Z
DTEND:20250424T150000Z
ORGANIZER:mailto:org@example.test
ATTENDEE;PARTSTAT=ACCEPTED:mailto:a@example.test
SEQUENCE:0
END:VEVENT
END:VCALENDAR
`)
			cal, err := jscalendar.ParseICS(strings.NewReader(body))
			if err != nil {
				t.Fatalf("ParseICS: %v", err)
			}
			jsev, err := cal.Events[0].ToJSCalendarEvent(method)
			if err != nil {
				t.Fatalf("ToJSCalendarEvent: %v", err)
			}
			if jsev.UID != "rt-1" {
				t.Fatalf("UID lost: %q", jsev.UID)
			}
			if jsev.OrganizerEmail() != "org@example.test" {
				t.Fatalf("Organizer = %q", jsev.OrganizerEmail())
			}
			if err := jsev.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// TestParseICS_RejectsEmbeddedCRLFInParameter confirms that ParseICS
// refuses content lines that smuggle a raw CR or LF byte into a
// parameter value. RFC 5545 §3.1 forbids it; allowing it lets a
// malicious sender corrupt the iCalendar object the bridge re-emits
// in outbound iMIP and would, in the worst case, enable header
// injection. The crash seed at
// testdata/fuzz/FuzzParseICS/481fd31f4df828e3 reproduces the original
// failure ("DTEND;TZID=0\r0:") and is retained as a regression seed.
func TestParseICS_RejectsEmbeddedCRLFInParameter(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "regression seed (TZID with embedded CR)",
			body: "BEGIN:VCALENDAR\nBEGIN:VEVENT\nDTEND;TZID=0\r0:\nEND:VEVENT\nEND:VCALENDAR",
		},
		{
			name: "embedded CR inside quoted parameter",
			body: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID;X=\"a\rb\":id\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jscalendar.ParseICS(strings.NewReader(tc.body))
			if err == nil {
				t.Fatalf("ParseICS accepted embedded CR/LF in parameter value")
			}
			if !strings.Contains(err.Error(), "CR or LF") {
				t.Fatalf("error %q does not mention CR/LF rejection", err)
			}
		})
	}
}

func TestResolveTZID(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Pacific Standard Time", "America/Los_Angeles", true},
		{"Central European Standard Time", "Europe/Warsaw", true},
		{"UTC", "UTC", true},
		{"America/New_York", "America/New_York", true},
		{"Made Up Time Zone", "", false},
	}
	for _, tc := range cases {
		got, ok := jscalendar.ResolveTZID(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ResolveTZID(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
