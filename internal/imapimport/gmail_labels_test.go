package imapimport

// gmail_labels_test.go covers the X-GM-LABELS per-message Gmail label placement
// path (REQ-IMAP-IMP-53):
//
//   - gmailLabelSetToMailboxNames: label-set -> herold-mailbox mapping (pure).
//   - syncAllFoldersGmailLabels: end-to-end placement via a fake Conn that
//     returns canned labelled messages (a message with K placement labels lands
//     in K herold mailboxes; unlabelled mail lands in Archive). The in-process
//     imapmemserver does not implement X-GM-EXT-1 / X-GM-LABELS, so the wire is
//     faked at the Conn boundary.
//   - prodConn.UIDFetchWithLabels: the real client wrapper parsing an
//     X-GM-LABELS FETCH response from a scripted IMAP server, exercising the
//     patched go-imap fork through herold's production Conn.

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// --------------------------------------------------------------------------
// gmailLabelSetToMailboxNames (pure mapping)
// --------------------------------------------------------------------------

func TestGmailLabelSetToMailboxNames(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   []string
	}{
		{"inbox", []string{`\Inbox`}, []string{"INBOX"}},
		{"sent", []string{`\Sent`}, []string{"Sent"}},
		{"draft", []string{`\Draft`}, []string{"Drafts"}},
		{"spam", []string{`\Spam`}, []string{"Junk"}},
		{"trash", []string{`\Trash`}, []string{"Trash"}},
		{"user label", []string{"Travel"}, []string{"Travel"}},
		{"user label with slash", []string{"Receipts/2024"}, []string{"Receipts/2024"}},
		{"inbox + user", []string{`\Inbox`, "Travel"}, []string{"INBOX", "Travel"}},
		{"important+starred only -> none", []string{`\Important`, `\Starred`}, nil},
		{"inbox + important + starred -> inbox", []string{`\Inbox`, `\Important`, `\Starred`}, []string{"INBOX"}},
		{"muted/chats dropped", []string{`\Muted`, `\Chats`, "Work"}, []string{"Work"}},
		{"unknown system label dropped", []string{`\Category_Personal`, "Keep"}, []string{"Keep"}},
		{"empty set", nil, nil},
		{"case-insensitive system labels", []string{`\INBOX`, `\sent`}, []string{"INBOX", "Sent"}},
		{"dedup", []string{`\Inbox`, "Travel", "Travel", `\Inbox`}, []string{"INBOX", "Travel"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := gmailLabelSetToMailboxNames(tc.labels, nil, nil)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d]=%q, want %q (full got=%q)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestGmailLabelSetToMailboxNames_FolderMapOverride(t *testing.T) {
	// Per-account override on the Gmail system folder-equivalent for \Sent.
	userMapping := map[string]string{"[Gmail]/Sent Mail": "Outbox"}
	got := gmailLabelSetToMailboxNames([]string{`\Sent`, "Personal"}, userMapping, nil)
	want := []string{"Outbox", "Personal"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// --------------------------------------------------------------------------
// End-to-end placement via a fake Conn
// --------------------------------------------------------------------------

// labeledMsg is one canned message the fakeLabelsConn serves.
type labeledMsg struct {
	uid    imap.UID
	raw    []byte
	labels []string
}

// fakeLabelsConn is a Conn that serves a fixed [Gmail]/All Mail with
// X-GM-LABELS, so the X-GM-LABELS placement path can be exercised without a
// real Gmail connection (the imapmemserver does not implement the extension).
type fakeLabelsConn struct {
	msgs    []labeledMsg
	uidNext imap.UID
}

func (c *fakeLabelsConn) Caps() imap.CapSet {
	return imap.CapSet{capGmailExt: {}, imap.CapIMAP4rev2: {}}
}
func (c *fakeLabelsConn) Logout() error { return nil }
func (c *fakeLabelsConn) Close() error  { return nil }
func (c *fakeLabelsConn) List(context.Context) ([]folderInfo, error) {
	return []folderInfo{{Name: gmailAllMail}}, nil
}
func (c *fakeLabelsConn) Select(context.Context, string) (selectInfo, error) {
	return selectInfo{UIDValidity: 1, UIDNext: c.uidNext, NumMessages: uint32(len(c.msgs))}, nil
}
func (c *fakeLabelsConn) SelectReadWrite(context.Context, string) (selectInfo, error) {
	return c.Select(context.Background(), "")
}
func (c *fakeLabelsConn) UIDSearchSince(context.Context, time.Time) ([]imap.UID, error) {
	out := make([]imap.UID, 0, len(c.msgs))
	for _, m := range c.msgs {
		out = append(out, m.uid)
	}
	return out, nil
}
func (c *fakeLabelsConn) UIDFetch(context.Context, []imap.UID) ([]fetchedMessage, error) {
	return nil, nil
}
func (c *fakeLabelsConn) UIDFetchWithLabels(_ context.Context, uids []imap.UID) ([]fetchedMessageWithLabels, error) {
	want := make(map[imap.UID]bool, len(uids))
	for _, u := range uids {
		want[u] = true
	}
	var out []fetchedMessageWithLabels
	for _, m := range c.msgs {
		if !want[m.uid] {
			continue
		}
		out = append(out, fetchedMessageWithLabels{
			fetchedMessage: fetchedMessage{
				UID:          m.uid,
				InternalDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				RFC822:       m.raw,
			},
			Labels: m.labels,
		})
	}
	return out, nil
}
func (c *fakeLabelsConn) UIDFetchEnvelope(context.Context, []imap.UID) ([]envelopeFetchResult, error) {
	return nil, nil
}
func (c *fakeLabelsConn) UIDFetchFlags(context.Context, imap.UID) ([]imap.Flag, error) {
	return nil, nil
}
func (c *fakeLabelsConn) UIDFetchFlagsMulti(context.Context, []imap.UID) ([]uidFlags, error) {
	return nil, nil
}
func (c *fakeLabelsConn) UIDFetchFlagsChangedSince(context.Context, uint64) ([]uidFlags, error) {
	return nil, nil
}
func (c *fakeLabelsConn) UIDStoreFlags(context.Context, imap.UID, imap.StoreFlagsOp, []imap.Flag) error {
	return nil
}
func (c *fakeLabelsConn) UIDMove(context.Context, imap.UID, string) error { return nil }
func (c *fakeLabelsConn) UIDExpunge(context.Context, imap.UID) error      { return nil }
func (c *fakeLabelsConn) Noop(context.Context) error                      { return nil }
func (c *fakeLabelsConn) Idle(context.Context) (idleHandle, error)        { return nil, nil }

var _ Conn = (*fakeLabelsConn)(nil)

func TestSyncAllFoldersGmailLabels_Placement(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{})
	ctx := context.Background()

	p, err := ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "labels@example.test",
		DisplayName:    "labels",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	acc, err := ha.Store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "Gmail",
		Host:         "imap.gmail.com",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "labels@example.test",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: sealCred(t, "pw"),
		State:        store.IMAPImportAccountStateEnabled,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	d := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	conn := &fakeLabelsConn{
		uidNext: 5,
		msgs: []labeledMsg{
			{uid: 1, raw: buildRFC822("gm-inbox-travel@test", "Inbox+Travel", d), labels: []string{`\Inbox`, "Travel"}},
			{uid: 2, raw: buildRFC822("gm-sent@test", "Sent", d), labels: []string{`\Sent`}},
			{uid: 3, raw: buildRFC822("gm-archived@test", "Archived", d), labels: nil},
			{uid: 4, raw: buildRFC822("gm-inbox-flags@test", "Inbox+flags", d), labels: []string{`\Inbox`, `\Important`, `\Starred`}},
		},
	}

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		log:         newTestLogger(t),
		clk:         ha.Clock,
		categoriser: noopCategoriser{},
	})

	if err := w.syncAllFoldersGmailLabels(ctx, conn, []folderInfo{{Name: gmailAllMail}}); err != nil {
		t.Fatalf("syncAllFoldersGmailLabels: %v", err)
	}

	// uid 1: INBOX + Travel (2 memberships).
	m1, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, p.ID, "gm-inbox-travel@test")
	if err != nil {
		t.Fatalf("lookup inbox-travel: %v", err)
	}
	if len(m1.Mailboxes) != 2 {
		t.Errorf("inbox-travel has %d memberships; want 2", len(m1.Mailboxes))
	}
	if countMailboxMessages(t, ha.Store, p.ID, "INBOX") != 2 { // uid1 + uid4
		t.Errorf("INBOX count = %d; want 2", countMailboxMessages(t, ha.Store, p.ID, "INBOX"))
	}
	if countMailboxMessages(t, ha.Store, p.ID, "Travel") != 1 {
		t.Errorf("Travel count = %d; want 1", countMailboxMessages(t, ha.Store, p.ID, "Travel"))
	}
	// uid 2: Sent only.
	if countMailboxMessages(t, ha.Store, p.ID, "Sent") != 1 {
		t.Errorf("Sent count = %d; want 1", countMailboxMessages(t, ha.Store, p.ID, "Sent"))
	}
	// uid 3: Archive (no placement label).
	if countMailboxMessages(t, ha.Store, p.ID, "Archive") != 1 {
		t.Errorf("Archive count = %d; want 1", countMailboxMessages(t, ha.Store, p.ID, "Archive"))
	}
	// uid 4: INBOX only (Important/Starred carry no placement).
	m4, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, p.ID, "gm-inbox-flags@test")
	if err != nil {
		t.Fatalf("lookup inbox-flags: %v", err)
	}
	if len(m4.Mailboxes) != 1 {
		t.Errorf("inbox-flags has %d memberships; want 1 (INBOX only)", len(m4.Mailboxes))
	}

	// Import state recorded on All Mail for write-back addressing.
	if _, found, err := ha.Store.Meta().GetIMAPImportMessageStateByMessage(ctx, acc.ID, m1.ID); err != nil || !found {
		t.Errorf("expected message state for inbox-travel: found=%v err=%v", found, err)
	}

	// Second pass is idempotent: cursor prevents re-fetch, no duplicate
	// memberships.
	if err := w.syncAllFoldersGmailLabels(ctx, conn, []folderInfo{{Name: gmailAllMail}}); err != nil {
		t.Fatalf("second syncAllFoldersGmailLabels: %v", err)
	}
	m1b, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, p.ID, "gm-inbox-travel@test")
	if err != nil {
		t.Fatalf("re-lookup inbox-travel: %v", err)
	}
	if len(m1b.Mailboxes) != 2 {
		t.Errorf("after second pass inbox-travel has %d memberships; want 2 (idempotent)", len(m1b.Mailboxes))
	}
}

