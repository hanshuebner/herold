package fakestore_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

func newStore(t *testing.T) *fakestore.Store {
	t.Helper()
	s, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPrincipalCRUD(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if p.ID == 0 {
		t.Fatalf("expected ID assigned")
	}
	got, err := s.Meta().GetPrincipalByEmail(ctx, "ALICE@example.test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != p.ID {
		t.Fatalf("id mismatch: %d vs %d", got.ID, p.ID)
	}
	// Duplicate email -> ErrConflict
	_, err = s.Meta().InsertPrincipal(ctx, store.Principal{CanonicalEmail: "alice@example.test"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Missing -> ErrNotFound
	_, err = s.Meta().GetPrincipalByEmail(ctx, "bob@example.test")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMailboxAndMessageRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	if mb.UIDValidity == 0 {
		t.Fatalf("uidvalidity not assigned")
	}
	body := "From: bob\r\nSubject: hi\r\n\r\nhello there\r\n"
	ref, err := s.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	if ref.Hash == "" || ref.Size == 0 {
		t.Fatalf("bad blob ref: %+v", ref)
	}
	// Put again idempotent
	ref2, err := s.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put again: %v", err)
	}
	if ref2.Hash != ref.Hash {
		t.Fatalf("expected stable hash, got %s vs %s", ref2.Hash, ref.Hash)
	}
	uid, modseq, err := s.Meta().InsertMessage(ctx, store.Message{
		Size: ref.Size,
		Blob: ref,
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if uid == 0 || modseq == 0 {
		t.Fatalf("expected assigned uid/modseq, got %d/%d", uid, modseq)
	}
	// Read back body
	rc, err := s.Blobs().Get(ctx, ref.Hash)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Fatalf("blob mismatch: got %q want %q", got, body)
	}
}

func TestFTSIndexAndQuery(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, _ := s.Meta().InsertPrincipal(ctx, store.Principal{CanonicalEmail: "alice@example.test"})
	mb, _ := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	ref, _ := s.Blobs().Put(ctx, strings.NewReader("irrelevant"))
	msg := store.Message{Size: ref.Size, Blob: ref}
	_, _, err := s.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Re-read to get the assigned ID.
	// We cheat: the only message in a fresh store has ID 1.
	msgRow, err := s.Meta().GetMessage(ctx, 1)
	if err != nil {
		t.Fatalf("get msg: %v", err)
	}
	if err := s.FTS().IndexMessage(ctx, msgRow, "Hello quick fox budget report"); err != nil {
		t.Fatalf("index: %v", err)
	}
	hits, err := s.FTS().Query(ctx, p.ID, store.Query{Text: "budget"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	// No match
	hits, err = s.FTS().Query(ctx, p.ID, store.Query{Text: "unrelated"})
	if err != nil {
		t.Fatalf("query2: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(hits))
	}
}

func TestChangeFeedMonotonicity(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, _ := s.Meta().InsertPrincipal(ctx, store.Principal{CanonicalEmail: "alice@example.test"})
	mb, _ := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	ref, _ := s.Blobs().Put(ctx, strings.NewReader("x"))
	for i := 0; i < 3; i++ {
		_, _, err := s.Meta().InsertMessage(ctx, store.Message{Size: ref.Size, Blob: ref}, []store.MessageMailbox{{MailboxID: mb.ID}})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	changes, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	if err != nil {
		t.Fatalf("read feed: %v", err)
	}
	if len(changes) < 4 {
		t.Fatalf("expected at least 4 changes (1 mailbox + 3 messages), got %d", len(changes))
	}
	var last store.ChangeSeq
	for _, c := range changes {
		if c.Seq <= last {
			t.Fatalf("non-monotonic: prev=%d this=%d", last, c.Seq)
		}
		last = c.Seq
	}
	// FTS change feed is global; each InsertMessage fires one entry.
	ftsChanges, err := s.FTS().ReadChangeFeedForFTS(ctx, 0, 100)
	if err != nil {
		t.Fatalf("fts feed: %v", err)
	}
	var lastFTS uint64
	for _, c := range ftsChanges {
		if c.Seq <= lastFTS {
			t.Fatalf("non-monotonic fts: prev=%d this=%d", lastFTS, c.Seq)
		}
		lastFTS = c.Seq
	}
	if len(ftsChanges) != 3 {
		t.Fatalf("expected 3 fts entries, got %d", len(ftsChanges))
	}
}

func TestQuotaExceeded(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, _ := s.Meta().InsertPrincipal(ctx, store.Principal{
		CanonicalEmail: "alice@example.test",
		QuotaBytes:     10,
	})
	mb, _ := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	ref, _ := s.Blobs().Put(ctx, strings.NewReader("xxxxxxxxxxxxxxxxxx"))
	_, _, err := s.Meta().InsertMessage(ctx, store.Message{Size: 100, Blob: ref}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
}

func TestUnchangedSinceConflict(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, _ := s.Meta().InsertPrincipal(ctx, store.Principal{CanonicalEmail: "alice@example.test"})
	mb, _ := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	ref, _ := s.Blobs().Put(ctx, strings.NewReader("x"))
	_, _, err := s.Meta().InsertMessage(ctx, store.Message{Size: ref.Size, Blob: ref}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err = s.Meta().UpdateMessageFlags(ctx, 1, mb.ID, store.MessageFlagSeen, 0, nil, nil, 0)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// unchangedSince=0 disables check; with an old modseq we expect conflict.
	_, err = s.Meta().UpdateMessageFlags(ctx, 1, mb.ID, store.MessageFlagFlagged, 0, nil, nil, 0)
	if err != nil {
		t.Fatalf("update2: %v", err)
	}
	_, err = s.Meta().UpdateMessageFlags(ctx, 1, mb.ID, store.MessageFlagDraft, 0, nil, nil, 1)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}
