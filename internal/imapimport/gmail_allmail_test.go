package imapimport

// gmail_allmail_test.go drives syncFolderGmailAllMailEnvelopeDedup directly
// against the in-process imapmemserver. The full Gmail detection path is not
// exercisable (the memserver advertises neither X-GM-EXT-1 nor the special
// [Gmail]/All Mail semantics), but the All-Mail envelope-dedup method itself
// only needs a mailbox literally named "[Gmail]/All Mail" plus ENVELOPE FETCH,
// both of which the memserver supports. This lets us cover the D5 fix: the
// envelope-dedup skip branch must record the real herold message/mailbox ids
// in imapimport_message_state so write-back can address archived mail.

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// TestGmailAllMailSkipRecordsRealIDs verifies that when [Gmail]/All Mail's
// envelope-dedup pass skips a message already mirrored by a label folder, the
// imapimport_message_state row it writes carries the REAL herold message id and
// mailbox id (not 0/0). REQ-IMAP-IMP-34 / D5.
func TestGmailAllMailSkipRecordsRealIDs(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("gm1", "pw")
	// Create the All Mail folder verbatim; the memserver treats it as an
	// ordinary mailbox, which is all syncFolderGmailAllMailEnvelopeDedup needs.
	if err := u.Create(gmailAllMail, nil); err != nil {
		t.Fatalf("Create %q: %v", gmailAllMail, err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "gm1@example.test",
		username:            "gm1",
		credentialPlaintext: "pw",
	}, nil)

	d := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	// The same message lives in INBOX (a label folder) and in All Mail.
	raw := buildRFC822("allmail-dedup@test", "All Mail Dedup", d)
	appendToServer(t, ts, "gm1", "pw", "INBOX", raw, nil, d)
	allMailUID := appendToServer(t, ts, "gm1", "pw", gmailAllMail, raw, nil, d)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &fakeDialer{ts: ts},
		categoriser: noopCategoriser{},
	})

	credPlaintext, err := w.openCredential(ctx, acc)
	if err != nil {
		t.Fatalf("openCredential: %v", err)
	}
	conn, err := w.opts.dialer.Dial(ctx, dialParams{
		AccountID:           acc.ID,
		Host:                acc.Host,
		Port:                acc.Port,
		TLSMode:             string(acc.TLSMode),
		Username:            acc.Username,
		AuthMethod:          string(acc.AuthMethod),
		CredentialPlaintext: credPlaintext,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		conn.Logout()
		conn.Close()
	}()

	// Mirror the message via the label folder (INBOX) first, so All Mail's
	// envelope dedup will find it already present.
	if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
		t.Fatalf("syncFolder INBOX: %v", err)
	}
	inboxMB := getMailboxID(t, ha.Store, acc.PrincipalID, "INBOX")
	if inboxMB == 0 {
		t.Fatal("INBOX mailbox not created")
	}
	mirrored, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "allmail-dedup@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}

	// Now run the All Mail envelope-dedup pass. The message is already mirrored
	// so it must be SKIPPED (no body fetch into Archive).
	if err := w.syncFolderGmailAllMailEnvelopeDedup(ctx, conn); err != nil {
		t.Fatalf("syncFolderGmailAllMailEnvelopeDedup: %v", err)
	}

	// Archive must be empty: the message was deduped, not body-fetched.
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Archive"); got != 0 {
		t.Errorf("Archive has %d messages; want 0 (message should be deduped, not re-fetched)", got)
	}

	// The message_state row for the All Mail UID must carry the REAL ids (D5).
	ms, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, gmailAllMail, uint32(allMailUID))
	if err != nil {
		t.Fatalf("GetIMAPImportMessageState: %v", err)
	}
	if !found {
		t.Fatal("no message_state recorded for the skipped All Mail UID")
	}
	if ms.HeroldMessageID != mirrored.ID {
		t.Errorf("message_state HeroldMessageID = %d; want %d (the mirrored message)", ms.HeroldMessageID, mirrored.ID)
	}
	if ms.HeroldMessageID == 0 {
		t.Error("message_state HeroldMessageID is 0; write-back cannot address this UID (D5 regression)")
	}
	if ms.HeroldMailboxID != inboxMB {
		t.Errorf("message_state HeroldMailboxID = %d; want %d", ms.HeroldMailboxID, inboxMB)
	}
}
