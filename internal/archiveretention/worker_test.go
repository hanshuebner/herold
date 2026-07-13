package archiveretention_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/archiveretention"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// archiveFixture holds a store pre-populated with one mailing list and its
// archive mailbox. The FakeClock lets tests control InternalDate values.
type archiveFixture struct {
	store     store.Store
	clk       *clock.FakeClock
	listID    store.MailingListID
	archiveID store.MailboxID
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	owner, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "owner@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(owner): %v", err)
	}
	group, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindGroup, CanonicalEmail: "list@example.test", DisplayName: "List",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	archiveMB, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: group.ID, Name: "Lists/list@example.test", Attributes: store.MailboxAttrArchive,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(archive): %v", err)
	}
	ml, err := s.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID: group.ID, PostingAddress: "list@example.test", DisplayName: "List",
		OwnerID: owner.ID, ArchiveMailboxID: &archiveMB.ID,
	})
	if err != nil {
		t.Fatalf("InsertMailingList: %v", err)
	}
	return &archiveFixture{store: s, clk: clk, listID: ml.ID, archiveID: archiveMB.ID}
}

// setRetention patches the fixture's list with the given retention bounds.
func (f *archiveFixture) setRetention(t *testing.T, days, maxMessages int64) {
	t.Helper()
	ml, err := f.store.Meta().GetMailingList(context.Background(), f.listID)
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}
	ml.ArchiveRetentionDays = days
	ml.ArchiveRetentionMaxMessages = maxMessages
	if err := f.store.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList(retention): %v", err)
	}
}

// insertArchiveMessage inserts a message into the fixture's archive
// mailbox with the given InternalDate and body, returning its ID and blob
// hash. Distinct bodies get distinct blob hashes (content-addressed); an
// identical body reuses (dedups onto) the same hash and bumps its refcount.
func (f *archiveFixture) insertArchiveMessage(t *testing.T, internalDate time.Time, body string) (store.MessageID, string) {
	t.Helper()
	ctx := context.Background()
	blob, err := f.store.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("blob Put: %v", err)
	}
	group, err := f.store.Meta().GetMailingList(ctx, f.listID)
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}
	uid, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  group.PrincipalID,
		Blob:         blob,
		Size:         blob.Size,
		InternalDate: internalDate,
		ReceivedAt:   internalDate,
	}, []store.MessageMailbox{{MailboxID: f.archiveID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, err := f.store.Meta().ListMessages(ctx, f.archiveID, store.MessageFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m.ID, blob.Hash
		}
	}
	t.Fatalf("inserted message UID %d not found in ListMessages", uid)
	return 0, ""
}

func TestWorker_Defaults(t *testing.T) {
	f := newArchiveFixture(t)
	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store})
	if w.SweepInterval() != archiveretention.DefaultSweepInterval {
		t.Errorf("SweepInterval default = %v, want %v", w.SweepInterval(), archiveretention.DefaultSweepInterval)
	}
	if w.BatchSize() != archiveretention.DefaultBatchSize {
		t.Errorf("BatchSize default = %d, want %d", w.BatchSize(), archiveretention.DefaultBatchSize)
	}
}

// TestWorker_NoBoundConfigured_NoOp verifies a list with an archive but
// both retention fields left at 0 (unbounded) is visited but never
// deletes anything, regardless of message age.
func TestWorker_NoBoundConfigured_NoOp(t *testing.T) {
	f := newArchiveFixture(t)
	now := f.clk.Now()
	f.insertArchiveMessage(t, now.Add(-1000*24*time.Hour), "old body")

	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store, Clock: f.clk})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Tick deleted = %d, want 0 (no retention bound configured)", deleted)
	}
}

// TestWorker_NoArchive_NotVisited verifies a list with no archive
// mailbox (ArchiveMailboxID nil) is skipped even when it exists in the
// mailing_list table alongside a list that does have one.
func TestWorker_NoArchive_NotVisited(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	owner, err := f.store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "owner2@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	group2, err := f.store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindGroup, CanonicalEmail: "noarchive@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group2): %v", err)
	}
	if _, err := f.store.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID: group2.ID, PostingAddress: "noarchive@example.test",
		OwnerID: owner.ID, ArchiveRetentionDays: 1, // bound set, but no archive mailbox
	}); err != nil {
		t.Fatalf("InsertMailingList(no archive): %v", err)
	}

	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store, Clock: f.clk})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Tick deleted = %d, want 0", deleted)
	}
}

