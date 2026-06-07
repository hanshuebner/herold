package imapimport

// gmail_test.go covers the decomposable pure-function logic in gmail.go.
//
// The end-to-end Gmail sync path (syncAllFoldersGmail, syncFolderGmailAllMail)
// is NOT tested here because the in-process imapmemserver does not implement
// X-GM-EXT-1 or [Gmail]/All Mail semantics. See the package-level comment in
// gmail.go for what a real-Gmail integration test would require.
//
// Covered here:
//   - isGmailServer: capability + folder-list combinations
//   - shouldSkipGmailFolder: skip/sync decisions for every interesting folder
//   - gmailLabelsToMailboxNames: label-to-mailbox mapping in multiple locales
//   - extractXGmailLabels: header extraction from raw RFC822 bytes

import (
	"fmt"
	"testing"

	imap "github.com/emersion/go-imap/v2"
)

// --------------------------------------------------------------------------
// isGmailServer
// --------------------------------------------------------------------------

func TestIsGmailServer_CapAndAllMailFolder(t *testing.T) {
	caps := imap.CapSet{capGmailExt: {}}
	folders := []folderInfo{
		{Name: "INBOX"},
		{Name: "[Gmail]/All Mail"},
		{Name: "[Gmail]/Drafts"},
	}
	if !isGmailServer(caps, folders, "imap.gmail.com") {
		t.Error("expected true: X-GM-EXT-1 cap + All Mail folder present")
	}
}

func TestIsGmailServer_NoCapability(t *testing.T) {
	caps := imap.CapSet{} // no X-GM-EXT-1
	folders := []folderInfo{
		{Name: "[Gmail]/All Mail"},
	}
	if isGmailServer(caps, folders, "imap.gmail.com") {
		t.Error("expected false: missing X-GM-EXT-1 capability")
	}
}

func TestIsGmailServer_CapButNoAllMailFolder(t *testing.T) {
	caps := imap.CapSet{capGmailExt: {}}
	folders := []folderInfo{
		{Name: "INBOX"},
		{Name: "[Gmail]/Drafts"},
		// No [Gmail]/All Mail.
	}
	// Non-gmail.com host + cap but no All Mail -> false.
	if isGmailServer(caps, folders, "imap.fastmail.com") {
		t.Error("expected false: cap present but no All Mail folder and non-gmail host")
	}
}

func TestIsGmailServer_CapNoFolderButGmailHost(t *testing.T) {
	// Fallback: capability + imap.gmail.com host, empty folder list.
	caps := imap.CapSet{capGmailExt: {}}
	folders := []folderInfo{} // empty
	if !isGmailServer(caps, folders, "imap.gmail.com") {
		t.Error("expected true: cap + gmail host fallback")
	}
}

func TestIsGmailServer_NonGmailServer(t *testing.T) {
	caps := imap.CapSet{imap.Cap("IMAP4rev2"): {}, imap.Cap("IDLE"): {}}
	folders := []folderInfo{
		{Name: "INBOX"},
		{Name: "Sent"},
	}
	if isGmailServer(caps, folders, "mail.example.com") {
		t.Error("expected false: regular IMAP server")
	}
}

func TestIsGmailServer_AllMailFolderWithoutCap(t *testing.T) {
	// A folder named "[Gmail]/All Mail" alone (without cap) must not trigger Gmail mode.
	caps := imap.CapSet{}
	folders := []folderInfo{{Name: "[Gmail]/All Mail"}}
	if isGmailServer(caps, folders, "imap.gmail.com") {
		t.Error("expected false: All Mail folder present but no X-GM-EXT-1 cap")
	}
}

// --------------------------------------------------------------------------
// shouldSkipGmailFolder
// --------------------------------------------------------------------------

var gmailSkipCases = []struct {
	folder string
	skip   bool
}{
	// Must be skipped (subsumed by All Mail or is the All Mail source).
	{gmailAllMail, true},
	{"[Gmail]/Sent Mail", true},
	{"[Gmail]/Important", true},
	{"[Gmail]/Starred", true},
	{"Inbox", true},
	{"[Gmail]/Inbox", true},
	{"[Gmail]/Chats", true},

	// Must NOT be skipped (not in All Mail).
	{"[Gmail]/Drafts", false},
	{"[Gmail]/Spam", false},
	{"[Gmail]/Trash", false},

	// User-created labels are not in the skip map.
	{"Work", false},
	{"Travel/2024", false},
	{"[Gmail]/SomeOtherFolder", false},
}

