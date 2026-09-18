package admin

// spam_undo_recipient_only_test.go exercises undoRecipientOnlyOverrides
// (re #396, third round, `spam reclassify --undo-recipient-only`)
// against both store backends: a message resolved to spam solely on the
// second round's now-retired standalone recipient_not_own rule is moved
// out of Junk into the Inbox and its stored verdict corrected to ham; a
// message that is still decisive under today's rules (recipient_not_own
// combined with another decisive signal, or an independently decisive
// signal alongside it) is left untouched.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

func TestUndoRecipientOnlyOverrides_SQLite(t *testing.T) {
	testUndoRecipientOnlyOverrides(t, sqlitetest.Open(t, clock.NewReal()))
}

func TestUndoRecipientOnlyOverrides_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			_ = st.Close()
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = st.Close() })
	testUndoRecipientOnlyOverrides(t, st)
}

// insertUndoFixtureMessage inserts a message as a member of mbID and
// records an llm_classifications spam sub-record for it, returning the
// assigned message id.
func insertUndoFixtureMessage(t *testing.T, ctx context.Context, st store.Store, pid store.PrincipalID, msgIDHeader string, mbID store.MailboxID, verdict, modelVerdict string, signals []string) store.MessageID {
	t.Helper()
	blob, err := st.Blobs().Put(ctx, strings.NewReader("body of "+msgIDHeader))
	if err != nil {
		t.Fatalf("Blobs.Put(%s): %v", msgIDHeader, err)
	}
	_, _, err = st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: pid,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "undo-recipient-only", MessageID: msgIDHeader},
	}, []store.MessageMailbox{{MailboxID: mbID}})
	if err != nil {
		t.Fatalf("InsertMessage(%s): %v", msgIDHeader, err)
	}
	msg, err := st.Meta().GetMessageByMessageIDHeader(ctx, pid, msgIDHeader)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader(%s): %v", msgIDHeader, err)
	}

	rec := store.LLMClassificationRecord{
		MessageID:   msg.ID,
		PrincipalID: pid,
		SpamVerdict: strPtr(verdict),
	}
	if modelVerdict != "" {
		rec.SpamModelVerdict = strPtr(modelVerdict)
	}
	if len(signals) > 0 {
		rec.SpamSignals = spam.OptStringSlice(signals)
	}
	if err := st.Meta().SetLLMClassification(ctx, rec); err != nil {
		t.Fatalf("SetLLMClassification(%s): %v", msgIDHeader, err)
	}
	return msg.ID
}

// mailboxIDsOf returns the sorted mailbox ids msgID currently belongs to.
func mailboxIDsOf(t *testing.T, ctx context.Context, st store.Store, msgID store.MessageID) map[store.MailboxID]bool {
	t.Helper()
	msg, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	out := make(map[store.MailboxID]bool, len(msg.Mailboxes))
	for _, mm := range msg.Mailboxes {
		out[mm.MailboxID] = true
	}
	return out
}

