package storetest

import (
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// testSRSSecrets_InsertAndList exercises the SRS-secret repository (issue
// #204): an empty store starts with no secrets, insertion assigns an ID and
// CreatedAt, and List returns rows in ascending ID order so the caller can
// treat the last element as the current signing secret while trying every
// element on verify.
func testSRSSecrets_InsertAndList(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	empty, err := s.Meta().ListSRSSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSRSSecrets on empty store: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListSRSSecrets on empty store = %+v; want none", empty)
	}

	first, err := s.Meta().InsertSRSSecret(ctx, []byte("secret-one-32-bytes-of-material"))
	if err != nil {
		t.Fatalf("InsertSRSSecret first: %v", err)
	}
	if first.ID == 0 {
		t.Errorf("InsertSRSSecret returned zero ID")
	}
	if first.CreatedAt.IsZero() {
		t.Errorf("InsertSRSSecret: CreatedAt is zero")
	}

	second, err := s.Meta().InsertSRSSecret(ctx, []byte("secret-two-different-material!!"))
	if err != nil {
		t.Fatalf("InsertSRSSecret second: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("second InsertSRSSecret reused ID %d", first.ID)
	}

	all, err := s.Meta().ListSRSSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSRSSecrets: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListSRSSecrets = %d rows; want 2", len(all))
	}
	if all[0].ID != first.ID || all[1].ID != second.ID {
		t.Fatalf("ListSRSSecrets not in ascending ID order: %+v", all)
	}
	if string(all[0].Secret) != "secret-one-32-bytes-of-material" {
		t.Errorf("all[0].Secret = %q; want the first inserted secret", all[0].Secret)
	}
	if string(all[1].Secret) != "secret-two-different-material!!" {
		t.Errorf("all[1].Secret = %q; want the second inserted secret", all[1].Secret)
	}
}
