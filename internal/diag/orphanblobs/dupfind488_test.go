package orphanblobs_test

// dupfind488_test.go covers re #488: a live sent message's Message-ID
// reported as not duplicated by `diag orphan-blobs list`, so restore would
// not have refused it.
//
// TestList_FindsLiveRootByPlainMessageIDComparison reproduces the reported
// shape directly: a sent message that roots its own thread (ThreadID stays
// 0, matching the production row cited in the issue), a reply that
// converges into its thread, and a draft-autosave blob carrying the same
// Message-ID that was never inserted as a message row at all (the earlier,
// un-sent revision of the draft that became the live message once
// finished). This passes on current code -- a plain, normalised
// env_message_id comparison already finds the live root -- and guards
// against a regression in that comparison.
//
// TestList_SurfacesSearchAdminMessagesErrorRatherThanMisreporting is the
// actual regression test for the defect this investigation found: List's
// per-orphan duplicate/thread-reference lookup silently treated any
// SearchAdminMessages error as "no match", so a transient store error (a
// lock timeout, a cancelled query) made a live duplicate invisible to the
// dry-run listing while Restore's identical lookup correctly surfaced the
// same error and refused to proceed blind. This test injects such an error
// and proves List now fails loudly instead of returning a wrong result;
// it goes red against the pre-fix describeOrphan, which used
// `serr == nil && len(hits) > 0` to decide the flag and discarded serr
// otherwise.

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/diag/orphanblobs"
	"github.com/hanshuebner/herold/internal/store"
)

func TestList_FindsLiveRootByPlainMessageIDComparison(t *testing.T) {
	for _, be := range backends(t) {
		t.Run(be.name, func(t *testing.T) {
			st := be.open(t)
			ctx := context.Background()
			pid, inbox := setUpPrincipal(t, st, "dupfind488@example.test")

			d := time.Date(2026, 7, 31, 17, 26, 54, 0, time.UTC)
			// Root sent message: ThreadID stays 0, as message 2364 in the
			// issue's production example did -- nothing precedes it.
			rootID, _ := insertMessage(t, st, pid, inbox, "pxotjqpyk87q1bhxini-owgx@mx.netzhansa.com", "", "QUniLator v1.14.0", "hans@huebner.org", d)
			// A reply referencing the root, converging into its thread
			// (mirrors the issue's messages 2366/2367).
			insertMessage(t, st, pid, inbox, "reply1@test", "pxotjqpyk87q1bhxini-owgx@mx.netzhansa.com", "Re: QUniLator v1.14.0", "someone@example.test", d.Add(time.Hour))

			// A draft-autosave blob carrying the same Message-ID, never
			// inserted as a message row: an earlier revision of the draft
			// that became the live root once it was sent.
			raw := "Message-ID: <pxotjqpyk87q1bhxini-owgx@mx.netzhansa.com>\r\nSubject: QUniLator v1.14.0\r\nFrom: hans@huebner.org\r\nDate: " +
				d.Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n\r\n draft body\r\n"
			blobRef, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(raw)))
			if err != nil {
				t.Fatalf("Blobs.Put: %v", err)
			}

			orphans, err := orphanblobs.List(ctx, st)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			var found *orphanblobs.OrphanBlob
			for i := range orphans {
				if orphans[i].Hash == blobRef.Hash {
					found = &orphans[i]
				}
			}
			if found == nil {
				t.Fatalf("List did not report the draft-autosave orphan blob; got %d orphans", len(orphans))
			}
			if found.DuplicateOfLiveMessageID != rootID {
				t.Errorf("DuplicateOfLiveMessageID = %d; want %d (the live root message)", found.DuplicateOfLiveMessageID, rootID)
			}
		})
	}
}

// failingSearchMetadata wraps a store.Metadata, failing every
// SearchAdminMessages call whose filter names a MessageID -- simulating the
// transient store error (lock timeout, cancelled query) that describeOrphan
// must surface rather than silently read as "no duplicate".
type failingSearchMetadata struct {
	store.Metadata
	err error
}

func (m *failingSearchMetadata) SearchAdminMessages(ctx context.Context, filter store.AdminMessageFilter) ([]store.AdminMessageHit, error) {
	if filter.MessageID != "" {
		return nil, m.err
	}
	return m.Metadata.SearchAdminMessages(ctx, filter)
}

// failingSearchStore wraps a store.Store, substituting a
// *failingSearchMetadata for Meta() while leaving Blobs/FTS/Close on the
// wrapped store untouched.
type failingSearchStore struct {
	store.Store
	meta *failingSearchMetadata
}

func (s *failingSearchStore) Meta() store.Metadata { return s.meta }

func TestList_SurfacesSearchAdminMessagesErrorRatherThanMisreporting(t *testing.T) {
	for _, be := range backends(t) {
		t.Run(be.name, func(t *testing.T) {
			st := be.open(t)
			ctx := context.Background()
			pid, inbox := setUpPrincipal(t, st, "errinject488@example.test")

			d := time.Date(2026, 7, 31, 17, 26, 54, 0, time.UTC)
			liveID, _ := insertMessage(t, st, pid, inbox, "live@test", "", "Live message", "hans@huebner.org", d)
			_ = liveID

			raw := "Message-ID: <live@test>\r\nSubject: Live message\r\nFrom: hans@huebner.org\r\nDate: " +
				d.Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n\r\n draft body\r\n"
			if _, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(raw))); err != nil {
				t.Fatalf("Blobs.Put: %v", err)
			}

			injectedErr := errors.New("storesqlite: SearchAdminMessages: database is locked")
			failing := &failingSearchStore{Store: st, meta: &failingSearchMetadata{Metadata: st.Meta(), err: injectedErr}}

			_, err := orphanblobs.List(ctx, failing)
			if err == nil {
				t.Fatalf("List returned no error while SearchAdminMessages(MessageID) failed for every orphan; " +
					"want List to fail loudly instead of reporting a live duplicate as absent")
			}
			if !errors.Is(err, injectedErr) {
				t.Errorf("List error = %v; want it to wrap the injected SearchAdminMessages error %v", err, injectedErr)
			}
		})
	}
}
