package storetest

// Sub-account substrate compliance tests (issue #227, REQ-SUBACCT-01..06,
// REQ-SUBACCT-11 store half only). The JMAP-facing surface
// (session.accounts, per-account state strings, capability gating) is a
// later phase owned by jmap-implementor; these tests exercise only the
// store contract this package exposes: InsertSubPrincipal /
// ListSubPrincipals / GetSubPrincipalParent, quota attribution
// (REQ-SUBACCT-05), admin-list exclusion (REQ-SUBACCT-06), and cascading
// deletion of a parent's sub-accounts and their mail (REQ-SUBACCT-06).

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

func testSubPrincipals_InsertListResolve(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	parent := mustInsertPrincipal(t, s, "subparent@example.com")

	sub1, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "Imported@Example.com",
		DisplayName:    "Imported Identity",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	if sub1.ID == 0 {
		t.Fatalf("sub1.ID unset")
	}
	if sub1.Kind != store.PrincipalKindSubAccount {
		t.Fatalf("sub1.Kind = %v, want PrincipalKindSubAccount", sub1.Kind)
	}
	if !sub1.IsSubAccount() {
		t.Fatalf("sub1.IsSubAccount() = false")
	}
	if sub1.ParentPrincipalID != parent.ID {
		t.Fatalf("sub1.ParentPrincipalID = %d, want %d", sub1.ParentPrincipalID, parent.ID)
	}
	if sub1.CanonicalEmail != "imported@example.com" {
		t.Fatalf("sub1.CanonicalEmail = %q, want lowercased", sub1.CanonicalEmail)
	}
	if sub1.PasswordHash != "" || len(sub1.TOTPSecret) != 0 {
		t.Fatalf("sub1 carries a credential: hash=%q totp=%v", sub1.PasswordHash, sub1.TOTPSecret)
	}
	if sub1.QuotaBytes != 0 {
		t.Fatalf("sub1.QuotaBytes = %d, want 0 (quota lives on the parent)", sub1.QuotaBytes)
	}

	sub2, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "second@example.com",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal(2): %v", err)
	}

	// GetPrincipalByID round-trips the same shape.
	got, err := s.Meta().GetPrincipalByID(ctx, sub1.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(sub1): %v", err)
	}
	if got.Kind != store.PrincipalKindSubAccount || got.ParentPrincipalID != parent.ID {
		t.Fatalf("GetPrincipalByID(sub1) = %+v, want kind=SubAccount parent=%d", got, parent.ID)
	}

	list, err := s.Meta().ListSubPrincipals(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSubPrincipals: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSubPrincipals len = %d, want 2", len(list))
	}
	seen := map[store.PrincipalID]bool{}
	for _, sp := range list {
		seen[sp.ID] = true
		if sp.ParentPrincipalID != parent.ID {
			t.Errorf("ListSubPrincipals entry %d has ParentPrincipalID = %d, want %d", sp.ID, sp.ParentPrincipalID, parent.ID)
		}
	}
	if !seen[sub1.ID] || !seen[sub2.ID] {
		t.Fatalf("ListSubPrincipals missing an inserted sub-principal: got %v", list)
	}

	// A principal with no sub-accounts lists empty.
	other := mustInsertPrincipal(t, s, "nosub@example.com")
	empty, err := s.Meta().ListSubPrincipals(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListSubPrincipals(no subs): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListSubPrincipals(no subs) = %v, want empty", empty)
	}

	// GetSubPrincipalParent resolves back to the parent.
	resolved, err := s.Meta().GetSubPrincipalParent(ctx, sub1.ID)
	if err != nil {
		t.Fatalf("GetSubPrincipalParent(sub1): %v", err)
	}
	if resolved.ID != parent.ID {
		t.Fatalf("GetSubPrincipalParent(sub1) = %d, want %d", resolved.ID, parent.ID)
	}

	// GetSubPrincipalParent on an ordinary principal (not a sub-account)
	// returns ErrNotFound.
	if _, err := s.Meta().GetSubPrincipalParent(ctx, parent.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSubPrincipalParent(parent) = %v, want ErrNotFound", err)
	}
}

func testSubPrincipals_RejectsCredential(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	parent := mustInsertPrincipal(t, s, "credparent@example.com")

	_, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "hascred@example.com",
		PasswordHash:   "$argon2id$v=19$m=1,t=1,p=1$AAAA$BBBB",
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertSubPrincipal(password) = %v, want ErrInvalidArgument", err)
	}

	_, err = s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "hastotp@example.com",
		TOTPSecret:     []byte("secret"),
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertSubPrincipal(totp) = %v, want ErrInvalidArgument", err)
	}
}

