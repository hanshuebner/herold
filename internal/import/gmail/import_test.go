package gmail_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	gmail "github.com/hanshuebner/herold/internal/import/gmail"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fixtureMbox is a small two-message mbox crafted to look like German
// Gmail Takeout output: localized X-Gmail-Labels, X-GM-THRID, German
// system labels, plus one user-defined label ("Archiviert"). The first
// message is in INBOX + has Important, Unread, the Updates category.
// The second is archived (no Inbox), has the Personal category, and
// the user's "Archiviert" label.
const fixtureMbox = `From sender@example.com Wed May 06 18:21:37 +0000 2026
X-GM-THRID: 1864464279665606918
X-Gmail-Labels: =?UTF-8?Q?Posteingang,Wichtig,Ungelesen,Kategorie:_=22Neuigkeiten=22?=
Delivered-To: hans.huebner@gmail.com
From: Sam <sam@cluesbysam.com>
To: hans@huebner.org
Subject: First message
Message-ID: <msg-1@example.com>
Date: Wed, 6 May 2026 18:21:24 +0000
Content-Type: text/plain

body of first

From sender@example.com Thu May 07 12:42:31 +0000 2026
X-GM-THRID: 1864533542565071336
X-Gmail-Labels: =?UTF-8?Q?Archiviert,Ge=C3=B6ffnet,Kategorie:_=22Pers=C3=B6nlich=22?=
Delivered-To: hans.huebner@gmail.com
From: friend@example.com
To: hans.huebner@gmail.com
Subject: Second message
Message-ID: <msg-2@example.com>
Date: Thu, 07 May 2026 12:42:30 +0000
Content-Type: text/plain

body of second
`

const fixtureFilters = `{
  "filter": [
    {
      "query": "subject:Chefkoch subject:Newsletter",
      "labelsToAdd": ["kochen", "Archiviert"],
      "labelsToRemove": ["Posteingang"]
    },
    {
      "query": "from:hetzner.de",
      "labelsToAdd": ["hetzner", "Archiviert"],
      "labelsToRemove": ["Posteingang"]
    }
  ]
}`

const fixtureBlocked = `{"addresses":["bad1@example.com","bad2@example.com"]}`
const fixtureForwarding = `{"addresses":["alt@example.com"]}`
const fixtureDelegated = `{"addresses":["alias1@example.com","alias2@example.com"]}`

func writeFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gmailDir := filepath.Join(dir, "Takeout", "Gmail")
	settingsDir := filepath.Join(gmailDir, "Nutzereinstellungen")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustWrite := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	mustWrite(filepath.Join(gmailDir, "Alle E-Mails einschliesslich Spam-Nachrichten und E.mbox"), fixtureMbox)
	mustWrite(filepath.Join(settingsDir, "Filter.json"), fixtureFilters)
	mustWrite(filepath.Join(settingsDir, "Blockierte Adressen.json"), fixtureBlocked)
	mustWrite(filepath.Join(settingsDir, "Weiterleitungsadressen.json"), fixtureForwarding)
	mustWrite(filepath.Join(settingsDir, "Delegierte Absenderadressen.json"), fixtureDelegated)
	return dir
}

func openTestStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := storesqlite.Open(context.Background(),
		filepath.Join(dir, "meta.db"),
		nil,
		clock.NewFake(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustInsertPrincipal(t *testing.T, s store.Store) store.Principal {
	t.Helper()
	p, err := s.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "hans@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return p
}

func TestImporterEndToEndGerman(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)
	src := writeFixtureDir(t)

	imp := gmail.Importer{
		Store:     s,
		Principal: p,
		Options: gmail.Options{
			Source: src,
		},
	}
	res, err := imp.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Locale != "de" {
		t.Errorf("Locale = %q, want de", res.Locale)
	}
	if res.MessagesScanned != 2 {
		t.Errorf("MessagesScanned = %d, want 2", res.MessagesScanned)
	}
	if res.MessagesImported != 2 {
		t.Errorf("MessagesImported = %d, want 2 (errors=%v)", res.MessagesImported, res.FirstErrors)
	}
	if res.MessagesSkippedDuplicate != 0 {
		t.Errorf("MessagesSkippedDuplicate = %d, want 0", res.MessagesSkippedDuplicate)
	}
	if res.FiltersTranslated != 2 {
		t.Errorf("FiltersTranslated = %d, want 2", res.FiltersTranslated)
	}
	if res.BlockedAddresses != 2 {
		t.Errorf("BlockedAddresses = %d, want 2", res.BlockedAddresses)
	}
	if res.ForwardingAddresses != 1 {
		t.Errorf("ForwardingAddresses = %d, want 1", res.ForwardingAddresses)
	}
	if res.DelegatedIdentitiesCreated != 2 {
		t.Errorf("DelegatedIdentitiesCreated = %d, want 2", res.DelegatedIdentitiesCreated)
	}

	// Verify the imported-from-gmail Sieve script landed and is inactive.
	text, err := s.Meta().GetSieveScriptByName(ctx, p.ID, "imported-from-gmail")
	if err != nil {
		t.Fatalf("GetSieveScriptByName: %v", err)
	}
	if text == "" {
		t.Errorf("Sieve script empty")
	}

	// Verify mailboxes: INBOX must exist (created if not), and the
	// user-defined "Archiviert" label must have been created.
	mboxes, err := s.Meta().ListMailboxes(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	gotNames := map[string]bool{}
	for _, m := range mboxes {
		gotNames[m.Name] = true
	}
	if !gotNames["INBOX"] {
		t.Errorf("expected INBOX mailbox; got %v", gotNames)
	}
	if !gotNames["Archiviert"] {
		t.Errorf("expected user label Archiviert; got %v", gotNames)
	}
}

func TestImporterDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)
	src := writeFixtureDir(t)

	imp := gmail.Importer{
		Store:     s,
		Principal: p,
		Options:   gmail.Options{Source: src, DryRun: true},
	}
	res, err := imp.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MessagesImported != 2 {
		t.Errorf("dry-run MessagesImported counter = %d, want 2", res.MessagesImported)
	}
	// Sieve script should NOT have been written.
	if _, err := s.Meta().GetSieveScriptByName(ctx, p.ID, "imported-from-gmail"); err == nil {
		t.Errorf("dry-run: Sieve script unexpectedly present")
	}
	// User-defined Archiviert mailbox should NOT have been created.
	mboxes, _ := s.Meta().ListMailboxes(ctx, p.ID)
	for _, m := range mboxes {
		if m.Name == "Archiviert" {
			t.Errorf("dry-run: user label Archiviert was unexpectedly created")
		}
	}
}