func TestShouldSkipGmailFolder(t *testing.T) {
	for _, tc := range gmailSkipCases {
		got := shouldSkipGmailFolder(tc.folder)
		if got != tc.skip {
			t.Errorf("shouldSkipGmailFolder(%q) = %v; want %v", tc.folder, got, tc.skip)
		}
	}
}

// --------------------------------------------------------------------------
// gmailLabelsToMailboxNames
// --------------------------------------------------------------------------

func TestGmailLabelsToMailboxNames_InboxEnglish(t *testing.T) {
	// English: "Inbox" label -> INBOX mailbox.
	got := gmailLabelsToMailboxNames("Inbox", "en")
	assertMailboxes(t, got, []string{"INBOX"})
}

func TestGmailLabelsToMailboxNames_SentEnglish(t *testing.T) {
	got := gmailLabelsToMailboxNames("Sent", "en")
	assertMailboxes(t, got, []string{"Sent"})
}

func TestGmailLabelsToMailboxNames_DraftsEnglish(t *testing.T) {
	got := gmailLabelsToMailboxNames("Drafts", "en")
	assertMailboxes(t, got, []string{"Drafts"})
}

func TestGmailLabelsToMailboxNames_SpamEnglish(t *testing.T) {
	got := gmailLabelsToMailboxNames("Spam", "en")
	assertMailboxes(t, got, []string{"Junk"})
}

func TestGmailLabelsToMailboxNames_TrashEnglish(t *testing.T) {
	got := gmailLabelsToMailboxNames("Trash", "en")
	assertMailboxes(t, got, []string{"Trash"})
}

func TestGmailLabelsToMailboxNames_MultiLabel(t *testing.T) {
	// A message in Inbox with a user label -> both mailboxes.
	got := gmailLabelsToMailboxNames("Inbox, Work", "en")
	assertContains(t, got, "INBOX")
	assertContains(t, got, "Work")
}

func TestGmailLabelsToMailboxNames_StarredAndImportant_NoMailbox(t *testing.T) {
	// Starred and Important map to flags, not mailboxes -> empty result.
	got := gmailLabelsToMailboxNames("Starred, Important", "en")
	if len(got) != 0 {
		t.Errorf("Starred+Important should produce no mailboxes; got %v", got)
	}
}

func TestGmailLabelsToMailboxNames_GermanLocale(t *testing.T) {
	// German: "Posteingang" = Inbox, "Gesendet" = Sent.
	gotInbox := gmailLabelsToMailboxNames("Posteingang", "de")
	assertMailboxes(t, gotInbox, []string{"INBOX"})

	gotSent := gmailLabelsToMailboxNames("Gesendet", "de")
	assertMailboxes(t, gotSent, []string{"Sent"})
}

func TestGmailLabelsToMailboxNames_FrenchLocale(t *testing.T) {
	// French: "Boîte de réception" = Inbox.
	got := gmailLabelsToMailboxNames("Boîte de réception", "fr")
	assertMailboxes(t, got, []string{"INBOX"})
}

func TestGmailLabelsToMailboxNames_UserLabelPassthrough(t *testing.T) {
	// User-defined labels pass through verbatim as mailbox names.
	got := gmailLabelsToMailboxNames("MyProject", "en")
	assertMailboxes(t, got, []string{"MyProject"})
}

func TestGmailLabelsToMailboxNames_UserLabelWithSlash(t *testing.T) {
	// Slashes in user labels are preserved for hierarchy.
	got := gmailLabelsToMailboxNames("Travel/2024/Italy", "en")
	assertMailboxes(t, got, []string{"Travel/2024/Italy"})
}

func TestGmailLabelsToMailboxNames_EmptyHeader(t *testing.T) {
	got := gmailLabelsToMailboxNames("", "en")
	if len(got) != 0 {
		t.Errorf("empty header should produce no mailboxes; got %v", got)
	}
}

func TestGmailLabelsToMailboxNames_EmptyLocale_FallsBackToEnglish(t *testing.T) {
	// Empty locale should behave as "en".
	got := gmailLabelsToMailboxNames("Inbox", "")
	assertMailboxes(t, got, []string{"INBOX"})
}

func TestGmailLabelsToMailboxNames_Dedup(t *testing.T) {
	// Same label twice should not produce duplicate mailbox entries.
	got := gmailLabelsToMailboxNames("Inbox, Inbox", "en")
	if len(got) != 1 {
		t.Errorf("duplicate label should produce exactly 1 mailbox; got %v", got)
	}
}

