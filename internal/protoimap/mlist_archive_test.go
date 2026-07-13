package protoimap_test

// Mailing-list archive mailbox, Stage 4 (epic #187,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-70..74).
// These are the protocol-level proof that the landed mailbox-grant
// substrate (epic #210) delivers REQ-MLIST-73's "reachable read-only
// through IMAP" with NO protocol change: the grant here is written by
// internal/maillist.SyncMemberArchiveGrant (the same call the admin REST
// roster handlers make), not by SETACL or a hand-rolled grant row, and
// every assertion runs over the real IMAP wire against a real
// *protoimap.Server -- exactly the fixture style acl_test.go already
// uses for the generic mailbox-grant surface (TestGrantMailboxRead_
// AllowsSelectFetch_DeniesWrite), applied to the mailing-list-specific
// wiring in internal/maillist/archive.go instead of a hand-seeded row.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// mlistArchiveFixture is a three-principal fixture: a Group principal
// backing the list (never itself logged into over IMAP), a member
// principal with a REQ-MLIST-72 archive read grant, and a non-member
// principal with none.
type mlistArchiveFixture struct {
	*aclFixture
	ml          store.MailingList
	archiveMB   store.Mailbox
	memberID    store.PrincipalID
	memberPass  string
	nonMemberID store.PrincipalID
	nonMemPass  string
}

func newMlistArchiveFixture(t *testing.T) *mlistArchiveFixture {
	t.Helper()
	af := newACLFixture(t)
	ctx := context.Background()

	group, err := af.ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindGroup, CanonicalEmail: "list@example.test", DisplayName: "Archive List",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	ml, err := af.ha.Store.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID: group.ID, PostingAddress: "list@example.test", DisplayName: "Archive List",
		OwnerID: af.aliceID,
	})
	if err != nil {
		t.Fatalf("InsertMailingList: %v", err)
	}
	archiveMB, err := af.ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: group.ID, Name: maillist.ArchiveMailboxName(ml), Attributes: store.MailboxAttrArchive,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(archive): %v", err)
	}
	ml.ArchiveMailboxID = &archiveMB.ID
	if err := af.ha.Store.Meta().UpdateMailingList(ctx, ml); err != nil {
		t.Fatalf("UpdateMailingList(set archive): %v", err)
	}

	dir := directory.New(af.ha.Store.Meta(), af.ha.Logger, af.ha.Clock, nil)
	memberPass := "member-correct-horse"
	memberID, err := dir.CreatePrincipal(ctx, "member@example.test", memberPass)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	nonMemPass := "outsider-correct-horse"
	nonMemberID, err := dir.CreatePrincipal(ctx, "outsider@example.test", nonMemPass)
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}

	memRow, err := af.ha.Store.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: ml.ID, PrincipalID: &memberID, DeliveryMode: store.MailingListDeliveryNoMail,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}
	// The exact call the admin REST roster handlers make (mlist_members.go)
	// -- proving the wiring, not a hand-rolled grant row.
	if err := maillist.SyncMemberArchiveGrant(ctx, af.ha.Store.Meta(), ml, memRow); err != nil {
		t.Fatalf("SyncMemberArchiveGrant: %v", err)
	}

	return &mlistArchiveFixture{
		aclFixture: af, ml: ml, archiveMB: archiveMB,
		memberID: memberID, memberPass: memberPass,
		nonMemberID: nonMemberID, nonMemPass: nonMemPass,
	}
}

// seedArchivePost inserts a message into f's archive mailbox, mirroring
// what Expander.fileArchive persists on a real fan-out (though here via
// direct InsertMessage since this file tests the READ surface, not the
// filing path -- filing is covered by internal/maillist/archive_test.go).
func (f *mlistArchiveFixture) seedArchivePost(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	msg := buildMessage("archived-post", "the body")
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	if err != nil {
		t.Fatalf("Blobs().Put: %v", err)
	}
	if _, _, err := f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{MailboxID: f.archiveMB.ID}}); err != nil {
		t.Fatalf("seed archive post: %v", err)
	}
}

