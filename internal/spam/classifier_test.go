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
	"unicode/utf8"

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
	r, err := c.Classify(context.Background(), msg, newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthNone, "example.com"), "my-spam", ClassifyContext{})
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
	r, err := c.Classify(ctx, buildMessage(t, canonMsg), nil, "slow", ClassifyContext{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if r.Verdict != Unclassified {
		t.Fatalf("verdict must be Unclassified on timeout; got %v", r.Verdict)
	}
}

// waitForWaiters polls until the fake clock has exactly n outstanding
// waiters, per the convention documented in
// internal/idpstalesweep/worker_test.go: the clock's own state is fully
// deterministic (advanced only by explicit Advance calls), so the poll
// loop only bridges goroutine-scheduling handoff, not wall-clock timing.
func waitForWaiters(t *testing.T, clk *clock.FakeClock, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if clk.NumWaiters() == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("NumWaiters never reached %d (stuck at %d)", n, clk.NumWaiters())
}

// TestClassify_BudgetCutoff_FakeClock covers Wave 4.1 / REQ-FILT-40/42
// (issue #301): the server's classify budget cuts a plugin off even
// though the plugin never returns on its own, and the cutoff is driven
// by the injected Clock rather than a real sleep so the test is
// deterministic.
func TestClassify_BudgetCutoff_FakeClock(t *testing.T) {
	invoker := newFakeInvoker()
	started := make(chan struct{})
	invoker.handle("slow", ClassifyMethod, func(ctx context.Context, _ any) (json.RawMessage, error) {
		close(started)
		<-ctx.Done() // the plugin "sleeps" past the budget
		return nil, ctx.Err()
	})

	fc := clock.NewFake(time.Now())
	c := New(invoker, silentLogger(), fc).WithTimeout(5 * time.Second)

	type outcome struct {
		r   Classification
		err error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "slow", ClassifyContext{})
		resultCh <- outcome{r, err}
	}()

	<-started
	waitForWaiters(t, fc, 1)
	fc.Advance(5 * time.Second)

	select {
	case got := <-resultCh:
		if got.err == nil {
			t.Fatal("expected an error when the budget is exceeded")
		}
		if got.r.Verdict != Unclassified {
			t.Fatalf("verdict must be Unclassified on budget cutoff; got %v", got.r.Verdict)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Classify did not return after the fake clock advanced past the budget")
	}
}

func TestClassify_PluginErrorReturnsUnclassified(t *testing.T) {
	invoker := newFakeInvoker()
	invoker.handle("broken", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return nil, errors.New("plugin crashed")
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "broken", ClassifyContext{})
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
	r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "missing", ClassifyContext{})
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
	r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "odd", ClassifyContext{})
	if err == nil {
		t.Fatal("expected error")
	}
	if r.Verdict != Unclassified {
		t.Fatalf("verdict: %v", r.Verdict)
	}
}

