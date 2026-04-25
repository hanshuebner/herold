package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
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
		{"FTSCursor_GetSet_EmptyIsZero", testFTSCursorEmptyIsZero},
		{"FTSCursor_Upsert_Roundtrip", testFTSCursorUpsertRoundtrip},
		{"FTSCursor_Concurrent", testFTSCursorConcurrent},
		{"DeletePrincipal_Cascade", testDeletePrincipalCascade},
		{"ListPrincipals_Keyset", testListPrincipalsKeyset},
		{"DeleteOIDCProvider_Cascade", testDeleteOIDCProviderCascade},
		{"ListOIDCProviders", testListOIDCProviders},
		{"UnlinkOIDC_NotFoundWhenAbsent", testUnlinkOIDCNotFound},
		{"AuditLog_AppendAndList", testAuditLogAppendAndList},
		{"PrincipalFlagTOTPEnabled", testPrincipalFlagTOTPEnabled},
		{"GetMailboxByName", testGetMailboxByName},
		{"ListMessagesPagination", testListMessagesPagination},
		{"SetMailboxSubscribed", testSetMailboxSubscribed},
		{"RenameMailbox", testRenameMailbox},
		{"SieveScript_EmptyReturnsEmpty", testSieveScriptEmpty},
		{"SieveScript_SetGetRoundtrip", testSieveScriptRoundtrip},
		{"SieveScript_Overwrite", testSieveScriptOverwrite},
		{"SieveScript_CascadeOnDeletePrincipal", testSieveScriptCascade},
		{"ListAliases_ByDomain", testListAliasesByDomain},
		{"DeleteAlias_NotFoundWhenAbsent", testDeleteAliasNotFound},
		{"DeleteDomain_NotFoundWhenAbsent", testDeleteDomainNotFound},
		{"ListAPIKeysByPrincipal", testListAPIKeysByPrincipal},
		{"DeleteAPIKey_NotFoundWhenAbsent", testDeleteAPIKeyNotFound},
		{"ListOIDCLinksByPrincipal", testListOIDCLinksByPrincipal},
		// -- Phase 2 Wave 2.0 --------------------------------------
		{"QueueEnqueueAndList", testQueueEnqueueAndList},
		{"QueueIdempotency", testQueueIdempotency},
		{"QueueClaimDueTransitionsState", testQueueClaimDueTransitionsState},
		{"QueueCompleteSuccessVsFailure", testQueueCompleteSuccessVsFailure},
		{"QueueRescheduleBumpsAttempts", testQueueRescheduleBumpsAttempts},
		{"QueueHoldRelease", testQueueHoldRelease},
		{"QueueDeleteCascadeOnPrincipalDelete", testQueueDeleteCascadeOnPrincipalDelete},
		{"QueueCountByState", testQueueCountByState},
		{"DKIMUpsertAndList", testDKIMUpsertAndList},
		{"DKIMRotateOneTx", testDKIMRotateOneTx},
		{"DKIMActiveLookup", testDKIMActiveLookup},
		{"ACMEAccountOrderCertLifecycle", testACMEAccountOrderCertLifecycle},
		{"ACMECertExpiringList", testACMECertExpiringList},
		{"WebhookCRUD", testWebhookCRUD},
		{"WebhookActiveForDomain", testWebhookActiveForDomain},
		{"DMARCInsertAndAggregate", testDMARCInsertAndAggregate},
		{"DMARCDeduplicates", testDMARCDeduplicates},
		{"MailboxACLGrantListRevoke", testMailboxACLGrantListRevoke},
		{"MailboxACLAnyoneRow", testMailboxACLAnyoneRow},
		{"JMAPStatesIncrementAtomic", testJMAPStatesIncrementAtomic},
		{"TLSRPTAppendAndRange", testTLSRPTAppendAndRange},
		// -- Wave 2.2.5 JMAP persistence ---------------------------
		{"EmailSubmission_InsertGet_Roundtrip", testEmailSubmissionInsertGetRoundtrip},
		{"EmailSubmission_List_FilterByIdentity_FilterByUndoStatus_FilterByTimeRange", testEmailSubmissionListFilters},
		{"EmailSubmission_UpdateUndoStatus_Reflected_OnGet", testEmailSubmissionUpdateUndoStatus},
		{"EmailSubmission_Delete_NotFoundAfter", testEmailSubmissionDeleteNotFoundAfter},
		{"JMAPIdentity_InsertGet_Roundtrip", testJMAPIdentityInsertGetRoundtrip},
		{"JMAPIdentity_List_ByPrincipal", testJMAPIdentityListByPrincipal},
		{"JMAPIdentity_Update_RoundTrips", testJMAPIdentityUpdateRoundtrips},
		{"JMAPIdentity_Delete_NotFoundAfter", testJMAPIdentityDeleteNotFoundAfter},
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
		if c.Kind == store.EntityKindEmail && c.Op == store.ChangeOpCreated && c.EntityID != 0 {
			ids = append(ids, store.MessageID(c.EntityID))
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

// firstMessageIDFromFeed finds the MessageID of the first email-created
// entry (Kind == EntityKindEmail, Op == ChangeOpCreated) in the
// principal's change feed. Helper used by tests that need a MessageID
// but InsertMessage does not return one (the interface returns UID +
// ModSeq; id discovery is via the feed or a later ListMessages method).
func firstMessageIDFromFeed(t *testing.T, s store.Store, principalID store.PrincipalID) store.MessageID {
	t.Helper()
	feed, err := s.Meta().ReadChangeFeed(ctxT(t), principalID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for _, c := range feed {
		if c.Kind == store.EntityKindEmail && c.Op == store.ChangeOpCreated && c.EntityID != 0 {
			return store.MessageID(c.EntityID)
		}
	}
	t.Fatalf("no email-created entry in feed")
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

// -- Wave 2 compliance cases ------------------------------------------

func testFTSCursorEmptyIsZero(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	seq, err := s.Meta().GetFTSCursor(ctx, "fts")
	if err != nil {
		t.Fatalf("GetFTSCursor(absent): %v", err)
	}
	if seq != 0 {
		t.Fatalf("absent cursor = %d, want 0", seq)
	}
}

func testFTSCursorUpsertRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().SetFTSCursor(ctx, "fts", 42); err != nil {
		t.Fatalf("SetFTSCursor: %v", err)
	}
	got, err := s.Meta().GetFTSCursor(ctx, "fts")
	if err != nil {
		t.Fatalf("GetFTSCursor: %v", err)
	}
	if got != 42 {
		t.Fatalf("cursor = %d, want 42", got)
	}
	// Upsert (advance).
	if err := s.Meta().SetFTSCursor(ctx, "fts", 100); err != nil {
		t.Fatalf("SetFTSCursor advance: %v", err)
	}
	got, err = s.Meta().GetFTSCursor(ctx, "fts")
	if err != nil {
		t.Fatalf("GetFTSCursor after advance: %v", err)
	}
	if got != 100 {
		t.Fatalf("cursor after advance = %d, want 100", got)
	}
	// Idempotent re-apply.
	if err := s.Meta().SetFTSCursor(ctx, "fts", 100); err != nil {
		t.Fatalf("SetFTSCursor idempotent: %v", err)
	}
}

// testFTSCursorConcurrent drives a handful of parallel SetFTSCursor
// calls on different keys and asserts they all land without data race
// or interleaving corruption. Run under -race in CI.
func testFTSCursorConcurrent(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	keys := []string{"a", "b", "c", "d", "e"}
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func(k string, v uint64) {
			defer wg.Done()
			if err := s.Meta().SetFTSCursor(ctx, k, v); err != nil {
				t.Errorf("SetFTSCursor(%s): %v", k, err)
			}
		}(k, uint64(i+1))
	}
	wg.Wait()
	for i, k := range keys {
		got, err := s.Meta().GetFTSCursor(ctx, k)
		if err != nil {
			t.Fatalf("GetFTSCursor(%s): %v", k, err)
		}
		if got != uint64(i+1) {
			t.Fatalf("cursor[%s] = %d, want %d", k, got, i+1)
		}
	}
}

func testDeletePrincipalCascade(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cascade@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "cascade-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "cascade.alias", Domain: "example.com", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "casc-provider", IssuerURL: "https://example.com", ClientID: "cid",
		ClientSecretRef: "x",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "casc-provider", Subject: "sub-c",
	}); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	if _, err := s.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID, Hash: "casc-h", Name: "t",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	if err := s.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:        time.Now().UTC(),
		ActorKind: store.ActorPrincipal,
		ActorID:   "1",
		Action:    "principal.login",
		Subject:   subjectPrincipal(p.ID),
		Outcome:   store.OutcomeSuccess,
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}

	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}

	// Principal row gone.
	if _, err := s.Meta().GetPrincipalByID(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPrincipalByID after delete = %v, want ErrNotFound", err)
	}
	// Mailbox gone.
	if _, err := s.Meta().GetMailboxByID(ctx, mb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByID after delete = %v", err)
	}
	// Alias gone.
	if _, err := s.Meta().ResolveAlias(ctx, "cascade.alias", "example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveAlias after delete = %v", err)
	}
	// OIDC link gone.
	if _, err := s.Meta().LookupOIDCLink(ctx, "casc-provider", "sub-c"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LookupOIDCLink after delete = %v", err)
	}
	// API key gone.
	if _, err := s.Meta().GetAPIKeyByHash(ctx, "casc-h"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAPIKeyByHash after delete = %v", err)
	}
	// Audit entries for this principal gone.
	entries, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{PrincipalID: p.ID})
	if err != nil {
		t.Fatalf("ListAuditLog after delete: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("audit entries after delete = %d, want 0", len(entries))
	}
	// Second delete -> ErrNotFound.
	if err := s.Meta().DeletePrincipal(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeletePrincipal(gone) = %v", err)
	}
}

