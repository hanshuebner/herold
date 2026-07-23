package recomputebodymeta_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/recomputebodymeta"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// rawHTMLOnlyMessage has a text/html body and no text/plain part -- the
// exact shape the pre-#263 mailparse.BodyPreview defect persisted as
// raw, untruncated HTML instead of extracted text.
const rawHTMLOnlyMessage = "From: Carol <carol@example.test>\r\n" +
	"To: Dave <dave@example.test>\r\n" +
	"Subject: HTML only\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<html><body><p>Hello&nbsp;&amp;&nbsp;welcome</p><p>to the newsletter</p></body></html>\r\n"

// wantExtractedPreview is the extracted-text preview mailparse.BodyPreview
// computes for rawHTMLOnlyMessage under the fixed (#263) logic.
const wantExtractedPreview = "Hello & welcome to the newsletter"

// wrongStoredPreview simulates the pre-#263 persisted value: the raw HTML
// itself, exactly as the old (unfixed) BodyPreview would have returned it
// for an HTML-only message with no text/plain part.
const wrongStoredPreview = "<html><body><p>Hello&nbsp;&amp;&nbsp;welcome</p><p>to the newsletter</p></body></html>"

// rawPlainMessage is a genuine text/plain message whose correct preview is
// simply its trimmed body -- used to pin no-regression on already-correct
// rows.
const rawPlainMessage = "From: Erin <erin@example.test>\r\n" +
	"To: Frank <frank@example.test>\r\n" +
	"Subject: Already correct\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Plain body text.\r\n"

const wantPlainPreview = "Plain body text."

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

// openPostgres returns a fresh (truncated) Postgres-backed store.Store, or
// skips the test when HEROLD_PG_DSN is unset -- the standard dual-backend
// gate used across the herold test suite.
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