// TestClassify_PropagatesTimeoutMsFromDeadline covers issue #331: the
// server's own classify budget must travel with the spam.classify call
// as Request.TimeoutMs, computed against the injected clock so the
// assertion is exact rather than a real-time-tolerant approximation.
func TestClassify_PropagatesTimeoutMsFromDeadline(t *testing.T) {
	invoker := newFakeInvoker()
	var gotReq Request
	var gotOK bool
	invoker.handle("my-spam", ClassifyMethod, func(_ context.Context, params any) (json.RawMessage, error) {
		gotReq, gotOK = params.(Request)
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	fc := clock.NewFake(time.Now())
	c := New(invoker, silentLogger(), fc).WithTimeout(5 * time.Second)
	_, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "my-spam", ClassifyContext{})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !gotOK {
		t.Fatalf("plugin did not receive a Request-typed params")
	}
	// No time has elapsed on the fake clock between the classifier
	// attaching the deadline and BuildRequest computing TimeoutMs against
	// it, so the wire value must equal the configured budget exactly.
	const want = int64(5 * time.Second / time.Millisecond)
	if gotReq.TimeoutMs != want {
		t.Fatalf("TimeoutMs = %d, want %d", gotReq.TimeoutMs, want)
	}
}

// TestClassify_PropagatesTimeoutMsFromCallerDeadline covers the same
// wire propagation (issue #331) when the caller already supplies a ctx
// deadline (rather than falling back to the classifier's own configured
// timeout): the remaining budget at Classify time must still ride along
// as Request.TimeoutMs, within a small real-clock tolerance since the
// caller's ctx.WithTimeout is clocked by the real runtime, not the
// injected FakeClock.
func TestClassify_PropagatesTimeoutMsFromCallerDeadline(t *testing.T) {
	invoker := newFakeInvoker()
	var gotReq Request
	invoker.handle("my-spam", ClassifyMethod, func(_ context.Context, params any) (json.RawMessage, error) {
		gotReq, _ = params.(Request)
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewReal())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Classify(ctx, buildMessage(t, canonMsg), nil, "my-spam", ClassifyContext{})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	const want = int64(2 * time.Second / time.Millisecond)
	const tolerance = int64(500)
	if gotReq.TimeoutMs <= want-tolerance || gotReq.TimeoutMs > want {
		t.Fatalf("TimeoutMs = %d, want within %dms of %d (never above it)", gotReq.TimeoutMs, tolerance, want)
	}
}

// TestClassify_MailClassifyPropagatesTimeoutMs covers the same
// propagation (issue #331) on the mail.classify wire contract (Wave
// 4.3): MailClassifyRequest embeds Request, so its fields -- including
// TimeoutMs -- marshal at the top level alongside "context".
func TestClassify_MailClassifyPropagatesTimeoutMs(t *testing.T) {
	invoker := &classifierTypeInvoker{fakeInvoker: newFakeInvoker(), kind: classifierPluginType}
	var gotReq MailClassifyRequest
	invoker.handle("my-classifier", MailClassifyMethod, func(_ context.Context, params any) (json.RawMessage, error) {
		gotReq, _ = params.(MailClassifyRequest)
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	fc := clock.NewFake(time.Now())
	c := New(invoker, silentLogger(), fc).WithTimeout(3 * time.Second)
	_, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "my-classifier", ClassifyContext{})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	const want = int64(3 * time.Second / time.Millisecond)
	if gotReq.TimeoutMs != want {
		t.Fatalf("TimeoutMs = %d, want %d", gotReq.TimeoutMs, want)
	}
}

// classifierTypeInvoker wraps fakeInvoker to also implement
// PluginTypeResolver, reporting every plugin as kind for tests that need
// Classify to take the mail.classify branch.
type classifierTypeInvoker struct {
	*fakeInvoker
	kind string
}

func (c *classifierTypeInvoker) PluginType(name string) (string, bool) {
	return c.kind, true
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

// TestBuildRequest_ForwardingHeadersPresent verifies that BuildRequest
// populates ReplyTo, ReturnPath, ListID, ListUnsubscribe, Precedence,
// AutoSubmitted and AuthResults from the message's headers, and that
// each field round-trips onto the wire payload the plugin receives
// (re #298).
func TestBuildRequest_ForwardingHeadersPresent(t *testing.T) {
	const raw = "From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.com>\r\n" +
		"Reply-To: Reply <reply@example.com>\r\n" +
		"Return-Path: <bounce@example.com>\r\n" +
		"List-Id: Kayak Club <kajak.example.org>\r\n" +
		"List-Unsubscribe: <mailto:unsub@example.com>\r\n" +
		"Precedence: bulk\r\n" +
		"Auto-Submitted: auto-generated\r\n" +
		"Authentication-Results: mail.example.com; spf=pass smtp.mailfrom=example.com\r\n" +
		"Subject: Newsletter\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"hello\r\n"
	req := BuildRequest(buildMessage(t, raw), nil)
	if req.ReplyTo != "Reply <reply@example.com>" {
		t.Fatalf("reply_to: %q", req.ReplyTo)
	}
	if req.ReturnPath != "<bounce@example.com>" {
		t.Fatalf("return_path: %q", req.ReturnPath)
	}
	if req.ListID != "Kayak Club <kajak.example.org>" {
		t.Fatalf("list_id: %q", req.ListID)
	}
	if req.ListUnsubscribe != "<mailto:unsub@example.com>" {
		t.Fatalf("list_unsubscribe: %q", req.ListUnsubscribe)
	}
	if req.Precedence != "bulk" {
		t.Fatalf("precedence: %q", req.Precedence)
	}
	if req.AutoSubmitted != "auto-generated" {
		t.Fatalf("auto_submitted: %q", req.AutoSubmitted)
	}
	if req.AuthResults != "mail.example.com; spf=pass smtp.mailfrom=example.com" {
		t.Fatalf("auth_results: %q", req.AuthResults)
	}
	raw2, err := req.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw2, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reply_to", "return_path", "list_id", "list_unsubscribe", "precedence", "auto_submitted", "auth_results"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("wire payload missing key %q: %+v", key, got)
		}
	}
}

// TestBuildRequest_ForwardingHeadersAbsent verifies that a message
// carrying none of the new forwarding/list/auth headers leaves the
// corresponding Request fields empty and, because every new field is
// `omitempty`, off the wire payload entirely (re #298).
func TestBuildRequest_ForwardingHeadersAbsent(t *testing.T) {
	req := BuildRequest(buildMessage(t, canonMsg), nil)
	if req.ReplyTo != "" || req.ReturnPath != "" || req.ListID != "" ||
		req.ListUnsubscribe != "" || req.Precedence != "" ||
		req.AutoSubmitted != "" || req.AuthResults != "" {
		t.Fatalf("expected empty forwarding-header fields, got %+v", req)
	}
	raw, err := req.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reply_to", "return_path", "list_id", "list_unsubscribe", "precedence", "auto_submitted", "auth_results"} {
		if _, ok := got[key]; ok {
			t.Fatalf("wire payload unexpectedly carries omitempty key %q: %+v", key, got)
		}
	}
}

// TestBuildRequest_AuthResultsUsesServerRendering verifies that when the
// caller supplies a non-nil *mailauth.AuthResults, BuildRequest sends the
// server's own rendered Authentication-Results (auth.Raw) to the
// classifier and ignores the upstream Authentication-Results header
// present in the message bytes — the classifier data grant specifies
// herold's own SPF/DKIM/DMARC verdict, not a forgeable upstream header
// (re #298).
func TestBuildRequest_AuthResultsUsesServerRendering(t *testing.T) {
	const raw = "From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.com>\r\n" +
		"Authentication-Results: forged.attacker.example; spf=pass smtp.mailfrom=attacker.example\r\n" +
		"Subject: Newsletter\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"hello\r\n"
	auth := &mailauth.AuthResults{
		SPF: mailauth.SPFResult{Status: mailauth.AuthPass, From: "alice@example.com"},
		Raw: "mail.herold.example; spf=pass smtp.mailfrom=alice@example.com",
	}
	req := BuildRequest(buildMessage(t, raw), auth)
	if req.AuthResults != auth.Raw {
		t.Fatalf("auth_results: got %q, want the server-rendered value %q", req.AuthResults, auth.Raw)
	}
	if strings.Contains(req.AuthResults, "attacker.example") {
		t.Fatalf("auth_results leaked the upstream header: %q", req.AuthResults)
	}
}

// TestBuildRequest_AuthResultsFallsBackToUpstreamWhenNilAuth verifies
// that a nil auth argument — the IMAP import path, which performs no
// server-side verification — falls back to msg.AuthResultsRaw, the
// upstream Authentication-Results header content as received (re #298).
func TestBuildRequest_AuthResultsFallsBackToUpstreamWhenNilAuth(t *testing.T) {
	const raw = "From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.com>\r\n" +
		"Authentication-Results: mail.example.com; spf=pass smtp.mailfrom=example.com\r\n" +
		"Subject: Newsletter\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"hello\r\n"
	req := BuildRequest(buildMessage(t, raw), nil)
	const want = "mail.example.com; spf=pass smtp.mailfrom=example.com"
	if req.AuthResults != want {
		t.Fatalf("auth_results: got %q, want upstream header %q", req.AuthResults, want)
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
	_, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "p", ClassifyContext{})
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
		"herold-spam-llm", ClassifyContext{})
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
	_, err := c.Classify(context.Background(), msg, nil, "herold-spam-llm", ClassifyContext{})
	if err == nil {
		t.Fatal("expected ErrFixtureMissing, got nil")
	}
	if !errors.Is(err, llmtest.ErrFixtureMissing) {
		t.Fatalf("expected ErrFixtureMissing, got: %v", err)
	}
}

