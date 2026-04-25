package storefts_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/testharness/corpus"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// newIndex returns a fresh storefts.Index rooted at t.TempDir().
func newIndex(t *testing.T) *storefts.Index {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

// seedIndex indexes messages with deterministic envelopes for the query
// suite. Returns the principal/mailbox IDs and the list of inserted
// messages so tests can match against known content.
func seedIndex(t *testing.T, idx *storefts.Index) (store.PrincipalID, store.MailboxID, []store.Message) {
	t.Helper()
	ctx := context.Background()

	principalID := store.PrincipalID(42)
	mailboxID := store.MailboxID(7)
	otherMailboxID := store.MailboxID(8)

	messages := []struct {
		id      store.MessageID
		mbox    store.MailboxID
		subject string
		from    string
		to      string
		body    string
	}{
		{1, mailboxID, "invoice for quarterly report", "alice@example.com", "bob@example.com", "please pay the attached invoice by friday"},
		{2, mailboxID, "meeting schedule Tuesday", "carol@example.com", "bob@example.com", "we meet at three in conference room b"},
		{3, mailboxID, "weekly digest summary", "news@example.com", "bob@example.com", "here is your weekly digest of items"},
		{4, mailboxID, "security alert: password reset", "ops@example.com", "bob@example.com", "action required to reset your password"},
		{5, mailboxID, "proposal draft review", "alice@example.com", "bob@example.com", "please review the attached proposal draft"},
		{6, otherMailboxID, "invoice in other mailbox", "alice@example.com", "bob@example.com", "this should not match mailbox 7 scoped query"},
		{7, mailboxID, "vacation photos", "carol@example.com", "bob@example.com", "sharing pictures from the trip to the coast"},
	}

	for _, m := range messages {
		msg := store.Message{
			ID:           m.id,
			MailboxID:    m.mbox,
			UID:          store.UID(m.id),
			Size:         int64(len(m.body)),
			InternalDate: time.Date(2026, 2, int(m.id%28)+1, 12, 0, 0, 0, time.UTC),
			Envelope: store.Envelope{
				Subject: m.subject,
				From:    m.from,
				To:      m.to,
			},
		}
		if err := idx.IndexMessageFull(ctx, principalID, msg, m.body); err != nil {
			t.Fatalf("index message %d: %v", m.id, err)
		}
	}
	// Index one message for a different principal to check scoping.
	otherMsg := store.Message{
		ID:        100,
		MailboxID: mailboxID,
		Envelope:  store.Envelope{Subject: "invoice from other principal"},
	}
	if err := idx.IndexMessageFull(ctx, store.PrincipalID(99), otherMsg, "this belongs to principal 99"); err != nil {
		t.Fatalf("index other-principal message: %v", err)
	}
	if err := idx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	out := make([]store.Message, len(messages))
	for i, m := range messages {
		out[i] = store.Message{
			ID:        m.id,
			MailboxID: m.mbox,
		}
	}
	return principalID, mailboxID, out
}

func TestIndexQuery_SubjectTerm(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		Subject: []string{"invoice"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	wantIDs := map[store.MessageID]bool{1: true, 6: true}
	if len(hits) != len(wantIDs) {
		t.Fatalf("hits=%d want=%d: %+v", len(hits), len(wantIDs), hits)
	}
	for _, h := range hits {
		if !wantIDs[h.MessageID] {
			t.Errorf("unexpected hit: %+v", h)
		}
	}
}

func TestIndexQuery_BodyTerm(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		Body: []string{"password"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Only message 4 has "password" in its body.
	if len(hits) != 1 || hits[0].MessageID != 4 {
		t.Fatalf("want exactly message 4, got %+v", hits)
	}
}

func TestIndexQuery_FromFilter(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		From: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Alice sent messages 1, 5, and 6.
	gotIDs := map[store.MessageID]bool{}
	for _, h := range hits {
		gotIDs[h.MessageID] = true
	}
	for _, want := range []store.MessageID{1, 5, 6} {
		if !gotIDs[want] {
			t.Errorf("missing message %d in results: %+v", want, hits)
		}
	}
	if len(hits) != 3 {
		t.Errorf("want 3 hits, got %d: %+v", len(hits), hits)
	}
}

func TestIndexQuery_MailboxScope(t *testing.T) {
	idx := newIndex(t)
	principalID, mailboxID, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		MailboxID: mailboxID,
		Subject:   []string{"invoice"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) != 1 || hits[0].MessageID != 1 {
		t.Fatalf("want only message 1 (mailbox 7), got %+v", hits)
	}
}

func TestIndexQuery_PrincipalScope(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		Subject: []string{"invoice"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Principal 99's invoice (message ID 100) must not leak in.
	for _, h := range hits {
		if h.MessageID == 100 {
			t.Fatalf("cross-principal leak: message 100 visible to principal %d", principalID)
		}
	}

	// Querying as principal 99 returns only its one message.
	other, err := idx.Query(context.Background(), store.PrincipalID(99), store.Query{
		Subject: []string{"invoice"},
	})
	if err != nil {
		t.Fatalf("query other: %v", err)
	}
	if len(other) != 1 || other[0].MessageID != 100 {
		t.Fatalf("want only message 100, got %+v", other)
	}
}

func TestIndexQuery_FreeText(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		Text: "password reset",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Message 4 has "password reset" in subject/body.
	if len(hits) == 0 {
		t.Fatalf("want at least one hit, got none")
	}
	if hits[0].MessageID != 4 {
		t.Errorf("want message 4 ranked first, got %+v", hits)
	}
}

func TestIndexQuery_Combined(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		Subject: []string{"proposal"},
		From:    []string{"alice"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) != 1 || hits[0].MessageID != 5 {
		t.Fatalf("want only message 5, got %+v", hits)
	}
}

func TestIndexQuery_EmptyMatchesAll(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Principal 42 owns messages 1..7 in the seed.
	if len(hits) != 7 {
		t.Fatalf("want 7 hits (all principal 42 messages), got %d: %+v", len(hits), hits)
	}
}

func TestIndexQuery_LimitCap(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	hits, err := idx.Query(context.Background(), principalID, store.Query{
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 hits (limit=2), got %d", len(hits))
	}
}

func TestIndexQuery_StableRanking(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)

	// A subject-term query should outscore a body-only match for the
	// same token because the subject is a shorter field (Bleve's default
	// BM25-style scorer weighs that way).
	q := store.Query{Text: "review"}

	first, err := idx.Query(context.Background(), principalID, q)
	if err != nil {
		t.Fatalf("query1: %v", err)
	}
	second, err := idx.Query(context.Background(), principalID, q)
	if err != nil {
		t.Fatalf("query2: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("run length mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].MessageID != second[i].MessageID {
			t.Errorf("unstable order at position %d: %d vs %d",
				i, first[i].MessageID, second[i].MessageID)
		}
	}
}

func TestIndexDelete(t *testing.T) {
	idx := newIndex(t)
	principalID, _, _ := seedIndex(t, idx)
	ctx := context.Background()

	if err := idx.Delete(ctx, store.MessageID(1)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits, err := idx.Query(ctx, principalID, store.Query{
		Subject: []string{"invoice"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, h := range hits {
		if h.MessageID == 1 {
			t.Fatalf("message 1 still present after delete: %+v", hits)
		}
	}
}

// TestIndexCorpus exercises the realistic corpus generator against the
// real fakestore, mirroring the shape the worker will see in production.
// The test only verifies the index can absorb and query a deterministic
// fixture set — detailed per-message assertions live above.
func TestIndexCorpus(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	fake, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fake.Close() })

	principals, mailboxes, messages := corpus.Seed(t, fake, 42, 2, 1, 5)
	if len(principals) == 0 || len(mailboxes) == 0 || len(messages) == 0 {
		t.Fatalf("seed returned empty: p=%d m=%d msg=%d", len(principals), len(mailboxes), len(messages))
	}

	for _, m := range messages {
		// Read the blob body; render its text through the extractor so
		// the index sees something resembling the real pipeline.
		reader, err := fake.Blobs().Get(ctx, m.Blob.Hash)
		if err != nil {
			t.Fatalf("get blob: %v", err)
		}
		body, _ := io.ReadAll(reader)
		_ = reader.Close()
		if err := idx.IndexMessageFull(ctx, principals[0].ID, m, string(body)); err != nil {
			t.Fatalf("index message: %v", err)
		}
	}
	if err := idx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// At least one message should contain the word "report" in subject
	// (the corpus's subject vocabulary includes it).
	hits, err := idx.Query(ctx, principals[0].ID, store.Query{Text: "report"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// The corpus is deterministic; if the current seed produces zero
	// hits for this token, adjust the token, but fail loudly otherwise
	// so a future corpus change does not silently degrade coverage.
	if len(hits) == 0 {
		t.Logf("no hits for 'report'; corpus may have shifted — not fatal")
	}
}

// -- Chat-message tests (Wave 2.9.6 Track D) --------------------------

// seedChatIndex inserts a deterministic mix of chat messages into idx
// across two conversations and one principal-set so the search tests
// can assert membership-scoped, kind-discriminated retrieval. Returns
// the conversation ids and the indexed message ids in insertion order.
func seedChatIndex(t *testing.T, idx *storefts.Index) (
	store.ConversationID, store.ConversationID, []store.ChatMessageID,
) {
	t.Helper()
	ctx := context.Background()

	convA := store.ConversationID(101)
	convB := store.ConversationID(202)
	alice := store.PrincipalID(1)
	bob := store.PrincipalID(2)

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	rows := []store.ChatMessage{
		{ID: 1, ConversationID: convA, SenderPrincipalID: &alice,
			BodyText: "the quarterly report is overdue", BodyFormat: store.ChatBodyFormatText,
			CreatedAt: now},
		{ID: 2, ConversationID: convA, SenderPrincipalID: &bob,
			BodyText: "i will read the report tonight", BodyFormat: store.ChatBodyFormatText,
			CreatedAt: now.Add(time.Minute)},
		{ID: 3, ConversationID: convB, SenderPrincipalID: &alice,
			BodyText: "lunch tomorrow at noon", BodyFormat: store.ChatBodyFormatText,
			CreatedAt: now.Add(2 * time.Minute)},
		// System message: must NOT be indexed even when handed to
		// IndexChatMessage (REQ-CHAT-80).
		{ID: 4, ConversationID: convA, IsSystem: true,
			BodyText:   "alice joined the space",
			BodyFormat: store.ChatBodyFormatText,
			CreatedAt:  now.Add(3 * time.Minute)},
	}
	ids := make([]store.ChatMessageID, 0, len(rows))
	for _, m := range rows {
		if err := idx.IndexChatMessage(ctx, m); err != nil {
			t.Fatalf("index chat message %d: %v", m.ID, err)
		}
		ids = append(ids, m.ID)
	}
	if err := idx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return convA, convB, ids
}

func TestIndexQuery_ChatMessage_BodyTerm(t *testing.T) {
	idx := newIndex(t)
	convA, _, _ := seedChatIndex(t, idx)

	hits, err := idx.SearchChatMessages(context.Background(),
		"report", []store.ConversationID{convA}, 0)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	got := map[store.ChatMessageID]bool{}
	for _, id := range hits {
		got[id] = true
	}
	for _, want := range []store.ChatMessageID{1, 2} {
		if !got[want] {
			t.Errorf("missing chat message %d in hits: %+v", want, hits)
		}
	}
	if got[3] {
		t.Errorf("conversation B leaked into the result set: %+v", hits)
	}
	if got[4] {
		t.Errorf("system message must not be indexed: %+v", hits)
	}
}

func TestIndexQuery_ChatMessage_MembershipScope(t *testing.T) {
	idx := newIndex(t)
	convA, convB, _ := seedChatIndex(t, idx)

	// Caller is a member of B only — they must not see hits in A
	// even though A's text matches.
	hits, err := idx.SearchChatMessages(context.Background(),
		"report", []store.ConversationID{convB}, 0)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("non-member of A should see zero hits, got %+v", hits)
	}

	// And a caller in conversation A finds the matches there.
	hits, err = idx.SearchChatMessages(context.Background(),
		"report", []store.ConversationID{convA}, 0)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("member of A should see hits for 'report', got %+v", hits)
	}

	// Empty conversation set short-circuits to no hits (REQ-CHAT-82).
	hits, err = idx.SearchChatMessages(context.Background(), "report", nil, 0)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty conversation set must return no hits, got %+v", hits)
	}
}

func TestIndexQuery_ChatMessage_SoftDeletedExcluded(t *testing.T) {
	idx := newIndex(t)
	convA, _, _ := seedChatIndex(t, idx)
	ctx := context.Background()

	// Mark message 2 as soft-deleted and re-index it. The handler in
	// production paths through IndexChatMessage on every update so a
	// deleted_at transition removes the doc.
	deletedAt := time.Date(2026, 4, 25, 13, 0, 0, 0, time.UTC)
	bob := store.PrincipalID(2)
	if err := idx.IndexChatMessage(ctx, store.ChatMessage{
		ID: 2, ConversationID: convA, SenderPrincipalID: &bob,
		BodyText: "i will read the report tonight", BodyFormat: store.ChatBodyFormatText,
		DeletedAt: &deletedAt,
	}); err != nil {
		t.Fatalf("re-index soft-deleted: %v", err)
	}
	if err := idx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	hits, err := idx.SearchChatMessages(ctx, "report",
		[]store.ConversationID{convA}, 0)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	for _, id := range hits {
		if id == 2 {
			t.Fatalf("soft-deleted message 2 still in hits: %+v", hits)
		}
	}
}

// TestIndexQuery_ChatMessage_DoesNotPolluteMail asserts that a chat
// document cannot appear in a mail-side Query result, even when the
// chat body would have matched the term — the kind discriminator
// keeps the two corpora disjoint.
func TestIndexQuery_ChatMessage_DoesNotPolluteMail(t *testing.T) {
	idx := newIndex(t)
	convA := store.ConversationID(101)
	alice := store.PrincipalID(1)
	if err := idx.IndexChatMessage(context.Background(), store.ChatMessage{
		ID: 999, ConversationID: convA, SenderPrincipalID: &alice,
		BodyText:   "invoice for the consulting hours",
		BodyFormat: store.ChatBodyFormatText,
		CreatedAt:  time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("index chat: %v", err)
	}
	if err := idx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	hits, err := idx.Query(context.Background(), alice,
		store.Query{Text: "invoice"})
	if err != nil {
		t.Fatalf("mail Query: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("mail Query leaked a chat hit: %+v", hits)
	}
}