func testListPrincipalsKeyset(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	const n = 50
	ids := make([]store.PrincipalID, 0, n)
	for i := 0; i < n; i++ {
		p := mustInsertPrincipal(t, s, fmt.Sprintf("kp-%02d@example.com", i))
		ids = append(ids, p.ID)
	}
	// Page through with limit=20; each principal must appear exactly
	// once, in ascending ID order.
	seen := make(map[store.PrincipalID]struct{}, n)
	var after store.PrincipalID
	var prev store.PrincipalID
	for {
		page, err := s.Meta().ListPrincipals(ctx, after, 20)
		if err != nil {
			t.Fatalf("ListPrincipals(after=%d): %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		for _, p := range page {
			if p.ID <= prev {
				t.Fatalf("ListPrincipals not sorted asc: prev=%d curr=%d", prev, p.ID)
			}
			prev = p.ID
			if _, dup := seen[p.ID]; dup {
				t.Fatalf("duplicate id %d", p.ID)
			}
			seen[p.ID] = struct{}{}
		}
		after = page[len(page)-1].ID
	}
	// Only our seeded principals should appear (the factory hands us a
	// fresh store per sub-test).
	if len(seen) < n {
		t.Fatalf("saw %d principals, want >= %d", len(seen), n)
	}
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing principal id %d", id)
		}
	}
}

func testDeleteOIDCProviderCascade(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p1 := mustInsertPrincipal(t, s, "dp1@example.com")
	p2 := mustInsertPrincipal(t, s, "dp2@example.com")
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "todelete", IssuerURL: "https://i.test", ClientID: "c",
		ClientSecretRef: "x",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	for i, pid := range []store.PrincipalID{p1.ID, p2.ID} {
		if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
			PrincipalID: pid, ProviderName: "todelete",
			Subject: fmt.Sprintf("sub-%d", i),
		}); err != nil {
			t.Fatalf("LinkOIDC: %v", err)
		}
	}
	if err := s.Meta().DeleteOIDCProvider(ctx, store.OIDCProviderID("todelete")); err != nil {
		t.Fatalf("DeleteOIDCProvider: %v", err)
	}
	// Provider gone.
	if _, err := s.Meta().GetOIDCProvider(ctx, "todelete"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetOIDCProvider after delete = %v", err)
	}
	// Links gone.
	for i := 0; i < 2; i++ {
		if _, err := s.Meta().LookupOIDCLink(ctx, "todelete", fmt.Sprintf("sub-%d", i)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("link %d present after provider delete", i)
		}
	}
	// Principals survive.
	if _, err := s.Meta().GetPrincipalByID(ctx, p1.ID); err != nil {
		t.Fatalf("principal 1 vanished: %v", err)
	}
	if _, err := s.Meta().GetPrincipalByID(ctx, p2.ID); err != nil {
		t.Fatalf("principal 2 vanished: %v", err)
	}
	// Second delete -> ErrNotFound.
	if err := s.Meta().DeleteOIDCProvider(ctx, store.OIDCProviderID("todelete")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteOIDCProvider(gone) = %v", err)
	}
}

func testListOIDCProviders(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	names := []string{"aaa", "bbb", "ccc"}
	for _, n := range names {
		if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
			Name: n, IssuerURL: "https://" + n + ".test", ClientID: "c",
			ClientSecretRef: "x",
		}); err != nil {
			t.Fatalf("InsertOIDCProvider(%s): %v", n, err)
		}
	}
	out, err := s.Meta().ListOIDCProviders(ctx)
	if err != nil {
		t.Fatalf("ListOIDCProviders: %v", err)
	}
	if len(out) < len(names) {
		t.Fatalf("len=%d, want >= %d", len(out), len(names))
	}
	// Every seeded name must appear.
	for _, want := range names {
		found := false
		for _, got := range out {
			if got.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing provider %q in list", want)
		}
	}
}

func testUnlinkOIDCNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "unlink@example.com")
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "pv-unlink", IssuerURL: "https://u.test", ClientID: "c",
		ClientSecretRef: "x",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	// Absent -> ErrNotFound.
	if err := s.Meta().UnlinkOIDC(ctx, p.ID, store.OIDCProviderID("pv-unlink")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UnlinkOIDC(absent) = %v", err)
	}
	// Round-trip: link then unlink.
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "pv-unlink", Subject: "sub-u",
	}); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	if err := s.Meta().UnlinkOIDC(ctx, p.ID, store.OIDCProviderID("pv-unlink")); err != nil {
		t.Fatalf("UnlinkOIDC: %v", err)
	}
	// Now it's gone.
	if _, err := s.Meta().LookupOIDCLink(ctx, "pv-unlink", "sub-u"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link still present after unlink")
	}
}

func testAuditLogAppendAndList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p1 := mustInsertPrincipal(t, s, "al1@example.com")
	p2 := mustInsertPrincipal(t, s, "al2@example.com")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	actions := []string{"principal.login", "principal.password.change", "totp.enroll"}
	for i := 0; i < 12; i++ {
		pid := p1.ID
		if i%2 == 1 {
			pid = p2.ID
		}
		if err := s.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:        base.Add(time.Duration(i) * time.Minute),
			ActorKind: store.ActorPrincipal,
			ActorID:   fmt.Sprintf("%d", pid),
			Action:    actions[i%len(actions)],
			Subject:   subjectPrincipal(pid),
			Outcome:   store.OutcomeSuccess,
			Metadata:  map[string]string{"idx": fmt.Sprintf("%d", i)},
		}); err != nil {
			t.Fatalf("AppendAuditLog %d: %v", i, err)
		}
	}

	// All.
	all, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{})
	if err != nil {
		t.Fatalf("ListAuditLog(all): %v", err)
	}
	if len(all) < 12 {
		t.Fatalf("audit all len = %d, want >= 12", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("audit list not ordered: %d then %d", all[i-1].ID, all[i].ID)
		}
	}
	// Filter by principal.
	byP1, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{PrincipalID: p1.ID})
	if err != nil {
		t.Fatalf("ListAuditLog(p1): %v", err)
	}
	if len(byP1) != 6 {
		t.Fatalf("p1 entries = %d, want 6", len(byP1))
	}
	for _, e := range byP1 {
		if e.Subject != subjectPrincipal(p1.ID) {
			t.Fatalf("unexpected subject %q in p1 filter", e.Subject)
		}
	}
	// Filter by action.
	byAction, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{Action: "totp.enroll"})
	if err != nil {
		t.Fatalf("ListAuditLog(action): %v", err)
	}
	if len(byAction) != 4 {
		t.Fatalf("action entries = %d, want 4", len(byAction))
	}
	// Filter by time range: minutes [3, 6) -> minutes 3, 4, 5 = 3 rows.
	byTime, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		Since: base.Add(3 * time.Minute),
		Until: base.Add(6 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ListAuditLog(time): %v", err)
	}
	if len(byTime) != 3 {
		t.Fatalf("time entries = %d, want 3", len(byTime))
	}
	// Verify metadata round-trips.
	first := byTime[0]
	if first.Metadata["idx"] == "" {
		t.Fatalf("metadata idx empty after roundtrip: %+v", first.Metadata)
	}
}