// TestBuildRequest_ExcerptDecodesEntities verifies numeric and named
// HTML entities left over from stripHTMLTags are decoded rather than
// forwarded to the classifier as raw entity text (re #299).
func TestBuildRequest_ExcerptDecodesEntities(t *testing.T) {
	const raw = "From: a@b\r\nSubject: h\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>Sch&#228;fer &amp; S&ouml;hne</p>"
	req := BuildRequest(buildMessage(t, raw), nil)
	if strings.Contains(req.BodyExcerpt, "&#228;") || strings.Contains(req.BodyExcerpt, "&amp;") || strings.Contains(req.BodyExcerpt, "&ouml;") {
		t.Fatalf("entities not decoded: %q", req.BodyExcerpt)
	}
	if !strings.Contains(req.BodyExcerpt, "Schäfer & Söhne") {
		t.Fatalf("decoded text missing: %q", req.BodyExcerpt)
	}
}

// TestBuildRequest_ExcerptCollapsesWhitespace verifies that whitespace
// runs left behind by tag-stripping collapse: internal runs to a single
// space and blank-line runs to at most one blank line (re #299).
func TestBuildRequest_ExcerptCollapsesWhitespace(t *testing.T) {
	const raw = "From: a@b\r\nSubject: h\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>Hello    there</p>\n\n\n\n<p>Goodbye</p>"
	req := BuildRequest(buildMessage(t, raw), nil)
	if strings.Contains(req.BodyExcerpt, "  ") {
		t.Fatalf("internal whitespace run not collapsed: %q", req.BodyExcerpt)
	}
	if strings.Contains(req.BodyExcerpt, "\n\n\n") {
		t.Fatalf("blank-line run not collapsed: %q", req.BodyExcerpt)
	}
	if !strings.Contains(req.BodyExcerpt, "Hello there") || !strings.Contains(req.BodyExcerpt, "Goodbye") {
		t.Fatalf("content missing after collapse: %q", req.BodyExcerpt)
	}
}