// testSubPrincipals_UpdateRejectsCredential pins the write-layer half of
// REQ-SUBACCT-02 (issue #241): UpdatePrincipal must refuse a credential
// write onto a sub-principal row exactly as InsertSubPrincipal already
// does at creation time. This is defence in depth against a future call
// site that fetches a sub-principal, sets PasswordHash/TOTPSecret, and
// writes it back -- the auth-time floor (Directory.Authenticate,
// Principal.IsAuthenticatable) already makes such a credential inert, but
// it must never be persisted in the first place.
func testSubPrincipals_UpdateRejectsCredential(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	parent := mustInsertPrincipal(t, s, "updcredparent@example.com")
	sub, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "updcredsub@example.com",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}

	// Writing a password hash onto the sub-principal row is refused.
	withPassword := sub
	withPassword.PasswordHash = "$argon2id$v=19$m=1,t=1,p=1$AAAA$BBBB"
	if err := s.Meta().UpdatePrincipal(ctx, withPassword); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("UpdatePrincipal(sub, password) = %v, want ErrInvalidArgument", err)
	}

	// Writing a TOTP secret onto the sub-principal row is refused.
	withTOTP := sub
	withTOTP.TOTPSecret = []byte("secret")
	if err := s.Meta().UpdatePrincipal(ctx, withTOTP); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("UpdatePrincipal(sub, totp) = %v, want ErrInvalidArgument", err)
	}

	// The row was not mutated by either rejected attempt.
	got, err := s.Meta().GetPrincipalByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(sub): %v", err)
	}
	if got.PasswordHash != "" || len(got.TOTPSecret) != 0 {
		t.Fatalf("sub-principal carries a credential after rejected updates: hash=%q totp=%v",
			got.PasswordHash, got.TOTPSecret)
	}

	// A non-credential field update on the same sub-principal row still
	// succeeds -- the guard must not over-broaden to reject legitimate
	// updates.
	renamed := sub
	renamed.DisplayName = "Renamed Sub"
	if err := s.Meta().UpdatePrincipal(ctx, renamed); err != nil {
		t.Fatalf("UpdatePrincipal(sub, display name only): %v", err)
	}
	got, err = s.Meta().GetPrincipalByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(sub) after rename: %v", err)
	}
	if got.DisplayName != "Renamed Sub" {
		t.Fatalf("sub.DisplayName = %q, want %q", got.DisplayName, "Renamed Sub")
	}

	// An ordinary (non-sub) principal's password/TOTP update is
	// unaffected by the guard.
	normal := mustInsertPrincipal(t, s, "updcredparent2@example.com")
	normal.PasswordHash = "$argon2id$v=19$m=1,t=1,p=1$CCCC$DDDD"
	normal.TOTPSecret = []byte("normal-secret")
	if err := s.Meta().UpdatePrincipal(ctx, normal); err != nil {
		t.Fatalf("UpdatePrincipal(normal principal, credential): %v", err)
	}
	got, err = s.Meta().GetPrincipalByID(ctx, normal.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(normal): %v", err)
	}
	if got.PasswordHash != normal.PasswordHash {
		t.Fatalf("normal.PasswordHash = %q, want %q", got.PasswordHash, normal.PasswordHash)
	}
	if string(got.TOTPSecret) != "normal-secret" {
		t.Fatalf("normal.TOTPSecret = %q, want %q", got.TOTPSecret, "normal-secret")
	}
}

func testSubPrincipals_RejectsInvalidParent(t *testing.T, s store.Store) {
	ctx := ctxT(t)

	// Unknown parent.
	_, err := s.Meta().InsertSubPrincipal(ctx, store.PrincipalID(1<<48), store.Principal{
		CanonicalEmail: "orphan@example.com",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("InsertSubPrincipal(unknown parent) = %v, want ErrNotFound", err)
	}

	// Group principal cannot own a sub-account.
	group, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindGroup,
		CanonicalEmail: "agroup@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	_, err = s.Meta().InsertSubPrincipal(ctx, group.ID, store.Principal{
		CanonicalEmail: "undergroup@example.com",
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertSubPrincipal(group parent) = %v, want ErrInvalidArgument", err)
	}

	// Sub-principals cannot nest.
	parent := mustInsertPrincipal(t, s, "nestparent@example.com")
	sub, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "midlevel@example.com",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal(mid-level): %v", err)
	}
	_, err = s.Meta().InsertSubPrincipal(ctx, sub.ID, store.Principal{
		CanonicalEmail: "grandchild@example.com",
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertSubPrincipal(nested parent) = %v, want ErrInvalidArgument", err)
	}
}

