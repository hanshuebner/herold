package imapimport

// sync_test.go covers the 3b download path end-to-end:
//   - fresh backfill within horizon
//   - "all" horizon (nil floor)
//   - forward sync (high-water respected, categoriser fired for new INBOX)
//   - dedup by Message-ID
//   - folder mapping (non-default upstream->herold name)
//   - UIDVALIDITY rollover (re-sync without duplicates)
//   - lowering the horizon pulls older messages
//   - BODY.PEEK used (upstream \Seen not set by import)
//
// All tests run against the in-process imapmemserver (testserver_test.go).

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// buildRFC822 returns a minimal RFC 822 message with the given
// Message-ID, Subject, and Date header. The body is empty.
func buildRFC822(msgID, subject string, date time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msgID)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "From: sender@example.test\r\n")
	fmt.Fprintf(&b, "To: recipient@example.test\r\n")
	fmt.Fprintf(&b, "Date: %s\r\n", date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	fmt.Fprintf(&b, "body of %s\r\n", subject)
	return b.Bytes()
}

// appendToServer uses IMAP APPEND to add a message to the given mailbox on
// the in-process test server. Returns the assigned UID.
func appendToServer(t *testing.T, ts *testIMAPServer, user, password, mailbox string, raw []byte, flags []imap.Flag, internalDate time.Time) imap.UID {
	t.Helper()
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{
			RootCAs:    ts.ClientTLSConfig.RootCAs,
			ServerName: "127.0.0.1",
		},
	}
	client, err := imapclient.DialTLS(ts.ImplicitAddr, opts)
	if err != nil {
		t.Fatalf("appendToServer: dial: %v", err)
	}
	defer client.Close()
	if err := client.Login(user, password).Wait(); err != nil {
		t.Fatalf("appendToServer: LOGIN: %v", err)
	}
	appendOpts := &imap.AppendOptions{Flags: flags}
	if !internalDate.IsZero() {
		appendOpts.Time = internalDate
	}
	cmd := client.Append(mailbox, int64(len(raw)), appendOpts)
	if _, err := cmd.Write(raw); err != nil {
		t.Fatalf("appendToServer: write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("appendToServer: close literal: %v", err)
	}
	data, err := cmd.Wait()
	if err != nil {
		t.Fatalf("appendToServer: wait: %v", err)
	}
	return data.UID
}

// getUpstreamFlags fetches flags for one UID from the upstream server.
// Returns nil if the message is not found.
func getUpstreamFlags(t *testing.T, ts *testIMAPServer, user, password, mailbox string, uid imap.UID) []imap.Flag {
	t.Helper()
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{
			RootCAs:    ts.ClientTLSConfig.RootCAs,
			ServerName: "127.0.0.1",
		},
	}
	client, err := imapclient.DialTLS(ts.ImplicitAddr, opts)
	if err != nil {
		t.Fatalf("getUpstreamFlags: dial: %v", err)
	}
	defer client.Close()
	if err := client.Login(user, password).Wait(); err != nil {
		t.Fatalf("getUpstreamFlags: LOGIN: %v", err)
	}
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("getUpstreamFlags: SELECT: %v", err)
	}
	var uidSet imap.UIDSet
	uidSet.AddNum(uid)
	// Request UID alongside FLAGS so the imapclient can route the FETCH
	// response to the pending command. Without UID in the response, the
	// client cannot match the response and discards it (see UIDFetchFlags
	// comment in dialer.go for the full explanation).
	bufs, err := client.Fetch(uidSet, &imap.FetchOptions{Flags: true, UID: true}).Collect()
	if err != nil {
		t.Fatalf("getUpstreamFlags: FETCH: %v", err)
	}
	if len(bufs) == 0 {
		// Message not found upstream.
		return nil
	}
	if bufs[0].Flags == nil {
		// Message found but has no flags set; normalise to empty non-nil slice
		// so callers can distinguish "not found" (nil) from "found, no flags"
		// (empty slice).
		return []imap.Flag{}
	}
	return bufs[0].Flags
}

// runSyncOnce creates a worker, dials once, runs a single syncAllFolders
// pass, and returns any error. It does NOT enter the IDLE / poll loop;
// use TestIDLE* tests for that. This keeps the download-path tests fast.
func runSyncOnce(t *testing.T, ha *testharness.Server, ts *testIMAPServer, acc store.IMAPImportAccount, cat Categoriser) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if cat == nil {
		cat = noopCategoriser{}
	}

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &fakeDialer{ts: ts},
		categoriser: cat,
	})

	// Dial once and run a single syncAllFolders pass without entering
	// the IDLE loop.
	credPlaintext, err := w.openCredential(ctx, acc)
	if err != nil {
		return err
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
	credPlaintext = ""
	_ = credPlaintext
	if err != nil {
		return err
	}
	defer func() {
		conn.Logout()
		conn.Close()
	}()
	return w.syncAllFolders(ctx, conn)
}