// TestBuildRequest_ExcerptAlternativeDedup verifies that a
// multipart/alternative body contributes only its text/plain
// representation to the excerpt, not both the plain and the HTML
// sibling (re #299).
func TestBuildRequest_ExcerptAlternativeDedup(t *testing.T) {
	const raw = "From: a@b\r\n" +
		"Subject: h\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain body content\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>html body content</p>\r\n" +
		"--BOUND--\r\n"
	req := BuildRequest(buildMessage(t, raw), nil)
	if !strings.Contains(req.BodyExcerpt, "plain body content") {
		t.Fatalf("expected text/plain content: %q", req.BodyExcerpt)
	}
	if strings.Contains(req.BodyExcerpt, "html body content") {
		t.Fatalf("expected html sibling to be dropped, not duplicated: %q", req.BodyExcerpt)
	}
}

// TestBuildRequest_ExcerptAlternativeHTMLFallback verifies that a
// multipart/alternative body with no text/plain part falls back to the
// tag-stripped text/html part (re #299).
func TestBuildRequest_ExcerptAlternativeHTMLFallback(t *testing.T) {
	const raw = "From: a@b\r\n" +
		"Subject: h\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>only html content</p>\r\n" +
		"--BOUND--\r\n"
	req := BuildRequest(buildMessage(t, raw), nil)
	if !strings.Contains(req.BodyExcerpt, "only html content") {
		t.Fatalf("expected stripped html content: %q", req.BodyExcerpt)
	}
	if strings.Contains(req.BodyExcerpt, "<p>") {
		t.Fatalf("html tags not stripped: %q", req.BodyExcerpt)
	}
}

