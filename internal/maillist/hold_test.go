package maillist_test

// hold_test.go — REQ-MLIST-80 held-post lifecycle tests (issue #189):
// a held post surviving a restart (a store row, not memory), approval
// fanning out through the SAME path as an allowed post with the
// REQ-MLIST-11 blob-dedup invariant intact, rejection/discard never
// fanning out and releasing the held blob reference, and the loop/abuse
// guards still applying to a moderated list's posts (they never become
// a way around REQ-MLIST-30..32).

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// openSQLiteStoreAt opens (or reopens) a SQLite store at a caller-chosen
// path, unlike openSQLiteStore's fresh-temp-file-per-call. Used to prove
// a held post survives a restart: the SAME on-disk file is closed and
// reopened in a new store handle with no in-memory state carried over.
func openSQLiteStoreAt(t *testing.T, path string) store.Store {
	t.Helper()
	clk := clock.NewFake(fixedNow)
	st, err := storesqlite.OpenWithRand(context.Background(), path, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestApproveHeldPost_FansOutNormally_OneBlobForNMembersPlusArchive is
// THE REQ-MLIST-11/80 regression test: a held post approved and fanned
// out to N members AND filed to the archive persists exactly ONE body
// blob, driven through the REAL queue and REAL blob store -- exactly
// like the equivalent S1/S4 dedup regression tests do for the
// never-held case.
func TestApproveHeldPost_FansOutNormally_OneBlobForNMembersPlusArchive(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		ml := mustInsertList(t, st, "list@example.test", false)
		const n = 5
		for i := 0; i < n; i++ {
			mustAddExternalMember(t, st, ml.ID, "m"+string(rune('0'+i))+"@example.net", store.MailingListMemberActive)
		}
		archiveID := mustInsertArchiveMailbox(t, st, ml)
		ml = withArchive(t, st, ml, archiveID)
		ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)

		clk := clock.NewFake(fixedNow)
		q := queue.New(queue.Options{Store: st, Logger: discardLogger(), Clock: clk})
		exp := maillist.NewExpander(st.Meta(), q, nil, clk, discardLogger())
		exp.Blobs = st.Blobs()

		raw := postFrom("poster@sender.test", ml.PostingAddress)
		res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
		if err != nil {
			t.Fatalf("Expand: %v", err)
		}
		if !res.Held {
			t.Fatalf("result = %+v, want Held=true", res)
		}

		// Nothing reached the queue or the archive while held.
		items, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 100})
		if err != nil {
			t.Fatalf("ListQueueItems (pre-approve): %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("queue items before approval = %d, want 0", len(items))
		}
		archivedPre, err := st.Meta().ListMessages(ctx, archiveID, store.MessageFilter{Limit: 10})
		if err != nil {
			t.Fatalf("ListMessages(archive, pre-approve): %v", err)
		}
		if len(archivedPre) != 0 {
			t.Fatalf("archive messages before approval = %d, want 0", len(archivedPre))
		}

		owner, err := st.Meta().GetPrincipalByID(ctx, ml.OwnerID)
		if err != nil {
			t.Fatalf("GetPrincipalByID(owner): %v", err)
		}
		fanRes, err := exp.ApproveHeldPost(ctx, res.HeldPostID, owner.ID)
		if err != nil {
			t.Fatalf("ApproveHeldPost: %v", err)
		}
		if fanRes.MemberCount != n {
			t.Fatalf("MemberCount = %d, want %d", fanRes.MemberCount, n)
		}
		if !fanRes.Archived {
			t.Fatalf("fanRes.Archived = false, want true")
		}

		items, err = st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 100})
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

		archived, err := st.Meta().ListMessages(ctx, archiveID, store.MessageFilter{Limit: 10})
		if err != nil {
			t.Fatalf("ListMessages(archive): %v", err)
		}
		if len(archived) != 1 {
			t.Fatalf("archive holds %d messages, want exactly 1", len(archived))
		}
		if archived[0].Blob.Hash != sharedHash {
			t.Fatalf("archive blob hash = %q, want the SAME hash as the fan-out copies %q", archived[0].Blob.Hash, sharedHash)
		}

		held, err := st.Meta().GetMailingListHeldPost(ctx, res.HeldPostID)
		if err != nil {
			t.Fatalf("GetMailingListHeldPost: %v", err)
		}
		if held.Status != store.MailingListHeldPostApproved {
			t.Fatalf("held.Status = %q, want approved", held.Status)
		}
		if held.DecidedBy == nil || *held.DecidedBy != owner.ID {
			t.Fatalf("held.DecidedBy = %v, want %d", held.DecidedBy, owner.ID)
		}

		// The held post's OWN blob reference was released on approval;
		// only the N queue rows + 1 archive copy remain as references.
		_, refCount, err := st.Meta().GetBlobRef(ctx, sharedHash)
		if err != nil {
			t.Fatalf("GetBlobRef(%q): %v", sharedHash, err)
		}
		if refCount != int64(n+1) {
			t.Fatalf("shared blob ref_count = %d, want %d (n members + 1 archive copy, held-post ref released)", refCount, n+1)
		}
	})
}

