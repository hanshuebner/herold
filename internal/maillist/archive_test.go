package maillist_test

// archive_test.go — Stage 4 archive mailbox tests (epic #187,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-70..74):
// filing a fanned-out post into the archive exactly once via the shared
// blob-dedup path, nomail exclusion from email fan-out, and the
// mailbox:read grant lifecycle as the roster changes. Protocol-level
// (IMAP/JMAP) read-only enforcement tests live in internal/protoimap and
// internal/protojmap; this file covers the maillist package's own
// filing/grant-maintenance logic and the store-level blob-dedup
// guarantee.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// mustInsertArchiveMailbox creates ml's archive mailbox on its backing
// Group principal, mirroring the admin REST archive-enable path
// (maillist.ArchiveMailboxName), and returns the assigned id.
func mustInsertArchiveMailbox(t *testing.T, st store.Store, ml store.MailingList) store.MailboxID {
	t.Helper()
	mb, err := st.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: ml.PrincipalID,
		Name:        maillist.ArchiveMailboxName(ml),
		Attributes:  store.MailboxAttrArchive,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(archive): %v", err)
	}
	return mb.ID
}

// withArchive re-reads ml after setting ArchiveMailboxID (and, if
// non-zero, the retention fields) and persisting the change, so callers
// get back a MailingList whose ArchiveMailboxID Expand() will honour.
func withArchive(t *testing.T, st store.Store, ml store.MailingList, archiveID store.MailboxID) store.MailingList {
	t.Helper()
	ml.ArchiveMailboxID = &archiveID
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList(set archive): %v", err)
	}
	updated, err := st.Meta().GetMailingList(context.Background(), ml.ID)
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}
	return updated
}

// TestExpand_ArchiveFilesOnce_SharesBlobWithFanout is THE REQ-MLIST-70/11
// regression test: a post fanned out to N `each` members AND filed to the
// archive persists exactly ONE body blob, shared by every queue row and
// the archived copy alike -- driven through the REAL queue and REAL blob
// store, exactly like TestExpand_UnsubscribeEnabled_SharesOneBlob (issue
// #184) proves for the member-only case.
func TestExpand_ArchiveFilesOnce_SharesBlobWithFanout(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", false)
		const n = 5
		for i := 0; i < n; i++ {
			mustAddExternalMember(t, st, ml.ID, "m"+string(rune('0'+i))+"@example.net", store.MailingListMemberActive)
		}
		archiveID := mustInsertArchiveMailbox(t, st, ml)
		ml = withArchive(t, st, ml, archiveID)

		clk := clock.NewFake(time.Now())
		q := queue.New(queue.Options{Store: st, Logger: discardLogger(), Clock: clk})
		exp := maillist.NewExpander(st.Meta(), q, nil, clk, discardLogger())
		exp.Blobs = st.Blobs()
		res, err := exp.Expand(context.Background(), maillist.ExpandInput{
			List:   ml,
			Parsed: mustParse(t, testMessage),
			Raw:    []byte(testMessage),
		})
		if err != nil {
			t.Fatalf("Expand: %v", err)
		}
		if res.MemberCount != n {
			t.Fatalf("MemberCount = %d, want %d", res.MemberCount, n)
		}
		if !res.Archived {
			t.Fatalf("result.Archived = false; want true")
		}

		items, err := st.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
		if err != nil {
			t.Fatalf("ListQueueItems: %v", err)
		}
		if len(items) != n {
			t.Fatalf("queue items = %d, want %d", len(items), n)
		}
		distinct := map[string]bool{}
		var sharedHash string
		for _, it := range items {
			distinct[it.BodyBlobHash] = true
			sharedHash = it.BodyBlobHash
		}
		if len(distinct) != 1 {
			t.Fatalf("distinct queue body_blob_hash values = %d, want 1: %v", len(distinct), distinct)
		}

		archived, err := st.Meta().ListMessages(context.Background(), archiveID, store.MessageFilter{Limit: 10})
		if err != nil {
			t.Fatalf("ListMessages(archive): %v", err)
		}
		if len(archived) != 1 {
			t.Fatalf("archive holds %d messages, want exactly 1 (filed once)", len(archived))
		}
		if archived[0].Blob.Hash != sharedHash {
			t.Fatalf("archive blob hash = %q, want the SAME hash as the fan-out copies %q (dedup broken)",
				archived[0].Blob.Hash, sharedHash)
		}

		// Cross-check the store's own refcount: N member queue rows + 1
		// archive message must all reference the one blob.
		_, refCount, err := st.Meta().GetBlobRef(context.Background(), sharedHash)
		if err != nil {
			t.Fatalf("GetBlobRef(%q): %v", sharedHash, err)
		}
		if refCount != int64(n+1) {
			t.Fatalf("shared blob ref_count = %d, want %d (n members + 1 archive copy)", refCount, n+1)
		}
	})
}