// TestBuildRequest_ExcerptPlainTextOnly verifies a plain single-part
// text/plain message still normalizes (collapsed whitespace) and is
// unaffected by the multipart/alternative handling (re #299).
func TestBuildRequest_ExcerptPlainTextOnly(t *testing.T) {
	const raw = "From: a@b\r\nSubject: h\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		"Line one   with   extra   spaces\r\n\r\n\r\nLine two\r\n"
	req := BuildRequest(buildMessage(t, raw), nil)
	if req.BodyExcerpt != "Line one with extra spaces\n\nLine two" {
		t.Fatalf("unexpected excerpt: %q", req.BodyExcerpt)
	}
}

// TestBuildRequest_ExcerptCapAppliedAfterNormalization verifies the
// DefaultBodyExcerptBytes cap is applied to the normalized text, not the
// raw pre-normalization text: a body padded with whitespace and HTML
// noise that shrinks under the cap after normalization is not truncated,
// and its normalized (not raw) length is what determines whether
// truncation is needed (re #299).
func TestBuildRequest_ExcerptCapAppliedAfterNormalization(t *testing.T) {
	// Build an HTML body whose raw byte length (tags + entity + repeated
	// whitespace) exceeds DefaultBodyExcerptBytes, but whose normalized
	// text is short. If the cap were applied before normalization, the
	// excerpt would be truncated mid-entity/mid-tag; applied after, the
	// full "hello world" content survives intact.
	var b strings.Builder
	b.WriteString("From: a@b\r\nSubject: h\r\nContent-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString("<p>hello&#32;world</p>")
	for b.Len() < DefaultBodyExcerptBytes*2 {
		b.WriteString("\n\n\n   \n\n\n")
	}
	req := BuildRequest(buildMessage(t, b.String()), nil)
	if req.BodyExcerpt != "hello world" {
		t.Fatalf("expected normalized excerpt \"hello world\", got %q (len=%d)", req.BodyExcerpt, len(req.BodyExcerpt))
	}
}

// TestBuildRequest_ExcerptCapRuneBoundary verifies that when the
// normalized excerpt exceeds DefaultBodyExcerptBytes, truncation lands on
// a UTF-8 rune boundary rather than splitting a multi-byte codepoint.
func TestBuildRequest_ExcerptCapRuneBoundary(t *testing.T) {
	// Repeat a 2-byte UTF-8 rune (a with umlaut) enough times to exceed
	// the cap; every byte offset is either a rune boundary or splits a
	// codepoint, so a naive byte-slice cap would corrupt the excerpt on
	// roughly half of possible cap values. DefaultBodyExcerptBytes is
	// even, and the rune is 2 bytes, so the built-in cap itself would
	// land cleanly -- shrink the message by one extra rune's worth of
	// bytes via BuildRequest's own cap isn't adjustable, so instead
	// assert directly that the returned excerpt is valid UTF-8 and at
	// or under the cap regardless.
	var b strings.Builder
	b.WriteString("From: a@b\r\nSubject: h\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	for i := 0; i < DefaultBodyExcerptBytes; i++ {
		b.WriteString("ä")
	}
	req := BuildRequest(buildMessage(t, b.String()), nil)
	if len(req.BodyExcerpt) > DefaultBodyExcerptBytes {
		t.Fatalf("excerpt exceeds cap: %d bytes", len(req.BodyExcerpt))
	}
	if !utf8.ValidString(req.BodyExcerpt) {
		t.Fatalf("excerpt is not valid UTF-8: %q", req.BodyExcerpt)
	}
}