// TestRejectHeldPost_NeverFansOut_ReleasesBlob is the mirror security
// property: a rejected held post produces NO queue rows and NO archive
// copy, ever, and its blob reference is released so it becomes
// GC-eligible (REQ-MLIST-74's root-rules discipline: nothing keeps a
// discarded post's blob alive forever).
func TestRejectHeldPost_NeverFansOut_ReleasesBlob(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	archiveID := mustInsertArchiveMailbox(t, st, ml)
	ml = withArchive(t, st, ml, archiveID)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)

	clk := clock.NewFake(fixedNow)
	q := queue.New(queue.Options{Store: st, Logger: discardLogger(), Clock: clk})
	exp := maillist.NewExpander(st.Meta(), q, nil, clk, discardLogger())
	exp.Blobs = st.Blobs()

	raw := postFrom("poster@sender.test", ml.PostingAddress)
	res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Held {
		t.Fatalf("result = %+v, want Held=true", res)
	}
	held, err := st.Meta().GetMailingListHeldPost(ctx, res.HeldPostID)
	if err != nil {
		t.Fatalf("GetMailingListHeldPost: %v", err)
	}
	_, refCount, err := st.Meta().GetBlobRef(ctx, held.BlobHash)
	if err != nil {
		t.Fatalf("GetBlobRef (pre-reject): %v", err)
	}
	if refCount != 1 {
		t.Fatalf("blob ref_count while held = %d, want 1", refCount)
	}

	owner, err := st.Meta().GetPrincipalByID(ctx, ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}
	decided, err := exp.RejectHeldPost(ctx, res.HeldPostID, owner.ID, "spam")
	if err != nil {
		t.Fatalf("RejectHeldPost: %v", err)
	}
	if decided.Status != store.MailingListHeldPostRejected {
		t.Fatalf("decided.Status = %q, want rejected", decided.Status)
	}
	if decided.DecisionNote != "spam" {
		t.Fatalf("decided.DecisionNote = %q, want %q", decided.DecisionNote, "spam")
	}

	items, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("queue items after reject = %d, want 0", len(items))
	}
	archived, err := st.Meta().ListMessages(ctx, archiveID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(archive): %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("archive messages after reject = %d, want 0", len(archived))
	}

	_, refCount, err = st.Meta().GetBlobRef(ctx, held.BlobHash)
	if err != nil {
		t.Fatalf("GetBlobRef (post-reject): %v", err)
	}
	if refCount != 0 {
		t.Fatalf("blob ref_count after reject = %d, want 0 (released, GC-eligible)", refCount)
	}
}

