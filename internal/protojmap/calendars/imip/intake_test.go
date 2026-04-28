package imip_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap/calendars/imip"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fixture wires a fakestore with one principal and an INBOX mailbox.
// Mirrors the maildmarc intake_test.go scaffold: this lets the worker
// observe iMIP messages on the global change feed without spinning up
// the full testharness HTTP listeners.
type fixture struct {
	store  *fakestore.Store
	pid    store.PrincipalID
	mailbx store.MailboxID
	intake *imip.Intake
	clock  clock.Clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	ctx := context.Background()
	p, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	mb, err := fs.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	in := imip.New(imip.Options{
		Store:        fs,
		Logger:       logger,
		Clock:        clk,
		PollInterval: 5 * time.Millisecond,
		CursorKey:    "calendars-imip-test-" + t.Name(),
	})
	return &fixture{
		store: fs, pid: p.ID, mailbx: mb.ID, intake: in, clock: clk,
	}
}

// runIntakeOnce runs the worker until ctx times out. The worker's
// poll loop sees the seeded change-feed entries on the first
// iteration; the timeout is generous enough for one full pass with
// margin.
func runIntakeOnce(t *testing.T, f *fixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := f.intake.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("intake.Run: %v", err)
	}
}

// deliverIMIP synthesises an inbound iMIP message carrying the
// supplied iCalendar bytes wrapped in a multipart/mixed envelope and
// persists it onto the principal's INBOX. The InsertMessage call
// appends the EntityKindEmail Created change-feed row the worker
// then picks up.
func deliverIMIP(t *testing.T, f *fixture, ics []byte) {
	t.Helper()
	body := []byte("From: organizer@example.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: Meeting invite\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n" +
		"\r\n" +
		string(ics))
	ctx := context.Background()
	ref, err := f.store.Blobs().Put(ctx, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	msg := store.Message{
		Size: ref.Size,
		Blob: ref,
		Envelope: store.Envelope{
			From:    "organizer@example.test",
			To:      "alice@example.test",
			Subject: "Meeting invite",
		},
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: f.mailbx}}); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func TestIMIP_Request_CreatesEventOnDefaultCalendar(t *testing.T) {
	f := newFixture(t)
	ics := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:meeting-1@example.test\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Quarterly review\r\n" +
		"ORGANIZER:mailto:organizer@example.test\r\n" +
		"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:alice@example.test\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	deliverIMIP(t, f, ics)
	runIntakeOnce(t, f)

	rows, err := f.store.Meta().ListCalendarEvents(context.Background(), store.CalendarEventFilter{
		PrincipalID: &f.pid,
	})
	if err != nil {
		t.Fatalf("ListCalendarEvents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(rows), rows)
	}
	if rows[0].UID != "meeting-1@example.test" {
		t.Errorf("uid = %q, want meeting-1@example.test", rows[0].UID)
	}
}

func TestIMIP_AutoCreatesDefaultCalendarWhenAbsent(t *testing.T) {
	f := newFixture(t)
	cals, _ := f.store.Meta().ListCalendars(context.Background(), store.CalendarFilter{
		PrincipalID: &f.pid,
	})
	if len(cals) != 0 {
		t.Fatalf("precondition: expected 0 calendars, got %d", len(cals))
	}
	ics := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:auto-1@example.test\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Auto-created calendar\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	deliverIMIP(t, f, ics)
	runIntakeOnce(t, f)

	cals, _ = f.store.Meta().ListCalendars(context.Background(), store.CalendarFilter{
		PrincipalID: &f.pid,
	})
	if len(cals) != 1 {
		t.Fatalf("expected 1 lazily-created calendar, got %d", len(cals))
	}
	if !cals[0].IsDefault {
		t.Errorf("lazily-created calendar should be default")
	}
}

func TestIMIP_Request_UpdatesExistingByUID_RespectsSequence(t *testing.T) {
	f := newFixture(t)
	first := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:seq-1@example.test\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Original\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	updated := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:seq-1@example.test\r\n" +
		"DTSTAMP:20260102T000000Z\r\n" +
		"DTSTART:20260316T100000Z\r\n" +
		"DTEND:20260316T110000Z\r\n" +
		"SUMMARY:Rescheduled\r\n" +
		"SEQUENCE:1\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	stale := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:seq-1@example.test\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Stale\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	deliverIMIP(t, f, first)
	runIntakeOnce(t, f)
	deliverIMIP(t, f, updated)
	runIntakeOnce(t, f)
	deliverIMIP(t, f, stale)
	runIntakeOnce(t, f)

	rows, _ := f.store.Meta().ListCalendarEvents(context.Background(), store.CalendarEventFilter{
		PrincipalID: &f.pid,
	})
	if len(rows) != 1 {
		t.Fatalf("expected 1 event (sequence-deduped), got %d: %+v", len(rows), rows)
	}
	if rows[0].Summary != "Rescheduled" {
		t.Errorf("expected stored summary 'Rescheduled' (the latest sequence), got %q", rows[0].Summary)
	}
}

func TestIMIP_Cancel_MarksStatusCancelled_NoDelete(t *testing.T) {
	f := newFixture(t)
	req := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:cancel-1@example.test\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Doomed meeting\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	cancel := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:CANCEL\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:cancel-1@example.test\r\n" +
		"DTSTAMP:20260105T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Doomed meeting\r\n" +
		"SEQUENCE:1\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	deliverIMIP(t, f, req)
	runIntakeOnce(t, f)
	deliverIMIP(t, f, cancel)
	runIntakeOnce(t, f)

	rows, _ := f.store.Meta().ListCalendarEvents(context.Background(), store.CalendarEventFilter{
		PrincipalID: &f.pid,
	})
	if len(rows) != 1 {
		t.Fatalf("expected event preserved post-CANCEL, got %d", len(rows))
	}
	if rows[0].Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", rows[0].Status)
	}
}

func TestIMIP_Reply_UpdatesAttendeePARTSTAT(t *testing.T) {
	f := newFixture(t)
	req := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:reply-1@example.test\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Replied meeting\r\n" +
		"ORGANIZER:mailto:alice@example.test\r\n" +
		"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:bob@example.test\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	reply := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REPLY\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:reply-1@example.test\r\n" +
		"DTSTAMP:20260102T000000Z\r\n" +
		"DTSTART:20260315T100000Z\r\n" +
		"DTEND:20260315T110000Z\r\n" +
		"SUMMARY:Replied meeting\r\n" +
		"ORGANIZER:mailto:alice@example.test\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@example.test\r\n" +
		"SEQUENCE:0\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	deliverIMIP(t, f, req)
	runIntakeOnce(t, f)
	deliverIMIP(t, f, reply)
	runIntakeOnce(t, f)

	rows, _ := f.store.Meta().ListCalendarEvents(context.Background(), store.CalendarEventFilter{
		PrincipalID: &f.pid,
	})
	if len(rows) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rows))
	}
	body := string(rows[0].JSCalendarJSON)
	// PARTSTAT=ACCEPTED should land in the JSCalendar
	// participants[].participationStatus = "accepted".
	if !strings.Contains(body, "accepted") {
		t.Errorf("expected JSCalendar body to carry 'accepted' participationStatus, got: %s", body)
	}
}