func testPrincipalFlagTOTPEnabled(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "totp-flag@example.com")
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("new principal has TOTPEnabled flag set")
	}
	p.Flags |= store.PrincipalFlagTOTPEnabled
	if err := s.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal set: %v", err)
	}
	got, err := s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after set: %v", err)
	}
	if !got.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("flag did not persist: %b", got.Flags)
	}
	got.Flags &^= store.PrincipalFlagTOTPEnabled
	if err := s.Meta().UpdatePrincipal(ctx, got); err != nil {
		t.Fatalf("UpdatePrincipal clear: %v", err)
	}
	got2, err := s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if got2.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("flag did not clear: %b", got2.Flags)
	}
}

func subjectPrincipal(pid store.PrincipalID) string {
	return fmt.Sprintf("principal:%d", pid)
}

// -- Wave 3 IMAP mailbox surface + Sieve scripts ---------------------

func testGetMailboxByName(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "gmbn@example.com")
	inbox := mustInsertMailbox(t, s, p.ID, "INBOX")
	_ = mustInsertMailbox(t, s, p.ID, "Archive")
	got, err := s.Meta().GetMailboxByName(ctx, p.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	if got.ID != inbox.ID {
		t.Fatalf("got id %d, want %d", got.ID, inbox.ID)
	}
	// Case-sensitive: "inbox" lowercase must not match INBOX.
	if _, err := s.Meta().GetMailboxByName(ctx, p.ID, "inbox"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("case-sensitive lookup = %v, want ErrNotFound", err)
	}
	// Different principal isolation.
	other := mustInsertPrincipal(t, s, "gmbn-other@example.com")
	if _, err := s.Meta().GetMailboxByName(ctx, other.ID, "INBOX"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-principal leak = %v, want ErrNotFound", err)
	}
}

func testListMessagesPagination(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "lmp@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	const n = 5
	for i := 0; i < n; i++ {
		ref := putBlob(t, s, fmt.Sprintf("lmp-%d", i))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
			MailboxID: mb.ID, Blob: ref, Size: ref.Size,
		}); err != nil {
			t.Fatalf("InsertMessage %d: %v", i, err)
		}
	}
	// Empty mailbox returns empty slice.
	other := mustInsertMailbox(t, s, p.ID, "Empty")
	empty, err := s.Meta().ListMessages(ctx, other.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty mailbox len = %d, want 0", len(empty))
	}
	// Full page, UID-ascending.
	msgs, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("full list len = %d, want %d", len(msgs), n)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].UID >= msgs[i].UID {
			t.Fatalf("not UID-ascending at %d: %d then %d", i, msgs[i-1].UID, msgs[i].UID)
		}
	}
	// AfterUID cursor skips earlier UIDs.
	rest, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{AfterUID: msgs[1].UID})
	if err != nil {
		t.Fatalf("ListMessages(AfterUID): %v", err)
	}
	if len(rest) != n-2 {
		t.Fatalf("after-UID len = %d, want %d", len(rest), n-2)
	}
	for _, m := range rest {
		if m.UID <= msgs[1].UID {
			t.Fatalf("page includes UID %d <= cursor %d", m.UID, msgs[1].UID)
		}
	}
	// Limit caps the page.
	capped, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListMessages(Limit): %v", err)
	}
	if len(capped) != 2 {
		t.Fatalf("limited page len = %d, want 2", len(capped))
	}
}

func testSetMailboxSubscribed(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sub@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	// Initial mailbox has no subscribed bit.
	if mb.Attributes&store.MailboxAttrSubscribed != 0 {
		t.Fatalf("fresh mailbox already subscribed: %b", mb.Attributes)
	}
	if err := s.Meta().SetMailboxSubscribed(ctx, mb.ID, true); err != nil {
		t.Fatalf("SetMailboxSubscribed(true): %v", err)
	}
	got, err := s.Meta().GetMailboxByID(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if got.Attributes&store.MailboxAttrSubscribed == 0 {
		t.Fatalf("subscribed bit did not set: %b", got.Attributes)
	}
	if err := s.Meta().SetMailboxSubscribed(ctx, mb.ID, false); err != nil {
		t.Fatalf("SetMailboxSubscribed(false): %v", err)
	}
	got, err = s.Meta().GetMailboxByID(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID after unsubscribe: %v", err)
	}
	if got.Attributes&store.MailboxAttrSubscribed != 0 {
		t.Fatalf("subscribed bit did not clear: %b", got.Attributes)
	}
	// Absent mailbox -> ErrNotFound.
	if err := s.Meta().SetMailboxSubscribed(ctx, store.MailboxID(99999), true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetMailboxSubscribed(absent) = %v, want ErrNotFound", err)
	}
}

func testRenameMailbox(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "rnm@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "OldName")
	_ = mustInsertMailbox(t, s, p.ID, "TakenName")
	if err := s.Meta().RenameMailbox(ctx, mb.ID, "NewName"); err != nil {
		t.Fatalf("RenameMailbox: %v", err)
	}
	if _, err := s.Meta().GetMailboxByName(ctx, p.ID, "OldName"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old name still present: %v", err)
	}
	got, err := s.Meta().GetMailboxByName(ctx, p.ID, "NewName")
	if err != nil {
		t.Fatalf("GetMailboxByName(NewName): %v", err)
	}
	if got.ID != mb.ID {
		t.Fatalf("renamed id = %d, want %d", got.ID, mb.ID)
	}
	// Collision with existing name -> ErrConflict.
	if err := s.Meta().RenameMailbox(ctx, mb.ID, "TakenName"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("RenameMailbox(collision) = %v, want ErrConflict", err)
	}
	// Absent mailbox -> ErrNotFound.
	if err := s.Meta().RenameMailbox(ctx, store.MailboxID(99999), "Whatever"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RenameMailbox(absent) = %v, want ErrNotFound", err)
	}
}

func testSieveScriptEmpty(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sse@example.com")
	got, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript(empty): %v", err)
	}
	if got != "" {
		t.Fatalf("empty script = %q, want \"\"", got)
	}
}

func testSieveScriptRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ssr@example.com")
	const script = `require "fileinto"; fileinto "INBOX.Hello";`
	if err := s.Meta().SetSieveScript(ctx, p.ID, script); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}
	got, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if got != script {
		t.Fatalf("script = %q, want %q", got, script)
	}
	// Setting empty text removes the script.
	if err := s.Meta().SetSieveScript(ctx, p.ID, ""); err != nil {
		t.Fatalf("SetSieveScript(empty): %v", err)
	}
	after, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript after delete: %v", err)
	}
	if after != "" {
		t.Fatalf("after delete = %q, want empty", after)
	}
}

func testSieveScriptOverwrite(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sso@example.com")
	const first = `keep;`
	const second = `require "fileinto"; fileinto "Updated";`
	if err := s.Meta().SetSieveScript(ctx, p.ID, first); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := s.Meta().SetSieveScript(ctx, p.ID, second); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != second {
		t.Fatalf("overwrite = %q, want %q", got, second)
	}
}

func testSieveScriptCascade(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ssc@example.com")
	if err := s.Meta().SetSieveScript(ctx, p.ID, `keep;`); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}
	// Give the principal at least one mailbox/message so DeletePrincipal
	// exercises the full cascade path.
	_ = mustInsertMailbox(t, s, p.ID, "INBOX")
	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	// Recreate the principal; the script must not come back.
	p2 := mustInsertPrincipal(t, s, "ssc@example.com")
	got, err := s.Meta().GetSieveScript(ctx, p2.ID)
	if err != nil {
		t.Fatalf("GetSieveScript after recreate: %v", err)
	}
	if got != "" {
		t.Fatalf("script survived DeletePrincipal: %q", got)
	}
}

// -- Wave 3 admin REST gap-closer methods -----------------------------

func testListAliasesByDomain(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "aliaslist@example.com")
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "a", Domain: "example.com", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias a: %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "b", Domain: "example.com", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias b: %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "c", Domain: "other.test", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias c: %v", err)
	}
	all, err := s.Meta().ListAliases(ctx, "")
	if err != nil {
		t.Fatalf("ListAliases all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListAliases all = %d, want 3", len(all))
	}
	scoped, err := s.Meta().ListAliases(ctx, "example.com")
	if err != nil {
		t.Fatalf("ListAliases example.com: %v", err)
	}
	if len(scoped) != 2 {
		t.Fatalf("ListAliases example.com = %d, want 2", len(scoped))
	}
	for _, a := range scoped {
		if a.Domain != "example.com" {
			t.Fatalf("filter leaked: %+v", a)
		}
	}
}

func testDeleteAliasNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().DeleteAlias(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteAlias(absent) = %v, want ErrNotFound", err)
	}
	p := mustInsertPrincipal(t, s, "aliasdel@example.com")
	a, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "ad", Domain: "example.com", TargetPrincipal: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if err := s.Meta().DeleteAlias(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAlias: %v", err)
	}
	if _, err := s.Meta().ResolveAlias(ctx, "ad", "example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveAlias after delete = %v, want ErrNotFound", err)
	}
}

func testDeleteDomainNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().DeleteDomain(ctx, "absent.example"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteDomain(absent) = %v, want ErrNotFound", err)
	}
	if err := s.Meta().InsertDomain(ctx, store.Domain{Name: "del.example", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	if err := s.Meta().DeleteDomain(ctx, "DEL.EXAMPLE"); err != nil {
		t.Fatalf("DeleteDomain case: %v", err)
	}
	if _, err := s.Meta().GetDomain(ctx, "del.example"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetDomain after delete = %v", err)
	}
}

func testListAPIKeysByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "keys@example.com")
	p2 := mustInsertPrincipal(t, s, "keys2@example.com")
	k1, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p.ID, Hash: "kh1", Name: "k1"})
	if err != nil {
		t.Fatalf("InsertAPIKey 1: %v", err)
	}
	k2, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p.ID, Hash: "kh2", Name: "k2"})
	if err != nil {
		t.Fatalf("InsertAPIKey 2: %v", err)
	}
	if _, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p2.ID, Hash: "kh3", Name: "k3"}); err != nil {
		t.Fatalf("InsertAPIKey 3: %v", err)
	}
	list, err := s.Meta().ListAPIKeysByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAPIKeysByPrincipal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListAPIKeysByPrincipal = %d, want 2", len(list))
	}
	if list[0].ID != k1.ID || list[1].ID != k2.ID {
		t.Fatalf("list order: %+v %+v", list[0], list[1])
	}
}

func testDeleteAPIKeyNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().DeleteAPIKey(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteAPIKey(absent) = %v, want ErrNotFound", err)
	}
	p := mustInsertPrincipal(t, s, "keydel@example.com")
	k, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p.ID, Hash: "zz1", Name: "z"})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	if err := s.Meta().DeleteAPIKey(ctx, k.ID); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	if _, err := s.Meta().GetAPIKeyByHash(ctx, "zz1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAPIKeyByHash after delete = %v", err)
	}
}

func testListOIDCLinksByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "prov1", IssuerURL: "https://issuer1", ClientID: "c1",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider 1: %v", err)
	}
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "prov2", IssuerURL: "https://issuer2", ClientID: "c2",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider 2: %v", err)
	}
	p := mustInsertPrincipal(t, s, "oidclist@example.com")
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "prov1", Subject: "sub-1",
	}); err != nil {
		t.Fatalf("LinkOIDC 1: %v", err)
	}
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "prov2", Subject: "sub-2",
	}); err != nil {
		t.Fatalf("LinkOIDC 2: %v", err)
	}
	list, err := s.Meta().ListOIDCLinksByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListOIDCLinksByPrincipal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListOIDCLinksByPrincipal = %d, want 2", len(list))
	}
	if list[0].ProviderName != "prov1" || list[1].ProviderName != "prov2" {
		t.Fatalf("ordering: %+v", list)
	}
}

// -- Phase 2 Wave 2.0 compliance cases -------------------------------

func mustEnqueue(t *testing.T, s store.Store, item store.QueueItem) store.QueueItemID {
	t.Helper()
	if item.BodyBlobHash == "" {
		ref := putBlob(t, s, "queue-body-"+item.RcptTo)
		item.BodyBlobHash = ref.Hash
	}
	id, err := s.Meta().EnqueueMessage(ctxT(t), item)
	if err != nil {
		t.Fatalf("EnqueueMessage(%s): %v", item.RcptTo, err)
	}
	return id
}

func testQueueEnqueueAndList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-list@example.com")
	id1 := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID,
		MailFrom:    "alice@example.com",
		RcptTo:      "bob@dest.test",
		EnvelopeID:  "env-1",
	})
	id2 := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID,
		MailFrom:    "alice@example.com",
		RcptTo:      "carol@dest.test",
		EnvelopeID:  "env-1",
	})
	if id1 == 0 || id2 == 0 || id1 == id2 {
		t.Fatalf("EnqueueMessage ids: %d %d", id1, id2)
	}
	got, err := s.Meta().GetQueueItem(ctx, id1)
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if got.RcptTo != "bob@dest.test" || got.State != store.QueueStateQueued {
		t.Fatalf("queue row mismatch: %+v", got)
	}
	all, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("queue len = %d, want 2", len(all))
	}
	byEnv, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: "env-1"})
	if err != nil {
		t.Fatalf("ListQueueItems(env): %v", err)
	}
	if len(byEnv) != 2 {
		t.Fatalf("byEnv len = %d, want 2", len(byEnv))
	}
	byDom, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{RecipientDomain: "dest.test"})
	if err != nil {
		t.Fatalf("ListQueueItems(domain): %v", err)
	}
	if len(byDom) != 2 {
		t.Fatalf("byDom len = %d, want 2", len(byDom))
	}
	byOther, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{RecipientDomain: "absent.test"})
	if err != nil {
		t.Fatalf("ListQueueItems(domain absent): %v", err)
	}
	if len(byOther) != 0 {
		t.Fatalf("byOther len = %d, want 0", len(byOther))
	}
}

func testQueueIdempotency(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-idem@example.com")
	ref := putBlob(t, s, "idem-body")
	id1, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
		PrincipalID:    p.ID,
		MailFrom:       "alice@example.com",
		RcptTo:         "x@dest.test",
		EnvelopeID:     "env-idem",
		BodyBlobHash:   ref.Hash,
		IdempotencyKey: "submit-7",
	})
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	id2, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
		PrincipalID:    p.ID,
		MailFrom:       "alice@example.com",
		RcptTo:         "x@dest.test",
		EnvelopeID:     "env-idem",
		BodyBlobHash:   ref.Hash,
		IdempotencyKey: "submit-7",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second enqueue err = %v, want ErrConflict", err)
	}
	if id2 != id1 {
		t.Fatalf("idempotent dedupe returned id=%d, want %d", id2, id1)
	}
}

func testQueueClaimDueTransitionsState(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-claim@example.com")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	id1 := mustEnqueue(t, s, store.QueueItem{
		PrincipalID:   p.ID,
		MailFrom:      "alice@example.com",
		RcptTo:        "due@dest.test",
		EnvelopeID:    "env-due",
		NextAttemptAt: now.Add(-time.Minute),
	})
	_ = mustEnqueue(t, s, store.QueueItem{
		PrincipalID:   p.ID,
		MailFrom:      "alice@example.com",
		RcptTo:        "future@dest.test",
		EnvelopeID:    "env-future",
		NextAttemptAt: now.Add(time.Hour),
	})
	due, err := s.Meta().ClaimDueQueueItems(ctx, now, 10)
	if err != nil {
		t.Fatalf("ClaimDueQueueItems: %v", err)
	}
	if len(due) != 1 || due[0].ID != id1 {
		t.Fatalf("claim returned %+v, want only id=%d", due, id1)
	}
	if due[0].State != store.QueueStateInflight {
		t.Fatalf("claimed row state = %v, want inflight", due[0].State)
	}
	got, err := s.Meta().GetQueueItem(ctx, id1)
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if got.State != store.QueueStateInflight {
		t.Fatalf("row state after claim = %v", got.State)
	}
	if got.LastAttemptAt.IsZero() {
		t.Fatalf("LastAttemptAt was not stamped")
	}
}