// TestDiscardHeldPost_NeverFansOut_ReleasesBlob mirrors the reject test
// for the discard disposition.
func TestDiscardHeldPost_NeverFansOut_ReleasesBlob(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)

	clk := clock.NewFake(fixedNow)
	q := queue.New(queue.Options{Store: st, Logger: discardLogger(), Clock: clk})
	exp := maillist.NewExpander(st.Meta(), q, nil, clk, discardLogger())
	exp.Blobs = st.Blobs()

	raw := postFrom("poster@sender.test", ml.PostingAddress)
	res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	held, err := st.Meta().GetMailingListHeldPost(ctx, res.HeldPostID)
	if err != nil {
		t.Fatalf("GetMailingListHeldPost: %v", err)
	}

	owner, err := st.Meta().GetPrincipalByID(ctx, ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}
	decided, err := exp.DiscardHeldPost(ctx, res.HeldPostID, owner.ID, "")
	if err != nil {
		t.Fatalf("DiscardHeldPost: %v", err)
	}
	if decided.Status != store.MailingListHeldPostDiscarded {
		t.Fatalf("decided.Status = %q, want discarded", decided.Status)
	}

	items, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("queue items after discard = %d, want 0", len(items))
	}
	_, refCount, err := st.Meta().GetBlobRef(ctx, held.BlobHash)
	if err != nil {
		t.Fatalf("GetBlobRef: %v", err)
	}
	if refCount != 0 {
		t.Fatalf("blob ref_count after discard = %d, want 0", refCount)
	}
}

// TestApproveHeldPost_AlreadyDecided_Conflict: a second decision on an
// already-decided held post is refused (ErrConflict), not silently
// re-applied -- guards against a double-approve double-fanning-out a
// post, or a reject racing an approve.
func TestApproveHeldPost_AlreadyDecided_Conflict(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "m1@example.net", store.MailingListMemberActive)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)

	clk := clock.NewFake(fixedNow)
	q := queue.New(queue.Options{Store: st, Logger: discardLogger(), Clock: clk})
	exp := maillist.NewExpander(st.Meta(), q, nil, clk, discardLogger())
	exp.Blobs = st.Blobs()

	raw := postFrom("poster@sender.test", ml.PostingAddress)
	res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	owner, err := st.Meta().GetPrincipalByID(ctx, ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}
	if _, err := exp.ApproveHeldPost(ctx, res.HeldPostID, owner.ID); err != nil {
		t.Fatalf("first ApproveHeldPost: %v", err)
	}
	if _, err := exp.ApproveHeldPost(ctx, res.HeldPostID, owner.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second ApproveHeldPost: err = %v, want ErrConflict", err)
	}
	if _, err := exp.RejectHeldPost(ctx, res.HeldPostID, owner.ID, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("RejectHeldPost after approve: err = %v, want ErrConflict", err)
	}

	items, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue items = %d, want exactly 1 (one member, fanned out exactly once)", len(items))
	}
}

// TestExpand_ModeratedPolicy_LoopGuardStillApplies is REQ-MLIST-30
// applied to a moderated list: a post that already carries the list's
// own List-ID is dropped by the loop guard, never held -- a held post
// must not become a way around the S1 loop/abuse guards.
func TestExpand_ModeratedPolicy_LoopGuardStillApplies(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)

	exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
	exp.Blobs = st.Blobs()

	raw := "From: poster@sender.test\r\n" +
		"To: " + ml.PostingAddress + "\r\n" +
		"Subject: loop\r\n" +
		"List-ID: " + maillist.ListIDHeaderValue(ml) + "\r\n" +
		"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
		"\r\n" +
		"Body.\r\n"
	res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Held {
		t.Fatalf("result = %+v, want Held=false (loop guard must fire before the moderated hold)", res)
	}
	if !res.Dropped || res.DropReason != maillist.DropReasonLoop {
		t.Fatalf("result = %+v, want Dropped with DropReasonLoop", res)
	}

	posts, err := st.Meta().ListMailingListHeldPosts(ctx, store.MailingListHeldPostFilter{ListID: ml.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMailingListHeldPosts: %v", err)
	}
	if len(posts) != 0 {
		t.Fatalf("held posts = %d, want 0 (the loop-flagged post must never be held)", len(posts))
	}
}

