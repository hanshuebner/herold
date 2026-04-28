package spam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/llmtest"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
)

// fakeInvoker is a test PluginInvoker: the caller registers scripted
// handlers per plugin name + method.
type fakeInvoker struct {
	mu     sync.Mutex
	routes map[string]func(ctx context.Context, params any) (json.RawMessage, error)
}

func newFakeInvoker() *fakeInvoker {
	return &fakeInvoker{routes: map[string]func(ctx context.Context, params any) (json.RawMessage, error){}}
}

func (f *fakeInvoker) handle(plugin, method string, fn func(ctx context.Context, params any) (json.RawMessage, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[plugin+"|"+method] = fn
}

func (f *fakeInvoker) Call(ctx context.Context, plugin, method string, params, result any) error {
	f.mu.Lock()
	fn, ok := f.routes[plugin+"|"+method]
	f.mu.Unlock()
	if !ok {
		return errors.New("fakeInvoker: no route")
	}
	raw, err := fn(ctx, params)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(raw, result)
}

func buildMessage(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	msg, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.ParseOptions{StrictBoundary: false})
	if err != nil {
		t.Fatalf("mailparse: %v", err)
	}
	return msg
}

const canonMsg = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Subject: Promo\r\n" +
	"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Buy cheap widgets at https://widgets.example.com\r\n"

// newAuth builds a mailauth.AuthResults literal with the supplied
// per-method verdicts and DMARC header-from. Tests use this in place
// of the old stubAuth reader now that spam + sieve consume
// *mailauth.AuthResults directly.
func newAuth(spf, dkim, dmarc, arc mailauth.AuthStatus, domain string) *mailauth.AuthResults {
	return &mailauth.AuthResults{
		SPF:   mailauth.SPFResult{Status: spf},
		DKIM:  []mailauth.DKIMResult{{Status: dkim}},
		DMARC: mailauth.DMARCResult{Status: dmarc, HeaderFrom: domain},
		ARC:   mailauth.ARCResult{Status: arc},
	}
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestClassify_ReturnsSpamVerdict(t *testing.T) {
	invoker := newFakeInvoker()
	invoker.handle("my-spam", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.92,"reason":"promo link"}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	msg := buildMessage(t, canonMsg)
	r, err := c.Classify(context.Background(), msg, newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthNone, "example.com"), "my-spam")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if r.Verdict != Spam || r.Score < 0.9 {
		t.Fatalf("expected spam/high score, got %+v", r)
	}
}

func TestClassify_TimeoutReturnsUnclassified(t *testing.T) {
	invoker := newFakeInvoker()
	invoker.handle("slow", ClassifyMethod, func(ctx context.Context, _ any) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r, err := c.Classify(ctx, buildMessage(t, canonMsg), nil, "slow")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if r.Verdict != Unclassified {
		t.Fatalf("verdict must be Unclassified on timeout; got %v", r.Verdict)
	}
}

func TestClassify_PluginErrorReturnsUnclassified(t *testing.T) {
	invoker := newFakeInvoker()
	invoker.handle("broken", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return nil, errors.New("plugin crashed")
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "broken")
	if err == nil {
		t.Fatal("expected error")
	}
	if r.Verdict != Unclassified {
		t.Fatalf("verdict: %v", r.Verdict)
	}
}

func TestClassify_PluginNotRegistered(t *testing.T) {
	invoker := newFakeInvoker()
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if r.Verdict != Unclassified {
		t.Fatalf("verdict: %v", r.Verdict)
	}
}

func TestClassify_UnparseableVerdict(t *testing.T) {
	invoker := newFakeInvoker()
	invoker.handle("odd", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"maybe","confidence":0.5}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "odd")
	if err == nil {
		t.Fatal("expected error")
	}
	if r.Verdict != Unclassified {
		t.Fatalf("verdict: %v", r.Verdict)
	}
}