func testQueueCompleteSuccessVsFailure(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-complete@example.com")
	idOK := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "ok@d.test", EnvelopeID: "ok",
	})
	idFail := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "fail@d.test", EnvelopeID: "fail",
	})
	if err := s.Meta().CompleteQueueItem(ctx, idOK, true, ""); err != nil {
		t.Fatalf("Complete OK: %v", err)
	}
	if err := s.Meta().CompleteQueueItem(ctx, idFail, false, "550 nope"); err != nil {
		t.Fatalf("Complete FAIL: %v", err)
	}
	gotOK, err := s.Meta().GetQueueItem(ctx, idOK)
	if err != nil {
		t.Fatalf("Get OK: %v", err)
	}
	if gotOK.State != store.QueueStateDone {
		t.Fatalf("ok state = %v, want done", gotOK.State)
	}
	gotFail, err := s.Meta().GetQueueItem(ctx, idFail)
	if err != nil {
		t.Fatalf("Get FAIL: %v", err)
	}
	if gotFail.State != store.QueueStateFailed {
		t.Fatalf("fail state = %v, want failed", gotFail.State)
	}
	if gotFail.LastError == "" {
		t.Fatalf("LastError empty")
	}
	if err := s.Meta().CompleteQueueItem(ctx, store.QueueItemID(99999), true, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Complete absent = %v, want ErrNotFound", err)
	}
}

func testQueueRescheduleBumpsAttempts(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-resch@example.com")
	id := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "x@d.test", EnvelopeID: "r",
	})
	next := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().RescheduleQueueItem(ctx, id, next, "451 try later"); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	got, err := s.Meta().GetQueueItem(ctx, id)
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if got.State != store.QueueStateDeferred {
		t.Fatalf("state = %v, want deferred", got.State)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
	if !got.NextAttemptAt.Equal(next) {
		t.Fatalf("NextAttemptAt = %v, want %v", got.NextAttemptAt, next)
	}
	if got.LastError == "" {
		t.Fatalf("LastError empty after reschedule")
	}
	// Reschedule again should bump attempts.
	if err := s.Meta().RescheduleQueueItem(ctx, id, next.Add(time.Hour), "451 still later"); err != nil {
		t.Fatalf("Reschedule 2: %v", err)
	}
	got, err = s.Meta().GetQueueItem(ctx, id)
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts after 2nd reschedule = %d, want 2", got.Attempts)
	}
}

func testQueueHoldRelease(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-hold@example.com")
	id := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "h@d.test", EnvelopeID: "h",
	})
	if err := s.Meta().HoldQueueItem(ctx, id); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	got, _ := s.Meta().GetQueueItem(ctx, id)
	if got.State != store.QueueStateHeld {
		t.Fatalf("state = %v, want held", got.State)
	}
	if err := s.Meta().ReleaseQueueItem(ctx, id); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got, _ = s.Meta().GetQueueItem(ctx, id)
	if got.State != store.QueueStateQueued {
		t.Fatalf("state after release = %v, want queued", got.State)
	}
	if err := s.Meta().HoldQueueItem(ctx, store.QueueItemID(99999)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Hold absent = %v, want ErrNotFound", err)
	}
}

func testQueueDeleteCascadeOnPrincipalDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-cascade@example.com")
	id := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "c@d.test", EnvelopeID: "c",
	})
	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if _, err := s.Meta().GetQueueItem(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("queue row still present after principal delete: %v", err)
	}
}

func testQueueCountByState(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-count@example.com")
	id1 := mustEnqueue(t, s, store.QueueItem{PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "1@d.test", EnvelopeID: "c1"})
	id2 := mustEnqueue(t, s, store.QueueItem{PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "2@d.test", EnvelopeID: "c2"})
	if err := s.Meta().HoldQueueItem(ctx, id2); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	counts, err := s.Meta().CountQueueByState(ctx)
	if err != nil {
		t.Fatalf("CountQueueByState: %v", err)
	}
	if counts[store.QueueStateQueued] != 1 {
		t.Fatalf("queued count = %d, want 1", counts[store.QueueStateQueued])
	}
	if counts[store.QueueStateHeld] != 1 {
		t.Fatalf("held count = %d, want 1", counts[store.QueueStateHeld])
	}
	_ = id1
}

func testDKIMUpsertAndList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	keyA := store.DKIMKey{
		Domain: "ex.test", Selector: "sel-a", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "PRIV-A", PublicKeyB64: "PUB-A", Status: store.DKIMKeyStatusActive,
	}
	keyB := store.DKIMKey{
		Domain: "ex.test", Selector: "sel-b", Algorithm: store.DKIMAlgorithmEd25519SHA256,
		PrivateKeyPEM: "PRIV-B", PublicKeyB64: "PUB-B", Status: store.DKIMKeyStatusRetiring,
	}
	if err := s.Meta().UpsertDKIMKey(ctx, keyA); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	if err := s.Meta().UpsertDKIMKey(ctx, keyB); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}
	// Idempotent re-upsert with status change.
	keyA.Status = store.DKIMKeyStatusRetiring
	if err := s.Meta().UpsertDKIMKey(ctx, keyA); err != nil {
		t.Fatalf("Upsert A (retire): %v", err)
	}
	list, err := s.Meta().ListDKIMKeys(ctx, "ex.test")
	if err != nil {
		t.Fatalf("ListDKIMKeys: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	for _, k := range list {
		if k.Status != store.DKIMKeyStatusRetiring {
			t.Fatalf("after upsert, %q status = %v", k.Selector, k.Status)
		}
	}
}

func testDKIMRotateOneTx(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().UpsertDKIMKey(ctx, store.DKIMKey{
		Domain: "rot.test", Selector: "old", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "OLD", PublicKeyB64: "OLDPUB", Status: store.DKIMKeyStatusActive,
	}); err != nil {
		t.Fatalf("Upsert old: %v", err)
	}
	if err := s.Meta().RotateDKIMKey(ctx, "rot.test", "old", store.DKIMKey{
		Domain: "rot.test", Selector: "new", Algorithm: store.DKIMAlgorithmEd25519SHA256,
		PrivateKeyPEM: "NEW", PublicKeyB64: "NEWPUB",
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	list, err := s.Meta().ListDKIMKeys(ctx, "rot.test")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var oldStatus, newStatus store.DKIMKeyStatus
	for _, k := range list {
		switch k.Selector {
		case "old":
			oldStatus = k.Status
		case "new":
			newStatus = k.Status
		}
	}
	if oldStatus != store.DKIMKeyStatusRetiring {
		t.Fatalf("old status = %v, want retiring", oldStatus)
	}
	if newStatus != store.DKIMKeyStatusActive {
		t.Fatalf("new status = %v, want active", newStatus)
	}
	// Rotating an absent selector returns NotFound.
	err = s.Meta().RotateDKIMKey(ctx, "rot.test", "ghost", store.DKIMKey{
		Domain: "rot.test", Selector: "n2", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "P", PublicKeyB64: "P",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Rotate ghost = %v, want ErrNotFound", err)
	}
}

func testDKIMActiveLookup(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().GetActiveDKIMKey(ctx, "absent.test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetActiveDKIMKey(absent) = %v", err)
	}
	if err := s.Meta().UpsertDKIMKey(ctx, store.DKIMKey{
		Domain: "act.test", Selector: "s1", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "P", PublicKeyB64: "B", Status: store.DKIMKeyStatusActive,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Meta().GetActiveDKIMKey(ctx, "act.test")
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	if got.Selector != "s1" {
		t.Fatalf("selector = %q, want s1", got.Selector)
	}
}

func testACMEAccountOrderCertLifecycle(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	acc, err := s.Meta().UpsertACMEAccount(ctx, store.ACMEAccount{
		DirectoryURL:  "https://acme.test/dir",
		ContactEmail:  "ops@example.com",
		AccountKeyPEM: "ACCT-PEM",
	})
	if err != nil {
		t.Fatalf("UpsertACMEAccount: %v", err)
	}
	if acc.ID == 0 {
		t.Fatalf("account id unset")
	}
	// Idempotent re-upsert: same (directory_url, contact) returns same id.
	acc2, err := s.Meta().UpsertACMEAccount(ctx, store.ACMEAccount{
		DirectoryURL:  "https://acme.test/dir",
		ContactEmail:  "ops@example.com",
		AccountKeyPEM: "ACCT-PEM-V2",
		KID:           "kid-1",
	})
	if err != nil {
		t.Fatalf("UpsertACMEAccount v2: %v", err)
	}
	if acc2.ID != acc.ID {
		t.Fatalf("upsert id = %d, want %d", acc2.ID, acc.ID)
	}
	got, err := s.Meta().GetACMEAccount(ctx, "https://acme.test/dir", "ops@example.com")
	if err != nil {
		t.Fatalf("GetACMEAccount: %v", err)
	}
	if got.KID != "kid-1" {
		t.Fatalf("kid = %q after upsert", got.KID)
	}

	// Order.
	order, err := s.Meta().InsertACMEOrder(ctx, store.ACMEOrder{
		AccountID:     acc.ID,
		Hostnames:     []string{"a.example.com", "b.example.com"},
		Status:        store.ACMEOrderStatusPending,
		OrderURL:      "https://acme.test/order/1",
		FinalizeURL:   "https://acme.test/order/1/finalize",
		ChallengeType: store.ChallengeTypeHTTP01,
	})
	if err != nil {
		t.Fatalf("InsertACMEOrder: %v", err)
	}
	order.Status = store.ACMEOrderStatusValid
	order.CertificateURL = "https://acme.test/cert/1"
	if err := s.Meta().UpdateACMEOrder(ctx, order); err != nil {
		t.Fatalf("UpdateACMEOrder: %v", err)
	}
	gotOrder, err := s.Meta().GetACMEOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetACMEOrder: %v", err)
	}
	if gotOrder.Status != store.ACMEOrderStatusValid {
		t.Fatalf("status = %v", gotOrder.Status)
	}
	if len(gotOrder.Hostnames) != 2 {
		t.Fatalf("hostnames = %v", gotOrder.Hostnames)
	}
	byStatus, err := s.Meta().ListACMEOrdersByStatus(ctx, store.ACMEOrderStatusValid)
	if err != nil {
		t.Fatalf("ListACMEOrdersByStatus: %v", err)
	}
	if len(byStatus) != 1 {
		t.Fatalf("byStatus len = %d, want 1", len(byStatus))
	}

	// Cert.
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname:      "a.example.com",
		ChainPEM:      "CHAIN",
		PrivateKeyPEM: "KEY",
		NotBefore:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:      notAfter,
		Issuer:        "Test CA",
		OrderID:       order.ID,
	}); err != nil {
		t.Fatalf("UpsertACMECert: %v", err)
	}
	cert, err := s.Meta().GetACMECert(ctx, "a.example.com")
	if err != nil {
		t.Fatalf("GetACMECert: %v", err)
	}
	if !cert.NotAfter.Equal(notAfter) {
		t.Fatalf("NotAfter = %v", cert.NotAfter)
	}
}

func testACMECertExpiringList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	soon := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	far := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname: "soon.test", ChainPEM: "C", PrivateKeyPEM: "K",
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: soon,
	}); err != nil {
		t.Fatalf("Upsert soon: %v", err)
	}
	if err := s.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname: "far.test", ChainPEM: "C", PrivateKeyPEM: "K",
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: far,
	}); err != nil {
		t.Fatalf("Upsert far: %v", err)
	}
	expiring, err := s.Meta().ListACMECertsExpiringBefore(ctx, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ListACMECertsExpiringBefore: %v", err)
	}
	if len(expiring) != 1 || expiring[0].Hostname != "soon.test" {
		t.Fatalf("expiring = %+v", expiring)
	}
}

func testWebhookCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	w, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind:    store.WebhookOwnerDomain,
		OwnerID:      "example.com",
		TargetURL:    "https://hook.example.com/incoming",
		HMACSecret:   []byte("secret"),
		DeliveryMode: store.DeliveryModeInline,
		RetryPolicy:  store.RetryPolicy{MaxAttempts: 5, InitialBackoffMS: 1000},
		Active:       true,
	})
	if err != nil {
		t.Fatalf("InsertWebhook: %v", err)
	}
	if w.ID == 0 || w.CreatedAt.IsZero() {
		t.Fatalf("webhook = %+v", w)
	}
	got, err := s.Meta().GetWebhook(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWebhook: %v", err)
	}
	if got.RetryPolicy.MaxAttempts != 5 {
		t.Fatalf("RetryPolicy roundtrip = %+v", got.RetryPolicy)
	}
	got.Active = false
	got.TargetURL = "https://hook.example.com/v2"
	if err := s.Meta().UpdateWebhook(ctx, got); err != nil {
		t.Fatalf("UpdateWebhook: %v", err)
	}
	got2, err := s.Meta().GetWebhook(ctx, w.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got2.Active || got2.TargetURL != "https://hook.example.com/v2" {
		t.Fatalf("update did not persist: %+v", got2)
	}
	listed, err := s.Meta().ListWebhooks(ctx, store.WebhookOwnerDomain, "example.com")
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListWebhooks = %d, want 1", len(listed))
	}
	if err := s.Meta().DeleteWebhook(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if err := s.Meta().DeleteWebhook(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteWebhook absent = %v", err)
	}
}

func testWebhookActiveForDomain(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "wh-user@example.com")
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerDomain, OwnerID: "example.com",
		TargetURL: "https://x.test/d", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: true,
	}); err != nil {
		t.Fatalf("Insert domain: %v", err)
	}
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerPrincipal, OwnerID: fmt.Sprintf("%d", p.ID),
		TargetURL: "https://x.test/p", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: true,
	}); err != nil {
		t.Fatalf("Insert principal: %v", err)
	}
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerDomain, OwnerID: "other.test",
		TargetURL: "https://x.test/o", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: true,
	}); err != nil {
		t.Fatalf("Insert other: %v", err)
	}
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerDomain, OwnerID: "example.com",
		TargetURL: "https://x.test/inactive", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: false,
	}); err != nil {
		t.Fatalf("Insert inactive: %v", err)
	}
	hooks, err := s.Meta().ListActiveWebhooksForDomain(ctx, "example.com")
	if err != nil {
		t.Fatalf("ListActiveWebhooksForDomain: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("active hooks = %d, want 2 (one domain, one principal): %+v", len(hooks), hooks)
	}
}

func testDMARCInsertAndAggregate(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	begin := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	report := store.DMARCReport{
		ReceivedAt:    time.Date(2026, 5, 2, 1, 0, 0, 0, time.UTC),
		ReporterEmail: "noreply@reporter.test",
		ReporterOrg:   "Reporter Org",
		ReportID:      "rpt-001",
		Domain:        "ex.test",
		DateBegin:     begin,
		DateEnd:       end,
		XMLBlobHash:   strings.Repeat("a", 64),
		ParsedOK:      true,
	}
	rows := []store.DMARCRow{
		{SourceIP: "1.1.1.1", Count: 10, Disposition: 0, SPFAligned: true, DKIMAligned: true, HeaderFrom: "ex.test"},
		{SourceIP: "2.2.2.2", Count: 5, Disposition: 1, SPFAligned: false, DKIMAligned: false, HeaderFrom: "ex.test"},
	}
	id, err := s.Meta().InsertDMARCReport(ctx, report, rows)
	if err != nil {
		t.Fatalf("InsertDMARCReport: %v", err)
	}
	if id == 0 {
		t.Fatalf("id = 0")
	}
	gotRep, gotRows, err := s.Meta().GetDMARCReport(ctx, id)
	if err != nil {
		t.Fatalf("GetDMARCReport: %v", err)
	}
	if gotRep.ReporterOrg != "Reporter Org" {
		t.Fatalf("reporter = %q", gotRep.ReporterOrg)
	}
	if len(gotRows) != 2 {
		t.Fatalf("rows = %d, want 2", len(gotRows))
	}
	listed, err := s.Meta().ListDMARCReports(ctx, store.DMARCReportFilter{Domain: "ex.test"})
	if err != nil {
		t.Fatalf("ListDMARCReports: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %d, want 1", len(listed))
	}
	agg, err := s.Meta().DMARCAggregate(ctx, "ex.test", begin, end.Add(time.Hour))
	if err != nil {
		t.Fatalf("DMARCAggregate: %v", err)
	}
	if len(agg) != 2 {
		t.Fatalf("agg = %d, want 2", len(agg))
	}
	var totalCount int64
	for _, a := range agg {
		totalCount += a.Count
	}
	if totalCount != 15 {
		t.Fatalf("total count = %d, want 15", totalCount)
	}
}

func testDMARCDeduplicates(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	report := store.DMARCReport{
		ReceivedAt:    time.Now().UTC(),
		ReporterEmail: "n@r.test", ReporterOrg: "Org", ReportID: "dup-1",
		Domain: "d.test", DateBegin: time.Now().UTC(), DateEnd: time.Now().UTC(),
		XMLBlobHash: "hash", ParsedOK: true,
	}
	if _, err := s.Meta().InsertDMARCReport(ctx, report, nil); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.Meta().InsertDMARCReport(ctx, report, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("dup insert = %v, want ErrConflict", err)
	}
}

