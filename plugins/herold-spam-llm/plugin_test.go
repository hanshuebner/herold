package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
)

// fakeLLM is a stand-in for an OpenAI-compatible endpoint. Tests set the
// response handler function to control what the model "returns"; the
// request path also records invocation count so tests can assert on
// retry-vs-no-retry behaviour.
type fakeLLM struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	calls   int64
	handler http.HandlerFunc
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.calls, 1)
		f.mu.Lock()
		h := f.handler
		f.mu.Unlock()
		if h == nil {
			http.Error(w, "no handler", http.StatusInternalServerError)
			return
		}
		h(w, r)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeLLM) setHandler(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = h
}

func (f *fakeLLM) endpoint() string { return f.server.URL + "/v1" }

// replyJSON writes a canned chat-completions response whose single choice
// carries the supplied assistant content verbatim.
func replyJSON(w http.ResponseWriter, assistant string) {
	body := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": assistant}},
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// buildPlugin compiles the plugin binary once per test. The binary lives
// in a per-test TempDir so parallel tests do not race on the same path.
func buildPlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herold-spam-llm")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "github.com/hanshuebner/herold/plugins/herold-spam-llm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// spawnedPlugin wires a running plugin binary to a supervisor-side Client.
// Callers drive it via Call; Close stops the read loop and waits for
// graceful exit.
type spawnedPlugin struct {
	t      *testing.T
	cmd    *exec.Cmd
	client *plug.Client
	done   chan error
}

func spawnPlugin(t *testing.T, bin string) *spawnedPlugin {
	t.Helper()
	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	// Drain stderr so the pipe buffer never fills and blocks the plugin.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	client := plug.NewClient(stdout, stdin, plug.ClientOptions{
		Name:          "herold-spam-llm",
		MaxConcurrent: 16,
	})
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()

	return &spawnedPlugin{t: t, cmd: cmd, client: client, done: done}
}

func (s *spawnedPlugin) close() {
	// Closing stdin signals EOF to the plugin; the plugin returns from
	// Run and exits. The Client's read loop then observes EOF and
	// returns nil.
	if p, ok := s.cmd.Stdin.(io.Closer); ok {
		_ = p.Close()
	}
	// Best-effort wait; do not fail the test if the plugin takes its
	// ShutdownGraceSec to exit.
	waited := make(chan error, 1)
	go func() { waited <- s.cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-waited
	}
}

func (s *spawnedPlugin) initialize(t *testing.T) {
	t.Helper()
	var res plug.InitializeResult
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.client.Call(ctx, plug.MethodInitialize, plug.InitializeParams{
		ServerVersion: "test",
		ABIVersion:    plug.ABIVersion,
	}, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if res.Manifest.Name != "herold-spam-llm" {
		t.Fatalf("manifest.Name = %q, want herold-spam-llm", res.Manifest.Name)
	}
	if res.Manifest.Type != plug.TypeSpam {
		t.Fatalf("manifest.Type = %q, want %q", res.Manifest.Type, plug.TypeSpam)
	}
	if res.Manifest.MaxConcurrentRequests != 16 {
		t.Fatalf("manifest.MaxConcurrentRequests = %d, want 16", res.Manifest.MaxConcurrentRequests)
	}
}

func (s *spawnedPlugin) configure(t *testing.T, opts map[string]any) error {
	t.Helper()
	var res plug.ConfigureResult
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.client.Call(ctx, plug.MethodConfigure, plug.ConfigureParams{Options: opts}, &res)
}

func (s *spawnedPlugin) classify(ctx context.Context, params map[string]any) (map[string]any, error) {
	var res map[string]any
	err := s.client.Call(ctx, "spam.classify", params, &res)
	return res, err
}

// canonicalPayload returns the flat JSON object internal/spam's
// BuildRequest produces on the wire. The plugin receives only the fields
// that happen to line up with sdk.SpamClassifyParams (body_excerpt);
// tests exercise that path.
func canonicalPayload(body string) map[string]any {
	return map[string]any{
		"from":          []string{"alice@example.com"},
		"to":            []string{"bob@example.com"},
		"subject":       "Hello",
		"dkim_pass":     true,
		"spf_pass":      true,
		"dmarc_pass":    true,
		"from_domain":   "example.com",
		"body_excerpt":  body,
		"received_date": "2026-04-24T00:00:00Z",
	}
}

func TestClassify_SpamVerdict(t *testing.T) {
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		replyJSON(w, `{"verdict":"spam","score":0.93,"reason":"urgency + mismatched sender"}`)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":       llm.endpoint(),
		"model":          "fake",
		"timeout_sec":    5,
		"spam_threshold": 0.5,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := p.classify(ctx, canonicalPayload("WIN A PRIZE"))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got, _ := res["verdict"].(string); got != "spam" {
		t.Fatalf("verdict = %q, want spam (full=%v)", got, res)
	}
	if got, _ := res["confidence"].(float64); got != 0.93 {
		t.Fatalf("confidence = %v, want 0.93", got)
	}
	if got, _ := res["reason"].(string); !strings.Contains(got, "urgency") {
		t.Fatalf("reason = %q, want to contain 'urgency'", got)
	}
}

func TestClassify_HamVerdictBelowThreshold(t *testing.T) {
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		replyJSON(w, `{"verdict":"spam","score":0.2,"reason":"weak signals"}`)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":       llm.endpoint(),
		"model":          "fake",
		"spam_threshold": 0.8,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := p.classify(ctx, canonicalPayload("hello"))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got, _ := res["verdict"].(string); got != "ham" {
		t.Fatalf("verdict = %q, want ham (score 0.2 < threshold 0.8)", got)
	}
}

// TestClassify_TolerantJSONExtraction pins that the plugin accepts model
// output that wraps the JSON object in prose. Small local models do this
// despite the "ONLY JSON" instruction in the system prompt.
func TestClassify_TolerantJSONExtraction(t *testing.T) {
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		replyJSON(w, "Sure! Here is the analysis:\n\n"+
			`{"verdict":"ham","score":0.1,"reason":"friendly email"}`+
			"\n\nHope that helps.")
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":       llm.endpoint(),
		"model":          "fake",
		"spam_threshold": 0.7,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := p.classify(ctx, canonicalPayload("hello"))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got, _ := res["verdict"].(string); got != "ham" {
		t.Fatalf("verdict = %q, want ham", got)
	}
}

func TestClassify_MalformedLLMOutput(t *testing.T) {
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		replyJSON(w, "not json at all")
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint": llm.endpoint(),
		"model":    "fake",
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.classify(ctx, canonicalPayload("x"))
	if err == nil {
		t.Fatalf("expected error on malformed LLM output")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *plug.Error, got %T: %v", err, err)
	}
}

func TestClassify_HTTP500(t *testing.T) {
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint": llm.endpoint(),
		"model":    "fake",
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.classify(ctx, canonicalPayload("x"))
	if err == nil {
		t.Fatalf("expected error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error %v does not mention status 500", err)
	}
	// The plugin must not retry: exactly one request hit the fake.
	if n := atomic.LoadInt64(&llm.calls); n != 1 {
		t.Fatalf("llm got %d calls, want 1 (no retry from plugin)", n)
	}
}

// TestClassify_ContextDeadlineWins asserts that a server-side ctx deadline
// shorter than the plugin's configured timeout is honored: the call
// returns well before timeout_sec elapses.
func TestClassify_ContextDeadlineWins(t *testing.T) {
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		// Stall until the client's ctx deadline elapses. The plugin
		// should abort and return an error.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			replyJSON(w, `{"verdict":"ham","score":0.0,"reason":"late"}`)
		}
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":    llm.endpoint(),
		"model":       "fake",
		"timeout_sec": 30,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// Supervisor-side deadline of 300 ms — must win over timeout_sec=30.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := p.classify(ctx, canonicalPayload("x"))
	elapsed := time.Since(started)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("call took %s, want <3s (ctx deadline ignored)", elapsed)
	}
}