func testSubPrincipals_ExcludedFromAdminLists(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	parent, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "listparent@excludeme.example",
		DisplayName:    "List Parent Unique Marker",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(parent): %v", err)
	}
	sub, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "excludedsub@excludeme.example",
		DisplayName:    "Excluded Sub Unique Marker",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}

	// ListPrincipals never returns the sub-principal, even scanning past
	// its ID.
	list, err := s.Meta().ListPrincipals(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	for _, p := range list {
		if p.ID == sub.ID {
			t.Fatalf("ListPrincipals includes sub-principal %d (REQ-SUBACCT-06)", sub.ID)
		}
	}
	foundParent := false
	for _, p := range list {
		if p.ID == parent.ID {
			foundParent = true
		}
	}
	if !foundParent {
		t.Fatalf("ListPrincipals excludes the parent (should only exclude the sub-principal)")
	}

	// SearchPrincipalsByText: search by the sub's own display name never
	// surfaces it.
	results, err := s.Meta().SearchPrincipalsByText(ctx, "Excluded Sub Unique Marker", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByText: %v", err)
	}
	for _, p := range results {
		if p.ID == sub.ID {
			t.Fatalf("SearchPrincipalsByText surfaces sub-principal %d (REQ-SUBACCT-06)", sub.ID)
		}
	}
	if len(results) != 0 {
		t.Fatalf("SearchPrincipalsByText(sub-only marker) = %v, want empty", results)
	}

	// SearchPrincipalsByText by email local-part prefix likewise excludes it.
	byEmail, err := s.Meta().SearchPrincipalsByText(ctx, "excludedsub", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByText(email prefix): %v", err)
	}
	for _, p := range byEmail {
		if p.ID == sub.ID {
			t.Fatalf("SearchPrincipalsByText(email prefix) surfaces sub-principal %d", sub.ID)
		}
	}

	// SearchPrincipalsByTextInDomain, scoped to the sub's own domain.
	byDomain, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "excludedsub", "excludeme.example", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByTextInDomain: %v", err)
	}
	for _, p := range byDomain {
		if p.ID == sub.ID {
			t.Fatalf("SearchPrincipalsByTextInDomain surfaces sub-principal %d", sub.ID)
		}
	}
}

func testSubPrincipals_QuotaCountsAgainstParent(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	parent, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "quotaparent@example.com",
		QuotaBytes:     20, // tight
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(parent): %v", err)
	}
	sub, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "quotasub@example.com",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	subMB := mustInsertMailbox(t, s, sub.ID, "INBOX")

	// A message small enough to fit the parent's quota succeeds and is
	// attributed to the sub-account's own mailbox.
	small := putBlob(t, s, "12345")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: sub.ID, Blob: small, Size: small.Size,
	}, []store.MessageMailbox{{MailboxID: subMB.ID}}); err != nil {
		t.Fatalf("InsertMessage(small, sub): %v", err)
	}

	// The parent's used_bytes reflects the sub-account's mail; the
	// sub-account's own used_bytes stays zero.
	parentAfter, err := s.Meta().GetPrincipalByID(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(parent): %v", err)
	}
	if parentAfter.QuotaBytes == 0 {
		t.Fatalf("parent lost its QuotaBytes")
	}

	// A message that would push the parent over quota is rejected, even
	// though it is inserted under the sub-account.
	big := putBlob(t, s, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, _, err = s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: sub.ID, Blob: big, Size: big.Size,
	}, []store.MessageMailbox{{MailboxID: subMB.ID}})
	if !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("InsertMessage(over parent quota, sub) = %v, want ErrQuotaExceeded", err)
	}

	// Inserting the same oversized message directly under the parent
	// hits the identical ceiling -- confirms both accounts draw from one
	// pool, not two independent ones.
	parentMB := mustInsertMailbox(t, s, parent.ID, "INBOX")
	_, _, err = s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: parent.ID, Blob: big, Size: big.Size,
	}, []store.MessageMailbox{{MailboxID: parentMB.ID}})
	if !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("InsertMessage(over quota, parent) = %v, want ErrQuotaExceeded", err)
	}

	subAfter, err := s.Meta().GetPrincipalByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(sub) after inserts: %v", err)
	}
	if subAfter.QuotaBytes != 0 {
		t.Fatalf("sub.QuotaBytes = %d, want 0 (quota lives on the parent only)", subAfter.QuotaBytes)
	}
}

func testSubPrincipals_DeleteParentCascades(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	parent := mustInsertPrincipal(t, s, "cascadeparent@example.com")
	sub, err := s.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "cascadesub@example.com",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	subMB := mustInsertMailbox(t, s, sub.ID, "INBOX")
	ref := putBlob(t, s, "cascade-me")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: sub.ID, Blob: ref, Size: ref.Size,
	}, []store.MessageMailbox{{MailboxID: subMB.ID}}); err != nil {
		t.Fatalf("InsertMessage(sub): %v", err)
	}

	if err := s.Meta().DeletePrincipal(ctx, parent.ID); err != nil {
		t.Fatalf("DeletePrincipal(parent): %v", err)
	}

	// The sub-principal row itself is gone (REQ-SUBACCT-06).
	if _, err := s.Meta().GetPrincipalByID(ctx, sub.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPrincipalByID(sub) after parent delete = %v, want ErrNotFound", err)
	}
	// Its mailbox (and by extension its mail) is gone too.
	if _, err := s.Meta().GetMailboxByID(ctx, subMB.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByID(sub mailbox) after parent delete = %v, want ErrNotFound", err)
	}
}

