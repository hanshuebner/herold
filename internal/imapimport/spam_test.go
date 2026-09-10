package imapimport

// spam_test.go covers the #300 spam-classification seam (REQ-FILT-02):
//   - a spam verdict on a genuinely-new INBOX-mapped live arrival routes the
//     message to Junk instead of INBOX, and records the verdict
//   - a suspect verdict keeps the message in INBOX with the "$Junk" keyword
//   - a ham verdict keeps the message in INBOX with no extra keyword
//   - a message the source already filed in its own Junk-attributed folder
//     is never classified
//   - the historical/initial backfill of an INBOX-mapped folder is never
//     classified, mirroring the LLM categoriser gate (REQ-IMAP-IMP-31 / D1)
//   - a classifier that reports Unclassified (the fake's stand-in for a
//     plugin timeout/error) degrades to INBOX without failing the import

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fakeSpamClassifier is a scripted imapimport.SpamClassifier: each Classify
// call consumes the next verdict off the queue (or repeats the last one once
// the queue is drained). RecordVerdict calls are counted and the last
// recorded classification is retained for assertions.
type fakeSpamClassifier struct {
	verdicts []spam.Classification

	classifyCalls atomic.Int64
	recordCalls   atomic.Int64

	mu               sync.Mutex
	lastRecordedVerd spam.Verdict
	lastRecordedPID  store.PrincipalID
	lastRecordedMID  store.MessageID
}

func (f *fakeSpamClassifier) Classify(context.Context, mailparse.Message) spam.Classification {
	i := f.classifyCalls.Add(1) - 1
	if len(f.verdicts) == 0 {
		return spam.Classification{Verdict: spam.Unclassified, Score: -1}
	}
	if int(i) >= len(f.verdicts) {
		return f.verdicts[len(f.verdicts)-1]
	}
	return f.verdicts[i]
}

// RecordVerdict mirrors the documented SpamClassifier contract: a no-op
// when classification.Verdict is spam.Unclassified (the real adapter,
// internal/admin/imap_import_spam.go, applies the same gate before writing
// the llm_classifications row).
func (f *fakeSpamClassifier) RecordVerdict(_ context.Context, principalID store.PrincipalID, messageID store.MessageID, _ mailparse.Message, classification spam.Classification) {
	if classification.Verdict == spam.Unclassified {
		return
	}
	f.recordCalls.Add(1)
	f.mu.Lock()
	f.lastRecordedVerd = classification.Verdict
	f.lastRecordedPID = principalID
	f.lastRecordedMID = messageID
	f.mu.Unlock()
}

// mustGetMailboxByName returns the mailbox owned by pid named name, failing
// the test if absent.
func mustGetMailboxByName(t *testing.T, s store.Store, pid store.PrincipalID, name string) store.Mailbox {
	t.Helper()
	mb, err := s.Meta().GetMailboxByName(context.Background(), pid, name)
	if err != nil {
		t.Fatalf("GetMailboxByName(%q): %v", name, err)
	}
	return mb
}

// keywordsForMailbox returns the per-mailbox-membership keyword set for msg
// in the mailbox mbID, or nil if msg is not a member of mbID.
func keywordsForMailbox(msg store.Message, mbID store.MailboxID) []string {
	for _, mm := range msg.Mailboxes {
		if mm.MailboxID == mbID {
			return mm.Keywords
		}
	}
	return nil
}

