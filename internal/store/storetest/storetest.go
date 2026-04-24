package storetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Factory returns a freshly-opened Store backed by per-test temporary
// resources. The returned cleanup is called from t.Cleanup; test code
// does not need to invoke it manually.
type Factory func(t *testing.T) (s store.Store, cleanup func())

// Run executes every compliance test against the backend produced by f.
// Invoke from each backend's _test.go with t.Run("compliance", ...).
func Run(t *testing.T, f Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"PrincipalsCRUD", testPrincipalsCRUD},
		{"PrincipalConflict", testPrincipalConflict},
		{"DomainsCRUD", testDomainsCRUD},
		{"AliasesCRUDAndResolve", testAliases},
		{"OIDCProviderAndLinks", testOIDC},
		{"APIKeys", testAPIKeys},
		{"MailboxesCRUD", testMailboxesCRUD},
		{"MailboxConflict", testMailboxConflict},
		{"InsertMessageAllocatesUIDAndModSeq", testInsertMessageAllocatesUIDAndModSeq},
		{"UpdateFlagsBumpsModSeq", testUpdateFlagsBumpsModSeq},
		{"UpdateFlagsUnchangedSince", testUpdateFlagsUnchangedSince},
		{"UpdateFlagsKeywords", testUpdateFlagsKeywords},
		{"ExpungeMessages", testExpungeMessages},
		{"ChangeFeedMonotonic", testChangeFeedMonotonic},
		{"QuotaEnforcement", testQuotaEnforcement},
		{"DeleteMailboxCascades", testDeleteMailboxCascades},
		{"BlobRoundTrip", testBlobRoundTrip},
		{"BlobDedup", testBlobDedup},
		{"BlobNotFound", testBlobNotFound},
		{"FTSSmoke", testFTSSmoke},
	}
	for _, c := range cases {
		tc := c
		t.Run(tc.name, func(t *testing.T) {
			s, cleanup := f(t)
			t.Cleanup(cleanup)
			tc.fn(t, s)
		})
	}
}

// RunMigrationIdempotency is kept separate because it needs two Open
// calls against the same data directory. Backend tests pass a factory
// that re-opens a stable path; see storesqlite_test.go for the idiom.
func RunMigrationIdempotency(t *testing.T, openAgain func(t *testing.T) store.Store) {
	t.Helper()
	// The first Open in the caller already ran migrations; a second Open
	// must succeed and be a no-op.
	s := openAgain(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	// A trivial read must work after re-opening.
	if _, err := s.Meta().ListLocalDomains(ctx); err != nil {
		t.Fatalf("ListLocalDomains after reopen: %v", err)
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustInsertPrincipal(t *testing.T, s store.Store, email string) store.Principal {
	t.Helper()
	p, err := s.Meta().InsertPrincipal(ctxT(t), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
		DisplayName:    email,
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return p
}

func mustInsertMailbox(t *testing.T, s store.Store, pID store.PrincipalID, name string) store.Mailbox {
	t.Helper()
	mb, err := s.Meta().InsertMailbox(ctxT(t), store.Mailbox{
		PrincipalID: pID,
		Name:        name,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(%s): %v", name, err)
	}
	return mb
}

func putBlob(t *testing.T, s store.Store, body string) store.BlobRef {
	t.Helper()
	ref, err := s.Blobs().Put(ctxT(t), strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	return ref
}

func testPrincipalsCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "alice@example.com")
	if p.ID == 0 {
		t.Fatalf("principal id unset")
	}
	if p.CreatedAt.IsZero() {
		t.Fatalf("principal CreatedAt zero")
	}
	got, err := s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if got.CanonicalEmail != p.CanonicalEmail {
		t.Fatalf("GetPrincipalByID: got %q, want %q", got.CanonicalEmail, p.CanonicalEmail)
	}
	byEmail, err := s.Meta().GetPrincipalByEmail(ctx, p.CanonicalEmail)
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	if byEmail.ID != p.ID {
		t.Fatalf("GetPrincipalByEmail id = %d, want %d", byEmail.ID, p.ID)
	}
	_, err = s.Meta().GetPrincipalByEmail(ctx, "missing@example.com")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPrincipalByEmail(absent) = %v, want ErrNotFound", err)
	}
	p.DisplayName = "Alice Updated"
	if err := s.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	got, err = s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID after update: %v", err)
	}
	if got.DisplayName != "Alice Updated" {
		t.Fatalf("DisplayName = %q, want %q", got.DisplayName, "Alice Updated")
	}
}

func testPrincipalConflict(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	_ = mustInsertPrincipal(t, s, "dup@example.com")
	_, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "dup@example.com",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate InsertPrincipal = %v, want ErrConflict", err)
	}
}

func testDomainsCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	if err := s.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate domain = %v, want ErrConflict", err)
	}
	d, err := s.Meta().GetDomain(ctx, "example.com")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if !d.IsLocal {
		t.Fatalf("GetDomain IsLocal = false, want true")
	}
	if _, err := s.Meta().GetDomain(ctx, "missing.test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetDomain(absent) = %v", err)
	}
	ds, err := s.Meta().ListLocalDomains(ctx)
	if err != nil {
		t.Fatalf("ListLocalDomains: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("ListLocalDomains len = %d, want 1", len(ds))
	}
}

