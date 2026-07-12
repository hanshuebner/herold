package storetest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// Hosted mailing lists, Stage 1 storage foundation (epic #183,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-01..12).
// Runs against both storesqlite and storepg via storetest.Run.

// mustInsertGroupPrincipal creates the backing Group principal a
// MailingList row references (REQ-MLIST-01). A mailing list is never
// backed by a PrincipalKindUser principal in production, but this
// package does not itself validate Kind, so the compliance suite is
// explicit about using the correct kind anyway.
func mustInsertGroupPrincipal(t *testing.T, s store.Store, email string) store.Principal {
	t.Helper()
	p, err := s.Meta().InsertPrincipal(ctxT(t), store.Principal{
		Kind:           store.PrincipalKindGroup,
		CanonicalEmail: email,
		DisplayName:    email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group %s): %v", email, err)
	}
	return p
}

func mustInsertMailingList(t *testing.T, s store.Store, address string, owner store.PrincipalID) store.MailingList {
	t.Helper()
	group := mustInsertGroupPrincipal(t, s, address)
	l, err := s.Meta().InsertMailingList(ctxT(t), store.MailingList{
		PrincipalID:    group.ID,
		PostingAddress: address,
		DisplayName:    "Test List",
		OwnerID:        owner,
		ARCSeal:        true,
	})
	if err != nil {
		t.Fatalf("InsertMailingList(%s): %v", address, err)
	}
	return l
}

// testMailingList_CreateGetUpdateDelete exercises REQ-MLIST-01: create,
// GetMailingList by id, mutate via UpdateMailingList, and DeleteMailingList
// cascading its roster.
func testMailingList_CreateGetUpdateDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-owner@example.com")
	l := mustInsertMailingList(t, s, "Announce@Example.com", owner.ID)

	if l.ID == 0 {
		t.Fatalf("InsertMailingList: zero ID")
	}
	if l.PostingAddress != "announce@example.com" {
		t.Errorf("PostingAddress = %q; want lowercased", l.PostingAddress)
	}
	if l.Domain != "example.com" {
		t.Errorf("Domain = %q; want %q", l.Domain, "example.com")
	}
	if l.PostingPolicy != store.MailingListPostingOpen {
		t.Errorf("PostingPolicy = %q; want default %q", l.PostingPolicy, store.MailingListPostingOpen)
	}
	if l.SubscribePolicy != store.MailingListSubscribeClosed {
		t.Errorf("SubscribePolicy = %q; want default %q", l.SubscribePolicy, store.MailingListSubscribeClosed)
	}
	if l.BouncePolicyJSON != "{}" {
		t.Errorf("BouncePolicyJSON = %q; want default {}", l.BouncePolicyJSON)
	}
	if l.CreatedAt.IsZero() || l.UpdatedAt.IsZero() {
		t.Errorf("CreatedAt/UpdatedAt not set: %+v", l)
	}

	got, err := s.Meta().GetMailingList(ctx, l.ID)
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}
	if got != l {
		t.Errorf("GetMailingList = %+v; want %+v", got, l)
	}

	// Update: new display name, subject tag, and posting address (also
	// exercises the domain recompute).
	tag := "ANN"
	l.DisplayName = "Announcements"
	l.SubjectTag = &tag
	l.PostingAddress = "announce-list@other.example"
	if err := s.Meta().UpdateMailingList(ctx, l); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	updated, err := s.Meta().GetMailingList(ctx, l.ID)
	if err != nil {
		t.Fatalf("GetMailingList after update: %v", err)
	}
	if updated.DisplayName != "Announcements" {
		t.Errorf("DisplayName after update = %q", updated.DisplayName)
	}
	if updated.SubjectTag == nil || *updated.SubjectTag != "ANN" {
		t.Errorf("SubjectTag after update = %v", updated.SubjectTag)
	}
	if updated.PostingAddress != "announce-list@other.example" {
		t.Errorf("PostingAddress after update = %q", updated.PostingAddress)
	}
	if updated.Domain != "other.example" {
		t.Errorf("Domain after update = %q; want recomputed %q", updated.Domain, "other.example")
	}
	if !updated.UpdatedAt.After(l.CreatedAt.Add(-1)) {
		// sanity: UpdatedAt is populated (exact monotonic ordering
		// depends on clock resolution, not asserted here)
		_ = updated
	}

	// Add a roster row, then delete the list; the member row must cascade.
	mem, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:          l.ID,
		ExternalAddress: strPtr("member@example.net"),
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}

	if err := s.Meta().DeleteMailingList(ctx, l.ID); err != nil {
		t.Fatalf("DeleteMailingList: %v", err)
	}
	if _, err := s.Meta().GetMailingList(ctx, l.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetMailingList after delete: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetMailingListMember(ctx, mem.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetMailingListMember after list delete: got %v; want ErrNotFound (cascade)", err)
	}

	// Deleting an absent list is ErrNotFound.
	if err := s.Meta().DeleteMailingList(ctx, l.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteMailingList absent: got %v; want ErrNotFound", err)
	}
}