// hasKeyword reports whether ss contains s.
func hasKeyword(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// msgIsMemberOf reports whether msg has a membership row for mbID.
func msgIsMemberOf(msg store.Message, mbID store.MailboxID) bool {
	for _, mm := range msg.Mailboxes {
		if mm.MailboxID == mbID {
			return true
		}
	}
	return false
}

// TestSpamVerdictRoutesLiveInboxArrival exercises the three verdict outcomes
// (REQ-FILT-02) against a genuine live arrival (second sync pass) mapped to
// INBOX by the default folder mapping.
func TestSpamVerdictRoutesLiveInboxArrival(t *testing.T) {
	cases := []struct {
		name        string
		verdict     spam.Verdict
		wantMailbox string
		wantDisp    store.MessageDeliveryDisposition
		wantKeyword string
	}{
		{name: "ham", verdict: spam.Ham, wantMailbox: "INBOX", wantDisp: store.DeliveryDispositionInbox},
		{name: "suspect", verdict: spam.Suspect, wantMailbox: "INBOX", wantDisp: store.DeliveryDispositionInbox, wantKeyword: "$Junk"},
		{name: "spam", verdict: spam.Spam, wantMailbox: "Junk", wantDisp: store.DeliveryDispositionJunk},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			user := "spam-" + tc.name
			ts.addUser(user, "pw")
			ha, _ := testharness.Start(t, testharness.Options{})

			base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
			// Seed one old message so the first sync pass is a genuine
			// initial backfill (categorise=false) and the second pass is a
			// genuine forward/live arrival (categorise=true), matching
			// TestForwardSync's pattern.
			oldRaw := buildRFC822(fmt.Sprintf("spam-old-%s@test", tc.name), "Old", base)
			appendToServer(t, ts, user, "pw", "INBOX", oldRaw, nil, base)

			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               user + "@example.test",
				username:            user,
				credentialPlaintext: "pw",
			}, nil)

			fc := &fakeSpamClassifier{}
			if err := runSyncOnceSpam(t, ha, ts, acc, nil, fc); err != nil {
				t.Fatalf("first (backfill) sync: %v", err)
			}
			if fc.classifyCalls.Load() != 0 {
				t.Errorf("classifier called %d times during initial backfill; want 0 (D1)", fc.classifyCalls.Load())
			}

			// Live arrival: append the message under test, then sync again.
			d := base.AddDate(0, 0, 5)
			msgIDHeader := "spam-new-" + tc.name + "@test"
			raw := buildRFC822(msgIDHeader, "New "+tc.name, d)
			appendToServer(t, ts, user, "pw", "INBOX", raw, nil, d)

			fc.verdicts = []spam.Classification{{Verdict: tc.verdict, Score: 0.5}}
			if err := runSyncOnceSpam(t, ha, ts, acc, nil, fc); err != nil {
				t.Fatalf("second (live) sync: %v", err)
			}
			if fc.classifyCalls.Load() != 1 {
				t.Fatalf("classifier called %d times for the live arrival; want 1", fc.classifyCalls.Load())
			}

			ctx := context.Background()
			msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, msgIDHeader)
			if err != nil {
				t.Fatalf("GetMessageByMessageIDHeader: %v", err)
			}

			wantMB := mustGetMailboxByName(t, ha.Store, acc.PrincipalID, tc.wantMailbox)
			if !msgIsMemberOf(msg, wantMB.ID) {
				t.Errorf("message not a member of %s (mailboxes=%v)", tc.wantMailbox, msg.Mailboxes)
			}
			if tc.wantKeyword != "" {
				kws := keywordsForMailbox(msg, wantMB.ID)
				if !hasKeyword(kws, tc.wantKeyword) {
					t.Errorf("keywords in %s = %v; want %q present", tc.wantMailbox, kws, tc.wantKeyword)
				}
			}

			// Disposition: message-research read path (re #143).
			hits, err := ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{MessageID: msgIDHeader, Limit: 10})
			if err != nil {
				t.Fatalf("SearchAdminMessages: %v", err)
			}
			if len(hits) != 1 {
				t.Fatalf("SearchAdminMessages(%q) hits = %d, want 1", msgIDHeader, len(hits))
			}
			if hits[0].Disposition != tc.wantDisp {
				t.Errorf("Disposition = %v, want %v", hits[0].Disposition, tc.wantDisp)
			}

			// RecordVerdict was called once with the message's real id.
			if fc.recordCalls.Load() != 1 {
				t.Errorf("RecordVerdict called %d times; want 1", fc.recordCalls.Load())
			}
			fc.mu.Lock()
			gotVerd, gotPID, gotMID := fc.lastRecordedVerd, fc.lastRecordedPID, fc.lastRecordedMID
			fc.mu.Unlock()
			if gotVerd != tc.verdict {
				t.Errorf("recorded verdict = %v, want %v", gotVerd, tc.verdict)
			}
			if gotPID != acc.PrincipalID {
				t.Errorf("recorded principal = %v, want %v", gotPID, acc.PrincipalID)
			}
			if gotMID != msg.ID {
				t.Errorf("recorded message id = %v, want %v", gotMID, msg.ID)
			}
		})
	}
}