func TestGmailLabelsToMailboxNames_ChatsDropped(t *testing.T) {
	// "Chats" system label is excluded from mailbox placement (chat history
	// is not RFC822 mail and not useful as a herold mailbox).
	got := gmailLabelsToMailboxNames("Chats", "en")
	if len(got) != 0 {
		t.Errorf("Chats label should produce no mailboxes; got %v", got)
	}
}

func TestGmailLabelsToMailboxNames_RFC2047Encoded(t *testing.T) {
	// Gmail encodes non-ASCII labels in RFC 2047. ParseGmailLabels decodes
	// them, so the mapping function sees the decoded string.
	// "=?UTF-8?Q?Posteingang?=" decodes to "Posteingang" (German Inbox).
	got := gmailLabelsToMailboxNames("=?UTF-8?Q?Posteingang?=", "de")
	assertMailboxes(t, got, []string{"INBOX"})
}

// --------------------------------------------------------------------------
// extractXGmailLabels
// --------------------------------------------------------------------------

func TestExtractXGmailLabels_SimpleHeader(t *testing.T) {
	msg := buildRFC822WithLabels("test@test", "Inbox, Sent", "Subject")
	got := extractXGmailLabels(msg)
	if got != "Inbox, Sent" {
		t.Errorf("got %q; want %q", got, "Inbox, Sent")
	}
}

func TestExtractXGmailLabels_CaseInsensitive(t *testing.T) {
	msg := []byte("X-GMAIL-LABELS: Inbox\r\nSubject: test\r\n\r\nbody\r\n")
	got := extractXGmailLabels(msg)
	if got != "Inbox" {
		t.Errorf("got %q; want %q", got, "Inbox")
	}
}

func TestExtractXGmailLabels_Absent(t *testing.T) {
	msg := []byte("Message-ID: <test@test>\r\nSubject: test\r\n\r\nbody\r\n")
	got := extractXGmailLabels(msg)
	if got != "" {
		t.Errorf("got %q; want empty", got)
	}
}

func TestExtractXGmailLabels_FoldedHeader(t *testing.T) {
	// Folded (multi-line) header: continuation starts with whitespace.
	msg := []byte("X-Gmail-Labels: Inbox,\r\n Sent\r\nSubject: test\r\n\r\nbody\r\n")
	got := extractXGmailLabels(msg)
	// Should collect "Inbox, Sent" (trimmed).
	if got == "" {
		t.Error("folded header should be captured")
	}
}

func TestExtractXGmailLabels_HeaderAfterBody(t *testing.T) {
	// Label header in body area should NOT be picked up.
	msg := []byte("Subject: test\r\n\r\nX-Gmail-Labels: Inbox\r\n")
	got := extractXGmailLabels(msg)
	if got != "" {
		t.Errorf("header in body should not be captured; got %q", got)
	}
}

func TestExtractXGmailLabels_LFOnly(t *testing.T) {
	// LF-only line endings (some servers use them).
	msg := []byte("X-Gmail-Labels: Work\nSubject: test\n\nbody\n")
	got := extractXGmailLabels(msg)
	if got != "Work" {
		t.Errorf("got %q; want %q", got, "Work")
	}
}

func TestExtractXGmailLabels_EmptyBody(t *testing.T) {
	// Message with no body separator (edge case).
	msg := []byte("X-Gmail-Labels: Inbox")
	got := extractXGmailLabels(msg)
	// Should still find the header.
	if got != "Inbox" {
		t.Errorf("got %q; want %q", got, "Inbox")
	}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// buildRFC822WithLabels constructs a minimal RFC822 message with an
// X-Gmail-Labels header. Used by extractXGmailLabels tests.
func buildRFC822WithLabels(msgID, labels, subject string) []byte {
	return []byte(fmt.Sprintf(
		"Message-ID: <%s>\r\nX-Gmail-Labels: %s\r\nSubject: %s\r\nFrom: a@b.test\r\nTo: c@d.test\r\n\r\nbody\r\n",
		msgID, labels, subject,
	))
}

// assertMailboxes checks that got is a non-nil slice equal to want (order
// independent for single-element slices; order preserved for multi-element
// since label order is deterministic given a fixed locale).
func assertMailboxes(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got mailboxes %v; want %v", got, want)
		return
	}
	seen := make(map[string]bool, len(want))
	for _, w := range want {
		seen[w] = true
	}
	for _, g := range got {
		if !seen[g] {
			t.Errorf("unexpected mailbox %q in %v; want %v", g, got, want)
		}
	}
}

// assertContains checks that name appears in slice.
func assertContains(t *testing.T, slice []string, name string) {
	t.Helper()
	for _, s := range slice {
		if s == name {
			return
		}
	}
	t.Errorf("expected %q in %v", name, slice)
}