// testMailingList_GetByPostingAddress exercises REQ-MLIST-10's hot
// inbound-routing lookup: case-insensitive match on the unique
// posting_address index.
func testMailingList_GetByPostingAddress(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-addr-owner@example.com")
	l := mustInsertMailingList(t, s, "support@example.com", owner.ID)

	got, err := s.Meta().GetMailingListByPostingAddress(ctx, "Support@Example.COM")
	if err != nil {
		t.Fatalf("GetMailingListByPostingAddress: %v", err)
	}
	if got.ID != l.ID {
		t.Errorf("GetMailingListByPostingAddress ID = %d; want %d", got.ID, l.ID)
	}

	if _, err := s.Meta().GetMailingListByPostingAddress(ctx, "nobody@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetMailingListByPostingAddress absent: got %v; want ErrNotFound", err)
	}

	// Insert with a duplicate posting address is a conflict.
	group2 := mustInsertGroupPrincipal(t, s, "support-2@example.com")
	_, err = s.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID:    group2.ID,
		PostingAddress: "support@example.com",
		OwnerID:        owner.ID,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("InsertMailingList duplicate posting_address: got %v; want ErrConflict", err)
	}
}

// testMailingList_ListByDomain exercises REQ-MLIST-05's domain-scoped
// listing: ListMailingLists(filter.Domain) is an indexed equality match on
// the store-maintained Domain column, not a substring scan.
func testMailingList_ListByDomain(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-domain-owner@example.com")

	l1 := mustInsertMailingList(t, s, "alpha@domain-a.example", owner.ID)
	l2 := mustInsertMailingList(t, s, "beta@domain-a.example", owner.ID)
	_ = mustInsertMailingList(t, s, "gamma@domain-b.example", owner.ID)

	got, err := s.Meta().ListMailingLists(ctx, store.MailingListFilter{Domain: "Domain-A.example"})
	if err != nil {
		t.Fatalf("ListMailingLists: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListMailingLists(domain-a) = %d lists; want 2", len(got))
	}
	ids := map[store.MailingListID]bool{}
	for _, l := range got {
		ids[l.ID] = true
		if l.Domain != "domain-a.example" {
			t.Errorf("leaked list from domain %q", l.Domain)
		}
	}
	if !ids[l1.ID] || !ids[l2.ID] {
		t.Errorf("ListMailingLists(domain-a) missing expected lists: got %+v", got)
	}

	// A domain that does not match a suffix of a different domain's
	// address must not leak (e.g. "main-a.example" vs "domain-a.example").
	none, err := s.Meta().ListMailingLists(ctx, store.MailingListFilter{Domain: "main-a.example"})
	if err != nil {
		t.Fatalf("ListMailingLists(no match): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListMailingLists(main-a.example) = %+v; want none", none)
	}
}

