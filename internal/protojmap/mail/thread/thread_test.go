package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"path/filepath"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func setupWithStore(t *testing.T, st store.Store, canonicalEmail string) (*handlerSet, store.Store, store.Principal, store.Mailbox) {
	t.Helper()
	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: canonicalEmail,
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	return &handlerSet{store: st}, st, p, mb
}

func setup(t *testing.T) (*handlerSet, store.Store, store.Principal, store.Mailbox) {
	t.Helper()
	return setupWithStore(t, newStore(t), "alice@example.test")
}

// setupPostgres is setup's Postgres counterpart (mirrors the
// setupFixturePostgres pattern in internal/protojmap/mail/email):
// opens storepg against HEROLD_PG_DSN and skips when it is unset or
// unreachable, so tests using it run as a no-op locally and as a real
// parity check in CI's storepg leg. The principal's canonical email is
// nanosecond-suffixed because storepg.Open connects to a persistent
// database rather than a fresh per-test file.
func setupPostgres(t *testing.T) (*handlerSet, store.Store, store.Principal, store.Mailbox) {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	canonicalEmail := fmt.Sprintf("tk-samesecond-%d@example.test", time.Now().UnixNano())
	return setupWithStore(t, st, canonicalEmail)
}

func insertMsg(t *testing.T, st store.Store, mb store.Mailbox, msgID, inReplyTo, subject string) store.MessageID {
	t.Helper()
	return insertMsgWithRefs(t, st, mb, msgID, inReplyTo, "", subject)
}

func insertMsgWithRefs(t *testing.T, st store.Store, mb store.Mailbox, msgID, inReplyTo, references, subject string) store.MessageID {
	t.Helper()
	uid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		Blob: store.BlobRef{Hash: "deadbeef" + msgID, Size: 1},
		Envelope: store.Envelope{
			Subject:    subject,
			MessageID:  msgID,
			InReplyTo:  inReplyTo,
			References: references,
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	// Find the row's MessageID by UID -- the store assigns IDs
	// monotonically and exposes them through ListMessages.
	msgs, err := st.Meta().ListMessages(context.Background(), mb.ID, store.MessageFilter{
		Limit: 1000, WithEnvelope: true,
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m.ID
		}
	}
	t.Fatalf("inserted message UID=%d not found", uid)
	return 0
}

// insertMsgAt is insertMsgWithRefs's counterpart that pins ReceivedAt to a
// caller-supplied instant, so a test can seed several messages sharing one
// whole-second receivedAt value (re #527).
func insertMsgAt(t *testing.T, st store.Store, mb store.Mailbox, msgID, inReplyTo, references, subject string, receivedAt time.Time) store.MessageID {
	t.Helper()
	uid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		Blob:       store.BlobRef{Hash: "deadbeef" + msgID, Size: 1},
		ReceivedAt: receivedAt,
		Envelope: store.Envelope{
			Subject:    subject,
			MessageID:  msgID,
			InReplyTo:  inReplyTo,
			References: references,
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	msgs, err := st.Meta().ListMessages(context.Background(), mb.ID, store.MessageFilter{
		Limit: 1000, WithEnvelope: true,
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m.ID
		}
	}
	t.Fatalf("inserted message UID=%d not found", uid)
	return 0
}

// testSameSecondThreadOrder pins REQ-MAIL-133a's server-side contract:
// Thread/get's EmailIDs for messages that share one whole-second
// receivedAt value are ordered deterministically by (receivedAt, message
// id) — RFC 8621 Section 8.1 — regardless of insertion order or which
// backend stores the rows. Run against both SQLite and Postgres (re
// #527): the ordering is produced by sortThreadRows, a Go-level stable
// sort over the rows ListThreadsByKeys returns, so backend parity here
// specifically pins that ListThreadsByKeys's unordered SQL rows still
// resolve to the same EmailIDs order everywhere.
func testSameSecondThreadOrder(t *testing.T, h *handlerSet, st store.Store, p store.Principal, mb store.Mailbox) {
	t.Helper()
	sameSecond := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	id1 := insertMsgAt(t, st, mb, "<ss1@example.test>", "", "", "Same second", sameSecond)
	id2 := insertMsgAt(t, st, mb, "<ss2@example.test>", "<ss1@example.test>", "", "Re: Same second", sameSecond)
	id3 := insertMsgAt(t, st, mb, "<ss3@example.test>", "<ss2@example.test>", "<ss1@example.test> <ss2@example.test>", "Re: Same second", sameSecond)

	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Thread/get: %v", mErr)
	}
	g := resp.(getResponse)
	if len(g.List) != 1 {
		t.Fatalf("expected 1 thread, got %d: %+v", len(g.List), g)
	}
	thr := g.List[0]
	want := []string{renderEmailID(id1), renderEmailID(id2), renderEmailID(id3)}
	if len(thr.EmailIDs) != len(want) {
		t.Fatalf("EmailIDs = %v, want %v", thr.EmailIDs, want)
	}
	for i, got := range thr.EmailIDs {
		if got != want[i] {
			t.Fatalf("EmailIDs[%d] = %s, want %s (same-second order must be insertion/message-id order: %v)",
				i, got, want[i], thr.EmailIDs)
		}
	}
}

func TestThread_Get_SameSecondOrder_SQLite(t *testing.T) {
	h, st, p, mb := setup(t)
	testSameSecondThreadOrder(t, h, st, p, mb)
}

func TestThread_Get_SameSecondOrder_Postgres(t *testing.T) {
	h, st, p, mb := setupPostgres(t)
	testSameSecondThreadOrder(t, h, st, p, mb)
}

func TestThread_Get_DerivedFromMessages(t *testing.T) {
	h, st, p, mb := setup(t)
	id1 := insertMsg(t, st, mb, "<m1@example.test>", "", "Original subject")
	id2 := insertMsg(t, st, mb, "<m2@example.test>", "<m1@example.test>", "Re: Original subject")
	id3 := insertMsg(t, st, mb, "<m3@example.test>", "<m1@example.test> <m2@example.test>", "Re: Original subject")
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Thread/get: %v", mErr)
	}
	g := resp.(getResponse)
	if len(g.List) != 1 {
		t.Fatalf("expected 1 thread, got %d: %+v", len(g.List), g)
	}
	thr := g.List[0]
	if len(thr.EmailIDs) != 3 {
		t.Fatalf("expected 3 emails in thread, got %d: %+v", len(thr.EmailIDs), thr)
	}
	got := strings.Join(thr.EmailIDs, ",")
	for _, mid := range []store.MessageID{id1, id2, id3} {
		want := renderEmailID(mid)
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}

// TestThread_Get_ReferencesHeader verifies that thread assignment uses the
// References header, not only In-Reply-To. This is the root-cause scenario
// for the six RFC 8621 conformance failures: msg3 replies to msg1 only via
// References (no In-Reply-To), and must still land in the same thread.
//
// RFC 5256 sec 2.2 and RFC 8621 sec 8.1 both mandate unioning
// In-Reply-To and References for thread resolution.
func TestThread_Get_ReferencesHeader(t *testing.T) {
	h, st, p, mb := setup(t)
	// msg1: the thread root (no references).
	id1 := insertMsg(t, st, mb, "<m1@example.test>", "", "Original subject")
	// msg2: replies to msg1 via In-Reply-To (conventional reply).
	id2 := insertMsg(t, st, mb, "<m2@example.test>", "<m1@example.test>", "Re: Original subject")
	// msg3: references both ancestors via References only; In-Reply-To is empty.
	// This is the failing scenario: without References-based resolution,
	// msg3 would become its own singleton thread instead of joining the existing thread.
	id3 := insertMsgWithRefs(t, st, mb,
		"<m3@example.test>", "",
		"<m1@example.test> <m2@example.test>",
		"Re: Original subject")
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Thread/get: %v", mErr)
	}
	g := resp.(getResponse)
	if len(g.List) != 1 {
		t.Fatalf("expected 1 thread (got %d threads): msg3 should have been linked via References", len(g.List))
	}
	thr := g.List[0]
	if len(thr.EmailIDs) != 3 {
		t.Fatalf("expected 3 emails in thread, got %d: %+v", len(thr.EmailIDs), thr.EmailIDs)
	}
	got := strings.Join(thr.EmailIDs, ",")
	for _, mid := range []store.MessageID{id1, id2, id3} {
		want := renderEmailID(mid)
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in thread email list %s", want, got)
		}
	}
}