func testAliases(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "bob@example.com")
	a, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "bob.alias",
		Domain:          "example.com",
		TargetPrincipal: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if a.ID == 0 {
		t.Fatalf("alias id unset")
	}
	_, err = s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "bob.alias",
		Domain:          "example.com",
		TargetPrincipal: p.ID,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate InsertAlias = %v, want ErrConflict", err)
	}
	got, err := s.Meta().ResolveAlias(ctx, "bob.alias", "example.com")
	if err != nil {
		t.Fatalf("ResolveAlias: %v", err)
	}
	if got != p.ID {
		t.Fatalf("ResolveAlias = %d, want %d", got, p.ID)
	}
	// Canonical address routes to the principal as well.
	got2, err := s.Meta().ResolveAlias(ctx, "bob", "example.com")
	if err != nil {
		t.Fatalf("ResolveAlias(canonical): %v", err)
	}
	if got2 != p.ID {
		t.Fatalf("ResolveAlias(canonical) = %d, want %d", got2, p.ID)
	}
	if _, err := s.Meta().ResolveAlias(ctx, "nobody", "example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveAlias(absent) = %v", err)
	}
}

func testOIDC(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name:            "google",
		IssuerURL:       "https://accounts.google.com",
		ClientID:        "cid",
		ClientSecretRef: "file:/secret",
		Scopes:          []string{"openid", "email"},
		AutoProvision:   true,
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{Name: "google"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate OIDC = %v, want ErrConflict", err)
	}
	prov, err := s.Meta().GetOIDCProvider(ctx, "google")
	if err != nil {
		t.Fatalf("GetOIDCProvider: %v", err)
	}
	if prov.ClientID != "cid" {
		t.Fatalf("ClientID = %q", prov.ClientID)
	}
	if len(prov.Scopes) != 2 || prov.Scopes[0] != "openid" {
		t.Fatalf("Scopes = %v", prov.Scopes)
	}
	p := mustInsertPrincipal(t, s, "oidc@example.com")
	link := store.OIDCLink{
		PrincipalID:     p.ID,
		ProviderName:    "google",
		Subject:         "sub-123",
		EmailAtProvider: "oidc@example.com",
	}
	if err := s.Meta().LinkOIDC(ctx, link); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	if err := s.Meta().LinkOIDC(ctx, link); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate LinkOIDC = %v, want ErrConflict", err)
	}
	out, err := s.Meta().LookupOIDCLink(ctx, "google", "sub-123")
	if err != nil {
		t.Fatalf("LookupOIDCLink: %v", err)
	}
	if out.PrincipalID != p.ID {
		t.Fatalf("LookupOIDCLink principal = %d, want %d", out.PrincipalID, p.ID)
	}
	if _, err := s.Meta().LookupOIDCLink(ctx, "google", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LookupOIDCLink(absent) = %v", err)
	}
}

