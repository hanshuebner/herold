package reparseenvelopes_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/reparseenvelopes"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// rawLegacyMessage is a well-formed message whose Subject header uses
// RFC 2047 Q-encoding over ISO-8859-15 -- the exact shape the pre-fix
// mailparse header-charset defect (re #257) decoded to empty. The
// decoded subject is "Angebot IT-Museumsstücke (fwd)"; =FC is U+00FC
// (u with umlaut) in ISO-8859-15.
const rawLegacyMessage = "From: Carol <carol@example.test>\r\n" +
	"To: Dave <dave@example.test>\r\n" +
	"Subject: =?ISO-8859-15?Q?Angebot_IT-Museumsst=FCcke_=28fwd=29?=\r\n" +
	"Message-ID: <legacy-msg-1@example.test>\r\n" +
	"\r\n" +
	"Body text.\r\n"

const wantDecodedSubject = "Angebot IT-Museumsstücke (fwd)"

// rawCorrectMessage is a second, unrelated message whose stored
// Envelope already matches what a reparse would recover -- used to
// pin the no-regression / idempotency guarantee.
const rawCorrectMessage = "From: Erin <erin@example.test>\r\n" +
	"To: Frank <frank@example.test>\r\n" +
	"Subject: Already correct\r\n" +
	"Message-ID: <already-correct-1@example.test>\r\n" +
	"\r\n" +
	"Body text.\r\n"