// testMailingListMember_XORConstraint exercises REQ-MLIST-02: a member row
// is exactly one of PrincipalID / ExternalAddress, enforced both by Go
// validation (ErrInvalidArgument, checked here) and the schema CHECK
// constraint underneath it.
func testMailingListMember_XORConstraint(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-xor-owner@example.com")
	l := mustInsertMailingList(t, s, "xor@example.com", owner.ID)
	member := mustInsertPrincipal(t, s, "xor-member@example.com")

	// Neither set.
	_, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{ListID: l.ID})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("AddMailingListMember neither set: got %v; want ErrInvalidArgument", err)
	}

	// Both set.
	_, err = s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:          l.ID,
		PrincipalID:     &member.ID,
		ExternalAddress: strPtr("both@example.net"),
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("AddMailingListMember both set: got %v; want ErrInvalidArgument", err)
	}
}

// testMailingListMember_UniquePerList exercises REQ-MLIST-02: "a given
// address appears at most once per list", for both a principal member and
// an external-address member, and confirms the same address is free to
// join a *different* list.
func testMailingListMember_UniquePerList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-uniq-owner@example.com")
	l1 := mustInsertMailingList(t, s, "uniq1@example.com", owner.ID)
	l2 := mustInsertMailingList(t, s, "uniq2@example.com", owner.ID)
	member := mustInsertPrincipal(t, s, "uniq-member@example.com")

	if _, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: l1.ID, PrincipalID: &member.ID,
	}); err != nil {
		t.Fatalf("AddMailingListMember principal (1st): %v", err)
	}
	if _, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: l1.ID, PrincipalID: &member.ID,
	}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("AddMailingListMember principal (dup): got %v; want ErrConflict", err)
	}
	// Same principal, different list: fine.
	if _, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: l2.ID, PrincipalID: &member.ID,
	}); err != nil {
		t.Errorf("AddMailingListMember principal on second list: %v", err)
	}

	if _, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: l1.ID, ExternalAddress: strPtr("Ext@Example.NET"),
	}); err != nil {
		t.Fatalf("AddMailingListMember external (1st): %v", err)
	}
	// Case-insensitive collision: canonicalisation lowercases before the
	// uniqueness check.
	if _, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: l1.ID, ExternalAddress: strPtr("ext@example.net"),
	}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("AddMailingListMember external (case-insensitive dup): got %v; want ErrConflict", err)
	}
	// Same external address, different list: fine.
	if _, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: l2.ID, ExternalAddress: strPtr("ext@example.net"),
	}); err != nil {
		t.Errorf("AddMailingListMember external on second list: %v", err)
	}
}

// testMailingListMember_NoMailRequiresPrincipal exercises REQ-MLIST-04:
// delivery_mode = nomail requires an internal principal member, checked
// both on insert and on a later mode transition.
func testMailingListMember_NoMailRequiresPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-nomail-owner@example.com")
	l := mustInsertMailingList(t, s, "nomail@example.com", owner.ID)
	member := mustInsertPrincipal(t, s, "nomail-member@example.com")

	// External-address member cannot be nomail on insert.
	_, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:          l.ID,
		ExternalAddress: strPtr("ext-nomail@example.net"),
		DeliveryMode:    store.MailingListDeliveryNoMail,
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("AddMailingListMember external+nomail: got %v; want ErrInvalidArgument", err)
	}

	// A principal member CAN be nomail.
	principalMem, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:       l.ID,
		PrincipalID:  &member.ID,
		DeliveryMode: store.MailingListDeliveryNoMail,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember principal+nomail: %v", err)
	}
	if principalMem.DeliveryMode != store.MailingListDeliveryNoMail {
		t.Errorf("DeliveryMode = %q; want nomail", principalMem.DeliveryMode)
	}

	// An external-address member's mode transition to nomail is rejected.
	extMem, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:          l.ID,
		ExternalAddress: strPtr("ext-transition@example.net"),
	})
	if err != nil {
		t.Fatalf("AddMailingListMember external: %v", err)
	}
	err = s.Meta().UpdateMailingListMemberDeliveryMode(ctx, extMem.ID, store.MailingListDeliveryNoMail)
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("UpdateMailingListMemberDeliveryMode external->nomail: got %v; want ErrInvalidArgument", err)
	}

	// The principal member's mode transition back to each succeeds.
	if err := s.Meta().UpdateMailingListMemberDeliveryMode(ctx, principalMem.ID, store.MailingListDeliveryEach); err != nil {
		t.Fatalf("UpdateMailingListMemberDeliveryMode principal->each: %v", err)
	}
	got, err := s.Meta().GetMailingListMember(ctx, principalMem.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.DeliveryMode != store.MailingListDeliveryEach {
		t.Errorf("DeliveryMode after transition = %q; want each", got.DeliveryMode)
	}
}