// -- Sub-account promotion (issue #227, REQ-SUBACCT-09/10,
// REQ-IMAP-IMP-106/107): SeparateIdentity, RunSubAccountMigration,
// RemoveSubAccount. ----------------------------------------------------

// separationFixture is the common setup for the promotion tests: a
// parent with an INBOX, a persisted Identity, and an IMAP-import account
// (with a provenance label) attached to that Identity, all still owned
// by the parent (the state SeparateIdentity expects to find).
type separationFixture struct {
	parent     store.Principal
	inbox      store.Mailbox
	identityID string
	acc        store.IMAPImportAccount
}

func newSeparationFixture(t *testing.T, s store.Store, tag string) separationFixture {
	t.Helper()
	ctx := ctxT(t)
	parent := mustInsertPrincipal(t, s, tag+"@example.com")
	inbox := mustInsertMailbox(t, s, parent.ID, "INBOX")

	identityID := tag + "-identity"
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: identityID, PrincipalID: parent.ID, Email: tag + "@external.test",
		Name: "External " + tag, MayDelete: true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		IdentityID:       identityID,
		PrincipalID:      parent.ID,
		AccountName:      tag,
		Host:             "imap.external.test",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         tag,
		AuthMethod:       store.IMAPImportAuthMethodAppPassword,
		CredentialCT:     []byte("v1:pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	prov := mustInsertMailbox(t, s, parent.ID, tag+" label")
	if err := s.Meta().SetIMAPImportProvenanceMailbox(ctx, acc.ID, prov.ID); err != nil {
		t.Fatalf("SetIMAPImportProvenanceMailbox: %v", err)
	}
	acc.ProvenanceMailboxID = prov.ID

	return separationFixture{parent: parent, inbox: inbox, identityID: identityID, acc: acc}
}

// insertTrackedMessage inserts a message into f.inbox (and, when
// withLabel is true, also into the account's provenance label -- the
// normal ingest shape per REQ-IMAP-IMP-100), records the matching
// imapimport_message_state row, and returns the new MessageID.
func (f separationFixture) insertTrackedMessage(t *testing.T, s store.Store, uid uint32, body string, withLabel bool) store.MessageID {
	t.Helper()
	ctx := ctxT(t)
	ref := putBlob(t, s, body)
	if _, _, err := s.Meta().InsertMessage(ctx,
		store.Message{PrincipalID: f.parent.ID, Blob: ref, Size: ref.Size},
		[]store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg, err := s.Meta().GetMessageByBlobHash(ctx, f.parent.ID, ref.Hash)
	if err != nil {
		t.Fatalf("GetMessageByBlobHash: %v", err)
	}
	if withLabel {
		if _, _, err := s.Meta().AddMessageToMailbox(ctx, msg.ID, f.acc.ProvenanceMailboxID); err != nil {
			t.Fatalf("AddMessageToMailbox(label): %v", err)
		}
	}
	if err := s.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID: f.acc.ID, UpstreamFolder: "INBOX", UpstreamUID: uid,
		HeroldMessageID: msg.ID, HeroldMailboxID: f.inbox.ID,
	}); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState: %v", err)
	}
	return msg.ID
}

func testSubAccountMigration_SeparateIdentityIsIdempotent(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "sepident")

	mig1, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	if mig1.ID == "" || mig1.SubPrincipalID == 0 {
		t.Fatalf("SeparateIdentity returned incomplete row: %+v", mig1)
	}
	if mig1.ParentPrincipalID != f.parent.ID || mig1.IdentityID != f.identityID {
		t.Fatalf("SeparateIdentity row = %+v; want parent=%d identity=%s", mig1, f.parent.ID, f.identityID)
	}
	if mig1.Status != store.SubAccountMigrationStatusPending {
		t.Fatalf("SeparateIdentity Status = %q; want pending", mig1.Status)
	}

	sub, err := s.Meta().GetPrincipalByID(ctx, mig1.SubPrincipalID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(sub): %v", err)
	}
	if !sub.IsSubAccount() || sub.ParentPrincipalID != f.parent.ID {
		t.Fatalf("sub-principal = %+v; want a sub-account of %d", sub, f.parent.ID)
	}

	// System mailbox tree provisioned.
	subMailboxes, err := s.Meta().ListMailboxes(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ListMailboxes(sub): %v", err)
	}
	wantNames := map[string]bool{"INBOX": true, "Sent": true, "Drafts": true, "Trash": true, "Junk": true, "Archive": true}
	for _, mb := range subMailboxes {
		delete(wantNames, mb.Name)
	}
	if len(wantNames) != 0 {
		t.Errorf("sub-account missing system mailboxes: %v", wantNames)
	}

	// Identity and import account both moved.
	idn, err := s.Meta().GetJMAPIdentity(ctx, f.identityID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if idn.PrincipalID != sub.ID {
		t.Fatalf("identity.PrincipalID = %d; want sub %d", idn.PrincipalID, sub.ID)
	}
	gotAcc, err := s.Meta().GetIMAPImportAccount(ctx, f.acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if gotAcc.PrincipalID != sub.ID {
		t.Fatalf("account.PrincipalID = %d; want sub %d", gotAcc.PrincipalID, sub.ID)
	}

	// Idempotent: calling again returns the identical row.
	mig2, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity (second call): %v", err)
	}
	if mig2 != mig1 {
		t.Fatalf("second SeparateIdentity = %+v; want identical to first %+v", mig2, mig1)
	}
}