// TestMlistArchive_Member_CanReadAndSearch_CannotMutate is the
// REQ-MLIST-72/73 core scenario: a nomail member with the maillist-
// granted read-only ACL can LIST/SELECT/FETCH the archive over IMAP, but
// APPEND, STORE \Deleted, and EXPUNGE are all refused.
func TestMlistArchive_Member_CanReadAndSearch_CannotMutate(t *testing.T) {
	f := newMlistArchiveFixture(t)
	f.seedArchivePost(t)
	archiveName := maillist.ArchiveMailboxName(f.ml)

	c := loginAsACL(t, f.aclFixture, "member@example.test", f.memberPass)
	defer c.close()

	// LIST: the archive is visible.
	listResp := c.send("l1", `LIST "" "*"`)
	if !strings.Contains(strings.Join(listResp, "\n"), archiveName) {
		t.Fatalf("archive not visible in LIST for a granted member: %v", listResp)
	}

	// SELECT + FETCH: the member can read the archived post.
	selResp := c.send("s1", fmt.Sprintf(`SELECT %q`, archiveName))
	if !strings.Contains(selResp[len(selResp)-1], "OK") {
		t.Fatalf("SELECT the archive failed for a granted member: %v", selResp)
	}
	fetchResp := c.send("f1", `FETCH 1 (UID FLAGS ENVELOPE)`)
	if !strings.Contains(strings.Join(fetchResp, "\n"), "archived-post") {
		t.Fatalf("FETCH did not return the archived post: %v", fetchResp)
	}

	// SEARCH: read access includes search (RFC 4314 "r").
	searchResp := c.send("se1", `SEARCH SUBJECT "archived-post"`)
	joined := strings.Join(searchResp, "\n")
	if !strings.Contains(joined, "SEARCH 1") && !strings.Contains(joined, "* SEARCH 1") {
		t.Fatalf("SEARCH did not find the archived post: %v", searchResp)
	}

	// APPEND: denied (no 'i').
	msg2 := buildMessage("member-cannot-append", "body")
	c.write(fmt.Sprintf("a1 APPEND %q {%d}\r\n", archiveName, len(msg2)))
	var last string
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "+") {
			c.write(msg2 + "\r\n")
			continue
		}
		if strings.HasPrefix(line, "a1 ") {
			last = line
			break
		}
	}
	if !strings.Contains(last, "NO") {
		t.Fatalf("archive member APPEND must be refused: %v", last)
	}

	// STORE \Deleted: denied (no 't').
	storeResp := c.send("st1", `STORE 1 +FLAGS (\Deleted)`)
	if !strings.Contains(storeResp[len(storeResp)-1], "NO") {
		t.Fatalf("archive member STORE +FLAGS (\\Deleted) must be refused: %v", storeResp)
	}

	// EXPUNGE: denied (no 'e').
	expungeResp := c.send("e1", "EXPUNGE")
	if !strings.Contains(expungeResp[len(expungeResp)-1], "NO") {
		t.Fatalf("archive member EXPUNGE must be refused: %v", expungeResp)
	}

	// The archive post itself must still be present (none of the denied
	// mutations landed).
	c2 := loginAsACL(t, f.aclFixture, "member@example.test", f.memberPass)
	defer c2.close()
	c2.send("s2", fmt.Sprintf(`SELECT %q`, archiveName))
	stat := c2.send("st2", `FETCH 1 (UID)`)
	if !strings.Contains(strings.Join(stat, "\n"), "FETCH") {
		t.Fatalf("archived post disappeared after the denied-mutation attempts: %v", stat)
	}
}

// TestMlistArchive_NonMember_CannotSelectOrList verifies a principal who
// is not on the list's roster at all (no grant row of any kind) cannot
// see or select the archive.
func TestMlistArchive_NonMember_CannotSelectOrList(t *testing.T) {
	f := newMlistArchiveFixture(t)
	f.seedArchivePost(t)
	archiveName := maillist.ArchiveMailboxName(f.ml)

	c := loginAsACL(t, f.aclFixture, "outsider@example.test", f.nonMemPass)
	defer c.close()

	listResp := c.send("l1", `LIST "" "*"`)
	if strings.Contains(strings.Join(listResp, "\n"), archiveName) {
		t.Fatalf("archive must not be visible to a non-member: %v", listResp)
	}
	selResp := c.send("s1", fmt.Sprintf(`SELECT %q`, archiveName))
	if !strings.Contains(selResp[len(selResp)-1], "NO") {
		t.Fatalf("SELECT by a non-member must be refused: %v", selResp)
	}
}

// TestMlistArchive_RemovedMember_LosesAccess exercises the roster-change
// half of REQ-MLIST-72: after RevokeMemberArchiveGrant runs (the DELETE
// .../members/{mid} handler's exact call), a formerly-granted member can
// no longer SELECT the archive -- asserted at the protocol level, on a
// FRESH connection (so a stale session's already-open SELECT cannot mask
// the revocation).
func TestMlistArchive_RemovedMember_LosesAccess(t *testing.T) {
	f := newMlistArchiveFixture(t)
	f.seedArchivePost(t)
	archiveName := maillist.ArchiveMailboxName(f.ml)

	// Before removal: SELECT succeeds.
	c1 := loginAsACL(t, f.aclFixture, "member@example.test", f.memberPass)
	selBefore := c1.send("s1", fmt.Sprintf(`SELECT %q`, archiveName))
	if !strings.Contains(selBefore[len(selBefore)-1], "OK") {
		t.Fatalf("SELECT before removal should succeed: %v", selBefore)
	}
	c1.close()

	// Simulate DELETE /api/v1/lists/{id}/members/{mid}: remove the
	// roster row, then revoke the grant exactly as
	// protoadmin.handleRemoveMailingListMember does.
	ctx := context.Background()
	members, lerr := f.ha.Store.Meta().ListMailingListMembers(ctx, store.MailingListRosterFilter{ListID: f.ml.ID, Limit: 10})
	if lerr != nil || len(members) != 1 {
		t.Fatalf("ListMailingListMembers: %v (%d rows)", lerr, len(members))
	}
	removed := members[0]
	if err := f.ha.Store.Meta().RemoveMailingListMember(ctx, removed.ID); err != nil {
		t.Fatalf("RemoveMailingListMember: %v", err)
	}
	if err := maillist.RevokeMemberArchiveGrant(ctx, f.ha.Store.Meta(), f.ml, removed); err != nil {
		t.Fatalf("RevokeMemberArchiveGrant: %v", err)
	}

	// After removal, on a brand-new connection: SELECT is refused and
	// the archive no longer appears in LIST.
	c2 := loginAsACL(t, f.aclFixture, "member@example.test", f.memberPass)
	defer c2.close()
	listResp := c2.send("l1", `LIST "" "*"`)
	if strings.Contains(strings.Join(listResp, "\n"), archiveName) {
		t.Fatalf("archive still visible in LIST after removal: %v", listResp)
	}
	selAfter := c2.send("s2", fmt.Sprintf(`SELECT %q`, archiveName))
	if !strings.Contains(selAfter[len(selAfter)-1], "NO") {
		t.Fatalf("SELECT after removal should be refused: %v", selAfter)
	}
}