func testAPIKeys(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "apik@example.com")
	k, err := s.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        "h0",
		Name:        "test",
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	if k.ID == 0 || k.CreatedAt.IsZero() {
		t.Fatalf("APIKey = %+v", k)
	}
	got, err := s.Meta().GetAPIKeyByHash(ctx, "h0")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if got.ID != k.ID {
		t.Fatalf("GetAPIKeyByHash id = %d", got.ID)
	}
	if _, err := s.Meta().GetAPIKeyByHash(ctx, "absent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAPIKeyByHash(absent) = %v", err)
	}
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().TouchAPIKey(ctx, k.ID, now); err != nil {
		t.Fatalf("TouchAPIKey: %v", err)
	}
	got, err = s.Meta().GetAPIKeyByHash(ctx, "h0")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash after touch: %v", err)
	}
	if !got.LastUsedAt.Equal(now) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, now)
	}
}

func testMailboxesCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mbox@example.com")
	inbox := mustInsertMailbox(t, s, p.ID, "INBOX")
	if inbox.ID == 0 || inbox.UIDValidity == 0 {
		t.Fatalf("mailbox missing ID/UIDValidity: %+v", inbox)
	}
	_ = mustInsertMailbox(t, s, p.ID, "Drafts")
	list, err := s.Meta().ListMailboxes(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListMailboxes len = %d", len(list))
	}
	if list[0].Name >= list[1].Name {
		t.Fatalf("ListMailboxes not sorted: %q, %q", list[0].Name, list[1].Name)
	}
	got, err := s.Meta().GetMailboxByID(ctx, inbox.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if got.Name != "INBOX" {
		t.Fatalf("GetMailboxByID name = %q", got.Name)
	}
}

func testMailboxConflict(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mbx-dup@example.com")
	_ = mustInsertMailbox(t, s, p.ID, "INBOX")
	_, err := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate InsertMailbox = %v", err)
	}
}

func testInsertMessageAllocatesUIDAndModSeq(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "msg@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "first")
	uid1, modseq1, err := s.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    mb.ID,
		Blob:         ref,
		Size:         ref.Size,
		InternalDate: time.Unix(1000, 0).UTC(),
		ReceivedAt:   time.Unix(1000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("InsertMessage #1: %v", err)
	}
	if uid1 != 1 {
		t.Fatalf("first UID = %d, want 1", uid1)
	}
	ref2 := putBlob(t, s, "second")
	uid2, modseq2, err := s.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    mb.ID,
		Blob:         ref2,
		Size:         ref2.Size,
		InternalDate: time.Unix(1001, 0).UTC(),
		ReceivedAt:   time.Unix(1001, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("InsertMessage #2: %v", err)
	}
	if uid2 != 2 {
		t.Fatalf("second UID = %d, want 2", uid2)
	}
	if modseq2 <= modseq1 {
		t.Fatalf("ModSeq not monotonic: %d then %d", modseq1, modseq2)
	}
	got, err := s.Meta().GetMailboxByID(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if got.UIDNext != 3 {
		t.Fatalf("UIDNext = %d, want 3", got.UIDNext)
	}
	if got.HighestModSeq != modseq2 {
		t.Fatalf("HighestModSeq = %d, want %d", got.HighestModSeq, modseq2)
	}
}

func testUpdateFlagsBumpsModSeq(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "flags@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "body-a")
	_, modseq0, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Find the message id by listing (simple scan via expunge semantics
	// isn't exposed here; we rely on InsertMessage returning UID=1 and
	// assume id is discoverable via a Get by (mailbox, uid). The
	// interface does not currently expose such a method, so this test
	// uses a fresh message we know the ID of via round-trip.
	// Workaround: InsertMessage returns uid, modseq; we need the id.
	// The interface doesn't return it directly, so we use a helper that
	// enumerates messages via the change feed to discover ids.
	id := firstMessageIDFromFeed(t, s, p.ID)
	modseq1, err := s.Meta().UpdateMessageFlags(ctx, id, store.MessageFlagSeen, 0, nil, nil, 0)
	if err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}
	if modseq1 <= modseq0 {
		t.Fatalf("flag update did not bump ModSeq: %d then %d", modseq0, modseq1)
	}
	got, err := s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Flags&store.MessageFlagSeen == 0 {
		t.Fatalf("Flags missing \\Seen after add")
	}
	if got.ModSeq != modseq1 {
		t.Fatalf("GetMessage ModSeq = %d, want %d", got.ModSeq, modseq1)
	}
	// Clear the flag.
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, 0, store.MessageFlagSeen, nil, nil, 0); err != nil {
		t.Fatalf("clear \\Seen: %v", err)
	}
	got, err = s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage after clear: %v", err)
	}
	if got.Flags&store.MessageFlagSeen != 0 {
		t.Fatalf("Flags still has \\Seen after clear")
	}
}

