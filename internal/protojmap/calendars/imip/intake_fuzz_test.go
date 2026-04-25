package imip

// iMIP MIME-walker fuzz target (STANDARDS section 8.2). Track C of
// Wave 2.9.5: extractCalendarParts is the first untrusted-bytes
// surface the iMIP intake worker exposes — a remote sender can ship
// an arbitrary RFC 5322 message body, and we have to walk a
// potentially nested multipart tree to find every text/calendar
// part. A panic here would crash-loop the worker.
//
// The unexported entry point is reached by living in the same
// package (per Track C: do not modify production to widen visibility).
//
// Invariants:
//
//   1. Never panics on any input.
//   2. Each returned part's length is bounded by MaxBlobBytes (the
//      LimitReader cap inside extractCalendarParts).
//   3. Recursion depth in collectCalendar is bounded; a deeply
//      nested adversarial multipart must not blow the stack — the
//      depth>4 guard short-circuits it.

import (
	"strings"
	"testing"
)

// FuzzExtractCalendarParts drives extractCalendarParts over arbitrary
// RFC 5322 byte streams.
func FuzzExtractCalendarParts(f *testing.F) {
	// Seed 1: minimal text/calendar message.
	f.Add([]byte("Content-Type: text/calendar; method=REQUEST\r\n" +
		"\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:m1@example.com\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260101T120000Z\r\nSUMMARY:S\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	// Seed 2: multipart/alternative with text+calendar.
	f.Add([]byte("MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BND\"\r\n" +
		"\r\n" +
		"--BND\r\nContent-Type: text/plain\r\n\r\nSee invite.\r\n" +
		"--BND\r\nContent-Type: text/calendar; method=REQUEST\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:alt@example.com\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260101T120000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
		"--BND--\r\n"))
	// Seed 3: deeply nested multipart that must be rejected by the
	// depth guard. Five levels of multipart/related, each with an
	// inner text/calendar that should not be reached.
	{
		var b strings.Builder
		b.WriteString("MIME-Version: 1.0\r\n")
		b.WriteString("Content-Type: multipart/mixed; boundary=\"L0\"\r\n\r\n")
		boundaries := []string{"L0", "L1", "L2", "L3", "L4", "L5"}
		for i := 0; i < len(boundaries)-1; i++ {
			b.WriteString("--" + boundaries[i] + "\r\n")
			b.WriteString("Content-Type: multipart/related; boundary=\"" + boundaries[i+1] + "\"\r\n\r\n")
		}
		b.WriteString("--L5\r\nContent-Type: text/calendar\r\n\r\n")
		b.WriteString("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:deep@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
		for i := len(boundaries) - 1; i >= 0; i-- {
			b.WriteString("--" + boundaries[i] + "--\r\n")
		}
		f.Add([]byte(b.String()))
	}
	// Adversarial extras.
	f.Add([]byte(""))
	f.Add([]byte("not-a-message"))
	f.Add([]byte("Content-Type: text/calendar\r\n\r\n"))                    // empty body
	f.Add([]byte("Content-Type: multipart/alternative\r\n\r\nno boundary")) // no boundary param
	f.Add([]byte("Content-Type: multipart/alternative; boundary=\"\"\r\n\r\n--"))
	f.Add([]byte("Content-Type: text/calendar; charset=\"\xff\xfe\"\r\n\r\nBEGIN:VCALENDAR\r\n"))

	f.Fuzz(func(t *testing.T, in []byte) {
		parts, err := extractCalendarParts(in)
		if err != nil {
			return
		}
		for _, p := range parts {
			if len(p) > MaxBlobBytes {
				t.Fatalf("part exceeds MaxBlobBytes: %d", len(p))
			}
		}
	})
}