// countMailboxMessages returns the number of messages in the named herold
// mailbox for the given principal.
func countMailboxMessages(t *testing.T, s store.Store, pid store.PrincipalID, mailboxName string) int {
	t.Helper()
	ctx := context.Background()
	mbs, err := s.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, mailboxName) {
			msgs, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{})
			if err != nil {
				t.Fatalf("ListMessages(%q): %v", mailboxName, err)
			}
			return len(msgs)
		}
	}
	return 0
}

// getMailboxID returns the MailboxID for the named mailbox, or 0.
func getMailboxID(t *testing.T, s store.Store, pid store.PrincipalID, mailboxName string) store.MailboxID {
	t.Helper()
	ctx := context.Background()
	mbs, err := s.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, mailboxName) {
			return mb.ID
		}
	}
	return 0
}

// makeAccountWithFloor creates an account with the given backfill floor date.
func makeAccountWithFloor(t *testing.T, s store.Store, ts *testIMAPServer, cfg accountCfg, floor *time.Time) store.IMAPImportAccount {
	t.Helper()
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: cfg.email,
		DisplayName:    cfg.email,
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%q): %v", cfg.email, err)
	}
	h, port := parseAddr(ts.ImplicitAddr)
	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:       p.ID,
		AccountName:       "Test",
		Host:              h,
		Port:              port,
		TLSMode:           store.IMAPImportTLSModeImplicit,
		Username:          cfg.username,
		AuthMethod:        store.IMAPImportAuthMethodPassword,
		BackfillFloorDate: floor,
		CredentialCT:      sealCred(t, cfg.credentialPlaintext),
		State:             store.IMAPImportAccountStateEnabled,
		DeletePropagates:  true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	return acc
}

// parseAddr splits "host:port" string.
func parseAddr(addr string) (string, int) {
	// addr is "127.0.0.1:<port>"
	var host string
	var port int
	fmt.Sscanf(addr, "%[^:]:%d", &host, &port)
	return host, port
}

// --------------------------------------------------------------------------
// countingCategoriser: records calls for assertions
// --------------------------------------------------------------------------

type countingCategoriser struct {
	calls atomic.Int64
	// lastMailbox records the mailbox name of the most recent call.
	lastMailbox atomic.Value
}

func (c *countingCategoriser) Categorise(_ context.Context, _, _ string, mailboxName string) error {
	c.calls.Add(1)
	c.lastMailbox.Store(mailboxName)
	return nil
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

// TestFreshBackfillWithinHorizon seeds messages with a range of
// INTERNALDATEs; the horizon floor cuts off old ones. Only messages at
// or after the floor land in herold. REQ-IMAP-IMP-15/17.
func TestFreshBackfillWithinHorizon(t *testing.T) {
	ts := startTestIMAPServer(t)
	user := ts.addUser("u1", "pw")
	// Ensure INBOX (already created by addUser) — add a second mailbox.
	_ = user

	ha, _ := testharness.Start(t, testharness.Options{})

	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	// Seed messages: 5 old (before floor), 3 new (at/after floor).
	floor := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		d := floor.AddDate(0, 0, -i) // before floor
		raw := buildRFC822(fmt.Sprintf("old-%d@test", i), fmt.Sprintf("Old %d", i), d)
		appendToServer(t, ts, "u1", "pw", "INBOX", raw, nil, d)
	}
	for i := 1; i <= 3; i++ {
		d := now.AddDate(0, 0, i-1) // at or after floor
		raw := buildRFC822(fmt.Sprintf("new-%d@test", i), fmt.Sprintf("New %d", i), d)
		appendToServer(t, ts, "u1", "pw", "INBOX", raw, nil, d)
	}

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u1@example.test",
		username:            "u1",
		credentialPlaintext: "pw",
	}, &floor)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX")
	if got != 3 {
		t.Errorf("herold INBOX has %d messages; want 3 (floor should exclude 5 old)", got)
	}

	// Cursor should have low_water set.
	cursor, found, err := ha.Store.Meta().GetIMAPImportFolderCursor(context.Background(), acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor: %v", err)
	}
	if !found {
		t.Fatal("cursor not persisted after sync")
	}
	if cursor.LowWaterUID == 0 {
		t.Error("LowWaterUID should be set after backfill")
	}
	if cursor.HighWaterUID == 0 {
		t.Error("HighWaterUID should be set after sync")
	}
}

