package webpush

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

func newTestStore(t *testing.T) *fakestore.Store {
	t.Helper()
	s, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBuildPayload_Email(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	msg := store.Message{
		Flags:        0,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         123,
		Envelope: store.Envelope{
			From:    "Bob <bob@example.test>",
			Subject: strings.Repeat("X", 200), // overflow → truncate
		},
		Blob: store.BlobRef{Hash: "deadbeef", Size: 123},
	}
	_, _, err := st.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Read back to find the assigned ID.
	rows, err := st.Meta().ListMessages(ctx, mbid, store.MessageFilter{Limit: 10, WithEnvelope: true})
	if err != nil || len(rows) == 0 {
		t.Fatalf("list messages: %v %d", err, len(rows))
	}
	mid := rows[0].ID

	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	}
	res, err := BuildPayload(ctx, st, ev)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.JSON, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got["type"] != "email" {
		t.Fatalf("type=%v", got["type"])
	}
	subj, _ := got["subject"].(string)
	if len(subj) > PayloadCapBytes {
		t.Fatalf("subject %d bytes; cap is %d", len(subj), PayloadCapBytes)
	}
	if got["mailbox"] != "INBOX" {
		t.Fatalf("mailbox=%v", got["mailbox"])
	}
}

func TestBuildPayload_Calendar(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	pid := mustInsertPrincipal(t, st, "carol@example.test")
	cal := store.Calendar{
		PrincipalID: pid,
		Name:        "Work",
		IsDefault:   true,
	}
	calID, err := st.Meta().InsertCalendar(ctx, cal)
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	ev := store.CalendarEvent{
		CalendarID:     calID,
		PrincipalID:    pid,
		UID:            "evt-1",
		Summary:        "Standup",
		Start:          time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC),
		End:            time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC),
		JSCalendarJSON: []byte(`{"@type":"Event","title":"Standup","locations":{"l1":{"name":"Room 5"}}}`),
	}
	cevID, err := st.Meta().InsertCalendarEvent(ctx, ev)
	if err != nil {
		t.Fatalf("InsertCalendarEvent: %v", err)
	}
	change := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindCalendarEvent,
		EntityID:    uint64(cevID),
		Op:          store.ChangeOpCreated,
	}
	res, err := BuildPayload(ctx, st, change)
	if err != nil {
		t.Fatalf("BuildPayload calendar: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.JSON, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["type"] != "calendar" {
		t.Fatalf("type=%v", got["type"])
	}
	if got["title"] != "Standup" {
		t.Fatalf("title=%v", got["title"])
	}
	if got["location"] != "Room 5" {
		t.Fatalf("location=%v", got["location"])
	}
	if got["uid"] != "evt-1" {
		t.Fatalf("uid=%v", got["uid"])
	}
}

func TestBuildPayload_RejectsUnsupportedKind(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ev := store.StateChange{
		Kind: store.EntityKindMailbox,
		Op:   store.ChangeOpCreated,
	}
	if _, err := BuildPayload(context.Background(), st, ev); err == nil {
		t.Fatalf("want error on Mailbox kind")
	}
}

func TestTruncateUTF8(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 80, "hello"},
		{"hello", 3, "hel"},
		// Multi-byte at the boundary: "héllo" = 68 c3 a9 6c 6c 6f.
		// Cut at 2 -> the second byte is mid-rune; back off to 1.
		{"héllo", 2, "h"},
		// Cut at 3 -> exactly at the rune boundary after "hé".
		{"héllo", 3, "hé"},
	}
	for _, c := range cases {
		got := truncateUTF8(c.in, c.max)
		if got != c.want {
			t.Fatalf("truncateUTF8(%q, %d)=%q want %q", c.in, c.max, got, c.want)
		}
	}
}

func mustInsertPrincipal(t *testing.T, st *fakestore.Store, email string) store.PrincipalID {
	t.Helper()
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%q): %v", email, err)
	}
	return p.ID
}

func mustInsertMailbox(t *testing.T, st *fakestore.Store, pid store.PrincipalID, name string) store.MailboxID {
	t.Helper()
	m, err := st.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: pid,
		Name:        name,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	return m.ID
}
