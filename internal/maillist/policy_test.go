package maillist_test

// policy_test.go — REQ-MLIST-80 posting-policy unit tests (issue #189):
// what each of open / members-only / announce-only / moderated admits,
// and what happens to a post that does not conform. Exercised through
// Expand itself (not an internal decidePosting call) so these are the
// same observable behaviour deliver_maillist.go drives in production.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// fixedNow anchors the fake clock used by this file's and hold_test.go's
// Expander instances.
var fixedNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// mustAddPrincipalMember adds an internal-principal roster row, creating
// the member's own backing User principal first.
func mustAddPrincipalMember(t *testing.T, st store.Store, listID store.MailingListID, email string, state store.MailingListMemberState) store.MailingListMember {
	t.Helper()
	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%s): %v", email, err)
	}
	m, err := st.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:      listID,
		PrincipalID: &p.ID,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember(%s): %v", email, err)
	}
	if state != store.MailingListMemberActive {
		if err := st.Meta().UpdateMailingListMemberState(ctx, m.ID, state); err != nil {
			t.Fatalf("UpdateMailingListMemberState(%s): %v", email, err)
		}
	}
	return m
}

// mustSetPostingPolicy patches ml.PostingPolicy and returns the reread row.
func mustSetPostingPolicy(t *testing.T, st store.Store, ml store.MailingList, policy store.MailingListPostingPolicy) store.MailingList {
	t.Helper()
	ml.PostingPolicy = policy
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList(posting_policy=%s): %v", policy, err)
	}
	updated, err := st.Meta().GetMailingList(context.Background(), ml.ID)
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}
	return updated
}

func postFrom(from, listAddr string) string {
	return "From: " + from + "\r\n" +
		"To: " + listAddr + "\r\n" +
		"Subject: test post\r\n" +
		"Message-ID: <policy-test@sender.test>\r\n" +
		"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
		"\r\n" +
		"Body text.\r\n"
}

// TestExpand_OpenPolicy_AnyPosterAllowed is the S1 default, unaffected
// by REQ-MLIST-80 -- a control case showing "open" changes nothing.
func TestExpand_OpenPolicy_AnyPosterAllowed(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "m1@example.net", store.MailingListMemberActive)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingOpen)

	exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, postFrom("stranger@elsewhere.test", ml.PostingAddress)),
		Raw:    []byte(postFrom("stranger@elsewhere.test", ml.PostingAddress)),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.Held {
		t.Fatalf("open policy: result = %+v, want neither dropped nor held", res)
	}
	if res.MemberCount != 1 {
		t.Fatalf("MemberCount = %d, want 1", res.MemberCount)
	}
}

// TestExpand_MembersOnly_MemberAllowed_NonMemberRejected is
// REQ-MLIST-80's members-only policy: a roster member (external address
// or principal) posts through; a stranger is REJECTED (dropped, no
// queue rows) rather than held.
func TestExpand_MembersOnly_MemberAllowed_NonMemberRejected(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "ext-member@example.net", store.MailingListMemberActive)
	mustAddPrincipalMember(t, st, ml.ID, "principal-member@example.test", store.MailingListMemberActive)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingMembersOnly)

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(fixedNow), discardLogger())

	// External-address member posts: allowed.
	raw := postFrom("ext-member@example.net", ml.PostingAddress)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand(member): %v", err)
	}
	if res.Dropped || res.Held {
		t.Fatalf("member post: result = %+v, want neither dropped nor held", res)
	}

	// Principal member posts (case-insensitive address match): allowed.
	raw = postFrom("Principal-Member@Example.Test", ml.PostingAddress)
	res, err = exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand(principal member): %v", err)
	}
	if res.Dropped || res.Held {
		t.Fatalf("principal member post: result = %+v, want neither dropped nor held", res)
	}

	// A stranger's post is rejected outright -- not held, no queue rows.
	raw = postFrom("stranger@elsewhere.test", ml.PostingAddress)
	res, err = exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand(stranger): %v", err)
	}
	if !res.Dropped || res.Held {
		t.Fatalf("stranger post: result = %+v, want Dropped=true, Held=false", res)
	}
	if res.DropReason != maillist.DropReasonNotMember {
		t.Fatalf("DropReason = %q, want %q", res.DropReason, maillist.DropReasonNotMember)
	}

	for _, call := range sub.Calls() {
		if strings.Contains(string(call.MailFrom), "stranger") {
			t.Fatalf("stranger's post reached the queue: %+v", call)
		}
	}
	// Each of the two allowed posts fans out to BOTH roster members
	// (fan-out targets the whole active roster, not just the poster).
	if len(sub.Calls()) != 4 {
		t.Fatalf("queue submissions = %d, want exactly 4 (2 allowed posts x 2 members)", len(sub.Calls()))
	}
}