// TestExpand_ModeratedPolicy_AutoSubmittedStillRejected mirrors the loop
// test for REQ-MLIST-31 (Auto-Submitted).
func TestExpand_ModeratedPolicy_AutoSubmittedStillRejected(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)

	exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
	exp.Blobs = st.Blobs()

	raw := "From: bounce@sender.test\r\n" +
		"To: " + ml.PostingAddress + "\r\n" +
		"Subject: auto\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
		"\r\n" +
		"Body.\r\n"
	res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Held || !res.Dropped || res.DropReason != maillist.DropReasonAutoSubmitted {
		t.Fatalf("result = %+v, want Dropped with DropReasonAutoSubmitted, Held=false", res)
	}
}

// TestExpand_ModeratedPolicy_OversizeStillRejected mirrors the loop test
// for REQ-MLIST-32 (oversize).
func TestExpand_ModeratedPolicy_OversizeStillRejected(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)
	ml.MaxMessageSizeBytes = 100
	if err := st.Meta().UpdateMailingList(ctx, ml); err != nil {
		t.Fatalf("UpdateMailingList(max size): %v", err)
	}
	ml, err := st.Meta().GetMailingList(ctx, ml.ID)
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}

	exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
	exp.Blobs = st.Blobs()

	big := make([]byte, 500)
	for i := range big {
		big[i] = 'x'
	}
	raw := "From: poster@sender.test\r\n" +
		"To: " + ml.PostingAddress + "\r\n" +
		"Subject: big\r\n" +
		"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
		"\r\n" + string(big) + "\r\n"
	res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Held || !res.Dropped || res.DropReason != maillist.DropReasonOversize {
		t.Fatalf("result = %+v, want Dropped with DropReasonOversize, Held=false", res)
	}
}

// TestExpand_ModeratedPolicy_HoldSurvivesRestart proves a held post is a
// durable store row: re-opening the SAME on-disk SQLite file in a fresh
// process-level store handle still finds the pending held post, with no
// in-memory state carried over.
func TestExpand_ModeratedPolicy_HoldSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dsn := dir + "/test.db"

	var heldID store.MailingListHeldPostID
	var listID store.MailingListID
	func() {
		st := openSQLiteStoreAt(t, dsn)
		ctx := context.Background()
		ml := mustInsertList(t, st, "list@example.test", false)
		ml = mustSetPostingPolicy(t, st, ml, store.MailingListPostingModerated)
		listID = ml.ID

		exp := maillist.NewExpander(st.Meta(), &fakeSubmitter{}, nil, clock.NewFake(fixedNow), discardLogger())
		exp.Blobs = st.Blobs()
		raw := postFrom("poster@sender.test", ml.PostingAddress)
		res, err := exp.Expand(ctx, maillist.ExpandInput{List: ml, Parsed: mustParse(t, raw), Raw: []byte(raw)})
		if err != nil {
			t.Fatalf("Expand: %v", err)
		}
		if !res.Held {
			t.Fatalf("result = %+v, want Held=true", res)
		}
		heldID = res.HeldPostID
		_ = st.Close()
	}()

	// Fresh store handle over the same file -- simulates a server restart.
	st2 := openSQLiteStoreAt(t, dsn)
	held, err := st2.Meta().GetMailingListHeldPost(context.Background(), heldID)
	if err != nil {
		t.Fatalf("GetMailingListHeldPost after reopen: %v", err)
	}
	if held.ListID != listID || held.Status != store.MailingListHeldPostPending {
		t.Fatalf("held post after reopen = %+v, want ListID=%d Status=pending", held, listID)
	}
	// The blob itself is durable too -- readable back from the reopened
	// store's blob directory.
	rc, err := st2.Blobs().Get(context.Background(), held.BlobHash)
	if err != nil {
		t.Fatalf("Blobs().Get after reopen: %v", err)
	}
	_ = rc.Close()
}
