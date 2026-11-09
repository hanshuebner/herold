package storetest

import (
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// testQueueAuthenticatedLocalRoundtrip exercises migration 0120's
// queue.authenticated_local origin marker (issue #559): true survives
// GetQueueItem and ListQueueItems, and a row that leaves it unset reads
// back false (the safe, inbound-treatment default).
func testQueueAuthenticatedLocalRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-authlocal@example.com")
	idLocal := mustEnqueue(t, s, store.QueueItem{
		PrincipalID:        p.ID,
		MailFrom:           "alice@example.com",
		RcptTo:             "bob@example.com",
		EnvelopeID:         "env-authlocal-1",
		AuthenticatedLocal: true,
	})
	idFwd := mustEnqueue(t, s, store.QueueItem{
		MailFrom:   "srs0=x@example.com",
		RcptTo:     "bob@example.com",
		EnvelopeID: "env-authlocal-2",
	})
	got, err := s.Meta().GetQueueItem(ctx, idLocal)
	if err != nil {
		t.Fatalf("GetQueueItem(local): %v", err)
	}
	if !got.AuthenticatedLocal {
		t.Fatalf("AuthenticatedLocal = false, want true")
	}
	gotFwd, err := s.Meta().GetQueueItem(ctx, idFwd)
	if err != nil {
		t.Fatalf("GetQueueItem(forward): %v", err)
	}
	if gotFwd.AuthenticatedLocal {
		t.Fatalf("AuthenticatedLocal = true on a row that did not set it")
	}
	list, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: "env-authlocal-1"})
	if err != nil || len(list) != 1 || !list[0].AuthenticatedLocal {
		t.Fatalf("ListQueueItems = %+v, %v; want one row with AuthenticatedLocal", list, err)
	}
}

// testQueueInteractiveSubmissionRoundtrip exercises migration 0122's
// queue.interactive_submission marker (issue #565): true survives
// GetQueueItem and ListQueueItems, and a row that leaves it unset reads
// back false, which is also what a row queued before the migration reads.
func testQueueInteractiveSubmissionRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-interactive@example.com")
	idI := mustEnqueue(t, s, store.QueueItem{
		PrincipalID:           p.ID,
		MailFrom:              "alice@example.com",
		RcptTo:                "bob@example.com",
		EnvelopeID:            "env-interactive-1",
		AuthenticatedLocal:    true,
		InteractiveSubmission: true,
	})
	idA := mustEnqueue(t, s, store.QueueItem{
		PrincipalID:        p.ID,
		MailFrom:           "alice@example.com",
		RcptTo:             "bob@example.com",
		EnvelopeID:         "env-interactive-2",
		AuthenticatedLocal: true,
	})
	got, err := s.Meta().GetQueueItem(ctx, idI)
	if err != nil || !got.InteractiveSubmission || !got.AuthenticatedLocal {
		t.Fatalf("GetQueueItem(interactive) = %+v, %v; want both markers", got, err)
	}
	gotA, err := s.Meta().GetQueueItem(ctx, idA)
	if err != nil || gotA.InteractiveSubmission || !gotA.AuthenticatedLocal {
		t.Fatalf("GetQueueItem(automated) = %+v, %v; want AuthenticatedLocal only", gotA, err)
	}
	list, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: "env-interactive-1"})
	if err != nil || len(list) != 1 || !list[0].InteractiveSubmission {
		t.Fatalf("ListQueueItems = %+v, %v; want one interactive row", list, err)
	}
}