func testUpdateFlagsUnchangedSince(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ucs@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "ucs-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	modseq, err := s.Meta().UpdateMessageFlags(ctx, id, store.MessageFlagFlagged, 0, nil, nil, 0)
	if err != nil {
		t.Fatalf("initial update: %v", err)
	}
	// unchangedSince with stale ModSeq must conflict.
	_, err = s.Meta().UpdateMessageFlags(ctx, id, store.MessageFlagSeen, 0, nil, nil, modseq-1)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale UNCHANGEDSINCE = %v, want ErrConflict", err)
	}
	// unchangedSince == current ModSeq is allowed.
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, store.MessageFlagSeen, 0, nil, nil, modseq); err != nil {
		t.Fatalf("current UNCHANGEDSINCE: %v", err)
	}
}

func testUpdateFlagsKeywords(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "kw@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "kw-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, 0, 0, []string{"work", "urgent"}, nil, 0); err != nil {
		t.Fatalf("add keywords: %v", err)
	}
	got, err := s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !containsAll(got.Keywords, []string{"work", "urgent"}) {
		t.Fatalf("Keywords = %v, want work+urgent", got.Keywords)
	}
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, 0, 0, nil, []string{"urgent"}, 0); err != nil {
		t.Fatalf("remove keyword: %v", err)
	}
	got, err = s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage after remove: %v", err)
	}
	if containsAll(got.Keywords, []string{"urgent"}) {
		t.Fatalf("Keywords still has urgent: %v", got.Keywords)
	}
	if !containsAll(got.Keywords, []string{"work"}) {
		t.Fatalf("Keywords missing work: %v", got.Keywords)
	}
}

func testExpungeMessages(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "exp@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	var ids []store.MessageID
	for i := 0; i < 3; i++ {
		ref := putBlob(t, s, "exp-"+string(rune('a'+i)))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	// Gather all three ids from the feed (there is no ListMessages in
	// the interface yet).
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for _, c := range feed {
		if c.Kind == store.ChangeKindMessageCreated && c.MessageID != 0 {
			ids = append(ids, c.MessageID)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 created messages in feed, got %d", len(ids))
	}
	if err := s.Meta().ExpungeMessages(ctx, mb.ID, ids[:2]); err != nil {
		t.Fatalf("Expunge: %v", err)
	}
	// First two gone, last remains.
	if _, err := s.Meta().GetMessage(ctx, ids[0]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessage[0] after expunge = %v", err)
	}
	if _, err := s.Meta().GetMessage(ctx, ids[2]); err != nil {
		t.Fatalf("GetMessage[2] after expunge: %v", err)
	}
	// Re-expunging already-gone ids is a silent success unless all gone.
	if err := s.Meta().ExpungeMessages(ctx, mb.ID, []store.MessageID{ids[0], ids[2]}); err != nil {
		t.Fatalf("Expunge mixed: %v", err)
	}
	// Expunging only already-gone returns ErrNotFound.
	if err := s.Meta().ExpungeMessages(ctx, mb.ID, []store.MessageID{ids[0]}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Expunge all-gone = %v, want ErrNotFound", err)
	}
}

func testChangeFeedMonotonic(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "feed@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	var lastSeq store.ChangeSeq
	for i := 0; i < 5; i++ {
		ref := putBlob(t, s, "feed-"+string(rune('0'+i)))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	if len(feed) < 6 { // 1 mailbox + 5 messages
		t.Fatalf("feed len = %d, want >= 6", len(feed))
	}
	for i, c := range feed {
		if c.Seq <= lastSeq && i != 0 {
			t.Fatalf("feed not monotonic at %d: prev %d, got %d", i, lastSeq, c.Seq)
		}
		if c.PrincipalID != p.ID {
			t.Fatalf("feed entry principal = %d, want %d", c.PrincipalID, p.ID)
		}
		lastSeq = c.Seq
	}
	// Paginated read must skip entries with Seq <= fromSeq.
	cursor := feed[2].Seq
	rest, err := s.Meta().ReadChangeFeed(ctx, p.ID, cursor, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed paginated: %v", err)
	}
	if len(rest)+3 != len(feed) {
		t.Fatalf("paginated feed len = %d, want %d", len(rest), len(feed)-3)
	}
	for _, c := range rest {
		if c.Seq <= cursor {
			t.Fatalf("paginated feed includes Seq %d <= cursor %d", c.Seq, cursor)
		}
	}
}

func testQuotaEnforcement(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "quota@example.com",
		QuotaBytes:     10, // tight
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	small := putBlob(t, s, "1234567")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: small, Size: small.Size}); err != nil {
		t.Fatalf("Insert small: %v", err)
	}
	big := putBlob(t, s, "aaaaaaaaaaaaaaaaaaaaa")
	_, _, err = s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: big, Size: big.Size})
	if !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("Insert over quota = %v, want ErrQuotaExceeded", err)
	}
}

