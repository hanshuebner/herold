package webpush

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
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
	// Legacy type field.
	if got["type"] != "email" {
		t.Fatalf("type=%v", got["type"])
	}
	// SW dispatch key: buildNotificationOptions in sw.js dispatches on kind.
	if got["kind"] != "mail" {
		t.Fatalf("kind=%v (want \"mail\")", got["kind"])
	}
	subj, _ := got["subject"].(string)
	if len(subj) > PayloadCapBytes {
		t.Fatalf("subject %d bytes; cap is %d", len(subj), PayloadCapBytes)
	}
	// body must equal subject so the OS notification body is populated.
	if got["body"] != got["subject"] {
		t.Fatalf("body=%v subject=%v (must be equal)", got["body"], got["subject"])
	}
	if got["mailbox"] != "INBOX" {
		t.Fatalf("mailbox=%v", got["mailbox"])
	}
	// emailId must be present (and match msgid) for SW archive/mark-read actions.
	if got["emailId"] == nil || got["emailId"] == "" {
		t.Fatalf("emailId missing or empty: %v", got["emailId"])
	}
	if got["emailId"] != got["msgid"] {
		t.Fatalf("emailId=%v msgid=%v (must be equal)", got["emailId"], got["msgid"])
	}
	// inboxMailboxId must be present for the SW archive action.
	if got["inboxMailboxId"] == nil || got["inboxMailboxId"] == "" {
		t.Fatalf("inboxMailboxId missing or empty: %v", got["inboxMailboxId"])
	}
	// threadId must be present as "t<emailId>" for unthreaded messages
	// (ThreadID==0 in the store). The JMAP convention treats each unthreaded
	// email as its own thread with key "t<emailID>" (see threadIDForMessage
	// in internal/protojmap/mail/email/render.go). The "t" prefix is required
	// so that loadThread(threadId) in the SPA matches the Thread/get response's
	// {id: "t42"} field correctly (bare "42" would find nothing).
	emailId, _ := got["emailId"].(string)
	wantThread := "t" + emailId
	if got["threadId"] != wantThread {
		t.Fatalf("threadId=%v want %q", got["threadId"], wantThread)
	}
}

func TestBuildPayload_EmailWithThread(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	msg := store.Message{
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         10,
		Envelope:     store.Envelope{From: "Bob", Subject: "Hello"},
		Blob:         store.BlobRef{Hash: "abc", Size: 10},
	}
	_, _, err := st.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	rows, err := st.Meta().ListMessages(ctx, mbid, store.MessageFilter{Limit: 10, WithEnvelope: true})
	if err != nil || len(rows) == 0 {
		t.Fatalf("list messages: %v %d", err, len(rows))
	}
	mid := rows[0].ID
	// Assign a ThreadID so threadId appears in the payload.
	const testThreadID = uint64(42)
	if err := st.Meta().UpdateMessageThreadID(ctx, mid, testThreadID); err != nil {
		t.Fatalf("UpdateMessageThreadID: %v", err)
	}

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
		t.Fatalf("unmarshal: %v", err)
	}
	if got["kind"] != "mail" {
		t.Fatalf("kind=%v", got["kind"])
	}
	// threadId must be the JMAP thread-id wire form "t<threadID>" — matching
	// renderThreadID() in internal/protojmap/mail/thread/methods.go. The bare
	// numeric "42" that was sent before the fix caused loadThread("42") in the
	// SPA to fail because Thread/get responds with {id: "t42"} and the
	// find((t) => t.id === "42") equality check returned undefined.
	wantThread := fmt.Sprintf("t%d", testThreadID)
	if got["threadId"] != wantThread {
		t.Fatalf("threadId=%v want %q", got["threadId"], wantThread)
	}
	if got["emailId"] == nil || got["emailId"] == "" {
		t.Fatalf("emailId missing: %v", got["emailId"])
	}
	if got["inboxMailboxId"] == nil || got["inboxMailboxId"] == "" {
		t.Fatalf("inboxMailboxId missing: %v", got["inboxMailboxId"])
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
	if got["kind"] != "calendar-invite" {
		t.Fatalf("kind=%v (want \"calendar-invite\")", got["kind"])
	}
	if got["title"] != "Standup" {
		t.Fatalf("title=%v", got["title"])
	}
	if got["eventSummary"] != "Standup" {
		t.Fatalf("eventSummary=%v (want \"Standup\")", got["eventSummary"])
	}
	if got["body"] != "Standup" {
		t.Fatalf("body=%v (want \"Standup\")", got["body"])
	}
	if got["location"] != "Room 5" {
		t.Fatalf("location=%v", got["location"])
	}
	if got["uid"] != "evt-1" {
		t.Fatalf("uid=%v", got["uid"])
	}
	if got["eventUID"] != "evt-1" {
		t.Fatalf("eventUID=%v (want \"evt-1\")", got["eventUID"])
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

func mustInsertPrincipal(t *testing.T, st store.Store, email string) store.PrincipalID {
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

func mustInsertMailbox(t *testing.T, st store.Store, pid store.PrincipalID, name string) store.MailboxID {
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