// TestSpamNotClassifiedForSourceJunkFolder verifies that a message the
// upstream places in a folder mapped to a Junk-attributed herold mailbox is
// never routed through the classifier (REQ-FILT-02 / #300: "not already
// filed in the account's own Junk folder by the source server").
func TestSpamNotClassifiedForSourceJunkFolder(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("spamjunk", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})

	if err := u.Create("Junk", nil); err != nil {
		t.Fatalf("create Junk folder upstream: %v", err)
	}

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	oldRaw := buildRFC822("junk-old@test", "Old", base)
	appendToServer(t, ts, "spamjunk", "pw", "Junk", oldRaw, nil, base)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "spamjunk@example.test",
		username:            "spamjunk",
		credentialPlaintext: "pw",
	}, nil)

	fc := &fakeSpamClassifier{verdicts: []spam.Classification{{Verdict: spam.Spam, Score: 0.9}}}
	if err := runSyncOnceSpam(t, ha, ts, acc, nil, fc); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Live arrival in the Junk-mapped folder.
	d := base.AddDate(0, 0, 5)
	raw := buildRFC822("junk-new@test", "New", d)
	appendToServer(t, ts, "spamjunk", "pw", "Junk", raw, nil, d)
	if err := runSyncOnceSpam(t, ha, ts, acc, nil, fc); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if fc.classifyCalls.Load() != 0 {
		t.Errorf("classifier called %d times for a Junk-folder message; want 0", fc.classifyCalls.Load())
	}

	ctx := context.Background()
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "junk-new@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	junkMB := mustGetMailboxByName(t, ha.Store, acc.PrincipalID, "Junk")
	if !msgIsMemberOf(msg, junkMB.ID) {
		t.Errorf("message not filed in Junk (mailboxes=%v)", msg.Mailboxes)
	}
}

// TestSpamClassifierUnclassifiedDegradesToInbox verifies that a classifier
// reporting Unclassified -- the outcome an adapter maps a plugin
// timeout/error to (internal/admin/imap_import_spam.go Classify) -- never
// fails the import and leaves the message in INBOX with no
// llm_classifications row (REQ-FILT-66 only records verdicts a classifier
// actually reached).
func TestSpamClassifierUnclassifiedDegradesToInbox(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("spamerr", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	oldRaw := buildRFC822("err-old@test", "Old", base)
	appendToServer(t, ts, "spamerr", "pw", "INBOX", oldRaw, nil, base)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "spamerr@example.test",
		username:            "spamerr",
		credentialPlaintext: "pw",
	}, nil)

	fc := &fakeSpamClassifier{}
	if err := runSyncOnceSpam(t, ha, ts, acc, nil, fc); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	d := base.AddDate(0, 0, 5)
	raw := buildRFC822("err-new@test", "New", d)
	appendToServer(t, ts, "spamerr", "pw", "INBOX", raw, nil, d)
	// Degraded classifier: reports Unclassified (score -1), the same shape
	// spam.Classification{Verdict: Unclassified, Score: -1} that
	// spam.Classifier.Classify returns on a plugin timeout/error.
	fc.verdicts = []spam.Classification{{Verdict: spam.Unclassified, Score: -1}}
	if err := runSyncOnceSpam(t, ha, ts, acc, nil, fc); err != nil {
		t.Fatalf("second (live) sync: %v", err)
	}

	ctx := context.Background()
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "err-new@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	inboxMB := mustGetMailboxByName(t, ha.Store, acc.PrincipalID, "INBOX")
	if !msgIsMemberOf(msg, inboxMB.ID) {
		t.Errorf("degraded-classifier message not in INBOX (mailboxes=%v)", msg.Mailboxes)
	}

	if fc.recordCalls.Load() != 0 {
		t.Errorf("RecordVerdict called %d times for an Unclassified verdict; want 0", fc.recordCalls.Load())
	}
	if _, err := ha.Store.Meta().GetLLMClassification(ctx, msg.ID); err == nil {
		t.Errorf("GetLLMClassification succeeded for an unclassified message; want not-found")
	}
}