func testSubAccountMigration_RunMovesSoleClaimedMail(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "runmove")

	msg1 := f.insertTrackedMessage(t, s, 1, "runmove-body-1", true)
	msg2 := f.insertTrackedMessage(t, s, 2, "runmove-body-2", true)

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}

	done, err := store.RunSubAccountMigration(ctx, s, mig.ID)
	if err != nil {
		t.Fatalf("RunSubAccountMigration: %v", err)
	}
	if done.Status != store.SubAccountMigrationStatusDone {
		t.Fatalf("Status = %q; want done", done.Status)
	}
	if done.MessagesMoved != 2 || done.MessagesCopied != 0 {
		t.Fatalf("counts = moved=%d copied=%d; want moved=2 copied=0", done.MessagesMoved, done.MessagesCopied)
	}

	for _, msgID := range []store.MessageID{msg1, msg2} {
		got, err := s.Meta().GetMessage(ctx, msgID)
		if err != nil {
			t.Fatalf("GetMessage(%d): %v", msgID, err)
		}
		if got.PrincipalID != mig.SubPrincipalID {
			t.Errorf("message %d PrincipalID = %d; want sub %d", msgID, got.PrincipalID, mig.SubPrincipalID)
		}
		for _, mm := range got.Mailboxes {
			mb, err := s.Meta().GetMailboxByID(ctx, mm.MailboxID)
			if err != nil {
				t.Fatalf("GetMailboxByID: %v", err)
			}
			if mb.PrincipalID != mig.SubPrincipalID {
				t.Errorf("message %d has a membership in mailbox %d owned by %d, not the sub-account", msgID, mb.ID, mb.PrincipalID)
			}
		}
	}

	// The parent's original INBOX is empty; nothing was left behind.
	parentInbox, err := s.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages(parent INBOX): %v", err)
	}
	if len(parentInbox) != 0 {
		t.Errorf("parent INBOX still has %d message(s) after a sole-claim move", len(parentInbox))
	}

	// Re-running a done migration is a no-op success (idempotent).
	again, err := store.RunSubAccountMigration(ctx, s, mig.ID)
	if err != nil {
		t.Fatalf("RunSubAccountMigration (re-run on done): %v", err)
	}
	if again.MessagesMoved != 2 || again.MessagesCopied != 0 {
		t.Fatalf("re-run counts = moved=%d copied=%d; want unchanged moved=2 copied=0", again.MessagesMoved, again.MessagesCopied)
	}
}

func testSubAccountMigration_RunCopiesDedupSafe(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "runcopy")

	// A message claimed both by the import account (folder-mapped mailbox
	// + provenance label) AND by a foreign membership (simulating native
	// SMTP delivery into the same INBOX row via a second, independent
	// mailbox) must be copied, not moved: the parent's copy survives.
	shared := f.insertTrackedMessage(t, s, 1, "runcopy-shared-body", true)
	foreignMB := mustInsertMailbox(t, s, f.parent.ID, "AlsoNative")
	if _, _, err := s.Meta().AddMessageToMailbox(ctx, shared, foreignMB.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(foreign): %v", err)
	}
	// A second, ordinary sole-claimed message alongside it.
	solo := f.insertTrackedMessage(t, s, 2, "runcopy-solo-body", true)

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	done, err := store.RunSubAccountMigration(ctx, s, mig.ID)
	if err != nil {
		t.Fatalf("RunSubAccountMigration: %v", err)
	}
	if done.Status != store.SubAccountMigrationStatusDone {
		t.Fatalf("Status = %q; want done", done.Status)
	}
	if done.MessagesMoved != 1 || done.MessagesCopied != 1 {
		t.Fatalf("counts = moved=%d copied=%d; want moved=1 copied=1", done.MessagesMoved, done.MessagesCopied)
	}

	// The solo message moved: gone from the parent, present under the sub.
	soloAfter, err := s.Meta().GetMessage(ctx, solo)
	if err != nil {
		t.Fatalf("GetMessage(solo): %v", err)
	}
	if soloAfter.PrincipalID != mig.SubPrincipalID {
		t.Errorf("solo.PrincipalID = %d; want sub %d", soloAfter.PrincipalID, mig.SubPrincipalID)
	}

	// The shared message's original row survives under the parent,
	// still carrying its foreign membership.
	sharedAfter, err := s.Meta().GetMessage(ctx, shared)
	if err != nil {
		t.Fatalf("GetMessage(shared, original): %v", err)
	}
	if sharedAfter.PrincipalID != f.parent.ID {
		t.Errorf("shared original PrincipalID = %d; want parent %d (it must survive)", sharedAfter.PrincipalID, f.parent.ID)
	}
	foreignStillThere := false
	for _, mm := range sharedAfter.Mailboxes {
		if mm.MailboxID == foreignMB.ID {
			foreignStillThere = true
		}
	}
	if !foreignStillThere {
		t.Errorf("shared original lost its foreign membership")
	}

	// A copy exists under the sub-account's INBOX.
	subInbox, err := s.Meta().GetMailboxByName(ctx, mig.SubPrincipalID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName(sub INBOX): %v", err)
	}
	subMsgs, err := s.Meta().ListMessages(ctx, subInbox.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages(sub INBOX): %v", err)
	}
	if len(subMsgs) != 2 {
		t.Fatalf("sub INBOX has %d message(s); want 2 (the moved solo + the copy of shared)", len(subMsgs))
	}
}

