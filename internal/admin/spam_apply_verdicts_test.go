package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// TestParseSpamVerdictsCSV_ApplyVerdicts exercises the pure CSV
// parsing / validation rules with no store involved: header
// requirements, verdict validation, and the 0..100-vs-0..1
// confidence normalisation.
func TestParseSpamVerdictsCSV_ApplyVerdicts(t *testing.T) {
	rows, err := parseSpamVerdictsCSV(strings.NewReader(
		"id,verdict,confidence,reason,extra\n" +
			"1,spam,83,looks phishy,ignored\n" +
			"2,HAM,0.9,,ignored\n" +
			"3,spam,1,exact 1.0 confidence,ignored\n",
	))
	if err != nil {
		t.Fatalf("parseSpamVerdictsCSV: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].MessageID != 1 || rows[0].Verdict != "spam" || rows[0].Confidence != 0.83 || rows[0].Reason != "looks phishy" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].MessageID != 2 || rows[1].Verdict != "ham" || rows[1].Confidence != 0.9 || rows[1].Reason != "" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if rows[2].Confidence != 1 {
		t.Errorf("row 2 confidence = %v, want 1 (0..1 float taken as-is)", rows[2].Confidence)
	}

	if _, err := parseSpamVerdictsCSV(strings.NewReader("id,verdict,reason\n1,spam,x\n")); err == nil {
		t.Error("missing confidence column: want error, got nil")
	}
	if _, err := parseSpamVerdictsCSV(strings.NewReader("id,verdict,confidence,reason\n1,maybe,50,x\n")); err == nil {
		t.Error("invalid verdict: want error, got nil")
	}
	if _, err := parseSpamVerdictsCSV(strings.NewReader("id,verdict,confidence,reason\n1,spam,101,x\n")); err == nil {
		t.Error("out-of-range confidence: want error, got nil")
	}
}

// spamVerdictsFixture seeds principal p1 with INBOX/Sent/Drafts/Trash/
// Junk plus a custom "Work" folder, a second principal p2 with its
// own INBOX, and the messages the apply-verdicts test flow exercises.
// Returns p1's id and a name -> MessageID map.
func spamVerdictsFixture(t *testing.T, st store.Store) (store.PrincipalID, map[string]store.MessageID) {
	t.Helper()
	ctx := context.Background()

	p1, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "victim@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal p1: %v", err)
	}
	p2, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "other@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal p2: %v", err)
	}

	mkMailbox := func(pid store.PrincipalID, name string, attr store.MailboxAttributes) store.MailboxID {
		mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: name, Attributes: attr})
		if err != nil {
			t.Fatalf("InsertMailbox %s: %v", name, err)
		}
		return mb.ID
	}
	inbox := mkMailbox(p1.ID, "INBOX", store.MailboxAttrInbox)
	sent := mkMailbox(p1.ID, "Sent", store.MailboxAttrSent)
	mkMailbox(p1.ID, "Drafts", store.MailboxAttrDrafts)
	trash := mkMailbox(p1.ID, "Trash", store.MailboxAttrTrash)
	junk := mkMailbox(p1.ID, "Junk", store.MailboxAttrJunk)
	work := mkMailbox(p1.ID, "Work", 0)
	p2Inbox := mkMailbox(p2.ID, "INBOX", store.MailboxAttrInbox)

	insert := func(pid store.PrincipalID, subject string, targets ...store.MailboxID) {
		mm := make([]store.MessageMailbox, len(targets))
		for i, id := range targets {
			mm[i] = store.MessageMailbox{MailboxID: id}
		}
		body := "From: sender@example.test\r\nTo: victim@example.test\r\nSubject: " + subject + "\r\n\r\nbody\r\n"
		blob, err := st.Blobs().Put(ctx, strings.NewReader(body))
		if err != nil {
			t.Fatalf("Blobs.Put %s: %v", subject, err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:  pid,
			InternalDate: time.Now(),
			ReceivedAt:   time.Now(),
			Size:         blob.Size,
			Blob:         blob,
			Envelope:     store.Envelope{Subject: subject},
		}, mm); err != nil {
			t.Fatalf("InsertMessage %s: %v", subject, err)
		}
	}
	insert(p1.ID, "spam-new", inbox, work)
	insert(p1.ID, "spam-with-sent", inbox, sent)
	insert(p1.ID, "ham-msg", inbox)
	insert(p1.ID, "already-junk", junk)
	insert(p1.ID, "already-trash", trash)
	insert(p1.ID, "low-conf", inbox)
	insert(p1.ID, "dry-run-msg", inbox)
	insert(p1.ID, "undo-msg", inbox)
	insert(p2.ID, "other-principal-msg", p2Inbox)

	byName := make(map[string]store.MessageID)
	for _, name := range []string{
		"spam-new", "spam-with-sent", "ham-msg", "already-junk",
		"already-trash", "low-conf", "dry-run-msg", "undo-msg",
		"other-principal-msg",
	} {
		byName[name] = findSpamVerdictMessageBySubject(t, st, []store.PrincipalID{p1.ID, p2.ID}, name)
	}
	return p1.ID, byName
}