func testMailboxACLGrantListRevoke(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "acl-owner@example.com")
	other := mustInsertPrincipal(t, s, "acl-other@example.com")
	mb := mustInsertMailbox(t, s, owner.ID, "Shared")
	pid := other.ID
	if err := s.Meta().SetMailboxACL(ctx, mb.ID, &pid,
		store.ACLRightLookup|store.ACLRightRead, owner.ID); err != nil {
		t.Fatalf("SetMailboxACL: %v", err)
	}
	got, err := s.Meta().GetMailboxACL(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("acl rows = %d, want 1", len(got))
	}
	if !got[0].Rights.CanRead() {
		t.Fatalf("Rights.CanRead = false, want true")
	}
	accessible, err := s.Meta().ListMailboxesAccessibleBy(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListMailboxesAccessibleBy: %v", err)
	}
	if len(accessible) != 1 || accessible[0].ID != mb.ID {
		t.Fatalf("accessible = %+v", accessible)
	}
	// Update rights on existing row.
	if err := s.Meta().SetMailboxACL(ctx, mb.ID, &pid,
		store.ACLRightLookup|store.ACLRightRead|store.ACLRightWrite, owner.ID); err != nil {
		t.Fatalf("SetMailboxACL update: %v", err)
	}
	got, _ = s.Meta().GetMailboxACL(ctx, mb.ID)
	if len(got) != 1 {
		t.Fatalf("after update rows = %d", len(got))
	}
	if !got[0].Rights.CanWrite() {
		t.Fatalf("CanWrite false after update")
	}
	if err := s.Meta().RemoveMailboxACL(ctx, mb.ID, &pid); err != nil {
		t.Fatalf("RemoveMailboxACL: %v", err)
	}
	if err := s.Meta().RemoveMailboxACL(ctx, mb.ID, &pid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RemoveMailboxACL absent = %v", err)
	}
	accessible, _ = s.Meta().ListMailboxesAccessibleBy(ctx, other.ID)
	if len(accessible) != 0 {
		t.Fatalf("accessible after revoke = %+v", accessible)
	}
}

func testMailboxACLAnyoneRow(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "acl-anyone-owner@example.com")
	other := mustInsertPrincipal(t, s, "acl-anyone-other@example.com")
	mb := mustInsertMailbox(t, s, owner.ID, "Public")
	if err := s.Meta().SetMailboxACL(ctx, mb.ID, nil,
		store.ACLRightLookup|store.ACLRightRead, owner.ID); err != nil {
		t.Fatalf("SetMailboxACL anyone: %v", err)
	}
	rows, err := s.Meta().GetMailboxACL(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	if len(rows) != 1 || rows[0].PrincipalID != nil {
		t.Fatalf("anyone row = %+v", rows)
	}
	// Anyone row grants other principals access.
	accessible, err := s.Meta().ListMailboxesAccessibleBy(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListMailboxesAccessibleBy: %v", err)
	}
	if len(accessible) != 1 {
		t.Fatalf("anyone access = %+v", accessible)
	}
	// Remove anyone row.
	if err := s.Meta().RemoveMailboxACL(ctx, mb.ID, nil); err != nil {
		t.Fatalf("Remove anyone: %v", err)
	}
}

func testJMAPStatesIncrementAtomic(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "jmap@example.com")
	st, err := s.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	if st.Mailbox != 0 || st.Email != 0 {
		t.Fatalf("initial state = %+v", st)
	}
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindEmail); err != nil {
				t.Errorf("IncrementJMAPState: %v", err)
			}
		}()
	}
	wg.Wait()
	st, err = s.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates after increments: %v", err)
	}
	if st.Email != n {
		t.Fatalf("Email state = %d, want %d", st.Email, n)
	}
	// Independent kinds must not interfere.
	for i := 0; i < 3; i++ {
		if _, err := s.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindMailbox); err != nil {
			t.Fatalf("Increment Mailbox: %v", err)
		}
	}
	st, _ = s.Meta().GetJMAPStates(ctx, p.ID)
	if st.Mailbox != 3 {
		t.Fatalf("Mailbox state = %d, want 3", st.Mailbox)
	}
	if st.Email != n {
		t.Fatalf("Email state = %d, want %d (mailbox bumps must not affect email)", st.Email, n)
	}
}

func testTLSRPTAppendAndRange(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := s.Meta().AppendTLSRPTFailure(ctx, store.TLSRPTFailure{
			RecordedAt:           base.Add(time.Duration(i) * time.Hour),
			PolicyDomain:         "ex.test",
			ReceivingMTAHostname: "mx.peer.test",
			FailureType:          store.TLSRPTFailureMTASTS,
			FailureCode:          fmt.Sprintf("F%d", i),
			FailureDetailJSON:    `{"detail":"x"}`,
		}); err != nil {
			t.Fatalf("AppendTLSRPTFailure %d: %v", i, err)
		}
	}
	if err := s.Meta().AppendTLSRPTFailure(ctx, store.TLSRPTFailure{
		RecordedAt: base, PolicyDomain: "other.test", FailureType: store.TLSRPTFailureDANE,
	}); err != nil {
		t.Fatalf("AppendTLSRPTFailure other: %v", err)
	}
	rng, err := s.Meta().ListTLSRPTFailures(ctx, "ex.test", base.Add(time.Hour), base.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("ListTLSRPTFailures: %v", err)
	}
	if len(rng) != 3 {
		t.Fatalf("range len = %d, want 3", len(rng))
	}
	for i := 1; i < len(rng); i++ {
		if rng[i].RecordedAt.Before(rng[i-1].RecordedAt) {
			t.Fatalf("not ordered: %v then %v", rng[i-1].RecordedAt, rng[i].RecordedAt)
		}
	}
	all, err := s.Meta().ListTLSRPTFailures(ctx, "other.test", base, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListTLSRPTFailures other: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("other domain len = %d, want 1", len(all))
	}
}

// -- Wave 2.2.5 JMAP persistence ------------------------------------

func testEmailSubmissionInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esa@example.com")
	row := store.EmailSubmissionRow{
		ID:          "env-001",
		EnvelopeID:  store.EnvelopeID("env-001"),
		PrincipalID: p.ID,
		IdentityID:  "default",
		EmailID:     store.MessageID(42),
		ThreadID:    "T1",
		SendAtUs:    time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		CreatedAtUs: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		UndoStatus:  "pending",
		Properties:  []byte(`{"x":1}`),
	}
	if err := s.Meta().InsertEmailSubmission(ctx, row); err != nil {
		t.Fatalf("InsertEmailSubmission: %v", err)
	}
	got, err := s.Meta().GetEmailSubmission(ctx, "env-001")
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if got.ID != row.ID || got.EnvelopeID != row.EnvelopeID || got.PrincipalID != row.PrincipalID {
		t.Fatalf("identity mismatch: got %+v want %+v", got, row)
	}
	if got.IdentityID != "default" || got.EmailID != 42 || got.ThreadID != "T1" {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if got.UndoStatus != "pending" {
		t.Fatalf("undoStatus = %q, want pending", got.UndoStatus)
	}
	if string(got.Properties) != `{"x":1}` {
		t.Fatalf("properties = %q, want {\"x\":1}", got.Properties)
	}
	// Duplicate insert.
	if err := s.Meta().InsertEmailSubmission(ctx, row); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate Insert: err = %v, want ErrConflict", err)
	}
	// Missing.
	if _, err := s.Meta().GetEmailSubmission(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
}