func TestBuildRequest_Snapshot(t *testing.T) {
	msg := buildMessage(t, canonMsg)
	req := BuildRequest(msg, newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthFail, mailauth.AuthNone, "example.com"))
	if len(req.From) != 1 || !strings.Contains(req.From[0], "alice@example.com") {
		t.Fatalf("from: %+v", req.From)
	}
	if req.Subject != "Promo" {
		t.Fatalf("subject: %q", req.Subject)
	}
	if !req.SPFPass || !req.DKIMPass || req.DMARCPass {
		t.Fatalf("auth flags: spf=%v dkim=%v dmarc=%v", req.SPFPass, req.DKIMPass, req.DMARCPass)
	}
	if !strings.Contains(req.BodyExcerpt, "widgets.example.com") {
		t.Fatalf("excerpt missing URL: %q", req.BodyExcerpt)
	}
	if req.FromDomain != "example.com" {
		t.Fatalf("from-domain: %q", req.FromDomain)
	}
	raw, err := req.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	// Deterministic subfields — fixed seed equivalent.
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["subject"] != "Promo" {
		t.Fatalf("snapshot subject mismatch: %+v", got)
	}
}

func TestBuildRequest_HTMLStripped(t *testing.T) {
	const html = "From: a@b\r\nSubject: h\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><body>hello <a href=\"https://example.com\">link</a></body></html>"
	req := BuildRequest(buildMessage(t, html), nil)
	if strings.Contains(req.BodyExcerpt, "<html>") || strings.Contains(req.BodyExcerpt, "<a ") {
		t.Fatalf("HTML not stripped: %q", req.BodyExcerpt)
	}
	if !strings.Contains(req.BodyExcerpt, "link") || !strings.Contains(req.BodyExcerpt, "hello") {
		t.Fatalf("text content missing: %q", req.BodyExcerpt)
	}
}

func TestClassify_AppliesDefaultTimeout(t *testing.T) {
	// Use a FakeClock-driven timeout by passing a ctx with Done wired
	// through a short deadline; this exercises the deadline() branch
	// that supplies a default timeout.
	invoker := newFakeInvoker()
	var sawDeadline bool
	invoker.handle("p", ClassifyMethod, func(ctx context.Context, _ any) (json.RawMessage, error) {
		_, sawDeadline = ctx.Deadline()
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewReal()).WithTimeout(10 * time.Millisecond)
	_, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "p")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !sawDeadline {
		t.Fatalf("Classify did not apply a default deadline")
	}
}

// TestClassify_WithLLMReplayer exercises Classify end-to-end through
// the llmtest.Replayer. Skipped until Wave 3.16 records the fixture
// baseline via scripts/llm-capture.sh.
//
// The skip message is a contract: reviewer checks that every skipped
// test in this package has this exact reason prefix.
func TestClassify_WithLLMReplayer(t *testing.T) {
	t.Skip("LLM fixtures not yet captured — run scripts/llm-capture.sh; see Wave 3.16")

	replayer := llmtest.LoadReplayer(t, llmtest.KindSpamClassify)
	c := New(replayer, silentLogger(), clock.NewFake(time.Now()))
	msg := buildMessage(t, canonMsg)
	r, err := c.Classify(context.Background(), msg,
		newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthNone, "example.com"),
		"herold-spam-llm")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if r.Verdict == Unclassified {
		t.Fatalf("Replayer-backed Classify returned Unclassified; check fixture at internal/llmtest/fixtures/spam-classify/")
	}
}

// TestClassify_ReplayerMissingFixtureError verifies that Replayer
// surfaces ErrFixtureMissing (not a silent pass) when no matching
// fixture is recorded. This is the prompt-hash invalidation path
// described in REQ-FILT-302.
func TestClassify_ReplayerMissingFixtureError(t *testing.T) {
	// Empty replayer — no fixtures loaded.
	replayer := llmtest.NewReplayer(llmtest.KindSpamClassify, nil)
	c := New(replayer, silentLogger(), clock.NewFake(time.Now()))
	msg := buildMessage(t, canonMsg)
	_, err := c.Classify(context.Background(), msg, nil, "herold-spam-llm")
	if err == nil {
		t.Fatal("expected ErrFixtureMissing, got nil")
	}
	if !errors.Is(err, llmtest.ErrFixtureMissing) {
		t.Fatalf("expected ErrFixtureMissing, got: %v", err)
	}
}