// countdownContext lets ctx.Err() return context.Canceled starting on
// the (n+1)th call, while every other Context method (notably Done(),
// which internal DB drivers select on) delegates to a real, never-
// cancelled background context. This gives deterministic, message-
// granularity control over "abort after processing N items" without
// depending on wall-clock timing or a package-private batch constant.
type countdownContext struct {
	context.Context
	remaining int
}

func newCountdownContext(n int) *countdownContext {
	return &countdownContext{Context: context.Background(), remaining: n}
}

func (c *countdownContext) Err() error {
	if c.remaining <= 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

func testSubAccountMigration_RunIsCrashSafe(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "runcrash")

	const total = 6
	want := make([]store.MessageID, 0, total)
	for i := uint32(1); i <= total; i++ {
		body := fmt.Sprintf("runcrash-body-%d", i)
		want = append(want, f.insertTrackedMessage(t, s, i, body, true))
	}

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}

	// Abort after 3 of the 6 messages: RunSubAccountMigration's per-
	// message loop checks ctx.Err() before each message, so a
	// countdown of 3 processes exactly 3 and returns early with the row
	// still "running".
	cctx := newCountdownContext(3)
	partial, err := store.RunSubAccountMigration(cctx, s, mig.ID)
	if err != nil {
		t.Fatalf("RunSubAccountMigration (aborted): %v", err)
	}
	if partial.Status == store.SubAccountMigrationStatusDone {
		t.Fatalf("aborted run reports done; want running (partial)")
	}
	if partial.MessagesMoved == 0 || partial.MessagesMoved >= total {
		t.Fatalf("aborted run MessagesMoved = %d; want a partial count in (0, %d)", partial.MessagesMoved, total)
	}

	// At no point is a message absent from both accounts: every message
	// still exists, each under exactly one of {parent, sub}.
	assertNoLossNoDuplication := func(label string) {
		t.Helper()
		seenParent, seenSub := 0, 0
		for _, msgID := range want {
			m, err := s.Meta().GetMessage(ctx, msgID)
			if err != nil {
				t.Fatalf("%s: GetMessage(%d): %v", label, msgID, err)
			}
			switch m.PrincipalID {
			case f.parent.ID:
				seenParent++
			case mig.SubPrincipalID:
				seenSub++
			default:
				t.Fatalf("%s: message %d owned by unexpected principal %d", label, msgID, m.PrincipalID)
			}
			if len(m.Mailboxes) != 2 {
				// INBOX + provenance label -- a duplicated membership (the
				// bug this test guards against) would show up as an
				// unexpected count here on the destination side too, since
				// ReparentMessage is a move (delete+insert), never an add.
				t.Errorf("%s: message %d has %d membership(s); want 2 (INBOX + label)", label, msgID, len(m.Mailboxes))
			}
		}
		if seenParent+seenSub != total {
			t.Fatalf("%s: seenParent=%d seenSub=%d; want sum %d", label, seenParent, seenSub, total)
		}
	}
	assertNoLossNoDuplication("after abort")

	// Resume with a fresh, uncancelled context: completes the sweep.
	done, err := store.RunSubAccountMigration(ctx, s, mig.ID)
	if err != nil {
		t.Fatalf("RunSubAccountMigration (resume): %v", err)
	}
	if done.Status != store.SubAccountMigrationStatusDone {
		t.Fatalf("resumed Status = %q; want done", done.Status)
	}
	if done.MessagesMoved != total {
		t.Fatalf("resumed MessagesMoved = %d; want %d (nothing lost, nothing duplicated)", done.MessagesMoved, total)
	}
	assertNoLossNoDuplication("after resume")

	for _, msgID := range want {
		m, err := s.Meta().GetMessage(ctx, msgID)
		if err != nil {
			t.Fatalf("GetMessage(%d) after resume: %v", msgID, err)
		}
		if m.PrincipalID != mig.SubPrincipalID {
			t.Errorf("message %d PrincipalID = %d after full resume; want sub %d", msgID, m.PrincipalID, mig.SubPrincipalID)
		}
	}
}