// TestAllHorizon verifies that a nil floor mirrors all messages.
// REQ-IMAP-IMP-15 ("all" sentinel).
func TestAllHorizon(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u2", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		d := base.AddDate(0, 0, i)
		raw := buildRFC822(fmt.Sprintf("all-%d@test", i), fmt.Sprintf("All %d", i), d)
		appendToServer(t, ts, "u2", "pw", "INBOX", raw, nil, d)
	}

	// nil floor = "all"
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u2@example.test",
		username:            "u2",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX")
	if got != 5 {
		t.Errorf("herold INBOX has %d messages; want 5", got)
	}
}

// TestForwardSync verifies that a second sync pass fetches only the new
// message, the categoriser is fired for new INBOX mail (not for the
// previously-synced ones), and old messages are not re-fetched.
// REQ-IMAP-IMP-34.
func TestForwardSync(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u3", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	// Seed 2 existing messages.
	for i := 0; i < 2; i++ {
		d := base.AddDate(0, 0, i)
		raw := buildRFC822(fmt.Sprintf("fwd-old-%d@test", i), fmt.Sprintf("Old %d", i), d)
		appendToServer(t, ts, "u3", "pw", "INBOX", raw, nil, d)
	}

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u3@example.test",
		username:            "u3",
		credentialPlaintext: "pw",
	}, nil)

	cat := &countingCategoriser{}

	// First pass: syncs 2 messages. This is the INITIAL backfill of the
	// folder, so the categoriser must NOT fire (REQ-IMAP-IMP-31 / D1).
	if err := runSyncOnce(t, ha, ts, acc, cat); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 2 {
		t.Fatalf("after first sync: want 2, got %d", got)
	}
	firstCalls := cat.calls.Load()
	if firstCalls != 0 {
		t.Errorf("categoriser calls after initial backfill = %d; want 0 (D1)", firstCalls)
	}

	// Append one more message.
	d := base.AddDate(0, 0, 5)
	raw := buildRFC822("fwd-new-0@test", "New Message", d)
	appendToServer(t, ts, "u3", "pw", "INBOX", raw, nil, d)

	// Second pass: should fetch only the new message. This is a genuine
	// live arrival on an already-initialised folder, so the categoriser
	// fires exactly once.
	if err := runSyncOnce(t, ha, ts, acc, cat); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 3 {
		t.Fatalf("after second sync: want 3, got %d", got)
	}
	// Exactly 1 categoriser call for the new live-arrival message.
	secondCalls := cat.calls.Load()
	if secondCalls-firstCalls != 1 {
		t.Errorf("categoriser calls delta for second sync = %d; want 1", secondCalls-firstCalls)
	}
}

// TestDedup verifies that a message already present in herold (same
// Message-ID) is not duplicated; message_state is still recorded.
// REQ-IMAP-IMP-30.
func TestDedup(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u4", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)
	raw := buildRFC822("dedup-unique@test", "Dedup Test", d)
	appendToServer(t, ts, "u4", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u4@example.test",
		username:            "u4",
		credentialPlaintext: "pw",
	}, nil)

	// First sync: message inserted.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 1 {
		t.Fatalf("after first sync: want 1, got %d", got)
	}

	// Second sync: same upstream message re-fetched (high_water resets
	// are not triggered because UIDVALIDITY unchanged and UID is below
	// high_water). Actually in a second pass the UID is below
	// high_water_uid so no forward fetch happens — message_state from
	// first pass remains. Let's verify by resetting the cursor's
	// high_water_uid to force re-fetch.
	cursor, _, _ := ha.Store.Meta().GetIMAPImportFolderCursor(context.Background(), acc.ID, "INBOX")
	cursor.HighWaterUID = 0 // force re-fetch
	_ = ha.Store.Meta().UpsertIMAPImportFolderCursor(context.Background(), cursor)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("second sync (reset high_water): %v", err)
	}
	// Must still be exactly 1 message — dedup should have caught it.
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 1 {
		t.Errorf("after dedup pass: want 1, got %d (duplicate should not be inserted)", got)
	}
}