// TestExpand_MembersOnly_SuspendedMemberRejected: a member whose roster
// state is not `active` may not post even though the address is on the
// roster (REQ-MLIST-03: only active members are members in good
// standing).
func TestExpand_MembersOnly_SuspendedMemberRejected(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "suspended@example.net", store.MailingListMemberSuspended)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingMembersOnly)

	exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
	raw := postFrom("suspended@example.net", ml.PostingAddress)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Dropped || res.DropReason != maillist.DropReasonNotMember {
		t.Fatalf("result = %+v, want Dropped with DropReasonNotMember", res)
	}
}

// TestExpand_MembersOnly_OwnerAlwaysAllowed: the list owner may post to
// a members-only list even when not on the roster.
func TestExpand_MembersOnly_OwnerAlwaysAllowed(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingMembersOnly)
	owner, err := st.Meta().GetPrincipalByID(context.Background(), ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}

	exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
	raw := postFrom(owner.CanonicalEmail, ml.PostingAddress)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.Held {
		t.Fatalf("owner post: result = %+v, want neither dropped nor held", res)
	}
}

// TestExpand_AnnounceOnly_OwnerAllowed_MemberRejected is REQ-MLIST-80's
// announce-only policy: only the list owner may post; a roster member
// who is not the owner is REJECTED just like a stranger.
func TestExpand_AnnounceOnly_OwnerAllowed_MemberRejected(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingAnnounceOnly)
	owner, err := st.Meta().GetPrincipalByID(context.Background(), ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(fixedNow), discardLogger())

	raw := postFrom(owner.CanonicalEmail, ml.PostingAddress)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand(owner): %v", err)
	}
	if res.Dropped || res.Held {
		t.Fatalf("owner post: result = %+v, want neither dropped nor held", res)
	}

	raw = postFrom("member@example.net", ml.PostingAddress)
	res, err = exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand(member): %v", err)
	}
	if !res.Dropped || res.DropReason != maillist.DropReasonNotAnnouncer {
		t.Fatalf("member post: result = %+v, want Dropped with DropReasonNotAnnouncer", res)
	}

	if len(sub.Calls()) != 1 {
		t.Fatalf("queue submissions = %d, want exactly 1 (only the owner's post)", len(sub.Calls()))
	}
}

// TestExpand_ModeratedPolicy_HoldsEveryPost: under `moderated`, even the
// list owner's own post is held -- REQ-MLIST-80 gives moderation no
// sender-based exemption.
func TestExpand_ModeratedPolicy_HoldsEveryPost(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)
	owner, err := st.Meta().GetPrincipalByID(context.Background(), ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(fixedNow), discardLogger())
	exp.Blobs = st.Blobs()
	raw := postFrom(owner.CanonicalEmail, ml.PostingAddress)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Held || res.Dropped {
		t.Fatalf("result = %+v, want Held=true, Dropped=false", res)
	}
	if len(sub.Calls()) != 0 {
		t.Fatalf("queue submissions = %d, want 0 (held, never fanned out)", len(sub.Calls()))
	}
	held, err := st.Meta().GetMailingListHeldPost(context.Background(), res.HeldPostID)
	if err != nil {
		t.Fatalf("GetMailingListHeldPost: %v", err)
	}
	if held.Status != store.MailingListHeldPostPending {
		t.Fatalf("held.Status = %q, want pending", held.Status)
	}
	if held.Reason != store.MailingListHeldReasonModerated {
		t.Fatalf("held.Reason = %q, want %q", held.Reason, store.MailingListHeldReasonModerated)
	}
}

// TestExpand_UnknownPostingPolicy_FailsOpen: a posting_policy value this
// binary does not recognise (e.g. a downgrade after a forward
// migration) must not silently block every post; it degrades to open.
func TestExpand_UnknownPostingPolicy_FailsOpen(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml = mustSetPostingPolicy(t, st, ml, "some-future-policy")

	exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
	raw := postFrom("anyone@elsewhere.test", ml.PostingAddress)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.Held {
		t.Fatalf("result = %+v, want neither dropped nor held (fail-open)", res)
	}
}