// TestSyncAllFoldersGmailLabels_FallbackNoAllMail verifies that an X-GM-EXT-1
// server with no [Gmail]/All Mail folder falls back to folder-based placement
// rather than failing (REQ-IMAP-IMP-53 fallback clause).
func TestSyncAllFoldersGmailLabels_FallbackNoAllMail(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{})
	ctx := context.Background()
	p, err := ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "fb@example.test",
		DisplayName:    "fb",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	acc, err := ha.Store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "Gmail",
		Host:         "imap.gmail.com",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "fb@example.test",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: sealCred(t, "pw"),
		State:        store.IMAPImportAccountStateEnabled,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	w := newAccountWorker(accountWorkerOpts{
		account: acc, store: ha.Store, dataKey: testDataKey(t),
		log: newTestLogger(t), clk: ha.Clock, categoriser: noopCategoriser{},
	})
	// No All Mail folder, but the fake serves no messages either; the fallback
	// (syncAllFoldersGmail) must run without error over an empty folder list.
	conn := &fakeLabelsConn{uidNext: 1}
	if err := w.syncAllFoldersGmailLabels(ctx, conn, []folderInfo{{Name: "INBOX"}}); err != nil {
		t.Fatalf("fallback syncAllFoldersGmailLabels: %v", err)
	}
}