// TestExpand_NomailMembers_NoEmail_ButArchived exercises REQ-MLIST-71: a
// `nomail` member receives no Submit call at all, while a mix of `each`
// and `nomail` members still produces exactly one archived copy.
func TestExpand_NomailMembers_NoEmail_ButArchived(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "each@example.net", store.MailingListMemberActive)

	nomailP, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "nomail@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(nomail): %v", err)
	}
	if _, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID:       ml.ID,
		PrincipalID:  &nomailP.ID,
		DeliveryMode: store.MailingListDeliveryNoMail,
	}); err != nil {
		t.Fatalf("AddMailingListMember(nomail): %v", err)
	}

	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	exp.Blobs = st.Blobs()
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.MemberCount != 1 {
		t.Fatalf("MemberCount = %d, want 1 (only the each member)", res.MemberCount)
	}
	calls := sub.Calls()
	if len(calls) != 1 || calls[0].Recipients[0] != "each@example.net" {
		t.Fatalf("Submit calls = %+v, want exactly one call to each@example.net (nomail must get no copy)", calls)
	}
	if !res.Archived {
		t.Fatalf("result.Archived = false; want true")
	}
	archived, err := st.Meta().ListMessages(context.Background(), archiveID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(archive): %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("archive holds %d messages, want 1", len(archived))
	}
}

// TestExpand_ArchiveFiledEvenWithOnlyNomailMembers verifies a list whose
// roster is entirely `nomail` (zero email fan-out) still archives every
// accepted post (REQ-MLIST-70: filed independent of the each-member
// loop).
func TestExpand_ArchiveFiledEvenWithOnlyNomailMembers(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	nomailP, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "nomail@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(nomail): %v", err)
	}
	if _, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID:       ml.ID,
		PrincipalID:  &nomailP.ID,
		DeliveryMode: store.MailingListDeliveryNoMail,
	}); err != nil {
		t.Fatalf("AddMailingListMember(nomail): %v", err)
	}
	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	exp.Blobs = st.Blobs()
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.MemberCount != 0 {
		t.Fatalf("MemberCount = %d, want 0 (nobody in each mode)", res.MemberCount)
	}
	if len(sub.Calls()) != 0 {
		t.Fatalf("Submit called %d times, want 0", len(sub.Calls()))
	}
	if !res.Archived {
		t.Fatalf("result.Archived = false; want true (archive-only list must still archive)")
	}
}

// TestExpand_NoArchiveConfigured_NotFiled verifies a list with no
// ArchiveMailboxID never touches an archive mailbox.
func TestExpand_NoArchiveConfigured_NotFiled(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	exp.Blobs = st.Blobs()
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Archived {
		t.Fatalf("result.Archived = true; want false (no archive configured)")
	}
}

// mustInsertPrincipalUser inserts a plain user principal, for grant-lifecycle
// tests below that need a principal member independent of Expand.
func mustInsertPrincipalUser(t *testing.T, st store.Store, email string) store.PrincipalID {
	t.Helper()
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%s): %v", email, err)
	}
	return p.ID
}

