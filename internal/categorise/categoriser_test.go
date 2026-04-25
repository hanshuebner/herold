package categorise_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fakeServer is the test double for the OpenAI-compatible endpoint.
// Each test installs a handler that produces the desired response;
// the server's URL is fed into Options.DefaultEndpoint.
type fakeServer struct {
	srv      *httptest.Server
	requests atomic.Int64
	last     atomic.Pointer[http.Request]
	lastBody atomic.Pointer[[]byte]
}

func newFakeServer(t *testing.T, h http.HandlerFunc) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		fs.last.Store(r)
		bodyCopy := append([]byte(nil), body...)
		fs.lastBody.Store(&bodyCopy)
		// Restore body for the handler.
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		h(w, r)
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

// chatJSON returns a marshalled OpenAI chat-completions response with
// the given assistant content.
func chatJSON(content string) string {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// makeStoreAndPrincipal returns a fakestore + a principal id for a
// freshly inserted "alice@example.test" principal.
func makeStoreAndPrincipal(t *testing.T) (store.Store, store.PrincipalID) {
	t.Helper()
	st, err := fakestore.New(fakestore.Options{
		Clock: clock.NewFake(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test",
		PasswordHash:   "hash",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return st, p.ID
}

// parsedMessage returns a small parsed RFC 5322 message for use in
// the categoriser pipeline.
func parsedMessage(t *testing.T) mailparse.Message {
	t.Helper()
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: hello\r\nList-ID: <weekly.example>\r\n\r\nGood morning, team.\r\n"
	msg, err := mailparse.Parse(strings.NewReader(body), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return msg
}

func TestCategorise_HappyPath_ReturnsCategory(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatJSON(`{"category":"promotions"}`))
	})
	st, pid := makeStoreAndPrincipal(t)
	c := categorise.New(categorise.Options{
		Store:           st,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:           clock.NewFake(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)),
		DefaultEndpoint: fs.srv.URL,
		DefaultModel:    "test-model",
	})
	cat, err := c.Categorise(context.Background(), pid, parsedMessage(t), nil, spam.Ham)
	if err != nil {
		t.Fatalf("Categorise: %v", err)
	}
	if cat != "promotions" {
		t.Fatalf("category = %q, want %q", cat, "promotions")
	}
	if fs.requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", fs.requests.Load())
	}
}

func TestCategorise_NoneIsEmpty(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatJSON(`{"category":"none"}`))
	})
	st, pid := makeStoreAndPrincipal(t)
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	cat, err := c.Categorise(context.Background(), pid, parsedMessage(t), nil, spam.Ham)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cat != "" {
		t.Fatalf("category = %q, want empty", cat)
	}
}

func TestCategorise_UnknownNameIsEmpty(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatJSON(`{"category":"made-up-bucket"}`))
	})
	st, pid := makeStoreAndPrincipal(t)
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	cat, _ := c.Categorise(context.Background(), pid, parsedMessage(t), nil, spam.Ham)
	if cat != "" {
		t.Fatalf("category = %q, want empty (unknown -> empty + warn)", cat)
	}
}

func TestCategorise_HTTP500IsEmpty(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	st, pid := makeStoreAndPrincipal(t)
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	cat, err := c.Categorise(context.Background(), pid, parsedMessage(t), nil, spam.Ham)
	if err != nil {
		t.Fatalf("err = %v, want nil (failures NEVER block delivery)", err)
	}
	if cat != "" {
		t.Fatalf("category = %q, want empty on 500", cat)
	}
}

func TestCategorise_CtxDeadlineShorterThanTimeout(t *testing.T) {
	// Server hangs forever; the parent ctx deadline must win quickly.
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	st, pid := makeStoreAndPrincipal(t)
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		// Configured timeout is generous; the parent ctx's 50ms wins.
		DefaultTimeout: 10 * time.Second,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	cat, err := c.Categorise(ctx, pid, parsedMessage(t), nil, spam.Ham)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cat != "" {
		t.Fatalf("cat = %q, want empty on deadline", cat)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("call took %v; ctx deadline (50ms) did not win", elapsed)
	}
}

func TestCategorise_PerAccountPromptOverride_RewritesPromptInRequest(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatJSON(`{"category":"primary"}`))
	})
	st, pid := makeStoreAndPrincipal(t)
	// Override the prompt for this principal. The request body must
	// contain the override text in the system message.
	const customPrompt = "BANANA-PROMPT-7341 you are a custom classifier"
	cfg, err := st.Meta().GetCategorisationConfig(context.Background(), pid)
	if err != nil {
		t.Fatalf("GetCategorisationConfig: %v", err)
	}
	cfg.Prompt = customPrompt
	if err := st.Meta().UpdateCategorisationConfig(context.Background(), cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig: %v", err)
	}
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := c.Categorise(context.Background(), pid, parsedMessage(t), nil, spam.Ham); err != nil {
		t.Fatalf("Categorise: %v", err)
	}
	bodyPtr := fs.lastBody.Load()
	if bodyPtr == nil {
		t.Fatalf("no request body captured")
	}
	if !strings.Contains(string(*bodyPtr), "BANANA-PROMPT-7341") {
		t.Fatalf("request body does not carry override prompt: %s", string(*bodyPtr))
	}
}