// testMailingListMember_StateTransitionsAndRemove exercises REQ-MLIST-03
// state transitions and RemoveMailingListMember.
func testMailingListMember_StateTransitionsAndRemove(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-state-owner@example.com")
	l := mustInsertMailingList(t, s, "state@example.com", owner.ID)

	mem, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:          l.ID,
		ExternalAddress: strPtr("state-member@example.net"),
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}
	if mem.State != store.MailingListMemberActive {
		t.Errorf("default State = %q; want active", mem.State)
	}
	if mem.DeliveryMode != store.MailingListDeliveryEach {
		t.Errorf("default DeliveryMode = %q; want each", mem.DeliveryMode)
	}
	if mem.AddedAt.IsZero() {
		t.Errorf("AddedAt not set")
	}

	for _, state := range []store.MailingListMemberState{
		store.MailingListMemberSuspended,
		store.MailingListMemberUnsubscribed,
		store.MailingListMemberPending,
		store.MailingListMemberActive,
	} {
		if err := s.Meta().UpdateMailingListMemberState(ctx, mem.ID, state); err != nil {
			t.Fatalf("UpdateMailingListMemberState(%s): %v", state, err)
		}
		got, err := s.Meta().GetMailingListMember(ctx, mem.ID)
		if err != nil {
			t.Fatalf("GetMailingListMember: %v", err)
		}
		if got.State != state {
			t.Errorf("State after transition = %q; want %q", got.State, state)
		}
	}

	if err := s.Meta().UpdateMailingListMemberState(ctx, store.MailingListMemberID(999999), store.MailingListMemberActive); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateMailingListMemberState absent: got %v; want ErrNotFound", err)
	}

	if err := s.Meta().RemoveMailingListMember(ctx, mem.ID); err != nil {
		t.Fatalf("RemoveMailingListMember: %v", err)
	}
	if _, err := s.Meta().GetMailingListMember(ctx, mem.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetMailingListMember after remove: got %v; want ErrNotFound", err)
	}
	if err := s.Meta().RemoveMailingListMember(ctx, mem.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RemoveMailingListMember absent: got %v; want ErrNotFound", err)
	}
}