// archiveGrantRights returns the ACLRights principalID holds on
// archiveID, or 0 (with ok=false) if no row exists.
func archiveGrantRights(t *testing.T, st store.Store, archiveID store.MailboxID, principalID store.PrincipalID) (store.ACLRights, bool) {
	t.Helper()
	rows, err := st.Meta().GetMailboxACL(context.Background(), archiveID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	for _, row := range rows {
		if row.PrincipalID != nil && *row.PrincipalID == principalID {
			return row.Rights, true
		}
	}
	return 0, false
}

// TestSyncMemberArchiveGrant_ActivePrincipal_GetsReadOnlyGrant exercises
// REQ-MLIST-72's core case: an active internal-principal member gets a
// read-only (lrs) grant on the archive.
func TestSyncMemberArchiveGrant_ActivePrincipal_GetsReadOnlyGrant(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", false)
		archiveID := mustInsertArchiveMailbox(t, st, ml)
		ml = withArchive(t, st, ml, archiveID)
		pid := mustInsertPrincipalUser(t, st, "member@example.test")
		mem, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
			ListID: ml.ID, PrincipalID: &pid,
		})
		if err != nil {
			t.Fatalf("AddMailingListMember: %v", err)
		}

		if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
			t.Fatalf("SyncMemberArchiveGrant: %v", err)
		}
		rights, ok := archiveGrantRights(t, st, archiveID, pid)
		if !ok {
			t.Fatalf("no grant row for active member")
		}
		want := store.ACLRightLookup | store.ACLRightRead
		if rights != want {
			t.Fatalf("rights = %v, want read-only %v", rights, want)
		}
	})
}

// TestSyncMemberArchiveGrant_ExternalMember_NeverGrantedOrError verifies
// an external-address member (no PrincipalID) is silently skipped: no
// grant to write, and no error.
func TestSyncMemberArchiveGrant_ExternalMember_NeverGrantedOrError(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)
	mem := mustAddExternalMember(t, st, ml.ID, "ext@example.net", store.MailingListMemberActive)

	if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("SyncMemberArchiveGrant(external): %v", err)
	}
	rows, err := st.Meta().GetMailboxACL(context.Background(), archiveID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("GetMailboxACL = %+v; want no rows for an external-only roster", rows)
	}
}

// TestSyncMemberArchiveGrant_NoArchive_NoOp verifies a list with no
// archive never writes a grant, regardless of member state.
func TestSyncMemberArchiveGrant_NoArchive_NoOp(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	pid := mustInsertPrincipalUser(t, st, "member@example.test")
	mem, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID: ml.ID, PrincipalID: &pid,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}
	if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("SyncMemberArchiveGrant(no archive): %v", err)
	}
	// Nothing to assert against a mailbox that does not exist; the call
	// simply must not error (ml.ArchiveMailboxID == nil, no store write).
}

// TestSyncMemberArchiveGrant_StateTransitions_AddRemoveGrant exercises
// the full lifecycle: active -> suspended revokes, suspended -> active
// (reactivate) restores.
func TestSyncMemberArchiveGrant_StateTransitions_AddRemoveGrant(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)
	pid := mustInsertPrincipalUser(t, st, "member@example.test")
	mem, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID: ml.ID, PrincipalID: &pid,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}
	if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("sync (active): %v", err)
	}
	if _, ok := archiveGrantRights(t, st, archiveID, pid); !ok {
		t.Fatalf("expected a grant after adding an active member")
	}

	mem.State = store.MailingListMemberSuspended
	if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("sync (suspended): %v", err)
	}
	if _, ok := archiveGrantRights(t, st, archiveID, pid); ok {
		t.Fatalf("grant survived suspension")
	}

	mem.State = store.MailingListMemberActive
	if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("sync (reactivated): %v", err)
	}
	if _, ok := archiveGrantRights(t, st, archiveID, pid); !ok {
		t.Fatalf("grant not restored after reactivation")
	}
}

// TestRevokeMemberArchiveGrant_Unconditional verifies
// RevokeMemberArchiveGrant removes the grant regardless of the passed-in
// member's State field (the roster row is already gone by the time this
// is called from the DELETE handler).
func TestRevokeMemberArchiveGrant_Unconditional(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)
	pid := mustInsertPrincipalUser(t, st, "member@example.test")
	mem, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID: ml.ID, PrincipalID: &pid,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}
	if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := st.Meta().RemoveMailingListMember(context.Background(), mem.ID); err != nil {
		t.Fatalf("RemoveMailingListMember: %v", err)
	}
	// mem.State is still "active" (stale, roster row is gone) --
	// RevokeMemberArchiveGrant must not consult it.
	if err := maillist.RevokeMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("RevokeMemberArchiveGrant: %v", err)
	}
	if _, ok := archiveGrantRights(t, st, archiveID, pid); ok {
		t.Fatalf("grant survived RevokeMemberArchiveGrant")
	}
	// Idempotent: calling again on an already-revoked grant is a no-op,
	// not an error.
	if err := maillist.RevokeMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
		t.Fatalf("RevokeMemberArchiveGrant (idempotent repeat): %v", err)
	}
}