func testSubAccountMigration_RemoveKeep(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "removekeep")
	msgID := f.insertTrackedMessage(t, s, 1, "removekeep-body", true)

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	if _, err := store.RunSubAccountMigration(ctx, s, mig.ID); err != nil {
		t.Fatalf("RunSubAccountMigration: %v", err)
	}

	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, false); err != nil {
		t.Fatalf("RemoveSubAccount(keep): %v", err)
	}

	// The sub-principal is gone.
	if _, err := s.Meta().GetPrincipalByID(ctx, mig.SubPrincipalID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPrincipalByID(sub) after keep-remove = %v; want ErrNotFound", err)
	}

	// The message moved back under the parent, still fully intact.
	m, err := s.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage after keep-remove: %v", err)
	}
	if m.PrincipalID != f.parent.ID {
		t.Fatalf("message PrincipalID = %d after keep-remove; want parent %d", m.PrincipalID, f.parent.ID)
	}

	// The Identity and import account moved back to the parent too.
	idn, err := s.Meta().GetJMAPIdentity(ctx, f.identityID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity after keep-remove: %v", err)
	}
	if idn.PrincipalID != f.parent.ID {
		t.Fatalf("identity.PrincipalID = %d after keep-remove; want parent %d", idn.PrincipalID, f.parent.ID)
	}
	acc, err := s.Meta().GetIMAPImportAccount(ctx, f.acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount after keep-remove: %v", err)
	}
	if acc.PrincipalID != f.parent.ID {
		t.Fatalf("account.PrincipalID = %d after keep-remove; want parent %d", acc.PrincipalID, f.parent.ID)
	}

	// Idempotent: removing an already-removed sub-account is a no-op.
	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, false); err != nil {
		t.Fatalf("RemoveSubAccount(keep, second call) = %v; want nil", err)
	}
}

func testSubAccountMigration_RemovePurge(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "removepurge")
	msgID := f.insertTrackedMessage(t, s, 1, "removepurge-body", true)

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	if _, err := store.RunSubAccountMigration(ctx, s, mig.ID); err != nil {
		t.Fatalf("RunSubAccountMigration: %v", err)
	}

	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, true); err != nil {
		t.Fatalf("RemoveSubAccount(purge): %v", err)
	}

	if _, err := s.Meta().GetPrincipalByID(ctx, mig.SubPrincipalID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPrincipalByID(sub) after purge = %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetMessage(ctx, msgID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessage after purge = %v; want ErrNotFound (mail purged with the sub-account)", err)
	}

	// Idempotent: purging an already-purged sub-account is a no-op.
	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, true); err != nil {
		t.Fatalf("RemoveSubAccount(purge, second call) = %v; want nil", err)
	}
}

// -- Alias retargeting on separation and removal (issue #312,
// REQ-SUBACCT-07): local SMTP delivery to a hosted alias address equal
// to a separated identity's email must land in the sub-account, and
// move back to the parent when the sub-account is removed. -----------

func testSubAccountMigration_SeparateRetargetsAlias_NoAliasRow(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "sepaliasnone")

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	if mig.SubPrincipalID == 0 {
		t.Fatalf("SeparateIdentity: sub principal unset")
	}

	// No alias row existed for the identity's address; SeparateIdentity
	// must not fabricate one. The address still routes correctly, via
	// the sub-principal's own canonical email.
	aliases, err := s.Meta().ListAliases(ctx, "external.test")
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("ListAliases after separate with no alias row = %d rows, want 0", len(aliases))
	}
}

func testSubAccountMigration_SeparateRetargetsAlias_TwoDomains(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "sepaliastwo")

	// An alias row exactly matching the identity's address, and a
	// second row with the same local part but a different hosted
	// domain -- it must not be retargeted.
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "sepaliastwo", Domain: "external.test", TargetPrincipal: f.parent.ID,
	}); err != nil {
		t.Fatalf("InsertAlias(matching): %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "sepaliastwo", Domain: "other.test", TargetPrincipal: f.parent.ID,
	}); err != nil {
		t.Fatalf("InsertAlias(other domain): %v", err)
	}

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}

	got, err := s.Meta().ResolveAlias(ctx, "sepaliastwo", "external.test")
	if err != nil {
		t.Fatalf("ResolveAlias(identity domain) after separate: %v", err)
	}
	if got != mig.SubPrincipalID {
		t.Fatalf("ResolveAlias(identity domain) after separate = %d, want sub %d", got, mig.SubPrincipalID)
	}
	gotOther, err := s.Meta().ResolveAlias(ctx, "sepaliastwo", "other.test")
	if err != nil {
		t.Fatalf("ResolveAlias(other domain) after separate: %v", err)
	}
	if gotOther != f.parent.ID {
		t.Fatalf("ResolveAlias(other domain) after separate = %d, want unchanged parent %d", gotOther, f.parent.ID)
	}
}