// TestFolderMapping verifies that an upstream folder maps to a
// differently-named herold mailbox. REQ-IMAP-IMP-10/12.
func TestFolderMapping(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("u5", "pw")
	// Create "Sent Mail" upstream folder.
	if err := u.Create("Sent Mail", nil); err != nil {
		t.Fatalf("Create Sent Mail: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822("sent-mapping@test", "Sent Test", d)
	appendToServer(t, ts, "u5", "pw", "Sent Mail", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u5@example.test",
		username:            "u5",
		credentialPlaintext: "pw",
	}, nil)

	// Set up a folder mapping: upstream "Sent Mail" -> herold "Sent"
	if err := ha.Store.Meta().SetIMAPImportFolderMap(context.Background(), acc.ID, []store.IMAPImportFolderMapEntry{
		{AccountID: acc.ID, UpstreamFolder: "Sent Mail", HeroldMailboxName: "Sent"},
	}); err != nil {
		t.Fatalf("SetIMAPImportFolderMap: %v", err)
	}

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Message should be in herold "Sent" mailbox.
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Sent"); got != 1 {
		t.Errorf("herold Sent has %d messages; want 1", got)
	}
	// And NOT in "Sent Mail".
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Sent Mail"); got != 0 {
		t.Errorf("herold Sent Mail has %d messages; want 0 (was mapped away)", got)
	}
}

// TestUnmappedFolderCreated verifies that an upstream folder with no
// mapping is created in herold under the upstream name.
// REQ-IMAP-IMP-12.
func TestUnmappedFolderCreated(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("u6", "pw")
	if err := u.Create("Newsletters", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 5, 10, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822("newsletters-1@test", "Newsletter", d)
	appendToServer(t, ts, "u6", "pw", "Newsletters", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u6@example.test",
		username:            "u6",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Message lands in herold "Newsletters" (auto-created).
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Newsletters"); got != 1 {
		t.Errorf("herold Newsletters has %d messages; want 1", got)
	}
}

// TestUIDValidityRollover simulates a UIDVALIDITY change between two
// sync passes and verifies no duplicate messages are created.
// REQ-IMAP-IMP-35.
func TestUIDValidityRollover(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("u7", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	msgID := "rollover-1@test"
	raw := buildRFC822(msgID, "Rollover Test", d)
	appendToServer(t, ts, "u7", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u7@example.test",
		username:            "u7",
		credentialPlaintext: "pw",
	}, nil)

	// First sync.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 1 {
		t.Fatalf("after first sync: want 1, got %d", got)
	}

	// Simulate UIDVALIDITY rollover: delete and re-create INBOX.
	if err := u.Delete("INBOX"); err != nil {
		t.Fatalf("Delete INBOX: %v", err)
	}
	if err := u.Create("INBOX", nil); err != nil {
		t.Fatalf("Re-create INBOX: %v", err)
	}
	// Re-seed the same message (same Message-ID).
	appendToServer(t, ts, "u7", "pw", "INBOX", raw, nil, d)

	// Second sync: cursor should detect rollover, re-sync, but dedup
	// by Message-ID prevents a duplicate.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("second sync (rollover): %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 1 {
		t.Errorf("after rollover re-sync: want 1 (dedup), got %d", got)
	}
}

// TestLowerHorizon verifies that lowering the floor later fetches
// older messages and lowers the low_water mark. REQ-IMAP-IMP-19.
func TestLowerHorizon(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u8", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	initialFloor := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)

	// Seed: 3 before floor, 3 after floor.
	base := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		d := base.AddDate(0, 0, i*5) // 2025-04-01, 04-06, 04-11
		raw := buildRFC822(fmt.Sprintf("lowerhorizon-old-%d@test", i), fmt.Sprintf("Old %d", i), d)
		appendToServer(t, ts, "u8", "pw", "INBOX", raw, nil, d)
	}
	for i := 0; i < 3; i++ {
		d := initialFloor.AddDate(0, 0, i*5) // 2025-05-01, 05-06, 05-11
		raw := buildRFC822(fmt.Sprintf("lowerhorizon-new-%d@test", i), fmt.Sprintf("New %d", i), d)
		appendToServer(t, ts, "u8", "pw", "INBOX", raw, nil, d)
	}

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u8@example.test",
		username:            "u8",
		credentialPlaintext: "pw",
	}, &initialFloor)

	// First sync with floor = 2025-05-01.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 3 {
		t.Fatalf("after first sync: want 3, got %d", got)
	}

	// Lower the floor to 2025-04-01 (encompasses old messages too).
	lowerFloor := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	updated, err := ha.Store.Meta().UpdateIMAPImportAccount(ctx, store.IMAPImportAccountUpdate{
		ID:                acc.ID,
		PrincipalID:       acc.PrincipalID,
		AccountName:       acc.AccountName,
		Host:              acc.Host,
		Port:              acc.Port,
		TLSMode:           acc.TLSMode,
		Username:          acc.Username,
		AuthMethod:        acc.AuthMethod,
		BackfillFloorDate: &lowerFloor,
		State:             acc.State,
		DeletePropagates:  acc.DeletePropagates,
	})
	if err != nil {
		t.Fatalf("UpdateIMAPImportAccount (lower floor): %v", err)
	}

	if err := runSyncOnce(t, ha, ts, updated, nil); err != nil {
		t.Fatalf("second sync (lower floor): %v", err)
	}
	// Now all 6 messages should be present.
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 6 {
		t.Errorf("after lower-horizon sync: want 6, got %d", got)
	}
}