func testUndoRecipientOnlyOverrides(t *testing.T, st store.Store) {
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "undo-recipient-only@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox INBOX: %v", err)
	}
	junk, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Junk", Attributes: store.MailboxAttrJunk,
	})
	if err != nil {
		t.Fatalf("InsertMailbox Junk: %v", err)
	}

	// (a) the false-positive shape: recipient_not_own alone. Must repair.
	falsePositive := insertUndoFixtureMessage(t, ctx, st, p.ID, "false-positive@test", junk.ID,
		"spam", "ham", []string{"recipient_not_own"})

	// (b) recipient_not_own alongside an independently decisive signal
	// (unsolicited_bulk_marketing). Still decisive today; must NOT repair.
	stillDecisiveIndependent := insertUndoFixtureMessage(t, ctx, st, p.ID, "still-decisive-independent@test", junk.ID,
		"spam", "ham", []string{"recipient_not_own", "unsolicited_bulk_marketing"})

	// (c) recipient_not_own combined with bulk_list_relay -- the new
	// combined decisive rule. Still decisive today; must NOT repair.
	stillDecisiveCombined := insertUndoFixtureMessage(t, ctx, st, p.ID, "still-decisive-combined@test", junk.ID,
		"spam", "ham", []string{"recipient_not_own", "bulk_list_relay"})

	// (d) already ham (never overridden). Not a candidate.
	alreadyHam := insertUndoFixtureMessage(t, ctx, st, p.ID, "already-ham@test", inbox.ID,
		"ham", "", nil)

	// (e) genuine spam with no model-verdict divergence. Not a candidate.
	genuineSpam := insertUndoFixtureMessage(t, ctx, st, p.ID, "genuine-spam@test", junk.ID,
		"spam", "", []string{"phishing"})

	rules := spam.DefaultDecisiveSpamSignals

	// --dry-run changes nothing.
	dryRunSum, err := undoRecipientOnlyOverrides(ctx, st, p.ID, rules, true)
	if err != nil {
		t.Fatalf("undoRecipientOnlyOverrides (dry-run): %v", err)
	}
	if dryRunSum.Repaired != 1 {
		t.Fatalf("dry-run Repaired = %d, want 1", dryRunSum.Repaired)
	}
	if !mailboxIDsOf(t, ctx, st, falsePositive)[junk.ID] {
		t.Fatalf("dry-run must not move the false-positive message out of Junk")
	}

	sum, err := undoRecipientOnlyOverrides(ctx, st, p.ID, rules, false)
	if err != nil {
		t.Fatalf("undoRecipientOnlyOverrides: %v", err)
	}
	if sum.Repaired != 1 {
		t.Fatalf("Repaired = %d, want 1", sum.Repaired)
	}

	// (a) moved to Inbox, verdict corrected, model-verdict divergence cleared.
	fpMailboxes := mailboxIDsOf(t, ctx, st, falsePositive)
	if fpMailboxes[junk.ID] || !fpMailboxes[inbox.ID] {
		t.Fatalf("false-positive message mailboxes = %v, want only Inbox (%d)", fpMailboxes, inbox.ID)
	}
	fpRec, err := st.Meta().GetLLMClassification(ctx, falsePositive)
	if err != nil {
		t.Fatalf("GetLLMClassification(falsePositive): %v", err)
	}
	if fpRec.SpamVerdict == nil || *fpRec.SpamVerdict != "ham" {
		t.Fatalf("false-positive SpamVerdict = %v, want ham", fpRec.SpamVerdict)
	}
	if fpRec.SpamModelVerdict != nil {
		t.Fatalf("false-positive SpamModelVerdict = %v, want nil (verdict divergence cleared)", *fpRec.SpamModelVerdict)
	}

	// (b), (c): still decisive today -- left in Junk, verdict untouched.
	for name, mid := range map[string]store.MessageID{
		"still-decisive-independent": stillDecisiveIndependent,
		"still-decisive-combined":    stillDecisiveCombined,
	} {
		mbs := mailboxIDsOf(t, ctx, st, mid)
		if !mbs[junk.ID] {
			t.Fatalf("%s must stay in Junk, got mailboxes %v", name, mbs)
		}
		rec, err := st.Meta().GetLLMClassification(ctx, mid)
		if err != nil {
			t.Fatalf("GetLLMClassification(%s): %v", name, err)
		}
		if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
			t.Fatalf("%s SpamVerdict = %v, want spam (untouched)", name, rec.SpamVerdict)
		}
	}

	// (d), (e): never candidates -- untouched.
	if mbs := mailboxIDsOf(t, ctx, st, alreadyHam); !mbs[inbox.ID] {
		t.Fatalf("already-ham message must stay in Inbox, got %v", mbs)
	}
	if mbs := mailboxIDsOf(t, ctx, st, genuineSpam); !mbs[junk.ID] {
		t.Fatalf("genuine-spam message must stay in Junk, got %v", mbs)
	}

	// A second run is idempotent: nothing left to repair.
	sum2, err := undoRecipientOnlyOverrides(ctx, st, p.ID, rules, false)
	if err != nil {
		t.Fatalf("undoRecipientOnlyOverrides (second run): %v", err)
	}
	if sum2.Repaired != 0 {
		t.Fatalf("second run Repaired = %d, want 0 (idempotent)", sum2.Repaired)
	}
}