func TestConfigure_UnknownOptionRejected(t *testing.T) {
	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	err := p.configure(t, map[string]any{
		"endpoint":       "http://localhost:11434/v1",
		"unknown_option": "x",
	})
	if err == nil {
		t.Fatalf("expected configure to fail on unknown option")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error %v does not mention 'unknown'", err)
	}
}

func TestConfigure_APIKeyEnvResolution(t *testing.T) {
	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	// api_key_env points at a var we know is unset — must fail.
	unsetVar := "HEROLD_SPAM_LLM_TEST_KEY_SHOULD_NOT_EXIST"
	_ = os.Unsetenv(unsetVar)
	err := p.configure(t, map[string]any{
		"endpoint":    "http://localhost:11434/v1",
		"api_key_env": unsetVar,
	})
	if err == nil {
		t.Fatalf("expected configure to fail when api_key_env points at unset var")
	}
	if !strings.Contains(err.Error(), unsetVar) {
		t.Fatalf("error %v does not mention %q", err, unsetVar)
	}
}

func TestConfigure_ThresholdRange(t *testing.T) {
	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	err := p.configure(t, map[string]any{
		"endpoint":       "http://localhost:11434/v1",
		"spam_threshold": 1.5,
	})
	if err == nil {
		t.Fatalf("expected configure to fail on out-of-range threshold")
	}
}

func TestClassify_APIKeyHeader(t *testing.T) {
	const envVar = "HEROLD_SPAM_LLM_TEST_KEY"
	const want = "sk-test-123"
	if err := os.Setenv(envVar, want); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(envVar) })

	var gotAuth string
	var authMu sync.Mutex
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		gotAuth = r.Header.Get("Authorization")
		authMu.Unlock()
		replyJSON(w, `{"verdict":"ham","score":0.0,"reason":"ok"}`)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":    llm.endpoint(),
		"model":       "fake",
		"api_key_env": envVar,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.classify(ctx, canonicalPayload("x")); err != nil {
		t.Fatalf("classify: %v", err)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if gotAuth != "Bearer "+want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer "+want)
	}
}

// TestHealth_Probe exercises the /models reachability probe.
func TestHealth_Probe(t *testing.T) {
	var hits int64
	llm := newFakeLLM(t)
	// Override /models handler to count.
	llm.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			atomic.AddInt64(&hits, 1)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		http.NotFound(w, r)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint": llm.endpoint(),
		"model":    "fake",
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res plug.HealthResult
	if err := p.client.Call(ctx, plug.MethodHealth, nil, &res); err != nil {
		t.Fatalf("health: %v", err)
	}
	if !res.OK {
		t.Fatalf("health not OK: %+v", res)
	}
	if atomic.LoadInt64(&hits) == 0 {
		t.Fatalf("health probe did not hit /v1/models")
	}
}