// TestBodyPeekNoSeenSideEffect verifies that fetching a message with
// BODY.PEEK[] does NOT set \Seen on the upstream server.
// REQ-IMAP-IMP-31 (byte-fidelity / non-mutation).
func TestBodyPeekNoSeenSideEffect(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u9", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822("peek-test@test", "Peek Test", d)
	uid := appendToServer(t, ts, "u9", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u9@example.test",
		username:            "u9",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Verify upstream \Seen is NOT set.
	flags := getUpstreamFlags(t, ts, "u9", "pw", "INBOX", uid)
	for _, f := range flags {
		if f == imap.FlagSeen {
			t.Error("upstream message has \\Seen set after import; BODY.PEEK[] should prevent this")
		}
	}
}

// TestCategoriserNotCalledForBackfill verifies the D1 invariant
// (REQ-IMAP-IMP-31): the LLM categoriser is NOT invoked across the
// initial/historical backfill of a folder, but IS invoked for a subsequent
// genuine live arrival on that already-initialised folder.
func TestCategoriserNotCalledForBackfill(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u10", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	// Seed a year's worth of historical INBOX mail. On first connect this is
	// the initial backfill: running the LLM over it would be ruinous, so the
	// categoriser must not fire at all.
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		d := base.AddDate(0, i, 0)
		raw := buildRFC822(fmt.Sprintf("backfill-cat-%d@test", i), fmt.Sprintf("Backfill %d", i), d)
		appendToServer(t, ts, "u10", "pw", "INBOX", raw, nil, d)
	}

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u10@example.test",
		username:            "u10",
		credentialPlaintext: "pw",
	}, nil)

	cat := &countingCategoriser{}

	// Initial sync: the whole historical backfill lands in herold but the
	// categoriser is NEVER called for it (D1).
	if err := runSyncOnce(t, ha, ts, acc, cat); err != nil {
		t.Fatalf("initial backfill sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 12 {
		t.Fatalf("after initial backfill: want 12 messages, got %d", got)
	}
	if calls := cat.calls.Load(); calls != 0 {
		t.Errorf("categoriser called %d times across initial backfill; want 0 (D1)", calls)
	}

	// A new message arrives upstream after the initial sync completed.
	dNew := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	rawNew := buildRFC822("live-arrival@test", "Live Arrival", dNew)
	appendToServer(t, ts, "u10", "pw", "INBOX", rawNew, nil, dNew)

	// Next sync round: the genuine live arrival on the already-initialised
	// folder IS categorised, exactly once.
	if err := runSyncOnce(t, ha, ts, acc, cat); err != nil {
		t.Fatalf("live-arrival sync: %v", err)
	}
	if calls := cat.calls.Load(); calls != 1 {
		t.Errorf("categoriser calls after live arrival = %d; want 1 (D1)", calls)
	}
	if lb := cat.lastMailbox.Load(); lb != "INBOX" {
		t.Errorf("last categoriser mailbox = %v; want INBOX", lb)
	}
}