func testEmailSubmissionListFilters(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esl@example.com")
	q := mustInsertPrincipal(t, s, "other@example.com")
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id, identity, thread string, eid store.MessageID, send time.Time, undo string, owner store.PrincipalID) {
		err := s.Meta().InsertEmailSubmission(ctx, store.EmailSubmissionRow{
			ID:          id,
			EnvelopeID:  store.EnvelopeID(id),
			PrincipalID: owner,
			IdentityID:  identity,
			EmailID:     eid,
			ThreadID:    thread,
			SendAtUs:    send.UnixMicro(),
			CreatedAtUs: send.UnixMicro(),
			UndoStatus:  undo,
		})
		if err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	mk("a", "default", "T1", 1, base, "pending", p.ID)
	mk("b", "alt", "T1", 2, base.Add(time.Hour), "final", p.ID)
	mk("c", "default", "T2", 3, base.Add(2*time.Hour), "pending", p.ID)
	mk("d", "default", "T1", 4, base.Add(3*time.Hour), "canceled", p.ID)
	mk("e", "default", "T1", 5, base.Add(4*time.Hour), "pending", q.ID)
	// Principal isolation: e is q's, must never appear in p's list.
	all, err := s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{})
	if err != nil {
		t.Fatalf("ListEmailSubmissions: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("all len = %d, want 4", len(all))
	}
	// Default sort is SendAtUs ASC.
	for i := 1; i < len(all); i++ {
		if all[i].SendAtUs < all[i-1].SendAtUs {
			t.Fatalf("not ascending: %v then %v", all[i-1].SendAtUs, all[i].SendAtUs)
		}
	}
	// IdentityIDs.
	got, err := s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{IdentityIDs: []string{"alt"}})
	if err != nil {
		t.Fatalf("List(IdentityIDs): %v", err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("IdentityIDs filter: %+v", got)
	}
	// EmailIDs.
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{EmailIDs: []store.MessageID{1, 4}})
	if err != nil {
		t.Fatalf("List(EmailIDs): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("EmailIDs filter len = %d, want 2", len(got))
	}
	// ThreadIDs.
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{ThreadIDs: []string{"T2"}})
	if err != nil {
		t.Fatalf("List(ThreadIDs): %v", err)
	}
	if len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("ThreadIDs filter: %+v", got)
	}
	// UndoStatus.
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{UndoStatus: "pending"})
	if err != nil {
		t.Fatalf("List(UndoStatus): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("UndoStatus pending len = %d, want 2", len(got))
	}
	// Time range: After base.Add(30m) and Before base.Add(2h30m).
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{
		AfterUs:  base.Add(30 * time.Minute).UnixMicro(),
		BeforeUs: base.Add(2*time.Hour + 30*time.Minute).UnixMicro(),
	})
	if err != nil {
		t.Fatalf("List(time range): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("time range len = %d, want 2", len(got))
	}
}

func testEmailSubmissionUpdateUndoStatus(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esuus@example.com")
	row := store.EmailSubmissionRow{
		ID:          "env-u1",
		EnvelopeID:  store.EnvelopeID("env-u1"),
		PrincipalID: p.ID,
		IdentityID:  "default",
		EmailID:     1,
		SendAtUs:    time.Now().UnixMicro(),
		CreatedAtUs: time.Now().UnixMicro(),
		UndoStatus:  "pending",
	}
	if err := s.Meta().InsertEmailSubmission(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().UpdateEmailSubmissionUndoStatus(ctx, "env-u1", "canceled"); err != nil {
		t.Fatalf("UpdateEmailSubmissionUndoStatus: %v", err)
	}
	got, err := s.Meta().GetEmailSubmission(ctx, "env-u1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UndoStatus != "canceled" {
		t.Fatalf("undoStatus = %q, want canceled", got.UndoStatus)
	}
	// Missing row → ErrNotFound.
	if err := s.Meta().UpdateEmailSubmissionUndoStatus(ctx, "nope", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update missing: err = %v, want ErrNotFound", err)
	}
}

func testEmailSubmissionDeleteNotFoundAfter(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esdel@example.com")
	row := store.EmailSubmissionRow{
		ID:          "env-d1",
		EnvelopeID:  store.EnvelopeID("env-d1"),
		PrincipalID: p.ID,
		IdentityID:  "default",
		EmailID:     1,
		SendAtUs:    time.Now().UnixMicro(),
		CreatedAtUs: time.Now().UnixMicro(),
	}
	if err := s.Meta().InsertEmailSubmission(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeleteEmailSubmission(ctx, "env-d1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Meta().GetEmailSubmission(ctx, "env-d1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	// Second delete returns ErrNotFound.
	if err := s.Meta().DeleteEmailSubmission(ctx, "env-d1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete twice: err = %v, want ErrNotFound", err)
	}
}

func testJMAPIdentityInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-rt@example.com")
	row := store.JMAPIdentity{
		ID:            "1",
		PrincipalID:   p.ID,
		Name:          "Display",
		Email:         "ji-rt@example.com",
		ReplyToJSON:   []byte(`[{"email":"reply@example.com"}]`),
		BccJSON:       []byte(`[{"email":"bcc@example.com"}]`),
		TextSignature: "sig",
		HTMLSignature: "<p>sig</p>",
		MayDelete:     true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, "1")
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if got.PrincipalID != p.ID || got.Name != "Display" || got.Email != "ji-rt@example.com" {
		t.Fatalf("scalar mismatch: %+v", got)
	}
	if string(got.ReplyToJSON) != `[{"email":"reply@example.com"}]` {
		t.Fatalf("ReplyToJSON = %q", got.ReplyToJSON)
	}
	if string(got.BccJSON) != `[{"email":"bcc@example.com"}]` {
		t.Fatalf("BccJSON = %q", got.BccJSON)
	}
	if got.TextSignature != "sig" || got.HTMLSignature != "<p>sig</p>" {
		t.Fatalf("signatures: %+v", got)
	}
	if !got.MayDelete {
		t.Fatalf("MayDelete = false, want true")
	}
	if got.CreatedAtUs == 0 || got.UpdatedAtUs == 0 {
		t.Fatalf("timestamps not populated: %+v", got)
	}
	// Conflict.
	if err := s.Meta().InsertJMAPIdentity(ctx, row); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate Insert: err = %v, want ErrConflict", err)
	}
	if _, err := s.Meta().GetJMAPIdentity(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
}

func testJMAPIdentityListByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-l@example.com")
	q := mustInsertPrincipal(t, s, "ji-l-other@example.com")
	for _, row := range []store.JMAPIdentity{
		{ID: "10", PrincipalID: p.ID, Email: "a@example.com", MayDelete: true},
		{ID: "11", PrincipalID: p.ID, Email: "b@example.com", MayDelete: true},
		{ID: "20", PrincipalID: q.ID, Email: "c@example.com", MayDelete: true},
	} {
		if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
			t.Fatalf("Insert %s: %v", row.ID, err)
		}
	}
	got, err := s.Meta().ListJMAPIdentities(ctx, p.ID)
	if err != nil {
		t.Fatalf("List p: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("p list len = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.PrincipalID != p.ID {
			t.Fatalf("foreign principal leaked: %+v", r)
		}
	}
	got, err = s.Meta().ListJMAPIdentities(ctx, q.ID)
	if err != nil {
		t.Fatalf("List q: %v", err)
	}
	if len(got) != 1 || got[0].ID != "20" {
		t.Fatalf("q list = %+v", got)
	}
}

func testJMAPIdentityUpdateRoundtrips(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-up@example.com")
	row := store.JMAPIdentity{
		ID: "100", PrincipalID: p.ID, Email: "ji-up@example.com",
		Name: "Old", TextSignature: "old", MayDelete: true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	row.Name = "New"
	row.TextSignature = "new"
	row.HTMLSignature = "<b>new</b>"
	row.ReplyToJSON = []byte(`[{"email":"r@x"}]`)
	row.BccJSON = []byte(`[{"email":"b@x"}]`)
	if err := s.Meta().UpdateJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("UpdateJMAPIdentity: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, "100")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "New" || got.TextSignature != "new" || got.HTMLSignature != "<b>new</b>" {
		t.Fatalf("update did not stick: %+v", got)
	}
	if string(got.ReplyToJSON) != `[{"email":"r@x"}]` || string(got.BccJSON) != `[{"email":"b@x"}]` {
		t.Fatalf("json fields: %+v", got)
	}
	// Update of missing row.
	row.ID = "missing"
	if err := s.Meta().UpdateJMAPIdentity(ctx, row); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update missing: err = %v, want ErrNotFound", err)
	}
}

func testJMAPIdentityDeleteNotFoundAfter(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-del@example.com")
	row := store.JMAPIdentity{
		ID: "200", PrincipalID: p.ID, Email: "ji-del@example.com", MayDelete: true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeleteJMAPIdentity(ctx, "200"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Meta().GetJMAPIdentity(ctx, "200"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	if err := s.Meta().DeleteJMAPIdentity(ctx, "200"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete twice: err = %v, want ErrNotFound", err)
	}
}
