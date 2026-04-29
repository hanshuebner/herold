package storetest

import (
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// testSeenAddressUpsertInsert verifies that the first UpsertSeenAddress call
// creates a row with isNew=true and the correct counts.
func testSeenAddressUpsertInsert(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sa-insert@example.com")

	got, isNew, err := s.Meta().UpsertSeenAddress(ctx, p.ID, "friend@example.com", "Friend", 1, 0)
	if err != nil {
		t.Fatalf("UpsertSeenAddress: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true on first insert")
	}
	if got.Email != "friend@example.com" {
		t.Errorf("email = %q; want %q", got.Email, "friend@example.com")
	}
	if got.DisplayName != "Friend" {
		t.Errorf("displayName = %q; want %q", got.DisplayName, "Friend")
	}
	if got.SendCount != 1 {
		t.Errorf("sendCount = %d; want 1", got.SendCount)
	}
	if got.ReceivedCount != 0 {
		t.Errorf("receivedCount = %d; want 0", got.ReceivedCount)
	}
	if got.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if got.FirstSeenAt.IsZero() {
		t.Error("expected non-zero FirstSeenAt")
	}
}

// testSeenAddressUpsertUpdate verifies that a second UpsertSeenAddress call on
// the same email increments counts and updates lastUsedAt.
func testSeenAddressUpsertUpdate(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sa-update@example.com")

	// Insert.
	first, isNew, err := s.Meta().UpsertSeenAddress(ctx, p.ID, "colleague@example.com", "Colleague", 1, 0)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true on first insert")
	}

	// Update.
	second, isNew, err := s.Meta().UpsertSeenAddress(ctx, p.ID, "colleague@example.com", "Colleague Updated", 0, 1)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if isNew {
		t.Error("expected isNew=false on update")
	}
	if second.SendCount != 1 {
		t.Errorf("sendCount = %d; want 1", second.SendCount)
	}
	if second.ReceivedCount != 1 {
		t.Errorf("receivedCount = %d; want 1", second.ReceivedCount)
	}
	if second.ID != first.ID {
		t.Errorf("id changed: %d -> %d", first.ID, second.ID)
	}
	if second.FirstSeenAt != first.FirstSeenAt {
		t.Error("firstSeenAt should not change on update")
	}
}

// testSeenAddressCap verifies that the 500-entry cap is enforced by eviction.
// We insert 5 entries with deterministically different times (by re-upserting
// the first to make it freshest), then add a 6th with the cap set to 5. But
// since we can't easily lower the cap in tests, we instead rely on the full
// 500+1 scenario and only verify the count invariant (not which specific
// email was evicted, since equal timestamps make that nondeterministic).
func testSeenAddressCap(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sa-cap@example.com")

	// Insert 500 entries.
	for i := 0; i < 500; i++ {
		email := fmt.Sprintf("addr%04d@example.com", i)
		if _, _, err := s.Meta().UpsertSeenAddress(ctx, p.ID, email, "", 0, 1); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Verify count is 500 before overflow.
	rows, err := s.Meta().ListSeenAddressesByPrincipal(ctx, p.ID, 0)
	if err != nil {
		t.Fatalf("list before overflow: %v", err)
	}
	if len(rows) != 500 {
		t.Fatalf("count before overflow = %d; want 500", len(rows))
	}

	// Insert a 501st entry — this should evict one row, keeping count at 500.
	if _, _, err := s.Meta().UpsertSeenAddress(ctx, p.ID, "overflow@example.com", "", 1, 0); err != nil {
		t.Fatalf("501st insert: %v", err)
	}

	// Total count should remain <= 500.
	rows, err = s.Meta().ListSeenAddressesByPrincipal(ctx, p.ID, 0)
	if err != nil {
		t.Fatalf("list after overflow: %v", err)
	}
	if len(rows) > 500 {
		t.Errorf("count after overflow = %d; want <= 500", len(rows))
	}
	// The overflow entry itself must be present.
	if _, err := s.Meta().GetSeenAddressByEmail(ctx, p.ID, "overflow@example.com"); err != nil {
		t.Errorf("overflow entry missing: %v", err)
	}
}

// testSeenAddressGetByEmail verifies GetSeenAddressByEmail.
func testSeenAddressGetByEmail(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sa-getbyemail@example.com")

	if _, _, err := s.Meta().UpsertSeenAddress(ctx, p.ID, "target@example.com", "Target", 2, 3); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Meta().GetSeenAddressByEmail(ctx, p.ID, "target@example.com")
	if err != nil {
		t.Fatalf("GetSeenAddressByEmail: %v", err)
	}
	if got.Email != "target@example.com" {
		t.Errorf("email = %q", got.Email)
	}
	if got.SendCount != 2 || got.ReceivedCount != 3 {
		t.Errorf("counts = %d/%d; want 2/3", got.SendCount, got.ReceivedCount)
	}
}

// testSeenAddressDestroy verifies DestroySeenAddress.
func testSeenAddressDestroy(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sa-destroy@example.com")

	sa, _, err := s.Meta().UpsertSeenAddress(ctx, p.ID, "removeme@example.com", "", 1, 0)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.Meta().DestroySeenAddress(ctx, p.ID, sa.ID); err != nil {
		t.Fatalf("DestroySeenAddress: %v", err)
	}

	// A second destroy should return ErrNotFound.
	if err := s.Meta().DestroySeenAddress(ctx, p.ID, sa.ID); err == nil {
		t.Error("expected ErrNotFound on second destroy")
	}
}

// testSeenAddressPurge verifies PurgeSeenAddressesByPrincipal.
func testSeenAddressPurge(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sa-purge@example.com")

	for i := 0; i < 10; i++ {
		if _, _, err := s.Meta().UpsertSeenAddress(ctx, p.ID,
			fmt.Sprintf("purge%02d@example.com", i), "", 1, 0); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	n, err := s.Meta().PurgeSeenAddressesByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("PurgeSeenAddressesByPrincipal: %v", err)
	}
	if n != 10 {
		t.Errorf("purged = %d; want 10", n)
	}

	rows, err := s.Meta().ListSeenAddressesByPrincipal(ctx, p.ID, 0)
	if err != nil {
		t.Fatalf("list after purge: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("count after purge = %d; want 0", len(rows))
	}
}