func TestThread_Get_OrphanReply(t *testing.T) {
	h, st, p, mb := setup(t)
	// Both replies reference a parent that was never ingested. The
	// ingest-time thread resolver looks up ancestors by env_message_id;
	// since no message with Message-ID "<missing@elsewhere>" exists in
	// the store, neither reply can locate a shared thread root at ingest
	// time. Each message therefore becomes its own singleton thread.
	// Full JWZ grouping of co-orphans (two messages that share the same
	// missing parent) is left for a future read-time pass.
	insertMsg(t, st, mb, "<reply1@example.test>", "<missing@elsewhere>", "Re: lost thread")
	insertMsg(t, st, mb, "<reply2@example.test>", "<missing@elsewhere>", "Re: lost thread")
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, _ := getHandler{h: h}.executeAs(p, args)
	g := resp.(getResponse)
	// Each orphan reply is its own singleton thread.
	if len(g.List) != 2 {
		t.Fatalf("expected 2 singleton threads for co-orphan replies, got %d", len(g.List))
	}
	for _, thr := range g.List {
		if len(thr.EmailIDs) != 1 {
			t.Fatalf("expected singleton thread, got %d emails: %v", len(thr.EmailIDs), thr.EmailIDs)
		}
	}
}

func TestThread_Get_DistinctThreadsForUnrelatedMessages(t *testing.T) {
	h, st, p, mb := setup(t)
	insertMsg(t, st, mb, "<u1@example.test>", "", "Topic A")
	insertMsg(t, st, mb, "<u2@example.test>", "", "Topic B")
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, _ := getHandler{h: h}.executeAs(p, args)
	g := resp.(getResponse)
	if len(g.List) != 2 {
		t.Fatalf("expected 2 threads, got %d: %+v", len(g.List), g)
	}
}

func TestThread_Changes_NoOpWhenSameState(t *testing.T) {
	h, st, p, _ := setup(t)
	// Get current thread state via Thread/get.
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID), "ids": []string{}})
	getResp, _ := getHandler{h: h}.executeAs(p, getArgs)
	currentState := getResp.(getResponse).State
	args, _ := json.Marshal(map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(p.ID),
		"sinceState": currentState,
	})
	_ = st
	resp, mErr := changesHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Thread/changes: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"updated":[]`) {
		t.Fatalf("expected empty updated: %s", js)
	}
}

func TestNormalizeSubject_StripsRePrefixes(t *testing.T) {
	cases := []struct {
		in        string
		want      string
		wantReply bool
	}{
		{"Re: hello", "hello", true},
		{"Fwd: hi", "hi", true},
		{"FW: hi", "hi", true},
		{"[List] Re: thing", "thing", true},
		{"plain", "plain", false},
	}
	for _, c := range cases {
		got, isReply := normalizeSubject(c.in)
		if got != c.want || isReply != c.wantReply {
			t.Errorf("normalizeSubject(%q) = (%q, %v), want (%q, %v)",
				c.in, got, isReply, c.want, c.wantReply)
		}
	}
}