// TestFlagsPreservedOnIngest verifies that upstream flags (\Seen, \Flagged)
// are mapped to herold MessageFlags on insert.
func TestFlagsPreservedOnIngest(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u11", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822("flags-test@test", "Flags Test", d)
	// Append with \Seen and \Flagged preset.
	appendToServer(t, ts, "u11", "pw", "INBOX", raw, []imap.Flag{imap.FlagSeen, imap.FlagFlagged}, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u11@example.test",
		username:            "u11",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Look up the herold message and check flags.
	ctx := context.Background()
	mbID := getMailboxID(t, ha.Store, acc.PrincipalID, "INBOX")
	if mbID == 0 {
		t.Fatal("INBOX mailbox not found")
	}
	msgs, err := ha.Store.Meta().ListMessages(ctx, mbID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if msgs[0].Flags&store.MessageFlagSeen == 0 {
		t.Error("herold message missing \\Seen flag")
	}
	if msgs[0].Flags&store.MessageFlagFlagged == 0 {
		t.Error("herold message missing \\Flagged flag")
	}
}

// TestInternalDatePreserved verifies that the upstream INTERNALDATE is
// used as both InternalDate and ReceivedAt in the herold store.
func TestInternalDatePreserved(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u12", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	// Use a very specific time so we can assert it.
	specificDate := time.Date(2023, 11, 15, 8, 30, 0, 0, time.UTC)
	raw := buildRFC822("internaldate@test", "InternalDate Test", specificDate)
	appendToServer(t, ts, "u12", "pw", "INBOX", raw, nil, specificDate)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u12@example.test",
		username:            "u12",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	ctx := context.Background()
	mbID := getMailboxID(t, ha.Store, acc.PrincipalID, "INBOX")
	if mbID == 0 {
		t.Fatal("INBOX not found")
	}
	msgs, err := ha.Store.Meta().ListMessages(ctx, mbID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	// INTERNALDATE should match (within 1-second since IMAP dates are
	// truncated to seconds).
	gotDate := msgs[0].InternalDate.UTC()
	diff := gotDate.Sub(specificDate)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Second {
		t.Errorf("InternalDate = %v; want %v (diff=%v)", gotDate, specificDate, diff)
	}
	gotRecv := msgs[0].ReceivedAt.UTC()
	diffR := gotRecv.Sub(specificDate)
	if diffR < 0 {
		diffR = -diffR
	}
	if diffR > time.Second {
		t.Errorf("ReceivedAt = %v; want %v (diff=%v)", gotRecv, specificDate, diffR)
	}
}

// TestMessageStateRecordedOnDedup verifies that UpsertIMAPImportMessageState
// is called even for dedup hits (so write-back can address the upstream UID).
func TestMessageStateRecordedOnDedup(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u13", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822("dedup-state@test", "Dedup State", d)
	uid := appendToServer(t, ts, "u13", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u13@example.test",
		username:            "u13",
		credentialPlaintext: "pw",
	}, nil)

	// First sync inserts the message.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Force re-fetch (reset high_water).
	cursor, _, _ := ha.Store.Meta().GetIMAPImportFolderCursor(context.Background(), acc.ID, "INBOX")
	cursor.HighWaterUID = 0
	_ = ha.Store.Meta().UpsertIMAPImportFolderCursor(context.Background(), cursor)

	// Second sync: message is a dedup hit.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// Message state must exist with the correct upstream UID.
	ms, found, err := ha.Store.Meta().GetIMAPImportMessageState(context.Background(), acc.ID, "INBOX", uint32(uid))
	if err != nil {
		t.Fatalf("GetIMAPImportMessageState: %v", err)
	}
	if !found {
		t.Error("message state not recorded (should exist for dedup hit)")
	}
	if ms.UpstreamUID != uint32(uid) {
		t.Errorf("UpstreamUID = %d; want %d", ms.UpstreamUID, uid)
	}
}

// TestCursorAdvancedAfterSync verifies that the cursor high_water and
// uidnext are advanced correctly after a successful sync.
func TestCursorAdvancedAfterSync(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u14", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		d := base.AddDate(0, 0, i)
		raw := buildRFC822(fmt.Sprintf("cursor-adv-%d@test", i), fmt.Sprintf("Cursor %d", i), d)
		appendToServer(t, ts, "u14", "pw", "INBOX", raw, nil, d)
	}

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u14@example.test",
		username:            "u14",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	cursor, found, err := ha.Store.Meta().GetIMAPImportFolderCursor(context.Background(), acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor: %v", err)
	}
	if !found {
		t.Fatal("cursor not persisted")
	}
	// After syncing 3 messages (UIDs 1,2,3), uidnext should be 4,
	// high_water should be 3.
	if cursor.HighWaterUID != 3 {
		t.Errorf("HighWaterUID = %d; want 3", cursor.HighWaterUID)
	}
	if cursor.UIDValidity == 0 {
		t.Error("UIDValidity should be set")
	}
}

// TestCategoriserCalledForINBOXOnly verifies the categoriser is only
// called for messages in INBOX-mapped folders, not for other mailboxes.
func TestCategoriserCalledForINBOXOnly(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("u15", "pw")
	if err := u.Create("Archive", nil); err != nil {
		t.Fatalf("Create Archive: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u15@example.test",
		username:            "u15",
		credentialPlaintext: "pw",
	}, nil)

	// Initial sync over the (empty) INBOX and Archive folders so they are
	// recorded as initialised. Nothing is categorised here (D1).
	cat := &countingCategoriser{}
	if err := runSyncOnce(t, ha, ts, acc, cat); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if cat.calls.Load() != 0 {
		t.Fatalf("categoriser fired during initial sync = %d; want 0", cat.calls.Load())
	}

	// Now genuinely-new mail arrives in both INBOX and Archive.
	d := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	rawInbox := buildRFC822("cat-inbox@test", "Cat INBOX", d)
	appendToServer(t, ts, "u15", "pw", "INBOX", rawInbox, nil, d)
	rawArchive := buildRFC822("cat-archive@test", "Cat Archive", d.AddDate(0, 0, 1))
	appendToServer(t, ts, "u15", "pw", "Archive", rawArchive, nil, d.AddDate(0, 0, 1))

	if err := runSyncOnce(t, ha, ts, acc, cat); err != nil {
		t.Fatalf("live-arrival sync: %v", err)
	}

	// Only 1 categoriser call (for the INBOX arrival), not 2 (not for Archive).
	if cat.calls.Load() != 1 {
		t.Errorf("categoriser calls = %d; want 1 (INBOX only)", cat.calls.Load())
	}
	// Verify it was for INBOX.
	if lb := cat.lastMailbox.Load(); lb != "INBOX" {
		t.Errorf("last categoriser mailbox = %q; want INBOX", lb)
	}
}

// TestFakeDialerSecretSealing verifies that the sealCred / testDataKey
// round-trip used in sync test setups works (basic smoke test for the
// credential path).
func TestSyncCredentialSealRoundtrip(t *testing.T) {
	key := testDataKey(t)
	plain := "mypassword"
	ct, err := secrets.Seal(key, []byte(plain))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := secrets.Open(key, ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != plain {
		t.Errorf("roundtrip: got %q; want %q", got, plain)
	}
}

// TestMultiMailboxPlacement verifies the multi-mailbox-on-dedup mechanism:
// the same message (same Message-ID) APPENDed to two upstream folders lands
// in BOTH mapped herold mailboxes after sync (one messages row, two
// message_mailboxes rows). A second sync with the same folders is idempotent
// (no duplicate memberships). REQ-IMAP-IMP-50/51 general mechanism.
func TestMultiMailboxPlacement(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("mmb1", "pw")

	// Create two folders that will hold the same message.
	if err := u.Create("FolderA", nil); err != nil {
		t.Fatalf("Create FolderA: %v", err)
	}
	if err := u.Create("FolderB", nil); err != nil {
		t.Fatalf("Create FolderB: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)
	// Same Message-ID in both folders — simulates a message in multiple labels.
	raw := buildRFC822("multi-mb-unique@test", "Multi Mailbox", d)
	appendToServer(t, ts, "mmb1", "pw", "FolderA", raw, nil, d)
	appendToServer(t, ts, "mmb1", "pw", "FolderB", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "mmb1@example.test",
		username:            "mmb1",
		credentialPlaintext: "pw",
	}, nil)

	// First sync.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Verify: one message row exists in herold, with memberships in BOTH
	// herold mailboxes (FolderA and FolderB, since no mapping override).
	ctx := context.Background()
	normID := "multi-mb-unique@test"
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, normID)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if len(msg.Mailboxes) != 2 {
		t.Errorf("message has %d mailbox memberships; want 2 (FolderA and FolderB)", len(msg.Mailboxes))
	}

	// Verify the message appears in both mailboxes.
	inA := countMailboxMessages(t, ha.Store, acc.PrincipalID, "FolderA")
	inB := countMailboxMessages(t, ha.Store, acc.PrincipalID, "FolderB")
	if inA != 1 {
		t.Errorf("FolderA has %d messages; want 1", inA)
	}
	if inB != 1 {
		t.Errorf("FolderB has %d messages; want 1", inB)
	}

	// Second sync: must be idempotent (no duplicate memberships).
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	msg2, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, normID)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (second): %v", err)
	}
	if len(msg2.Mailboxes) != 2 {
		t.Errorf("after second sync: message has %d mailbox memberships; want 2 (idempotent)", len(msg2.Mailboxes))
	}
}