func testDeleteMailboxCascades(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "del@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "Temp")
	ref := putBlob(t, s, "to-delete")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeleteMailbox(ctx, mb.ID); err != nil {
		t.Fatalf("DeleteMailbox: %v", err)
	}
	if _, err := s.Meta().GetMailboxByID(ctx, mb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByID after delete = %v", err)
	}
	if err := s.Meta().DeleteMailbox(ctx, mb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteMailbox(absent) = %v", err)
	}
}

func testBlobRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	body := []byte("hello blob world")
	ref, err := s.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Size != int64(len(body)) {
		t.Fatalf("Size = %d", ref.Size)
	}
	r, err := s.Blobs().Get(ctx, ref.Hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch")
	}
	size, refs, err := s.Blobs().Stat(ctx, ref.Hash)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(body)) || refs < 1 {
		t.Fatalf("Stat = (size=%d, refs=%d)", size, refs)
	}
}

func testBlobDedup(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	body := []byte("dedup me")
	a, err := s.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	b, err := s.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	if a != b {
		t.Fatalf("Put not dedup: %+v vs %+v", a, b)
	}
}

func testBlobNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	_, err := s.Blobs().Get(ctx, strings.Repeat("0", 64))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(absent) = %v, want ErrNotFound", err)
	}
	if err := s.Blobs().Delete(ctx, strings.Repeat("0", 64)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete(absent) = %v, want ErrNotFound", err)
	}
}

func testFTSSmoke(t *testing.T, s store.Store) {
	// The FTS backend in storesqlite/storepg is a trivial stub in Wave
	// 1; the real Bleve-backed index ships as internal/storefts. We only
	// assert the surface compiles and accepts calls without error on an
	// empty index.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "fts@example.com")
	_, err := s.FTS().Query(ctx, p.ID, store.Query{Text: "anything"})
	if err != nil {
		// Some backends return a "not implemented" error, which is also
		// acceptable. We accept any outcome that isn't a panic.
		t.Logf("FTS Query on empty index returned: %v", err)
	}
	// RemoveMessage of absent id must not explode.
	if err := s.FTS().RemoveMessage(ctx, store.MessageID(9999)); err != nil {
		t.Logf("FTS RemoveMessage returned: %v", err)
	}
	// Commit on empty batch must be a no-op.
	if err := s.FTS().Commit(ctx); err != nil {
		t.Logf("FTS Commit returned: %v", err)
	}
}

// firstMessageIDFromFeed finds the MessageID of the first
// ChangeKindMessageCreated entry in the principal's change feed. Helper
// used by tests that need a MessageID but InsertMessage does not return
// one (the interface returns UID + ModSeq; id discovery is via the feed
// or a later ListMessages method).
func firstMessageIDFromFeed(t *testing.T, s store.Store, principalID store.PrincipalID) store.MessageID {
	t.Helper()
	feed, err := s.Meta().ReadChangeFeed(ctxT(t), principalID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for _, c := range feed {
		if c.Kind == store.ChangeKindMessageCreated && c.MessageID != 0 {
			return c.MessageID
		}
	}
	t.Fatalf("no MessageCreated entry in feed")
	return 0
}

func containsAll(haystack, needles []string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
