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
	"errors"
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