// TestWorker_AgeBound_ExpungesOldPosts_RootsGC is the REQ-MLIST-74 core
// scenario, doubling as the blob-GC-roots proof: a post within the
// retention window is RETAINED and its blob survives; a post older than
// the bound is EXPUNGED and, once its refcount reaches zero, becomes
// eligible for the store's blob GC. storeblobfs.Delete is a low-level
// filesystem primitive that does not itself inspect ref_count (see its
// doc comment: "Refcounting is Metadata's concern; this layer does not
// inspect refs") -- the root discipline lives entirely in the metadata
// store's ref_count bookkeeping (GetBlobRef), which is what this test
// asserts, and a correctly-behaving GC worker consults before ever
// calling Delete. The simulated GC step (a direct Delete call gated on
// ref_count==0, matching the documented contract) is exercised on the
// now-orphaned blob to prove it is physically collectible once the
// sweep has run.
func TestWorker_AgeBound_ExpungesOldPosts_RootsGC(t *testing.T) {
	f := newArchiveFixture(t)
	f.setRetention(t, 30, 0)
	now := f.clk.Now()

	oldID, oldHash := f.insertArchiveMessage(t, now.Add(-31*24*time.Hour), "expired post")
	keepID, keepHash := f.insertArchiveMessage(t, now.Add(-1*24*time.Hour), "retained post")

	ctx := context.Background()
	// Before the sweep: both blobs are referenced (ref_count 1 each) --
	// neither is GC-eligible yet per the store's own bookkeeping.
	if _, n, err := f.store.Meta().GetBlobRef(ctx, oldHash); err != nil || n != 1 {
		t.Fatalf("pre-sweep GetBlobRef(old) = (%d, %v), want (1, nil)", n, err)
	}
	if _, n, err := f.store.Meta().GetBlobRef(ctx, keepHash); err != nil || n != 1 {
		t.Fatalf("pre-sweep GetBlobRef(keep) = (%d, %v), want (1, nil)", n, err)
	}

	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store, Clock: f.clk})
	deleted, err := w.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("Tick deleted = %d, want 1", deleted)
	}

	// The expired message is gone from the archive; the retained one
	// survives untouched.
	if _, err := f.store.Meta().GetMessage(ctx, oldID); err == nil {
		t.Errorf("expired message %d survived the sweep", oldID)
	}
	if got, err := f.store.Meta().GetMessage(ctx, keepID); err != nil {
		t.Errorf("retained message %d unexpectedly gone: %v", keepID, err)
	} else if got.Blob.Hash != keepHash {
		t.Errorf("retained message blob hash changed: got %q, want %q", got.Blob.Hash, keepHash)
	}

	// ROOT DISCIPLINE: the expunged post's blob refcount reached zero --
	// eligible for GC -- while the retained post's blob is UNCHANGED at
	// ref_count 1: the retention sweep must never touch a blob still
	// referenced by a live archive message.
	if _, n, err := f.store.Meta().GetBlobRef(ctx, oldHash); err != nil || n != 0 {
		t.Fatalf("post-sweep GetBlobRef(old) = (%d, %v), want (0, nil)", n, err)
	}
	if _, n, err := f.store.Meta().GetBlobRef(ctx, keepHash); err != nil || n != 1 {
		t.Fatalf("post-sweep GetBlobRef(keep) = (%d, %v), want (1, nil) -- retention must not touch a still-live archive blob", n, err)
	}
	// The retained blob is still readable (never collected).
	rc, err := f.store.Blobs().Get(ctx, keepHash)
	if err != nil {
		t.Fatalf("retained blob %q not readable after sweep: %v", keepHash, err)
	}
	_ = rc.Close()
	// The expunged blob is now GC-eligible per the store's own refcount
	// bookkeeping, and the simulated GC step succeeds.
	if err := f.store.Blobs().Delete(ctx, oldHash); err != nil {
		t.Fatalf("Blobs().Delete(old) after sweep (ref_count=0) failed: %v", err)
	}
}