func TestImporterIdempotentSecondRun(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)
	src := writeFixtureDir(t)

	first := gmail.Importer{Store: s, Principal: p, Options: gmail.Options{Source: src}}
	r1, err := first.Run(ctx)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if r1.MessagesImported != 2 {
		t.Fatalf("first run imported = %d, want 2", r1.MessagesImported)
	}

	second := gmail.Importer{Store: s, Principal: p, Options: gmail.Options{Source: src}}
	r2, err := second.Run(ctx)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if r2.MessagesImported != 0 {
		t.Errorf("second run imported = %d, want 0 (all dup)", r2.MessagesImported)
	}
	if r2.MessagesSkippedDuplicate != 2 {
		t.Errorf("second run dups = %d, want 2", r2.MessagesSkippedDuplicate)
	}
}

func TestImporterEmitsProgressLines(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)
	src := writeFixtureDir(t)

	var buf bytes.Buffer
	imp := gmail.Importer{
		Store:     s,
		Principal: p,
		Logger:    slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
		Options: gmail.Options{
			Source:        src,
			ProgressEvery: 1, // emit one line per message so the 2-msg fixture exercises the path
		},
	}
	if _, err := imp.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()
	wantLines := []string{
		"source scanned",
		"locale picked",
		"import progress",
		"import complete",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("progress log missing %q\nfull:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "scanned=2") {
		t.Errorf("progress log should report scanned=2; got:\n%s", out)
	}
}

// fixtureMalformedMbox carries one message with a multipart that
// declares a boundary but never closes it (mirroring the most common
// real-world failure pattern), and one message whose Content-Transfer-
// Encoding declares base64 but the body contains non-base64 characters.
// Both used to be silently dropped by the strict mailparse path; the
// importer now stores them via the lenient header-only parse.
const fixtureMalformedMbox = `From sender@example.com Wed May 06 18:21:37 +0000 2026
X-Gmail-Labels: Inbox
From: sender@example.com
To: recipient@example.com
Subject: truncated multipart
Message-ID: <truncated@example.com>
Date: Wed, 6 May 2026 18:21:24 +0000
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="boundary-never-closed"

--boundary-never-closed
Content-Type: text/plain

inner text but the closing boundary was never written

From sender@example.com Wed May 06 19:00:00 +0000 2026
X-Gmail-Labels: Inbox
From: sender@example.com
To: recipient@example.com
Subject: bad base64
Message-ID: <bad-base64@example.com>
Date: Wed, 6 May 2026 19:00:00 +0000
Content-Type: text/plain
Content-Transfer-Encoding: base64

thisisnotbase64!!!!@@@@####
`

func TestImporterImportsMalformedMimeMessages(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)

	dir := t.TempDir()
	mboxPath := filepath.Join(dir, "malformed.mbox")
	if err := os.WriteFile(mboxPath, []byte(fixtureMalformedMbox), 0o644); err != nil {
		t.Fatalf("write malformed mbox: %v", err)
	}

	imp := gmail.Importer{
		Store:     s,
		Principal: p,
		Options:   gmail.Options{Source: dir, SkipSettings: true},
	}
	res, err := imp.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MessagesScanned != 2 {
		t.Errorf("MessagesScanned = %d, want 2", res.MessagesScanned)
	}
	if res.MessagesImported != 2 {
		t.Errorf("MessagesImported = %d, want 2 (errors=%v)", res.MessagesImported, res.FirstErrors)
	}
	if res.MessagesSkippedError != 0 {
		t.Errorf("MessagesSkippedError = %d, want 0; lenient parser should accept these", res.MessagesSkippedError)
	}

	// Verify the messages are addressable by Message-ID — the dedup
	// path must work, which proves the envelope was extracted despite
	// the body being unparseable.
	for _, mid := range []string{"truncated@example.com", "bad-base64@example.com"} {
		if _, err := s.Meta().GetMessageByMessageIDHeader(ctx, p.ID, mid); err != nil {
			t.Errorf("GetMessageByMessageIDHeader(%q): %v", mid, err)
		}
	}
}