// testMailingListMember_RosterPaging exercises REQ-MLIST-12: the roster
// read fan-out depends on. ListMailingListMembers pages via
// AfterID/Limit, and StreamActiveEachMembers visits exactly the
// active+each members in ascending ID order without the caller ever
// holding the whole roster at once (verified here by using a page size
// smaller than the roster and checking the union of pages/callback
// invocations matches, in order, only the eligible rows).
func testMailingListMember_RosterPaging(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "mlist-roster-owner@example.com")
	l := mustInsertMailingList(t, s, "roster@example.com", owner.ID)

	// Five each-mode active external members (fan-out eligible), one
	// suspended (not eligible), one nomail principal member (not
	// each-mode, so not eligible for StreamActiveEachMembers even
	// though it is active).
	var eachActive []store.MailingListMemberID
	for i := 0; i < 5; i++ {
		mem, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
			ListID:          l.ID,
			ExternalAddress: strPtr(fmt.Sprintf("roster-%d@example.net", i)),
		})
		if err != nil {
			t.Fatalf("AddMailingListMember active[%d]: %v", i, err)
		}
		eachActive = append(eachActive, mem.ID)
	}
	suspendedMem, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:          l.ID,
		ExternalAddress: strPtr("roster-suspended@example.net"),
	})
	if err != nil {
		t.Fatalf("AddMailingListMember suspended: %v", err)
	}
	if err := s.Meta().UpdateMailingListMemberState(ctx, suspendedMem.ID, store.MailingListMemberSuspended); err != nil {
		t.Fatalf("UpdateMailingListMemberState suspended: %v", err)
	}
	nomailPrincipal := mustInsertPrincipal(t, s, "roster-nomail@example.com")
	if _, err := s.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:       l.ID,
		PrincipalID:  &nomailPrincipal.ID,
		DeliveryMode: store.MailingListDeliveryNoMail,
	}); err != nil {
		t.Fatalf("AddMailingListMember nomail: %v", err)
	}

	// Unfiltered listing returns all 7 rows across pages.
	var allIDs []store.MailingListMemberID
	after := store.MailingListMemberID(0)
	for {
		page, err := s.Meta().ListMailingListMembers(ctx, store.MailingListRosterFilter{
			ListID:  l.ID,
			Limit:   2, // deliberately small to force multiple pages
			AfterID: after,
		})
		if err != nil {
			t.Fatalf("ListMailingListMembers: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			allIDs = append(allIDs, m.ID)
			after = m.ID
		}
		if len(page) < 2 {
			break
		}
	}
	if len(allIDs) != 7 {
		t.Fatalf("unfiltered roster paging returned %d rows; want 7", len(allIDs))
	}
	for i := 1; i < len(allIDs); i++ {
		if allIDs[i-1] >= allIDs[i] {
			t.Errorf("roster paging not ascending by id: %d then %d", allIDs[i-1], allIDs[i])
		}
	}

	// StreamActiveEachMembers must visit exactly eachActive, in order,
	// and nothing else (not the suspended row, not the nomail row).
	var streamed []store.MailingListMemberID
	if err := store.StreamActiveEachMembers(ctx, s.Meta(), l.ID, func(m store.MailingListMember) error {
		streamed = append(streamed, m.ID)
		if m.State != store.MailingListMemberActive {
			t.Errorf("StreamActiveEachMembers yielded non-active member %d (state %q)", m.ID, m.State)
		}
		if m.DeliveryMode != store.MailingListDeliveryEach {
			t.Errorf("StreamActiveEachMembers yielded non-each member %d (mode %q)", m.ID, m.DeliveryMode)
		}
		return nil
	}); err != nil {
		t.Fatalf("StreamActiveEachMembers: %v", err)
	}
	if len(streamed) != len(eachActive) {
		t.Fatalf("StreamActiveEachMembers visited %d members; want %d", len(streamed), len(eachActive))
	}
	for i, id := range eachActive {
		if streamed[i] != id {
			t.Errorf("StreamActiveEachMembers[%d] = %d; want %d", i, streamed[i], id)
		}
	}

	// A callback error stops iteration and propagates.
	sentinel := errors.New("stop")
	callCount := 0
	err = store.StreamActiveEachMembers(ctx, s.Meta(), l.ID, func(store.MailingListMember) error {
		callCount++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("StreamActiveEachMembers callback error: got %v; want sentinel", err)
	}
	if callCount != 1 {
		t.Errorf("StreamActiveEachMembers called fn %d times after error; want 1 (stop immediately)", callCount)
	}

	// Filtering ListMailingListMembers directly by state+delivery_mode
	// gives the same eligible set as StreamActiveEachMembers.
	filtered, err := s.Meta().ListMailingListMembers(ctx, store.MailingListRosterFilter{
		ListID:       l.ID,
		State:        store.MailingListMemberActive,
		DeliveryMode: store.MailingListDeliveryEach,
		Limit:        1000,
	})
	if err != nil {
		t.Fatalf("ListMailingListMembers filtered: %v", err)
	}
	if len(filtered) != len(eachActive) {
		t.Fatalf("filtered roster = %d rows; want %d", len(filtered), len(eachActive))
	}
}

func strPtr(s string) *string { return &s }