// bodyMetaFixture seeds the store with:
//   - msgA: raw blob = rawHTMLOnlyMessage, stored body meta wrongly
//     persisted as raw HTML (the pre-#263 defect), has_attachment=false.
//   - msgB: raw blob = rawPlainMessage, stored body meta already correct,
//     to pin no-regression / idempotency.
//
// Returns the two message IDs.
func bodyMetaFixture(t *testing.T, st store.Store) (msgA, msgB store.MessageID) {
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

	refA, err := st.Blobs().Put(ctx, strings.NewReader(rawHTMLOnlyMessage))
	if err != nil {
		t.Fatalf("Blobs.Put (html-only): %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         refA.Size,
		Blob:         refA,
		Envelope:     store.Envelope{Subject: "HTML only", From: "Carol <carol@example.test>"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage (html-only): %v", err)
	}
	msgA = findMessageByFrom(t, st, mb.ID, "Carol <carol@example.test>")
	// Simulate the pre-#263 persisted state: wrong, non-empty raw-HTML
	// preview, body_meta_computed=true so the background worker would
	// never revisit it.
	if err := st.Meta().SetMessageBodyMeta(ctx, msgA, wrongStoredPreview, false); err != nil {
		t.Fatalf("SetMessageBodyMeta (simulate pre-#263 state): %v", err)
	}

	refB, err := st.Blobs().Put(ctx, strings.NewReader(rawPlainMessage))
	if err != nil {
		t.Fatalf("Blobs.Put (plain): %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         refB.Size,
		Blob:         refB,
		Envelope:     store.Envelope{Subject: "Already correct", From: "Erin <erin@example.test>"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage (plain): %v", err)
	}
	msgB = findMessageByFrom(t, st, mb.ID, "Erin <erin@example.test>")
	if err := st.Meta().SetMessageBodyMeta(ctx, msgB, wantPlainPreview, false); err != nil {
		t.Fatalf("SetMessageBodyMeta (already correct): %v", err)
	}

	return msgA, msgB
}

// findMessageByFrom lists mailbox mb's messages and returns the id whose
// stored Envelope.From matches wantFrom. Used because InsertMessage does
// not return the assigned MessageID directly.
func findMessageByFrom(t *testing.T, st store.Store, mb store.MailboxID, wantFrom string) store.MessageID {
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

func TestRecomputeBodyMeta_SQLite(t *testing.T) {
	testRecomputeBodyMeta(t, openSQLite(t))
}

func TestRecomputeBodyMeta_Postgres(t *testing.T) {
	testRecomputeBodyMeta(t, openPostgres(t))
}

// testRecomputeBodyMeta is the backend-agnostic body shared by the SQLite
// and Postgres variants above. It exercises, in order:
//  1. dry-run reports the wrong-raw-HTML-preview change but writes nothing;
//  2. apply repairs msgA's preview to the extracted text, leaves msgB (an
//     already-correct row) untouched;
//  3. a second apply run is a no-op (idempotency).
func testRecomputeBodyMeta(t *testing.T, st store.Store) {
	ctx := context.Background()
	msgA, msgB := bodyMetaFixture(t, st)

	preA, err := st.Meta().GetMessage(ctx, msgA)
	if err != nil {
		t.Fatalf("GetMessage(msgA) before: %v", err)
	}
	preB, err := st.Meta().GetMessage(ctx, msgB)
	if err != nil {
		t.Fatalf("GetMessage(msgB) before: %v", err)
	}
	if preA.Preview != wrongStoredPreview {
		t.Fatalf("fixture setup: msgA.Preview = %q, want %q", preA.Preview, wrongStoredPreview)
	}

	// -- 1. Dry-run: reports the change, writes nothing. --
	dryRun, err := recomputebodymeta.Run(ctx, st, recomputebodymeta.Options{Apply: false})
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
	if dryRun.Samples[0].NewPreview != wantExtractedPreview {
		t.Fatalf("dry-run sample NewPreview = %q, want %q", dryRun.Samples[0].NewPreview, wantExtractedPreview)
	}

	postDryRunA, err := st.Meta().GetMessage(ctx, msgA)
	if err != nil {
		t.Fatalf("GetMessage(msgA) after dry-run: %v", err)
	}
	if postDryRunA.Preview != preA.Preview || postDryRunA.HasAttachment != preA.HasAttachment {
		t.Fatalf("dry-run mutated msgA's stored body meta: before=(%q,%v) after=(%q,%v)",
			preA.Preview, preA.HasAttachment, postDryRunA.Preview, postDryRunA.HasAttachment)
	}

	// -- 2. Apply: repairs msgA, leaves msgB untouched. --
	applied, err := recomputebodymeta.Run(ctx, st, recomputebodymeta.Options{Apply: true})
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
	if postA.Preview != wantExtractedPreview {
		t.Fatalf("msgA Preview after apply = %q, want extracted text %q (not raw HTML)",
			postA.Preview, wantExtractedPreview)
	}
	if strings.Contains(postA.Preview, "<") || strings.Contains(postA.Preview, "html") {
		t.Fatalf("OBSERVED BUG: msgA Preview after apply still contains raw markup: %q", postA.Preview)
	}

	postB, err := st.Meta().GetMessage(ctx, msgB)
	if err != nil {
		t.Fatalf("GetMessage(msgB) after apply: %v", err)
	}
	if postB.Preview != preB.Preview || postB.HasAttachment != preB.HasAttachment {
		t.Fatalf("apply run regressed msgB's already-correct body meta: before=(%q,%v) after=(%q,%v)",
			preB.Preview, preB.HasAttachment, postB.Preview, postB.HasAttachment)
	}
	if postB.Preview != wantPlainPreview {
		t.Fatalf("msgB Preview after apply = %q, want unchanged %q", postB.Preview, wantPlainPreview)
	}

	// -- 3. Idempotency: a second apply run changes nothing. --
	second, err := recomputebodymeta.Run(ctx, st, recomputebodymeta.Options{Apply: true})
	if err != nil {
		t.Fatalf("Run (second apply): %v", err)
	}
	if second.Changed != 0 || second.Applied != 0 {
		t.Fatalf("second apply run: Changed=%d Applied=%d, want 0/0 (idempotent)", second.Changed, second.Applied)
	}
}
