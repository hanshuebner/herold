package recomputebodymeta_test

// recomputebodymeta_binaryleaf_test.go -- pins the backfill path for issue
// #325: a row that was already persisted with a corrupted PNG-byte preview
// (BodyMetaComputed=true, so the background bodymeta worker and Email/get's
// opportunistic persist both skip it -- see internal/bodymeta/worker.go and
// internal/protojmap/mail/email/get.go) is repaired by a recomputebodymeta
// Apply run, which recomputes unconditionally from the raw blob and
// overwrites whenever the recomputed value differs from what is stored,
// regardless of BodyMetaComputed.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/recomputebodymeta"
	"github.com/hanshuebner/herold/internal/store"
)

// rawPNGLeafMessage is shaped like herold issue #325's report: a
// multipart/related containing a multipart/alternative whose genuine
// text/plain part is empty (alongside a text/html part), plus a sibling
// leaf carrying a base64-encoded PNG signature mislabelled text/plain
// (mirroring the #324 extimg defect, which invalidates the Content-Type of
// an inline image so mailparse defaults it to "text/plain;
// charset=us-ascii" per RFC 2045).
const rawPNGLeafMessage = "From: Carol <carol@example.test>\r\n" +
	"To: Dave <dave@example.test>\r\n" +
	"Subject: Reduce tus costos de embalaje desde hoy\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=\"rel\"\r\n" +
	"\r\n" +
	"--rel\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <img1>\r\n" +
	"Content-Disposition: inline\r\n" +
	"\r\n" +
	"iVBORw0KGgoAAAANSUhEUg==\r\n" + // base64 of the PNG signature + start of an IHDR chunk
	"--rel\r\n" +
	"Content-Type: multipart/alternative; boundary=\"alt\"\r\n" +
	"\r\n" +
	"--alt\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"\r\n" +
	"--alt\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Reduce tus costos de embalaje desde hoy</p>\r\n" +
	"--alt--\r\n" +
	"--rel--\r\n"

// wantPNGLeafPreview is the clean preview mailparse.BodyPreview computes
// for rawPNGLeafMessage under the #325 fix: the mislabelled binary leaf is
// skipped, the genuine (empty) text/plain alternative yields nothing, and
// the HTML alternative's extracted text is used.
const wantPNGLeafPreview = "Reduce tus costos de embalaje desde hoy"

// corruptedPNGPreview simulates message 3400's actual pre-#325 persisted
// state: the PNG signature bytes selected as the "text/plain" body and
// truncated straight into the preview column, matching the issue's
// reported stored hex 89504E47201A20.
//
// This byte sequence is NOT valid UTF-8 (0x89 is not a legal UTF-8 lead
// byte): on SQLite -- production's actual backend for message 3400 -- the
// TEXT column has no encoding constraint, so the corrupted bytes persist
// exactly as reported. On Postgres, the "preview" column is UTF8-encoded
// and the server rejects the write outright (SQLSTATE 22021 "invalid byte
// sequence for encoding UTF8", confirmed by direct observation against a
// throwaway database) -- so this exact byte-for-byte corruption could
// never have been persisted to a Postgres-backed herold. The Postgres leg
// below therefore seeds with corruptedPDFPreview instead: a different
// LooksLikeText-rejected binary-magic prefix ("%PDF-", pure ASCII so it is
// valid UTF-8) that still exercises the same recompute/overwrite path.
const corruptedPNGPreview = "\x89PNG \x1a \x00\x00\x00 IHDR"

// corruptedPDFPreview is the Postgres-storable counterpart to
// corruptedPNGPreview: valid UTF-8 (pure ASCII), so it persists cleanly,
// while still starting with a mailparse.LooksLikeText-rejected binary
// magic prefix ("%PDF-") so recomputebodymeta.Run treats it exactly like
// corruptedPNGPreview -- a stored preview the current (fixed) BodyPreview
// logic would never produce, so an Apply run recomputes and overwrites it.
const corruptedPDFPreview = "%PDF-1.4 stream garbage"

// TestRecomputeBodyMeta_RepairsBinaryLeafPreview_SQLite is the SQLite leg
// of the #325 backfill scenario, seeded with the exact PNG-signature bytes
// message 3400's stored preview actually held.
func TestRecomputeBodyMeta_RepairsBinaryLeafPreview_SQLite(t *testing.T) {
	testRecomputeBodyMetaRepairsBinaryLeafPreview(t, openSQLite(t), corruptedPNGPreview)
}

// TestRecomputeBodyMeta_RepairsBinaryLeafPreview_Postgres is the Postgres
// leg of the #325 backfill scenario. Skips when HEROLD_PG_DSN is unset.
// Seeded with corruptedPDFPreview rather than the literal PNG bytes --
// see corruptedPNGPreview's doc comment for why the exact byte sequence
// cannot be persisted to a Postgres-backed store.
func TestRecomputeBodyMeta_RepairsBinaryLeafPreview_Postgres(t *testing.T) {
	testRecomputeBodyMetaRepairsBinaryLeafPreview(t, openPostgres(t), corruptedPDFPreview)
}

// testRecomputeBodyMetaRepairsBinaryLeafPreview is the backend-agnostic
// body: a row is seeded exactly as the pre-#325 code would have left it --
// BodyMetaComputed=true (via SetMessageBodyMeta, which always sets the
// flag) and Preview holding corruptedPreview -- then a recomputebodymeta
// Apply run must overwrite it with the clean, HTML-derived preview.
func testRecomputeBodyMetaRepairsBinaryLeafPreview(t *testing.T, st store.Store, corruptedPreview string) {
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "owner-325@example.test",
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

	ref, err := st.Blobs().Put(ctx, strings.NewReader(rawPNGLeafMessage))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     store.Envelope{Subject: "Reduce tus costos de embalaje desde hoy", From: "Carol <carol@example.test>"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgID := findMessageByFrom(t, st, mb.ID, "Carol <carol@example.test>")

	// Simulate the pre-#325 persisted state: the corrupted binary-leaf
	// preview, with BodyMetaComputed=true so neither the background
	// bodymeta worker nor Email/get's opportunistic persist would ever
	// revisit this row (both only compute when body_meta_computed is
	// false).
	if err := st.Meta().SetMessageBodyMeta(ctx, msgID, corruptedPreview, false); err != nil {
		t.Fatalf("SetMessageBodyMeta (simulate pre-#325 state): %v", err)
	}

	pre, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage before: %v", err)
	}
	if !pre.BodyMetaComputed {
		t.Fatalf("fixture setup: BodyMetaComputed = false, want true")
	}
	if pre.Preview != corruptedPreview {
		t.Fatalf("fixture setup: Preview = %q, want %q", pre.Preview, corruptedPreview)
	}

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

	post, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage after apply: %v", err)
	}
	if post.Preview != wantPNGLeafPreview {
		t.Fatalf("Preview after apply = %q, want %q", post.Preview, wantPNGLeafPreview)
	}
	if strings.Contains(post.Preview, "\x89PNG") {
		t.Fatalf("OBSERVED BUG: Preview after apply still contains the PNG signature: %q (bytes % x)",
			post.Preview, []byte(post.Preview))
	}
}
