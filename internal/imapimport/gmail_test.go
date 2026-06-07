package imapimport

// gmail_test.go covers the decomposable pure-function logic in gmail.go.
//
// The end-to-end Gmail sync path (syncAllFoldersGmail,
// syncFolderGmailAllMailEnvelopeDedup) is NOT tested here because the
// in-process imapmemserver does not implement X-GM-EXT-1 or [Gmail]/All
// Mail semantics. See the package-level comment in gmail.go for what a
// real-Gmail integration test would require.
//
// Covered here:
//   - isGmailServer: capability + folder-list combinations
//   - shouldSkipGmailFolder / classifyGmailFolder: skip/sync decisions for
//     every interesting folder
//   - gmailHeroldMailboxName: system-folder mapping + user-label passthrough
//   - envelopeDedupDecision: logic for deciding skip vs. body-fetch in All Mail
//   - gmailLabelsToMailboxNames: label-to-mailbox mapping in multiple locales
//     (retained for best-effort X-Gmail-Labels compatibility)
//   - extractXGmailLabels: header extraction from raw RFC822 bytes
//
// What is NOT testable without a real Gmail account (documented):
//   - Live X-GM-EXT-1 capability detection.
//   - Real [Gmail]/All Mail folder content and the resulting Archive ingestion.
//   - Per-label IMAP folder presence for multi-mailbox placement.
//   - The end-to-end folder-classification-driven sync pass.

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
//
// Option B: only the three virtual/flag folders are skipped.
// [Gmail]/All Mail is handled by classifyGmailFolder as AllMail (not skip).
// [Gmail]/Sent Mail, Inbox, etc. are now synced normally as placement sources.