func testSubAccountMigration_RemoveKeepRetargetsAliasBack(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "removekeepalias")

	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "removekeepalias", Domain: "external.test", TargetPrincipal: f.parent.ID,
	}); err != nil {
		t.Fatalf("InsertAlias(matching): %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "removekeepalias", Domain: "other.test", TargetPrincipal: f.parent.ID,
	}); err != nil {
		t.Fatalf("InsertAlias(other domain): %v", err)
	}

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	if _, err := store.RunSubAccountMigration(ctx, s, mig.ID); err != nil {
		t.Fatalf("RunSubAccountMigration: %v", err)
	}
	if got, err := s.Meta().ResolveAlias(ctx, "removekeepalias", "external.test"); err != nil || got != mig.SubPrincipalID {
		t.Fatalf("ResolveAlias after separate = (%d, %v), want (%d, nil)", got, err, mig.SubPrincipalID)
	}

	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, false); err != nil {
		t.Fatalf("RemoveSubAccount(keep): %v", err)
	}

	got, err := s.Meta().ResolveAlias(ctx, "removekeepalias", "external.test")
	if err != nil {
		t.Fatalf("ResolveAlias(identity domain) after keep-remove: %v", err)
	}
	if got != f.parent.ID {
		t.Fatalf("ResolveAlias(identity domain) after keep-remove = %d, want parent %d", got, f.parent.ID)
	}
	gotOther, err := s.Meta().ResolveAlias(ctx, "removekeepalias", "other.test")
	if err != nil {
		t.Fatalf("ResolveAlias(other domain) after keep-remove: %v", err)
	}
	if gotOther != f.parent.ID {
		t.Fatalf("ResolveAlias(other domain) after keep-remove = %d, want unchanged parent %d", gotOther, f.parent.ID)
	}

	// Idempotent: a second keep-remove leaves the alias alone.
	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, false); err != nil {
		t.Fatalf("RemoveSubAccount(keep, second call) = %v; want nil", err)
	}
	if got, err := s.Meta().ResolveAlias(ctx, "removekeepalias", "external.test"); err != nil || got != f.parent.ID {
		t.Fatalf("ResolveAlias after repeat keep-remove = (%d, %v), want (%d, nil)", got, err, f.parent.ID)
	}
}

func testSubAccountMigration_RemovePurgeRetargetsAliasBack(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newSeparationFixture(t, s, "removepurgealias")

	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "removepurgealias", Domain: "external.test", TargetPrincipal: f.parent.ID,
	}); err != nil {
		t.Fatalf("InsertAlias(matching): %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "removepurgealias", Domain: "other.test", TargetPrincipal: f.parent.ID,
	}); err != nil {
		t.Fatalf("InsertAlias(other domain): %v", err)
	}

	mig, err := store.SeparateIdentity(ctx, s, f.parent.ID, f.identityID)
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	if _, err := store.RunSubAccountMigration(ctx, s, mig.ID); err != nil {
		t.Fatalf("RunSubAccountMigration: %v", err)
	}
	if got, err := s.Meta().ResolveAlias(ctx, "removepurgealias", "external.test"); err != nil || got != mig.SubPrincipalID {
		t.Fatalf("ResolveAlias after separate = (%d, %v), want (%d, nil)", got, err, mig.SubPrincipalID)
	}

	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, true); err != nil {
		t.Fatalf("RemoveSubAccount(purge): %v", err)
	}

	// The alias row survives the purge -- it is not owned by the
	// sub-principal, it only pointed at it -- and now routes to the
	// parent again rather than being cascade-deleted along with the
	// sub-principal it used to target.
	got, err := s.Meta().ResolveAlias(ctx, "removepurgealias", "external.test")
	if err != nil {
		t.Fatalf("ResolveAlias(identity domain) after purge: %v", err)
	}
	if got != f.parent.ID {
		t.Fatalf("ResolveAlias(identity domain) after purge = %d, want parent %d", got, f.parent.ID)
	}
	gotOther, err := s.Meta().ResolveAlias(ctx, "removepurgealias", "other.test")
	if err != nil {
		t.Fatalf("ResolveAlias(other domain) after purge: %v", err)
	}
	if gotOther != f.parent.ID {
		t.Fatalf("ResolveAlias(other domain) after purge = %d, want unchanged parent %d", gotOther, f.parent.ID)
	}

	// Idempotent: a second purge (a no-op, the sub-principal is already
	// gone) leaves the alias alone.
	if err := store.RemoveSubAccount(ctx, s, mig.SubPrincipalID, true); err != nil {
		t.Fatalf("RemoveSubAccount(purge, second call) = %v; want nil", err)
	}
	if got, err := s.Meta().ResolveAlias(ctx, "removepurgealias", "external.test"); err != nil || got != f.parent.ID {
		t.Fatalf("ResolveAlias after repeat purge = (%d, %v), want (%d, nil)", got, err, f.parent.ID)
	}
}