// findSpamVerdictMessageBySubject returns the MessageID of the fixture
// message with the given Subject. InsertMessage does not return the
// assigned id directly, so this scans every mailbox of the given
// principals and matches on the (unique, per-fixture) Subject -- the
// same lookup-by-scan pattern recomputebodymeta's test fixture uses.
func findSpamVerdictMessageBySubject(t *testing.T, st store.Store, pids []store.PrincipalID, subject string) store.MessageID {
	t.Helper()
	ctx := context.Background()
	for _, pid := range pids {
		mailboxes, err := st.Meta().ListMailboxes(ctx, pid)
		if err != nil {
			continue
		}
		for _, mb := range mailboxes {
			msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 1000})
			if err != nil {
				continue
			}
			for _, m := range msgs {
				if m.Envelope.Subject == subject {
					return m.ID
				}
			}
		}
	}
	t.Fatalf("no message with subject %q found", subject)
	return 0
}

func mailboxSet(t *testing.T, st store.Store, id store.MessageID) map[string]bool {
	t.Helper()
	m, err := st.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage(%d): %v", id, err)
	}
	names := make(map[string]bool, len(m.Mailboxes))
	for _, mm := range m.Mailboxes {
		mb, err := st.Meta().GetMailboxByID(context.Background(), mm.MailboxID)
		if err != nil {
			t.Fatalf("GetMailboxByID(%d): %v", mm.MailboxID, err)
		}
		names[mb.Name] = true
	}
	return names
}

func TestSpamApplyVerdicts_SQLite(t *testing.T) {
	testSpamApplyVerdicts(t, sqlitetest.Open(t, clock.NewReal()))
}

func TestSpamApplyVerdicts_Postgres(t *testing.T) {
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
	testSpamApplyVerdicts(t, st)
}