// TestGrantExistingActiveMembersArchiveAccess_Retroactive exercises the
// admin REST archive-enable path's helper: every currently-active
// internal-principal member gets a grant in one call, suspended/
// unsubscribed/external members do not.
func TestGrantExistingActiveMembersArchiveAccess_Retroactive(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)

	activePID := mustInsertPrincipalUser(t, st, "active@example.test")
	if _, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID: ml.ID, PrincipalID: &activePID,
	}); err != nil {
		t.Fatalf("add active: %v", err)
	}
	suspendedPID := mustInsertPrincipalUser(t, st, "suspended@example.test")
	suspMem, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID: ml.ID, PrincipalID: &suspendedPID,
	})
	if err != nil {
		t.Fatalf("add suspended: %v", err)
	}
	if err := st.Meta().UpdateMailingListMemberState(context.Background(), suspMem.ID, store.MailingListMemberSuspended); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	mustAddExternalMember(t, st, ml.ID, "ext@example.net", store.MailingListMemberActive)

	if err := maillist.GrantExistingActiveMembersArchiveAccess(context.Background(), st.Meta(), ml); err != nil {
		t.Fatalf("GrantExistingActiveMembersArchiveAccess: %v", err)
	}
	if _, ok := archiveGrantRights(t, st, archiveID, activePID); !ok {
		t.Errorf("active principal member missing retroactive grant")
	}
	if _, ok := archiveGrantRights(t, st, archiveID, suspendedPID); ok {
		t.Errorf("suspended member unexpectedly granted")
	}
	rows, err := st.Meta().GetMailboxACL(context.Background(), archiveID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("GetMailboxACL = %+v; want exactly 1 row (the active principal only)", rows)
	}
}

// TestRevokeAllArchiveGrants_RemovesEveryRow exercises the admin REST
// archive-disable path's helper.
func TestRevokeAllArchiveGrants_RemovesEveryRow(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)

	for i := 0; i < 3; i++ {
		pid := mustInsertPrincipalUser(t, st, "m"+string(rune('a'+i))+"@example.test")
		mem, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
			ListID: ml.ID, PrincipalID: &pid,
		})
		if err != nil {
			t.Fatalf("add member: %v", err)
		}
		if err := maillist.SyncMemberArchiveGrant(context.Background(), st.Meta(), ml, mem); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}
	rows, err := st.Meta().GetMailboxACL(context.Background(), archiveID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("setup: GetMailboxACL = %d rows, want 3", len(rows))
	}

	if err := maillist.RevokeAllArchiveGrants(context.Background(), st.Meta(), archiveID); err != nil {
		t.Fatalf("RevokeAllArchiveGrants: %v", err)
	}
	rows, err = st.Meta().GetMailboxACL(context.Background(), archiveID)
	if err != nil {
		t.Fatalf("GetMailboxACL after revoke: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("GetMailboxACL after RevokeAllArchiveGrants = %+v; want none", rows)
	}
}

// TestShapeMessage_ListArchiveHeader_OnlyWhenConfigured exercises
// REQ-MLIST-20's List-Archive header: present and correctly formed when
// the list has an archive; absent otherwise.
func TestShapeMessage_ListArchiveHeader_OnlyWhenConfigured(t *testing.T) {
	ml := mustTestList()
	out := string(maillist.ShapeMessage([]byte(testMessage), ml, "quarterly update"))
	if strings.Contains(out, "List-Archive:") {
		t.Fatalf("List-Archive present without an archive configured:\n%s", out)
	}

	archiveID := store.MailboxID(42)
	ml.ArchiveMailboxID = &archiveID
	out = string(maillist.ShapeMessage([]byte(testMessage), ml, "quarterly update"))
	want := "List-Archive: " + maillist.ListArchiveHeaderValue(ml) + "\r\n"
	if !strings.Contains(out, want) {
		t.Fatalf("List-Archive header missing or malformed:\nwant substring %q\ngot:\n%s", want, out)
	}
}

// mustTestList returns a minimal store.MailingList for header-shaping
// tests that do not need a store at all.
func mustTestList() store.MailingList {
	return store.MailingList{
		ID:             1,
		PostingAddress: "list@example.test",
		Domain:         "example.test",
		DisplayName:    "Test List",
	}
}