// --------------------------------------------------------------------------
// prodConn.UIDFetchWithLabels wire parse (scripted server)
// --------------------------------------------------------------------------

// TestProdConnUIDFetchWithLabels drives the production Conn wrapper against a
// scripted plaintext IMAP server that returns an X-GM-LABELS FETCH response,
// exercising the patched go-imap fork end to end through herold's prodConn.
func TestProdConnUIDFetchWithLabels(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		write := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		writeRaw := func(s string) { _, _ = c.Write([]byte(s)) }
		write("* OK [CAPABILITY IMAP4rev2 X-GM-EXT-1] ready")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			sp := strings.SplitN(line, " ", 2)
			tag := sp[0]
			rest := ""
			if len(sp) > 1 {
				rest = strings.ToUpper(sp[1])
			}
			switch {
			case strings.HasPrefix(rest, "LOGIN"):
				write(tag + " OK ok")
			case strings.HasPrefix(rest, "SELECT"), strings.HasPrefix(rest, "EXAMINE"):
				write("* 1 EXISTS")
				write("* OK [UIDVALIDITY 1] .")
				write("* OK [UIDNEXT 43] .")
				write(tag + " OK [READ-WRITE] selected")
			case strings.Contains(rest, "FETCH"):
				// The {12} literal must be exactly 12 raw bytes ("hello world!")
				// with no trailing CRLF before the closing ')'.
				write(`* 1 FETCH (UID 42 X-GM-LABELS (\Inbox "Travel" "Receipts/2024") FLAGS (\Seen) INTERNALDATE "01-Jan-2025 00:00:00 +0000" BODY[] {12}`)
				writeRaw("hello world!")
				write(`)`)
				write(tag + " OK fetch done")
			default:
				write(tag + " OK done")
			}
		}
	}()

	netConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	client := imapclient.New(netConn, nil)
	defer client.Close()
	if err := client.Login("u", "p").Wait(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("Select: %v", err)
	}
	pc := &prodConn{client: client, notify: make(chan struct{}, 1)}

	msgs, err := pc.UIDFetchWithLabels(context.Background(), []imap.UID{42})
	if err != nil {
		t.Fatalf("UIDFetchWithLabels: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs)=%d, want 1", len(msgs))
	}
	got := msgs[0]
	if got.UID != 42 {
		t.Errorf("UID=%d, want 42", got.UID)
	}
	wantLabels := []string{`\Inbox`, "Travel", "Receipts/2024"}
	if len(got.Labels) != len(wantLabels) {
		t.Fatalf("Labels=%q, want %q", got.Labels, wantLabels)
	}
	for i := range wantLabels {
		if got.Labels[i] != wantLabels[i] {
			t.Errorf("Labels[%d]=%q, want %q", i, got.Labels[i], wantLabels[i])
		}
	}
	if string(got.RFC822) != "hello world!" {
		t.Errorf("RFC822=%q, want %q", got.RFC822, "hello world!")
	}
	// Placement derived from the parsed labels.
	names := gmailLabelSetToMailboxNames(got.Labels, nil, nil)
	if len(names) != 3 || names[0] != "INBOX" || names[1] != "Travel" || names[2] != "Receipts/2024" {
		t.Errorf("derived mailbox names = %q", names)
	}
}
