package maillist_test

// hold_concurrency_test.go — issue #189 verification fix: N goroutines
// calling ApproveHeldPost concurrently for the SAME held post must fan
// it out AT MOST ONCE, no matter how many of them race the approve
// call. This is the property a purely sequential test (call, then call
// again) cannot exercise -- run with -race and a high -count to shake
// out any reordering the two-phase claim/fanOut/finalize protocol in
// hold.go might still allow.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// concurrentApproveGoroutines is the fan-in width for
// TestApproveHeldPost_ConcurrentApprove_ExactlyOneFanOut, matching the
// scale the verification report's own repro used (20 goroutines, 4 of 5
// runs double-fanned-out against the pre-fix code).
const concurrentApproveGoroutines = 20

// TestApproveHeldPost_ConcurrentApprove_ExactlyOneFanOut drives
// concurrentApproveGoroutines goroutines against ApproveHeldPost for the
// SAME held post, backed by a REAL SQLite store and a REAL queue.Queue
// (not a fake submitter), and asserts:
//
//   - exactly one goroutine's ApproveHeldPost call returns success;
//   - every other goroutine gets store.ErrConflict (never any other
//     error, and never a silent success);
//   - the member is fanned out EXACTLY ONCE: one queue row, not
//     concurrentApproveGoroutines of them.
//
// Run with -race (data-race detector) and a high -count (e.g.
// `go test -race -count=20 -run TestApproveHeldPost_ConcurrentApprove_ExactlyOneFanOut ./internal/maillist/...`)
// to shake out scheduling-order flakiness; the underlying claim CAS
// (ClaimMailingListHeldPostForApproval) is expected to make the outcome
// deterministic run over run, not merely probable.
func TestApproveHeldPost_ConcurrentApprove_ExactlyOneFanOut(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)
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
		t.Fatalf("Expand result = %+v, want Held=true", res)
	}

	owner, err := st.Meta().GetPrincipalByID(ctx, ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}

	var (
		wg        sync.WaitGroup
		successes int32
		conflicts int32
		other     int32
	)
	var otherErrs []error
	var otherErrsMu sync.Mutex
	for i := 0; i < concurrentApproveGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, aerr := exp.ApproveHeldPost(ctx, res.HeldPostID, owner.ID)
			switch {
			case aerr == nil:
				atomic.AddInt32(&successes, 1)
			case errors.Is(aerr, store.ErrConflict):
				atomic.AddInt32(&conflicts, 1)
			default:
				atomic.AddInt32(&other, 1)
				otherErrsMu.Lock()
				otherErrs = append(otherErrs, aerr)
				otherErrsMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if other != 0 {
		t.Fatalf("unexpected non-ErrConflict errors from %d goroutines: %v", other, otherErrs)
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 (successes+conflicts=%d+%d, goroutines=%d)",
			successes, successes, conflicts, concurrentApproveGoroutines)
	}
	if conflicts != concurrentApproveGoroutines-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, concurrentApproveGoroutines-1)
	}

	items, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue items = %d, want EXACTLY 1 (the single member fanned out at most once "+
			"despite %d concurrent ApproveHeldPost calls)", len(items), concurrentApproveGoroutines)
	}

	held, err := st.Meta().GetMailingListHeldPost(ctx, res.HeldPostID)
	if err != nil {
		t.Fatalf("GetMailingListHeldPost: %v", err)
	}
	if held.Status != store.MailingListHeldPostApproved {
		t.Fatalf("held.Status = %q, want approved", held.Status)
	}
}

// TestApproveHeldPost_ResumeAfterInterruptedFanOut_DoesNotDoubleSend
// proves the crash-recovery half of the fix: a held post left in
// MailingListHeldPostApproving (simulating a process death between the
// claim and the finalize, after fan-out already reached some members)
// is resumed by a later ApproveHeldPost call without mailing the
// already-delivered member a second time -- the per-member idempotency
// key collapses the retried Submit to a no-op.
func TestApproveHeldPost_ResumeAfterInterruptedFanOut_DoesNotDoubleSend(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)
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

	// Simulate the "died after claim, fan-out already ran once" window
	// directly: claim the row, then run fanOut's equivalent (a full
	// ApproveHeldPost call, which will claim+fanOut+finalize in one
	// shot the first time since nothing is holding it open). To
	// actually exercise the RESUME branch (status already 'approving'
	// when ApproveHeldPost is entered), call approve twice back to
	// back is not enough (the first call finalizes to 'approved'); this
	// test instead forces the row back to 'approving' post-hoc via the
	// same store method a crash would leave it in, then resumes.
	if _, err := st.Meta().ClaimMailingListHeldPostForApproval(ctx, res.HeldPostID, owner.ID, clk.Now()); err != nil {
		t.Fatalf("ClaimMailingListHeldPostForApproval: %v", err)
	}
	held, err := st.Meta().GetMailingListHeldPost(ctx, res.HeldPostID)
	if err != nil {
		t.Fatalf("GetMailingListHeldPost: %v", err)
	}
	if held.Status != store.MailingListHeldPostApproving {
		t.Fatalf("held.Status after claim = %q, want approving", held.Status)
	}

	// Now resume TWICE concurrently, simulating two racing recovery
	// attempts (an operator re-clicking approve, or two admin processes
	// both noticing the stuck row) against the SAME already-claimed
	// held post. Both calls run fanOut (harmless: the idempotency key
	// collapses whichever one loses the Submit race to a no-op), but
	// only one can win FinalizeMailingListHeldPostApproval's CAS, or —
	// if one resumer's initial status read happens only after the other
	// has ALREADY finalized — it may see the row already Approved and
	// return ErrConflict immediately, without running fanOut at all
	// (also correct: nothing left to resume). Either nil or
	// store.ErrConflict is an acceptable per-call outcome here; any
	// OTHER error is not.
	var wg sync.WaitGroup
	errsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, aerr := exp.ApproveHeldPost(ctx, res.HeldPostID, owner.ID)
			errsCh <- aerr
		}()
	}
	wg.Wait()
	close(errsCh)
	for aerr := range errsCh {
		if aerr != nil && !errors.Is(aerr, store.ErrConflict) {
			t.Errorf("resume call error: %v, want nil or ErrConflict", aerr)
		}
	}

	items, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue items after two concurrent resumes = %d, want EXACTLY 1 "+
			"(idempotency key must prevent the resumed fan-out from double-mailing the member)", len(items))
	}

	final, err := st.Meta().GetMailingListHeldPost(ctx, res.HeldPostID)
	if err != nil {
		t.Fatalf("GetMailingListHeldPost: %v", err)
	}
	if final.Status != store.MailingListHeldPostApproved {
		t.Fatalf("final.Status = %q, want approved", final.Status)
	}
}