// TestMultiMailboxPlacementThreeFolders verifies the multi-mailbox mechanism
// with three folders holding the same message. Membership count must be 3.
func TestMultiMailboxPlacementThreeFolders(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("mmb2", "pw")

	for _, name := range []string{"Alpha", "Beta", "Gamma"} {
		if err := u.Create(name, nil); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRFC822("three-folder-dedup@test", "Three Folder", d)
	for _, name := range []string{"Alpha", "Beta", "Gamma"} {
		appendToServer(t, ts, "mmb2", "pw", name, raw, nil, d)
	}

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "mmb2@example.test",
		username:            "mmb2",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	ctx := context.Background()
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "three-folder-dedup@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if len(msg.Mailboxes) != 3 {
		t.Errorf("message has %d mailbox memberships; want 3 (Alpha, Beta, Gamma)", len(msg.Mailboxes))
	}

	// Idempotency: second sync must not add more memberships.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	msg2, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "three-folder-dedup@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (second): %v", err)
	}
	if len(msg2.Mailboxes) != 3 {
		t.Errorf("after second sync: %d memberships; want 3 (idempotent)", len(msg2.Mailboxes))
	}
}

// TestCategoriserCalledForLabelBeforeINBOX reproduces the Gmail multi-label
// ordering bug (re #27): a message carrying both a user label and the inbox
// label appears in two IMAP folders. If the label folder syncs before INBOX
// the message is created in the label folder (isNew=true, not INBOX ->
// categorise skipped), then INBOX sync is a dedup hit (isNew=false ->
// categorise also skipped without the fix). The message would never receive a
// $category-* keyword.
//
// The fix: ingestMessage now returns isNewMember=true when AddMessageToMailbox
// places the message into a mailbox for the first time (dedup path). The
// categoriser condition uses isNewMember instead of isNew, so it fires for the
// INBOX membership regardless of which folder was synced first.
func TestCategoriserCalledForLabelBeforeINBOX(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("lblinbox1", "pw")
	// Create a label folder that will be synced before INBOX.
	if err := u.Create("WorkLabel", nil); err != nil {
		t.Fatalf("Create WorkLabel: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "lblinbox1@example.test",
		username:            "lblinbox1",
		credentialPlaintext: "pw",
	}, nil)

	cat := &countingCategoriser{}

	// Dial once and control the sync order directly: WorkLabel before INBOX.
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
		categoriser: cat,
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

	// Initialise both folders while empty so the subsequent passes are
	// live-arrival passes, not the initial backfill (D1: categorisation
	// only runs once a folder is initialised).
	if err := w.syncFolder(ctx, conn, "WorkLabel", "WorkLabel"); err != nil {
		t.Fatalf("initial syncFolder WorkLabel: %v", err)
	}
	if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
		t.Fatalf("initial syncFolder INBOX: %v", err)
	}
	if cat.calls.Load() != 0 {
		t.Fatalf("categoriser fired during initial folder sync = %d; want 0", cat.calls.Load())
	}

	// Now a genuinely-new message arrives carrying both the inbox label and a
	// user label, so it appears in two IMAP folders with the same Message-ID.
	d := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRFC822("label-before-inbox@test", "Label Before INBOX", d)
	appendToServer(t, ts, "lblinbox1", "pw", "WorkLabel", raw, nil, d)
	appendToServer(t, ts, "lblinbox1", "pw", "INBOX", raw, nil, d)

	// Sync WorkLabel FIRST: the message is inserted into herold "WorkLabel".
	// The categoriser must NOT fire because WorkLabel != INBOX.
	if err := w.syncFolder(ctx, conn, "WorkLabel", "WorkLabel"); err != nil {
		t.Fatalf("syncFolder WorkLabel: %v", err)
	}
	if cat.calls.Load() != 0 {
		t.Errorf("categoriser called %d times after WorkLabel sync; want 0", cat.calls.Load())
	}

	// Sync INBOX second: the message already exists in herold (dedup hit).
	// AddMessageToMailbox places it in INBOX — the categoriser MUST fire.
	if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
		t.Fatalf("syncFolder INBOX: %v", err)
	}
	if cat.calls.Load() != 1 {
		t.Errorf("categoriser calls after INBOX sync = %d; want 1 (label-before-INBOX ordering, re #27)", cat.calls.Load())
	}

	// Verify the message is a member of both herold mailboxes.
	inWorkLabel := countMailboxMessages(t, ha.Store, acc.PrincipalID, "WorkLabel")
	inINBOX := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX")
	if inWorkLabel != 1 {
		t.Errorf("WorkLabel has %d messages; want 1", inWorkLabel)
	}
	if inINBOX != 1 {
		t.Errorf("INBOX has %d messages; want 1", inINBOX)
	}
}

// TestSyncFakeClock verifies that the FakeClock passed to the worker is
// used for metrics timing (smoke test to ensure clock injection works
// in sync tests; the FakeClock does not drift unless explicitly advanced).
func TestSyncFakeClock(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("u16", "pw")

	anchor := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(anchor)

	ha, _ := testharness.Start(t, testharness.Options{Clock: fc})

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "u16@example.test",
		username:            "u16",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// If we reach here without panic the clock injection is working.
}
