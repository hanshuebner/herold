package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/chat"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/testharness"
)

// ftsFixture is the FTS-wired analogue of fixture (block_test.go). It
// composes the same alice/bob/carol principals but routes Message/query
// through a real storefts.Index + Worker so the FTS path
// (Wave 2.9.6 Track D, REQ-CHAT-80..82) is exercised end-to-end.
type ftsFixture struct {
	*fixture
	idx    *storefts.Index
	worker *storefts.Worker
	clk    *clock.FakeClock
}

func setupFTSFixture(t *testing.T) *ftsFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	srv, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
		Clock:     clk,
	})

	ctx := context.Background()
	alice, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal alice: %v", err)
	}
	bob, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
		DisplayName:    "Bob",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal bob: %v", err)
	}
	carol, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "carol@example.test",
		DisplayName:    "Carol",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal carol: %v", err)
	}

	plaintext := "hk_fts_alice_" + fmt.Sprintf("%d", alice.ID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: alice.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	w := storefts.NewWorker(idx, srv.Store, stringExtractor{}, nil, clk, storefts.WorkerOptions{})
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(wctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	chat.RegisterWithFTS(jmapServ.Registry(), srv.Store, idx, srv.Logger, srv.Clock, chat.DefaultLimits())

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	base0 := &fixture{
		srv: srv, pid: alice.ID, otherPID: bob.ID, thirdPID: carol.ID,
		client: client, baseURL: base, apiKey: plaintext, jmapServ: jmapServ,
	}
	return &ftsFixture{fixture: base0, idx: idx, worker: w, clk: clk}
}

// stringExtractor is a deterministic TextExtractor for tests, mirroring
// the storefts test helper. The chat FTS path does not currently
// exercise the email body extractor, but the Worker constructor
// requires one.
type stringExtractor struct{}

func (stringExtractor) Extract(ctx context.Context, _ store.Message, body io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// flushFTS drives the worker through one flush cycle so freshly
// inserted chat messages become searchable. Capture the flush signal
// BEFORE advancing the clock to close the race window where a fast
// worker (under heavy parallel CPU load) can flush between the Advance
// and the wait.
func (f *ftsFixture) flushFTS(t *testing.T) {
	t.Helper()
	flushSig := f.worker.CurrentFlushSignal()
	f.clk.Advance(storefts.DefaultFlushInterval + 10*time.Millisecond)
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-flushSig:
	case <-waitCtx.Done():
		t.Fatalf("wait for flush: %v", waitCtx.Err())
	}
}

func TestMessage_Query_FTS_TextFilter(t *testing.T) {
	f := setupFTSFixture(t)

	// alice creates a Space with bob + carol and posts two messages.
	cidA, _ := createSpace(t, f.fixture, "team")
	mPlanned := sendMessage(t, f.fixture, cidA, "we should plan the quarterly report tomorrow")
	mLunch := sendMessage(t, f.fixture, cidA, "lunch at noon would be nice")
	_ = mLunch

	// alice creates a DM with bob and posts a third.
	cidDM, _ := createDM(t, f.fixture)
	mDM := sendMessage(t, f.fixture, cidDM, "private message about the report")

	f.flushFTS(t)

	// Free-text search picks up the two "report" hits across both
	// conversations the caller is a member of.
	_, raw := f.invoke(t, "Message/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"text": "report"},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	got := map[string]bool{}
	for _, id := range resp.IDs {
		got[id] = true
	}
	wantIDs := []string{mPlanned["id"].(string), mDM["id"].(string)}
	for _, w := range wantIDs {
		if !got[w] {
			t.Errorf("missing expected hit %s in %+v", w, resp.IDs)
		}
	}
	// "lunch" should not be in the result set.
	if got[mLunch["id"].(string)] {
		t.Errorf("non-matching message %s leaked into hits: %+v", mLunch["id"], resp.IDs)
	}
}

func TestMessage_Query_FTS_NonMemberCannotHit(t *testing.T) {
	f := setupFTSFixture(t)
	ctx := context.Background()

	// bob and carol create a private Space (alice is NOT a member)
	// and post a message containing the search term.
	cid, err := f.srv.Store.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "private",
		CreatedByPrincipalID: f.otherPID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := f.srv.Store.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid,
		PrincipalID:    f.otherPID,
		Role:           store.ChatRoleOwner,
	}); err != nil {
		t.Fatalf("InsertChatMembership bob: %v", err)
	}
	if _, err := f.srv.Store.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid,
		PrincipalID:    f.thirdPID,
		Role:           store.ChatRoleMember,
	}); err != nil {
		t.Fatalf("InsertChatMembership carol: %v", err)
	}
	if _, err := f.srv.Store.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &f.otherPID,
		BodyText:          "secret password reset codes",
		BodyFormat:        store.ChatBodyFormatText,
	}); err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}

	f.flushFTS(t)

	// alice (the authenticated principal) searches for "secret".
	_, raw := f.invoke(t, "Message/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"text": "secret"},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.IDs) != 0 {
		t.Fatalf("non-member leaked search hits: %+v", resp.IDs)
	}
}

// TestMessage_Query_FTS_RelevanceSort_DefaultsToScore checks that when
// filter.text is supplied and no explicit sort comparator is provided,
// the handler returns hits in FTS-relevance descending order rather
// than chronological.
func TestMessage_Query_FTS_RelevanceSort_DefaultsToScore(t *testing.T) {
	f := setupFTSFixture(t)
	cid, _ := createSpace(t, f.fixture, "relevance")

	// First post (older) carries one matching token.
	first := sendMessage(t, f.fixture, cid, "report later")
	// Second post (newer) carries the token twice — Bleve's BM25
	// scores it higher.
	second := sendMessage(t, f.fixture, cid, "report report report report report report")

	f.flushFTS(t)

	_, raw := f.invoke(t, "Message/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"text": "report"},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.IDs) < 2 {
		t.Fatalf("expected ≥2 hits, got %+v", resp.IDs)
	}
	// Higher-score message must lead.
	if resp.IDs[0] != second["id"].(string) {
		t.Fatalf("relevance ordering wrong: ids=%+v want lead %s", resp.IDs, second["id"])
	}
	if resp.IDs[1] != first["id"].(string) {
		t.Fatalf("relevance ordering wrong: ids=%+v want second %s", resp.IDs, first["id"])
	}
}

// TestMessage_Query_NoText_KeepsChronologicalPath confirms the
// non-text path still works under the FTS-wired handler — empty
// filter.text must NOT route through SearchChatMessages, so the
// fallback comparator (createdAt asc) wins.
func TestMessage_Query_NoText_KeepsChronologicalPath(t *testing.T) {
	f := setupFTSFixture(t)
	cid, _ := createSpace(t, f.fixture, "team")
	first := sendMessage(t, f.fixture, cid, "alpha")
	// Force a clock advance so createdAt diverges between sends.
	f.clk.Advance(time.Minute)
	second := sendMessage(t, f.fixture, cid, "beta")

	_, raw := f.invoke(t, "Message/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"conversationId": cid},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.IDs) < 2 {
		t.Fatalf("expected ≥2 hits, got %+v", resp.IDs)
	}
	if resp.IDs[0] != first["id"].(string) || resp.IDs[1] != second["id"].(string) {
		t.Errorf("chronological order wrong: %+v want [%s, %s]", resp.IDs, first["id"], second["id"])
	}
}