// testSpamApplyVerdicts is the backend-agnostic body shared by the
// SQLite and Postgres variants. It exercises, against a single apply
// run:
//   - a fresh spam verdict moving a multi-mailbox message into Junk
//     alone (every other membership, including a non-special-use
//     custom folder, dropped);
//   - a fresh spam verdict on a message also in Sent, preserving the
//     Sent membership alongside the new Junk one;
//   - a ham verdict recording the classification and leaving
//     placement untouched;
//   - spam verdicts on messages already in Junk / already in Trash,
//     left untouched but still recorded (AlreadyJunk);
//   - a spam verdict below --min-confidence, recorded but not moved;
//   - an unknown message id and a message owned by a different
//     principal, both counted and skipped;
//
// then, in two separate runs, --dry-run (writes nothing) and
// --undo-log + --undo (records the pre-move membership set and
// restores it).
func testSpamApplyVerdicts(t *testing.T, st store.Store) {
	ctx := context.Background()
	pid, msg := spamVerdictsFixture(t, st)
	clk := clock.NewFake(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	csvBody := "id,verdict,confidence,reason\n" +
		itoaMsg(msg["spam-new"]) + ",spam,95,obvious phishing\n" +
		itoaMsg(msg["spam-with-sent"]) + ",spam,90,\n" +
		itoaMsg(msg["ham-msg"]) + ",ham,80,looks fine\n" +
		itoaMsg(msg["already-junk"]) + ",spam,88,\n" +
		itoaMsg(msg["already-trash"]) + ",spam,88,\n" +
		itoaMsg(msg["low-conf"]) + ",spam,10,\n" +
		"999999999,spam,90,\n" +
		itoaMsg(msg["other-principal-msg"]) + ",spam,90,\n"

	rows, err := parseSpamVerdictsCSV(strings.NewReader(csvBody))
	if err != nil {
		t.Fatalf("parseSpamVerdictsCSV: %v", err)
	}

	sum, err := applySpamVerdicts(ctx, st, clk, pid, rows, spamApplyVerdictsOptions{
		Engine:        "batch-test",
		MinConfidence: 0.5,
	})
	if err != nil {
		t.Fatalf("applySpamVerdicts: %v", err)
	}
	want := SpamApplyVerdictsSummary{
		RowsRead: 8, Applied: 6, Moved: 2, AlreadyJunk: 2,
		SkippedUnknown: 1, SkippedOtherPrincipal: 1,
	}
	if sum != want {
		t.Fatalf("summary = %+v, want %+v", sum, want)
	}

	if got := mailboxSet(t, st, msg["spam-new"]); len(got) != 1 || !got["Junk"] {
		t.Errorf("spam-new mailboxes = %v, want {Junk}", got)
	}
	if got := mailboxSet(t, st, msg["spam-with-sent"]); len(got) != 2 || !got["Junk"] || !got["Sent"] {
		t.Errorf("spam-with-sent mailboxes = %v, want {Junk, Sent}", got)
	}
	if got := mailboxSet(t, st, msg["ham-msg"]); len(got) != 1 || !got["INBOX"] {
		t.Errorf("ham-msg mailboxes = %v, want {INBOX}", got)
	}
	if got := mailboxSet(t, st, msg["already-junk"]); len(got) != 1 || !got["Junk"] {
		t.Errorf("already-junk mailboxes = %v, want {Junk}", got)
	}
	if got := mailboxSet(t, st, msg["already-trash"]); len(got) != 1 || !got["Trash"] {
		t.Errorf("already-trash mailboxes = %v, want {Trash}", got)
	}
	if got := mailboxSet(t, st, msg["low-conf"]); len(got) != 1 || !got["INBOX"] {
		t.Errorf("low-conf mailboxes = %v, want {INBOX} (below min-confidence, not moved)", got)
	}

	rec, err := st.Meta().GetLLMClassification(ctx, msg["spam-new"])
	if err != nil {
		t.Fatalf("GetLLMClassification(spam-new): %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Errorf("spam-new SpamVerdict = %v, want spam", rec.SpamVerdict)
	}
	if rec.SpamConfidence == nil || *rec.SpamConfidence != 0.95 {
		t.Errorf("spam-new SpamConfidence = %v, want 0.95", rec.SpamConfidence)
	}
	if rec.SpamModel == nil || *rec.SpamModel != "batch-test" {
		t.Errorf("spam-new SpamModel = %v, want batch-test", rec.SpamModel)
	}
	if rec.SpamReason == nil || *rec.SpamReason != "obvious phishing" {
		t.Errorf("spam-new SpamReason = %v, want %q", rec.SpamReason, "obvious phishing")
	}
	// re #385: apply-verdicts must record the request that produced the
	// verdict, like reclassify does, so every batch row is auditable.
	// spam-new is a plain fixture message (no IngestSource, no
	// Authentication-Results header), so the recovered auth is "none"
	// on every method -- never a false "fail".
	if rec.SpamPromptApplied == nil {
		t.Fatal("spam-new SpamPromptApplied is nil, want the recorded spam.Request JSON")
	}
	var promptReq spam.Request
	if err := json.Unmarshal([]byte(*rec.SpamPromptApplied), &promptReq); err != nil {
		t.Fatalf("unmarshal spam-new SpamPromptApplied: %v", err)
	}
	if promptReq.SPF != "none" || promptReq.DKIM != "none" || promptReq.DMARC != "none" {
		t.Errorf("spam-new recorded auth verdicts = spf=%q dkim=%q dmarc=%q, want none/none/none",
			promptReq.SPF, promptReq.DKIM, promptReq.DMARC)
	}

	hamRec, err := st.Meta().GetLLMClassification(ctx, msg["ham-msg"])
	if err != nil {
		t.Fatalf("GetLLMClassification(ham-msg): %v", err)
	}
	if hamRec.SpamVerdict == nil || *hamRec.SpamVerdict != "ham" {
		t.Errorf("ham-msg SpamVerdict = %v, want ham", hamRec.SpamVerdict)
	}

	// --- dry-run: nothing written, nothing moved -----------------------
	dryCSV := "id,verdict,confidence,reason\n" + itoaMsg(msg["dry-run-msg"]) + ",spam,95,\n"
	dryRows, err := parseSpamVerdictsCSV(strings.NewReader(dryCSV))
	if err != nil {
		t.Fatalf("parseSpamVerdictsCSV (dry-run): %v", err)
	}
	drySum, err := applySpamVerdicts(ctx, st, clk, pid, dryRows, spamApplyVerdictsOptions{
		Engine: "batch-test", DryRun: true,
	})
	if err != nil {
		t.Fatalf("applySpamVerdicts (dry-run): %v", err)
	}
	if drySum.Applied != 1 || drySum.Moved != 1 {
		t.Errorf("dry-run summary = %+v, want Applied=1 Moved=1 (reported, not written)", drySum)
	}
	if got := mailboxSet(t, st, msg["dry-run-msg"]); len(got) != 1 || !got["INBOX"] {
		t.Errorf("dry-run-msg mailboxes = %v, want {INBOX} (dry-run must not move)", got)
	}
	if _, err := st.Meta().GetLLMClassification(ctx, msg["dry-run-msg"]); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("dry-run-msg GetLLMClassification err = %v, want ErrNotFound (dry-run must not write)", err)
	}

	// --- undo: move with --undo-log, then restore -----------------------
	undoCSV := "id,verdict,confidence,reason\n" + itoaMsg(msg["undo-msg"]) + ",spam,95,\n"
	undoRows, err := parseSpamVerdictsCSV(strings.NewReader(undoCSV))
	if err != nil {
		t.Fatalf("parseSpamVerdictsCSV (undo): %v", err)
	}
	var undoBuf bytes.Buffer
	undoWriter := csv.NewWriter(&undoBuf)
	if err := undoWriter.Write([]string{"message_id", "previous_mailbox_ids"}); err != nil {
		t.Fatalf("write undo header: %v", err)
	}
	if _, err := applySpamVerdicts(ctx, st, clk, pid, undoRows, spamApplyVerdictsOptions{
		Engine: "batch-test", UndoLog: undoWriter,
	}); err != nil {
		t.Fatalf("applySpamVerdicts (undo source): %v", err)
	}
	undoWriter.Flush()
	if err := undoWriter.Error(); err != nil {
		t.Fatalf("flush undo writer: %v", err)
	}
	if got := mailboxSet(t, st, msg["undo-msg"]); len(got) != 1 || !got["Junk"] {
		t.Fatalf("undo-msg mailboxes before undo = %v, want {Junk}", got)
	}

	undoLogRows, err := parseSpamUndoCSV(strings.NewReader(undoBuf.String()))
	if err != nil {
		t.Fatalf("parseSpamUndoCSV: %v", err)
	}
	restoreSum, err := undoSpamVerdicts(ctx, st, pid, undoLogRows, false)
	if err != nil {
		t.Fatalf("undoSpamVerdicts: %v", err)
	}
	if restoreSum.Restored != 1 {
		t.Errorf("undo summary = %+v, want Restored=1", restoreSum)
	}
	if got := mailboxSet(t, st, msg["undo-msg"]); len(got) != 1 || !got["INBOX"] {
		t.Errorf("undo-msg mailboxes after undo = %v, want {INBOX}", got)
	}
}

func itoaMsg(id store.MessageID) string {
	return strconv.FormatUint(uint64(id), 10)
}

// TestApplySpamVerdicts_ReusesDeliveryAuthResults is the re #385
// regression test for the apply-verdicts path: an SMTP-delivered
// message's stamped Authentication-Results header must be recovered
// into the recorded spam.Request, the same as reclassifySpam.
func TestApplySpamVerdicts_ReusesDeliveryAuthResults_SQLite(t *testing.T) {
	testApplySpamVerdictsReusesDeliveryAuthResults(t, sqlitetest.Open(t, clock.NewReal()))
}

func TestApplySpamVerdicts_ReusesDeliveryAuthResults_Postgres(t *testing.T) {
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
	testApplySpamVerdictsReusesDeliveryAuthResults(t, st)
}

func testApplySpamVerdictsReusesDeliveryAuthResults(t *testing.T, st store.Store) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "apply-delivery-auth@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
	if err != nil {
		t.Fatalf("InsertMailbox INBOX: %v", err)
	}
	if _, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Junk", Attributes: store.MailboxAttrJunk}); err != nil {
		t.Fatalf("InsertMailbox Junk: %v", err)
	}

	const rawAuthResults = "mx.test.example; dkim=pass header.d=example.test header.s=s1; " +
		"spf=pass smtp.mailfrom=sender@example.test; dmarc=pass header.from=example.test"
	const subject = "apply-delivery-auth-message"
	body := "Authentication-Results: " + rawAuthResults + "\r\n" +
		"From: sender@example.test\r\nTo: apply-delivery-auth@example.test\r\nSubject: " + subject + "\r\n\r\nbody\r\n"
	blob, err := st.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         blob.Size,
		Blob:         blob,
		Envelope:     store.Envelope{Subject: subject},
		IngestSource: store.IngestSourceSMTP,
	}, []store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	mid := findSpamVerdictMessageBySubject(t, st, []store.PrincipalID{p.ID}, subject)

	csvBody := "id,verdict,confidence,reason\n" + itoaMsg(mid) + ",ham,90,looks fine\n"
	rows, err := parseSpamVerdictsCSV(strings.NewReader(csvBody))
	if err != nil {
		t.Fatalf("parseSpamVerdictsCSV: %v", err)
	}
	sum, err := applySpamVerdicts(ctx, st, clk, p.ID, rows, spamApplyVerdictsOptions{Engine: "batch:test"})
	if err != nil {
		t.Fatalf("applySpamVerdicts: %v", err)
	}
	if sum.Applied != 1 {
		t.Fatalf("summary = %+v, want Applied=1", sum)
	}

	rec, err := st.Meta().GetLLMClassification(ctx, mid)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamPromptApplied == nil {
		t.Fatal("SpamPromptApplied is nil, want the recorded spam.Request JSON")
	}
	var req spam.Request
	if err := json.Unmarshal([]byte(*rec.SpamPromptApplied), &req); err != nil {
		t.Fatalf("unmarshal SpamPromptApplied: %v", err)
	}
	if req.SPF != "pass" || req.DKIM != "pass" || req.DMARC != "pass" {
		t.Fatalf("auth verdicts = spf=%q dkim=%q dmarc=%q, want pass/pass/pass matching the stamped header",
			req.SPF, req.DKIM, req.DMARC)
	}
	if req.AuthResults != rawAuthResults {
		t.Fatalf("auth_results = %q, want %q (the stamped header verbatim)", req.AuthResults, rawAuthResults)
	}
}