// TestWorker_AgeBound_SharedBlob_NotCollectedWhileOtherRefExists extends
// the roots proof to the fan-out-shared-blob case (REQ-MLIST-11): a post
// archived AND also present in a member's own mailbox (the same content,
// same blob hash, refcount 2) has its archive copy expired and expunged,
// but the blob survives because the member's own copy still references
// it -- the archive expunge decrements the shared refcount by exactly
// one, never below the number of surviving references.
func TestWorker_AgeBound_SharedBlob_NotCollectedWhileOtherRefExists(t *testing.T) {
	f := newArchiveFixture(t)
	f.setRetention(t, 30, 0)
	now := f.clk.Now()
	ctx := context.Background()

	const body = "shared fan-out body"
	archiveMsgID, hash := f.insertArchiveMessage(t, now.Add(-31*24*time.Hour), body)

	// A member's own mailbox holds the identical content -- same
	// content-addressed hash, refcount now 2.
	member, err := f.store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "member@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(member): %v", err)
	}
	inbox, err := f.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: member.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(INBOX): %v", err)
	}
	blob, err := f.store.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("blob Put: %v", err)
	}
	if blob.Hash != hash {
		t.Fatalf("content-addressed hash mismatch: %q vs %q (test fixture bug)", blob.Hash, hash)
	}
	memberMsgUID, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: member.ID, Blob: blob, Size: blob.Size,
		InternalDate: now, ReceivedAt: now,
	}, []store.MessageMailbox{{MailboxID: inbox.ID}})
	if err != nil {
		t.Fatalf("InsertMessage(member copy): %v", err)
	}
	if _, n, err := f.store.Meta().GetBlobRef(ctx, hash); err != nil || n != 2 {
		t.Fatalf("pre-sweep GetBlobRef = (%d, %v), want (2, nil)", n, err)
	}

	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store, Clock: f.clk})
	deleted, err := w.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("Tick deleted = %d, want 1 (the archive copy only)", deleted)
	}
	if _, err := f.store.Meta().GetMessage(ctx, archiveMsgID); err == nil {
		t.Errorf("archive copy survived the sweep")
	}

	// The blob's refcount dropped by exactly one -- the member's own
	// copy still references it, so it is NOT GC-eligible.
	if _, n, err := f.store.Meta().GetBlobRef(ctx, hash); err != nil || n != 1 {
		t.Fatalf("post-sweep GetBlobRef = (%d, %v), want (1, nil) -- member's own copy must keep it alive", n, err)
	}
	// A correctly-behaving GC worker checks ref_count first (it is >0
	// here) and would never call Delete on this hash; the blob stays
	// physically readable.
	rc, err := f.store.Blobs().Get(ctx, hash)
	if err != nil {
		t.Fatalf("blob %q not readable after archive-only sweep: %v", hash, err)
	}
	_ = rc.Close()
	// The member's own message is untouched.
	msgs, err := f.store.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(inbox): %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.UID == memberMsgUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("member's own copy disappeared after the archive-only sweep")
	}
}

// TestWorker_CountBound_ExpungesOldestExcess exercises the count-bound
// side of REQ-MLIST-74: with ArchiveRetentionMaxMessages=2 and 5 archived
// posts (all within any age bound), the sweep leaves exactly the 2
// newest and removes the 3 oldest.
func TestWorker_CountBound_ExpungesOldestExcess(t *testing.T) {
	f := newArchiveFixture(t)
	f.setRetention(t, 0, 2)
	now := f.clk.Now()

	var ids []store.MessageID
	for i := 0; i < 5; i++ {
		id, _ := f.insertArchiveMessage(t, now.Add(time.Duration(i)*time.Hour),
			"post "+string(rune('0'+i)))
		ids = append(ids, id)
	}

	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store, Clock: f.clk})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("Tick deleted = %d, want 3 (5 posts - 2 kept)", deleted)
	}

	ctx := context.Background()
	// The three OLDEST (ids[0..2]) are gone; the two NEWEST (ids[3],
	// ids[4]) survive.
	for i, id := range ids {
		_, err := f.store.Meta().GetMessage(ctx, id)
		if i < 3 {
			if err == nil {
				t.Errorf("oldest post index %d (id %d) survived; want expunged", i, id)
			}
		} else {
			if err != nil {
				t.Errorf("newest post index %d (id %d) unexpectedly gone: %v", i, id, err)
			}
		}
	}
	total, _, err := f.store.Meta().CountMessages(ctx, f.archiveID)
	if err != nil {
		t.Fatalf("CountMessages: %v", err)
	}
	if total != 2 {
		t.Fatalf("archive message count after sweep = %d, want 2", total)
	}
}

// TestWorker_CountBound_UnderLimit_NoOp verifies a count bound larger
// than the current archive population deletes nothing.
func TestWorker_CountBound_UnderLimit_NoOp(t *testing.T) {
	f := newArchiveFixture(t)
	f.setRetention(t, 0, 100)
	now := f.clk.Now()
	f.insertArchiveMessage(t, now, "only post")

	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store, Clock: f.clk})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Tick deleted = %d, want 0 (under the count bound)", deleted)
	}
}

// TestWorker_DoubleRunRejected verifies Run refuses a second concurrent
// invocation, mirroring internal/trashretention's own guard.
func TestWorker_DoubleRunRejected(t *testing.T) {
	f := newArchiveFixture(t)
	w := archiveretention.NewWorker(archiveretention.Options{Store: f.store, Clock: f.clk, SweepInterval: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	// Brief wait so the goroutine enters Run before we try a second one.
	time.Sleep(10 * time.Millisecond)
	err := w.Run(ctx)
	cancel()
	<-done
	if err == nil {
		t.Error("second Run call should have returned an error")
	}
}