// openSQLite returns a fresh, empty SQLite-backed store.Store.
func openSQLite(t *testing.T) store.Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// openPostgres returns a fresh (truncated) Postgres-backed store.Store,
// or skips the test when HEROLD_PG_DSN is unset -- the standard dual-
// backend gate used across the herold test suite (e.g.
// internal/storepg/storepg_test.go).
func openPostgres(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, filepath.Join(t.TempDir(), "blobs"), nil, clk)
	if err != nil {
		t.Fatalf("storepg.Open: %v", err)
	}
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// legacyFixture seeds the store with:
//   - msgA: raw blob = rawLegacyMessage, stored Envelope has Subject and
//     MessageID blanked (the pre-fix defect) but From deliberately set to
//     a value that does NOT match the blob's real sender, to pin the
//     "never overwrite a non-empty stored field" rule.
//   - msgB: raw blob = rawCorrectMessage, stored Envelope already fully
//     matches what a reparse would recover, to pin no-regression /
//     idempotency.
//
// Returns the two message IDs.
func legacyFixture(t *testing.T, st store.Store) (msgA, msgB store.MessageID) {
	t.Helper()
	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "owner@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	refA, err := st.Blobs().Put(ctx, strings.NewReader(rawLegacyMessage))
	if err != nil {
		t.Fatalf("Blobs.Put (legacy): %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         refA.Size,
		Blob:         refA,
		Envelope: store.Envelope{
			// Subject and MessageID blanked, exactly as the pre-#257
			// / pre-#244 ingest bug left them.
			Subject:   "",
			MessageID: "",
			// From deliberately wrong-but-non-empty: must survive
			// the repair pass untouched.
			From: "preserved@example.test",
			To:   "dave@example.test",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage (legacy): %v", err)
	}
	msgA = findMessageBySubjectOrFrom(t, st, p.ID, mb.ID, "preserved@example.test")

	refB, err := st.Blobs().Put(ctx, strings.NewReader(rawCorrectMessage))
	if err != nil {
		t.Fatalf("Blobs.Put (correct): %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         refB.Size,
		Blob:         refB,
		Envelope: store.Envelope{
			Subject:   "Already correct",
			MessageID: "already-correct-1@example.test",
			From:      "Erin <erin@example.test>",
			To:        "Frank <frank@example.test>",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage (correct): %v", err)
	}
	msgB = findMessageBySubjectOrFrom(t, st, p.ID, mb.ID, "Erin <erin@example.test>")
	return msgA, msgB
}

// findMessageBySubjectOrFrom lists mailbox mb's messages and returns
// the id whose stored Envelope.From matches wantFrom. Used because
// InsertMessage does not return the assigned MessageID directly.
func findMessageBySubjectOrFrom(t *testing.T, st store.Store, pid store.PrincipalID, mb store.MailboxID, wantFrom string) store.MessageID {
	t.Helper()
	msgs, err := st.Meta().ListMessages(context.Background(), mb, store.MessageFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.Envelope.From == wantFrom {
			return m.ID
		}
	}
	t.Fatalf("no message with From=%q among %d messages", wantFrom, len(msgs))
	return 0
}

func TestReparseEnvelopes_SQLite(t *testing.T) {
	testReparseEnvelopes(t, openSQLite(t))
}

func TestReparseEnvelopes_Postgres(t *testing.T) {
	testReparseEnvelopes(t, openPostgres(t))
}

// testReparseEnvelopes is the backend-agnostic body shared by the
// SQLite and Postgres variants above. It exercises, in order:
//  1. dry-run reports the blanked-subject change but writes nothing;
//  2. apply repairs the blanked Subject/MessageID, decoding the RFC
//     2047 header correctly, while leaving the deliberately-wrong
//     From field untouched (never overwrite a non-empty field) and
//     leaving the already-correct message byte-identical;
//  3. a second apply run is a no-op (idempotency).
func testReparseEnvelopes(t *testing.T, st store.Store) {
	ctx := context.Background()
	msgA, msgB := legacyFixture(t, st)

	preA, err := st.Meta().GetMessage(ctx, msgA)
	if err != nil {
		t.Fatalf("GetMessage(msgA) before: %v", err)
	}
	preB, err := st.Meta().GetMessage(ctx, msgB)
	if err != nil {
		t.Fatalf("GetMessage(msgB) before: %v", err)
	}

	// -- 1. Dry-run: reports the change, writes nothing. --
	dryRun, err := reparseenvelopes.Run(ctx, st, reparseenvelopes.Options{Apply: false})
	if err != nil {
		t.Fatalf("Run (dry-run): %v", err)
	}
	if dryRun.Scanned != 2 {
		t.Fatalf("dry-run Scanned = %d, want 2", dryRun.Scanned)
	}
	if dryRun.Changed != 1 {
		t.Fatalf("dry-run Changed = %d, want 1 (only msgA)", dryRun.Changed)
	}
	if dryRun.Applied != 0 {
		t.Fatalf("dry-run Applied = %d, want 0 (dry-run must write nothing)", dryRun.Applied)
	}
	if len(dryRun.Samples) != 1 || dryRun.Samples[0].ID != msgA {
		t.Fatalf("dry-run Samples = %+v, want one entry for msgA=%d", dryRun.Samples, msgA)
	}
	if dryRun.Samples[0].NewSubject != wantDecodedSubject {
		t.Fatalf("dry-run sample NewSubject = %q, want %q", dryRun.Samples[0].NewSubject, wantDecodedSubject)
	}

	postDryRunA, err := st.Meta().GetMessage(ctx, msgA)
	if err != nil {
		t.Fatalf("GetMessage(msgA) after dry-run: %v", err)
	}
	if postDryRunA.Envelope != preA.Envelope {
		t.Fatalf("dry-run mutated msgA's stored Envelope: before=%+v after=%+v", preA.Envelope, postDryRunA.Envelope)
	}

	// -- 2. Apply: repairs msgA, leaves msgB untouched. --
	applied, err := reparseenvelopes.Run(ctx, st, reparseenvelopes.Options{Apply: true})
	if err != nil {
		t.Fatalf("Run (apply): %v", err)
	}
	if applied.Changed != 1 || applied.Applied != 1 {
		t.Fatalf("apply run: Changed=%d Applied=%d, want 1/1", applied.Changed, applied.Applied)
	}
	if applied.WriteErrors != 0 {
		t.Fatalf("apply run: WriteErrors = %d, want 0", applied.WriteErrors)
	}

	postA, err := st.Meta().GetMessage(ctx, msgA)
	if err != nil {
		t.Fatalf("GetMessage(msgA) after apply: %v", err)
	}
	if postA.Envelope.Subject != wantDecodedSubject {
		t.Fatalf("msgA Subject after apply = %q, want %q", postA.Envelope.Subject, wantDecodedSubject)
	}
	if postA.Envelope.MessageID != "legacy-msg-1@example.test" {
		t.Fatalf("msgA MessageID after apply = %q, want %q", postA.Envelope.MessageID, "legacy-msg-1@example.test")
	}
	if postA.Envelope.From != "preserved@example.test" {
		t.Fatalf("OBSERVED BUG: msgA From after apply = %q, want unchanged %q (never overwrite a non-empty field)",
			postA.Envelope.From, "preserved@example.test")
	}

	postB, err := st.Meta().GetMessage(ctx, msgB)
	if err != nil {
		t.Fatalf("GetMessage(msgB) after apply: %v", err)
	}
	if postB.Envelope != preB.Envelope {
		t.Fatalf("apply run regressed msgB's already-correct Envelope: before=%+v after=%+v", preB.Envelope, postB.Envelope)
	}

	// -- 3. Idempotency: a second apply run changes nothing. --
	second, err := reparseenvelopes.Run(ctx, st, reparseenvelopes.Options{Apply: true})
	if err != nil {
		t.Fatalf("Run (second apply): %v", err)
	}
	if second.Changed != 0 || second.Applied != 0 {
		t.Fatalf("second apply run: Changed=%d Applied=%d, want 0/0 (idempotent)", second.Changed, second.Applied)
	}
}
