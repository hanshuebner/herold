package sendpolicy_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth/sendpolicy"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// TestStoreChecker_VerifiedIdentity_Owned reproduces issue #342: a
// non-admin principal with a verified jmap_identities row (no alias
// row) must be recognised as owning that address by
// StoreChecker.PrincipalOwnsAddress / CheckFrom.
func TestStoreChecker_VerifiedIdentity_Owned(t *testing.T) {
	ctx := context.Background()
	st, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           "id-verified",
		PrincipalID:  p.ID,
		Name:         "Alice work",
		Email:        "alice-work@foreign.example",
		MayDelete:    true,
		VerifiedAtUs: 1,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity (verified): %v", err)
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "id-unverified",
		PrincipalID: p.ID,
		Name:        "Alice unverified",
		Email:       "alice-pending@foreign.example",
		MayDelete:   true,
		// VerifiedAtUs left zero: verification never completed.
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity (unverified): %v", err)
	}

	chk := sendpolicy.StoreChecker{Meta: st.Meta()}

	dec, err := sendpolicy.CheckFrom(ctx, chk, p, nil, "alice-work@foreign.example")
	if err != nil {
		t.Fatalf("CheckFrom (verified identity): unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("CheckFrom (verified identity): expected allowed, got reason=%q", dec.Reason)
	}

	dec, err = sendpolicy.CheckFrom(ctx, chk, p, nil, "alice-pending@foreign.example")
	if err != nil {
		t.Fatalf("CheckFrom (unverified identity): unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("CheckFrom (unverified identity): expected denied")
	}
	if dec.Reason != sendpolicy.ReasonNotOwned {
		t.Errorf("CheckFrom (unverified identity): reason=%q want %q", dec.Reason, sendpolicy.ReasonNotOwned)
	}

	// An address owned by nobody at all is still refused.
	dec, err = sendpolicy.CheckFrom(ctx, chk, p, nil, "somebody-else@foreign.example")
	if err != nil {
		t.Fatalf("CheckFrom (unrelated address): unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("CheckFrom (unrelated address): expected denied")
	}
}