// fixtureCollidingMsgIDsMbox is two messages with the same Message-ID
// but different bodies — the spam-ring pattern. The importer must keep
// both (content-hash dedup): dedup-by-Message-ID would silently drop
// the second.
const fixtureCollidingMsgIDsMbox = `From sender@example.com Wed May 06 18:21:37 +0000 2026
X-Gmail-Labels: Spam
From: spam-a@example.com
To: hans.huebner@gmail.com
Subject: spam variant A .5332#.
Message-ID: <recycled@example.com>
Date: Wed, 6 May 2026 18:21:24 +0000
Content-Type: text/plain

distinct body A 5332

From sender@example.com Wed May 06 18:21:38 +0000 2026
X-Gmail-Labels: Spam
From: spam-b@example.com
To: hans.huebner@gmail.com
Subject: spam variant B .7486#.
Message-ID: <recycled@example.com>
Date: Wed, 6 May 2026 18:21:25 +0000
Content-Type: text/plain

distinct body B 7486
`

// fixtureIdenticalBodiesMbox carries two byte-identical messages
// followed by a sentinel third message; the sentinel ensures both
// duplicates are in mid-file and pick up the same trailing
// inter-message blank-line separator (mbox-format quirk: the last
// message has no trailing separator). Only one of the dupes plus the
// sentinel should land.
const fixtureIdenticalBodiesMbox = `From sender@example.com Wed May 06 18:21:37 +0000 2026
X-Gmail-Labels: Inbox
From: dup@example.com
To: hans.huebner@gmail.com
Subject: identical
Message-ID: <id-dup@example.com>
Date: Wed, 6 May 2026 18:21:24 +0000
Content-Type: text/plain

same body

From sender@example.com Wed May 06 18:21:38 +0000 2026
X-Gmail-Labels: Inbox
From: dup@example.com
To: hans.huebner@gmail.com
Subject: identical
Message-ID: <id-dup@example.com>
Date: Wed, 6 May 2026 18:21:24 +0000
Content-Type: text/plain

same body

From sender@example.com Wed May 06 18:21:39 +0000 2026
X-Gmail-Labels: Inbox
From: sentinel@example.com
To: hans.huebner@gmail.com
Subject: sentinel
Message-ID: <sentinel@example.com>
Date: Wed, 6 May 2026 18:21:25 +0000
Content-Type: text/plain

different body
`

func TestImporterKeepsDistinctMessagesSharingMessageID(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)

	dir := t.TempDir()
	mboxPath := filepath.Join(dir, "spamring.mbox")
	if err := os.WriteFile(mboxPath, []byte(fixtureCollidingMsgIDsMbox), 0o644); err != nil {
		t.Fatalf("write mbox: %v", err)
	}
	imp := gmail.Importer{
		Store:     s,
		Principal: p,
		Options:   gmail.Options{Source: dir, SkipSettings: true},
	}
	res, err := imp.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MessagesImported != 2 {
		t.Errorf("MessagesImported = %d, want 2 (errors=%v, dups=%d)",
			res.MessagesImported, res.FirstErrors, res.MessagesSkippedDuplicate)
	}
	if res.MessagesSkippedDuplicate != 0 {
		t.Errorf("MessagesSkippedDuplicate = %d, want 0", res.MessagesSkippedDuplicate)
	}
}

func TestImporterCollapsesIdenticalBodies(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)

	dir := t.TempDir()
	mboxPath := filepath.Join(dir, "twins.mbox")
	if err := os.WriteFile(mboxPath, []byte(fixtureIdenticalBodiesMbox), 0o644); err != nil {
		t.Fatalf("write mbox: %v", err)
	}
	imp := gmail.Importer{
		Store:     s,
		Principal: p,
		Options:   gmail.Options{Source: dir, SkipSettings: true},
	}
	res, err := imp.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MessagesImported != 2 {
		t.Errorf("MessagesImported = %d, want 2 (one of the dupes + sentinel)", res.MessagesImported)
	}
	if res.MessagesSkippedDuplicate != 1 {
		t.Errorf("MessagesSkippedDuplicate = %d, want 1 (in-batch dedup)", res.MessagesSkippedDuplicate)
	}
}

func TestImporterRequiresDirectorySource(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := mustInsertPrincipal(t, s)

	tmp := t.TempDir()
	bogus := filepath.Join(tmp, "not-a-dir.tgz")
	if err := os.WriteFile(bogus, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write bogus: %v", err)
	}
	imp := gmail.Importer{Store: s, Principal: p, Options: gmail.Options{Source: bogus}}
	if _, err := imp.Run(ctx); err == nil {
		t.Fatalf("want error for non-directory source")
	}
}