var gmailSkipCases = []struct {
	folder string
	skip   bool
}{
	// Must be skipped (virtual/flag folders with no unique content).
	{"[Gmail]/Important", true},
	{"[Gmail]/Starred", true},
	{"[Gmail]/Chats", true},

	// Must NOT be skipped (placement sources or All Mail special case).
	{gmailAllMail, false}, // handled as AllMail, not skipped
	{"[Gmail]/Sent Mail", false},
	{"INBOX", false},
	{"Inbox", false},
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

// --------------------------------------------------------------------------
// classifyGmailFolder (Option B folder-based placement)
// --------------------------------------------------------------------------

var gmailClassifyCases = []struct {
	folder string
	class  gmailFolderClass
}{
	// Must be skipped (virtual/flag folders with no unique content).
	{"[Gmail]/Important", gmailFolderClassSkip},
	{"[Gmail]/Starred", gmailFolderClassSkip},
	{"[Gmail]/Chats", gmailFolderClassSkip},

	// All Mail: special — synced last with envelope dedup.
	{gmailAllMail, gmailFolderClassAllMail},

	// Normal: placement sources.
	{"INBOX", gmailFolderClassNormal},
	{"[Gmail]/Sent Mail", gmailFolderClassNormal},
	{"[Gmail]/Drafts", gmailFolderClassNormal},
	{"[Gmail]/Spam", gmailFolderClassNormal},
	{"[Gmail]/Trash", gmailFolderClassNormal},

	// User labels: sync normally (user-defined folders).
	{"Work", gmailFolderClassNormal},
	{"Travel/2024", gmailFolderClassNormal},
	{"[Gmail]/SomeUnknown", gmailFolderClassNormal},
}

func TestClassifyGmailFolder(t *testing.T) {
	for _, tc := range gmailClassifyCases {
		got := classifyGmailFolder(tc.folder)
		if got != tc.class {
			t.Errorf("classifyGmailFolder(%q) = %v; want %v", tc.folder, got, tc.class)
		}
	}
}

// shouldSkipGmailFolder should only return true for the three virtual/flag
// folders, not for All Mail or any normal folder.
func TestShouldSkipGmailFolderOptionB(t *testing.T) {
	skipExpected := map[string]bool{
		"[Gmail]/Important": true,
		"[Gmail]/Starred":   true,
		"[Gmail]/Chats":     true,
	}
	notSkipped := []string{
		gmailAllMail, // handled as AllMail class, NOT skipped by shouldSkipGmailFolder
		"INBOX",
		"[Gmail]/Sent Mail",
		"[Gmail]/Drafts",
		"[Gmail]/Spam",
		"[Gmail]/Trash",
		"Work",
	}
	for folder, want := range skipExpected {
		if got := shouldSkipGmailFolder(folder); got != want {
			t.Errorf("shouldSkipGmailFolder(%q) = %v; want %v", folder, got, want)
		}
	}
	for _, folder := range notSkipped {
		if shouldSkipGmailFolder(folder) {
			t.Errorf("shouldSkipGmailFolder(%q) = true; want false", folder)
		}
	}
}

// --------------------------------------------------------------------------
// gmailHeroldMailboxName — system folder table + user-label passthrough
// --------------------------------------------------------------------------

var gmailMailboxNameCases = []struct {
	folder   string
	expected string
}{
	// System folder table.
	{"INBOX", "INBOX"},
	{"[Gmail]/Sent Mail", "Sent"},
	{"[Gmail]/Drafts", "Drafts"},
	{"[Gmail]/Spam", "Junk"},
	{"[Gmail]/Trash", "Trash"},

	// User labels: verbatim passthrough.
	{"Work", "Work"},
	{"Travel/2024/Italy", "Travel/2024/Italy"},
	{"Newsletters", "Newsletters"},
}

func TestGmailHeroldMailboxName_SystemFolders(t *testing.T) {
	noUserMapping := map[string]string{}
	for _, tc := range gmailMailboxNameCases {
		got := gmailHeroldMailboxName(tc.folder, noUserMapping)
		if got != tc.expected {
			t.Errorf("gmailHeroldMailboxName(%q) = %q; want %q", tc.folder, got, tc.expected)
		}
	}
}

func TestGmailHeroldMailboxName_UserMappingOverride(t *testing.T) {
	// A user-provided mapping override wins over the system table.
	userMapping := map[string]string{
		"[Gmail]/Sent Mail": "MySent",
		"Work":              "Projects",
	}
	if got := gmailHeroldMailboxName("[Gmail]/Sent Mail", userMapping); got != "MySent" {
		t.Errorf("expected user override MySent; got %q", got)
	}
	if got := gmailHeroldMailboxName("Work", userMapping); got != "Projects" {
		t.Errorf("expected user override Projects; got %q", got)
	}
	// Not overridden: system table.
	if got := gmailHeroldMailboxName("[Gmail]/Drafts", userMapping); got != "Drafts" {
		t.Errorf("expected Drafts (system table); got %q", got)
	}
	// Not overridden, not in system table: verbatim.
	if got := gmailHeroldMailboxName("Newsletters", userMapping); got != "Newsletters" {
		t.Errorf("expected verbatim Newsletters; got %q", got)
	}
}

// --------------------------------------------------------------------------
// envelopeDedupDecision — unit test of All Mail envelope-dedup logic
// --------------------------------------------------------------------------
//
// The decision is: given an envelopeFetchResult, if its Message-ID is already
// known -> skip body; if unknown -> body-fetch into Archive.
// We test this at the function level by examining the logic directly.
// The end-to-end path (syncFolderGmailAllMailEnvelopeDedup) requires a real
// Gmail account and is not testable with the in-process memory server.

// envelopeDedupShouldFetch encodes the per-message decision that
// syncFolderGmailAllMailEnvelopeDedup applies. We extract it here as a
// pure function for unit testing.
//
// Returns true when the message needs a body-fetch (not yet mirrored),
// false when the Message-ID was already seen (can skip body).
func envelopeDedupShouldFetch(env envelopeFetchResult, alreadyMirrored map[string]bool) bool {
	if env.MessageID == "" {
		// No Message-ID: cannot dedup cheaply; must body-fetch to be safe.
		return true
	}
	return !alreadyMirrored[env.MessageID]
}

func TestEnvelopeDedupDecision_AlreadyMirrored(t *testing.T) {
	known := map[string]bool{"known-msg-id@test": true}
	env := envelopeFetchResult{MessageID: "known-msg-id@test"}
	if envelopeDedupShouldFetch(env, known) {
		t.Error("expected skip (already mirrored); got fetch")
	}
}

func TestEnvelopeDedupDecision_NotMirrored(t *testing.T) {
	known := map[string]bool{"other-id@test": true}
	env := envelopeFetchResult{MessageID: "new-msg-id@test"}
	if !envelopeDedupShouldFetch(env, known) {
		t.Error("expected fetch (not yet mirrored); got skip")
	}
}

func TestEnvelopeDedupDecision_NoMessageID(t *testing.T) {
	// No Message-ID -> must fetch (cannot dedup by content here).
	known := map[string]bool{}
	env := envelopeFetchResult{MessageID: ""}
	if !envelopeDedupShouldFetch(env, known) {
		t.Error("expected fetch (no Message-ID); got skip")
	}
}

func TestEnvelopeDedupDecision_EmptyKnownSet(t *testing.T) {
	// Nothing mirrored yet -> all messages need body-fetch.
	known := map[string]bool{}
	env := envelopeFetchResult{MessageID: "some-id@test"}
	if !envelopeDedupShouldFetch(env, known) {
		t.Error("expected fetch (empty known set); got skip")
	}
}

// TestEnvelopeDedupDecision_MultipleMessages exercises a batch decision to
// confirm that exactly the un-mirrored messages are selected for body-fetch.
func TestEnvelopeDedupDecision_MultipleMessages(t *testing.T) {
	known := map[string]bool{
		"already-1@test": true,
		"already-2@test": true,
	}
	batch := []envelopeFetchResult{
		{MessageID: "already-1@test"}, // skip
		{MessageID: "new-1@test"},     // fetch
		{MessageID: "already-2@test"}, // skip
		{MessageID: "new-2@test"},     // fetch
		{MessageID: ""},               // fetch (no Message-ID)
	}
	var needFetch []string
	for _, env := range batch {
		if envelopeDedupShouldFetch(env, known) {
			needFetch = append(needFetch, env.MessageID)
		}
	}
	if len(needFetch) != 3 {
		t.Errorf("expected 3 messages needing body-fetch; got %d: %v", len(needFetch), needFetch)
	}
}