// TestApplySpamVerdicts_IgnoresForeignAuthResults is the re #385
// negative regression test for the apply-verdicts path: a message whose
// IngestSource is anything other than store.IngestSourceSMTP -- an IMAP
// import, or a pre-ingest-source row carrying the empty
// IngestSourceUnknown -- must never have its own Authentication-Results
// header trusted, even when that header claims a full pass on every
// method. deliveryAuthResults must return nil for such a message so the
// recorded spam.Request reads spf/dkim/dmarc = "none", never a forged
// "pass".
func TestApplySpamVerdicts_IgnoresForeignAuthResults_SQLite(t *testing.T) {
	testApplySpamVerdictsIgnoresForeignAuthResults(t, sqlitetest.Open(t, clock.NewReal()))
}

func TestApplySpamVerdicts_IgnoresForeignAuthResults_Postgres(t *testing.T) {
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
	testApplySpamVerdictsIgnoresForeignAuthResults(t, st)
}

func testApplySpamVerdictsIgnoresForeignAuthResults(t *testing.T, st store.Store) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	const forgedAuthResults = "attacker.example; dkim=pass header.d=example.test header.s=s1; " +
		"spf=pass smtp.mailfrom=sender@example.test; dmarc=pass header.from=example.test"

	for _, tc := range []struct {
		name   string
		source store.MessageIngestSource
	}{
		{"imap import", store.IngestSourceIMAPImport},
		{"unknown (empty)", store.IngestSourceUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email := "apply-foreign-auth-" + strings.ReplaceAll(tc.name, " ", "-") + "@example.test"
			p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
				Kind:           store.PrincipalKindUser,
				CanonicalEmail: email,
			})
			if err != nil {
				t.Fatalf("InsertPrincipal: %v", err)
			}
			inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
			if err != nil {
				t.Fatalf("InsertMailbox INBOX: %v", err)
			}
			if _, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Junk", Attributes: store.MailboxAttrJunk}); err != nil {
				t.Fatalf("InsertMailbox Junk: %v", err)
			}

			subject := "apply-foreign-auth-message-" + tc.name
			body := "Authentication-Results: " + forgedAuthResults + "\r\n" +
				"From: sender@example.test\r\nTo: " + email + "\r\nSubject: " + subject + "\r\n\r\nbody\r\n"
			blob, err := st.Blobs().Put(ctx, strings.NewReader(body))
			if err != nil {
				t.Fatalf("Blobs.Put: %v", err)
			}
			if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
				PrincipalID:  p.ID,
				InternalDate: time.Now(),
				ReceivedAt:   time.Now(),
				Size:         blob.Size,
				Blob:         blob,
				Envelope:     store.Envelope{Subject: subject},
				IngestSource: tc.source,
			}, []store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
				t.Fatalf("InsertMessage: %v", err)
			}
			mid := findSpamVerdictMessageBySubject(t, st, []store.PrincipalID{p.ID}, subject)

			csvBody := "id,verdict,confidence,reason\n" + itoaMsg(mid) + ",ham,90,looks fine\n"
			rows, err := parseSpamVerdictsCSV(strings.NewReader(csvBody))
			if err != nil {
				t.Fatalf("parseSpamVerdictsCSV: %v", err)
			}
			sum, err := applySpamVerdicts(ctx, st, clk, p.ID, rows, spamApplyVerdictsOptions{Engine: "batch:test"})
			if err != nil {
				t.Fatalf("applySpamVerdicts: %v", err)
			}
			if sum.Applied != 1 {
				t.Fatalf("summary = %+v, want Applied=1", sum)
			}

			rec, err := st.Meta().GetLLMClassification(ctx, mid)
			if err != nil {
				t.Fatalf("GetLLMClassification: %v", err)
			}
			if rec.SpamPromptApplied == nil {
				t.Fatal("SpamPromptApplied is nil, want the recorded spam.Request JSON")
			}
			var req spam.Request
			if err := json.Unmarshal([]byte(*rec.SpamPromptApplied), &req); err != nil {
				t.Fatalf("unmarshal SpamPromptApplied: %v", err)
			}
			if req.SPF != "none" || req.DKIM != "none" || req.DMARC != "none" {
				t.Fatalf("auth verdicts = spf=%q dkim=%q dmarc=%q, want none/none/none (ingest source %q must not trust its own Authentication-Results header)",
					req.SPF, req.DKIM, req.DMARC, tc.source)
			}
		})
	}
}