func TestCategorise_DisabledShortCircuits(t *testing.T) {
	calls := atomic.Int64{}
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, chatJSON(`{"category":"primary"}`))
	})
	st, pid := makeStoreAndPrincipal(t)
	cfg, _ := st.Meta().GetCategorisationConfig(context.Background(), pid)
	cfg.Enabled = false
	if err := st.Meta().UpdateCategorisationConfig(context.Background(), cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig: %v", err)
	}
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	cat, err := c.Categorise(context.Background(), pid, parsedMessage(t), nil, spam.Ham)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cat != "" {
		t.Fatalf("cat = %q, want empty when disabled", cat)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected zero LLM calls when disabled, got %d", calls.Load())
	}
}

// TestCategorise_PromptCarriesCategoryDescriptions confirms the
// system prompt the server posts to the LLM lists the category set
// (so the model can pick from a known vocabulary).
func TestCategorise_PromptCarriesCategoryDescriptions(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatJSON(`{"category":"primary"}`))
	})
	st, pid := makeStoreAndPrincipal(t)
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := c.Categorise(context.Background(), pid, parsedMessage(t), nil, spam.Ham); err != nil {
		t.Fatalf("Categorise: %v", err)
	}
	bodyPtr := fs.lastBody.Load()
	if bodyPtr == nil {
		t.Fatalf("no body captured")
	}
	body := string(*bodyPtr)
	for _, name := range []string{"primary", "social", "promotions", "updates", "forums"} {
		if !strings.Contains(body, name) {
			t.Errorf("system prompt missing category %q", name)
		}
	}
}

func TestCategorise_RecategoriseRecent_ReplacesKeyword(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatJSON(`{"category":"updates"}`))
	})
	st, pid := makeStoreAndPrincipal(t)
	ctx := context.Background()
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: receipt\r\n\r\nThanks for your order.\r\n"
	ref, err := st.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = st.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    mb.ID,
		Size:         int64(len(body)),
		Blob:         ref,
		ReceivedAt:   time.Now(),
		InternalDate: time.Now(),
		Keywords:     []string{"$category-promotions"}, // the categoriser must clear this
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	c := categorise.New(categorise.Options{
		Store: st, Clock: clock.NewFake(time.Now()),
		DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	var seen [][2]int
	processed, err := c.RecategoriseRecent(ctx, pid, 100, func(done, total int) {
		seen = append(seen, [2]int{done, total})
	})
	if err != nil {
		t.Fatalf("RecategoriseRecent: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if len(seen) != 1 || seen[0][0] != 1 || seen[0][1] != 1 {
		t.Fatalf("progress = %v, want [[1 1]]", seen)
	}
	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	want := "$category-updates"
	have := categorise.CategoryFromKeywords(msgs[0].Keywords)
	if have != "updates" {
		t.Fatalf("keyword = %q (full=%v), want %q", have, msgs[0].Keywords, want)
	}
}

func TestCategorise_NilCategoriserIsSafe(t *testing.T) {
	var c *categorise.Categoriser // nil
	cat, err := c.Categorise(context.Background(), 1, mailparse.Message{}, nil, spam.Ham)
	if err != nil || cat != "" {
		t.Fatalf("nil Categoriser must be no-op; got cat=%q err=%v", cat, err)
	}
}

// TestCategoriseJobRegistry_GetPutEvict covers the in-process job
// registry the admin REST surface uses for poll responses.
func TestCategoriseJobRegistry_GetPutEvict(t *testing.T) {
	r := categorise.NewJobRegistry(time.Hour, 2)
	now := time.Now()
	r.Put(now, categorise.JobStatus{ID: "a", State: categorise.JobStateRunning, Done: 1, Total: 10})
	r.Put(now.Add(time.Second), categorise.JobStatus{ID: "b", State: categorise.JobStateRunning, Done: 0, Total: 5})
	r.Put(now.Add(2*time.Second), categorise.JobStatus{ID: "c", State: categorise.JobStateRunning, Done: 0, Total: 5})
	if _, ok := r.Get("a"); ok {
		t.Fatalf("expected oldest entry 'a' to be evicted past maxSize=2")
	}
	if _, ok := r.Get("b"); !ok {
		t.Fatalf("expected 'b' to remain")
	}
	if _, ok := r.Get("c"); !ok {
		t.Fatalf("expected 'c' to remain")
	}
}

// guard against an undeclared import warning in environments that
// reorder imports.
var _ = errors.New
