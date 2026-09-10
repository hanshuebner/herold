package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// BuildRequest produces on the wire. The sdk.SpamClassifyParams shape
// mirrors spam.Request field-for-field after Wave 3, so every key
// round-trips through the supervisor into the plugin and onto the
// LLM prompt.
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

// TestClassify_FullPayloadReachesLLM asserts that every field
// produced by internal/spam.BuildRequest survives the sdk unmarshal
// and lands in the LLM's user-turn JSON. Before Wave 3 the plugin
// only received body_excerpt because sdk.SpamClassifyParams used
// nested envelope/headers maps that the flat payload did not match.
func TestClassify_FullPayloadReachesLLM(t *testing.T) {
	var captured string
	var mu sync.Mutex
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = string(body)
		mu.Unlock()
		replyJSON(w, `{"verdict":"ham","score":0.1,"reason":"ok"}`)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":       llm.endpoint(),
		"model":          "fake",
		"spam_threshold": 0.5,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.classify(ctx, canonicalPayload("please review")); err != nil {
		t.Fatalf("classify: %v", err)
	}

	mu.Lock()
	body := captured
	mu.Unlock()
	// The LLM request is a chat-completion envelope whose user turn
	// carries the payload as a JSON-stringified object; the inner
	// object's quote characters are therefore escaped. We scan for
	// the escaped keys so we assert on the shape that actually
	// reaches the model.
	for _, want := range []string{
		`\"from\"`, `alice@example.com`,
		`\"to\"`, `bob@example.com`,
		`\"subject\"`, `Hello`,
		`\"dkim_pass\":true`,
		`\"spf_pass\":true`,
		`\"dmarc_pass\":true`,
		`\"from_domain\"`, `example.com`,
		`\"body_excerpt\"`, `please review`,
		`\"received_date\"`, `2026-04-24T00:00:00Z`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("LLM body missing %s; got %s", want, body)
		}
	}
}

// TestClassify_ForwardingHeadersReachLLM asserts that the data-grant
// forwarding/list headers and the server's own Authentication-Results
// string -- ReplyTo, ReturnPath, ListID, ListUnsubscribe, Precedence,
// AutoSubmitted, AuthResults -- survive trimPayload and land in the
// LLM's user-turn JSON, matching what internal/spam.BuildRequest now
// puts on the wire (re #298).
func TestClassify_ForwardingHeadersReachLLM(t *testing.T) {
	var captured string
	var mu sync.Mutex
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = string(body)
		mu.Unlock()
		replyJSON(w, `{"verdict":"ham","score":0.1,"reason":"ok"}`)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":       llm.endpoint(),
		"model":          "fake",
		"spam_threshold": 0.5,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	payload := canonicalPayload("newsletter body")
	payload["reply_to"] = "Reply <reply@example.com>"
	payload["return_path"] = "<bounce@example.com>"
	payload["list_id"] = "Kayak Club <kajak.example.org>"
	payload["list_unsubscribe"] = "<mailto:unsub@example.com>"
	payload["precedence"] = "bulk"
	payload["auto_submitted"] = "auto-generated"
	payload["auth_results"] = "mail.example.com; spf=pass smtp.mailfrom=example.com"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.classify(ctx, payload); err != nil {
		t.Fatalf("classify: %v", err)
	}

	mu.Lock()
	body := captured
	mu.Unlock()
	// The LLM request is a chat-completion envelope whose user turn
	// carries the payload as a JSON-stringified object; scan for the
	// escaped keys/values so the assertion matches what actually
	// reaches the model.
	for _, want := range []string{
		`\"reply_to\"`, `reply@example.com`,
		`\"return_path\"`, `bounce@example.com`,
		`\"list_id\"`, `kajak.example.org`,
		`\"list_unsubscribe\"`, `unsub@example.com`,
		`\"precedence\":\"bulk\"`,
		`\"auto_submitted\":\"auto-generated\"`,
		`\"auth_results\"`, `spf=pass`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("LLM body missing %s; got %s", want, body)
		}
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

// captureRequestBody spawns and configures the plugin against a fakeLLM
// that decodes the raw chat-completions request body into a map, then
// issues one classify call and returns the decoded body. Each of the
// TestClassify_ResponseFormat* tests asserts on a different slice of
// that decoded shape.
func captureRequestBody(t *testing.T, extraOpts map[string]any) map[string]any {
	t.Helper()
	var captured map[string]any
	var mu sync.Mutex
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode captured request body: %v (raw=%s)", err, body)
		}
		mu.Lock()
		captured = decoded
		mu.Unlock()
		replyJSON(w, `{"verdict":"ham","score":0.1,"reason":"ok"}`)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	opts := map[string]any{
		"endpoint":       llm.endpoint(),
		"model":          "fake",
		"spam_threshold": 0.5,
	}
	for k, v := range extraOpts {
		opts[k] = v
	}
	if err := p.configure(t, opts); err != nil {
		t.Fatalf("configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.classify(ctx, canonicalPayload("please review")); err != nil {
		t.Fatalf("classify: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatalf("fakeLLM never received a request")
	}
	return captured
}

// TestClassify_ResponseFormatDefaultJSONObject asserts that with no
// response_format option set, the request carries the original
// {"type":"json_object"} shape unchanged (Ollama/OpenAI default).
func TestClassify_ResponseFormatDefaultJSONObject(t *testing.T) {
	body := captureRequestBody(t, nil)
	want := map[string]any{"type": "json_object"}
	got, _ := body["response_format"].(map[string]any)
	if len(got) != len(want) || got["type"] != want["type"] {
		t.Fatalf("response_format = %#v, want %#v", body["response_format"], want)
	}
}

// TestClassify_ResponseFormatJSONObjectExplicit exercises
// response_format="json_object" set explicitly, verifying it produces
// the same body as the default.
func TestClassify_ResponseFormatJSONObjectExplicit(t *testing.T) {
	body := captureRequestBody(t, map[string]any{"response_format": "json_object"})
	got, _ := body["response_format"].(map[string]any)
	if len(got) != 1 || got["type"] != "json_object" {
		t.Fatalf("response_format = %#v, want {type: json_object}", body["response_format"])
	}
}

// TestClassify_ResponseFormatJSONSchema asserts that
// response_format="json_schema" sends the strict spam_verdict schema
// Anthropic's OpenAI-compatible endpoint requires (re #302).
func TestClassify_ResponseFormatJSONSchema(t *testing.T) {
	body := captureRequestBody(t, map[string]any{"response_format": "json_schema"})
	rf, ok := body["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format missing or wrong type: %#v", body["response_format"])
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format.type = %v, want json_schema", rf["type"])
	}
	js, ok := rf["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("response_format.json_schema missing or wrong type: %#v", rf["json_schema"])
	}
	if js["name"] != "spam_verdict" {
		t.Fatalf("json_schema.name = %v, want spam_verdict", js["name"])
	}
	if js["strict"] != true {
		t.Fatalf("json_schema.strict = %v, want true", js["strict"])
	}
	schema, ok := js["schema"].(map[string]any)
	if !ok {
		t.Fatalf("json_schema.schema missing or wrong type: %#v", js["schema"])
	}
	if schema["type"] != "object" {
		t.Fatalf("schema.type = %v, want object", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema.additionalProperties = %v, want false", schema["additionalProperties"])
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema.required missing or wrong type: %#v", schema["required"])
	}
	gotRequired := map[string]bool{}
	for _, r := range required {
		gotRequired[fmt.Sprint(r)] = true
	}
	for _, want := range []string{"verdict", "score", "reason"} {
		if !gotRequired[want] {
			t.Fatalf("schema.required = %v, missing %q", required, want)
		}
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties missing or wrong type: %#v", schema["properties"])
	}
	verdictProp, ok := props["verdict"].(map[string]any)
	if !ok || verdictProp["type"] != "string" {
		t.Fatalf("schema.properties.verdict = %#v, want string-typed property", props["verdict"])
	}
	enumVals, ok := verdictProp["enum"].([]any)
	if !ok {
		t.Fatalf("schema.properties.verdict.enum missing or wrong type: %#v", verdictProp["enum"])
	}
	gotEnum := map[string]bool{}
	for _, e := range enumVals {
		gotEnum[fmt.Sprint(e)] = true
	}
	if !gotEnum["spam"] || !gotEnum["ham"] {
		t.Fatalf("schema.properties.verdict.enum = %v, want [spam ham]", enumVals)
	}
	scoreProp, ok := props["score"].(map[string]any)
	if !ok || scoreProp["type"] != "number" {
		t.Fatalf("schema.properties.score = %#v, want number-typed property", props["score"])
	}
	reasonProp, ok := props["reason"].(map[string]any)
	if !ok || reasonProp["type"] != "string" {
		t.Fatalf("schema.properties.reason = %#v, want string-typed property", props["reason"])
	}
}

// TestClassify_ResponseFormatNone asserts that response_format="none"
// omits the field entirely, relying on jsonObjectRE to extract the
// verdict from free-form model text.
func TestClassify_ResponseFormatNone(t *testing.T) {
	body := captureRequestBody(t, map[string]any{"response_format": "none"})
	if v, present := body["response_format"]; present {
		t.Fatalf("response_format = %#v, want field omitted entirely", v)
	}
}

// TestConfigure_ResponseFormatRejectsUnknownValue asserts that an
// unrecognized response_format value fails Configure and the error
// names all three accepted values.
func TestConfigure_ResponseFormatRejectsUnknownValue(t *testing.T) {
	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	err := p.configure(t, map[string]any{
		"endpoint":        "http://localhost:11434/v1",
		"response_format": "yaml",
	})
	if err == nil {
		t.Fatalf("expected configure to fail on unknown response_format value")
	}
	for _, want := range []string{"json_object", "json_schema", "none"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v does not mention %q", err, want)
		}
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

// TestConfigure_StringTypedOptionsFromServer drives OnConfigure with the
// exact map[string]any shape internal/admin's resolvePluginOptions
// produces for a [[plugin]] block: every value is a Go string, including
// numeric and boolean options and a pre-resolved api_key secret (re #302).
// sysconfig.PluginConfig.Options is map[string]string, and
// resolvePluginOptions forwards every value verbatim (after expanding any
// "$VAR"/"file:" reference) — the plugin never sees a JSON number or bool
// through this path in production.
func TestConfigure_StringTypedOptionsFromServer(t *testing.T) {
	var gotAuth string
	var authMu sync.Mutex
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		gotAuth = r.Header.Get("Authorization")
		authMu.Unlock()
		// The model reports "spam" with a low score; the string-configured
		// spam_threshold="0.9" must still downgrade this to "ham" for the
		// test to prove the threshold, not just the endpoint, took effect.
		replyJSON(w, `{"verdict":"spam","score":0.42,"reason":"borderline"}`)
	})

	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint":               llm.endpoint(),
		"model":                  "fake",
		"api_key":                "sk-resolved-secret-value",
		"timeout_sec":            "5",
		"spam_threshold":         "0.9",
		"system_prompt_override": "classify as instructed",
		"max_body_chars":         "2000",
		"log_samples":            "true",
	}); err != nil {
		t.Fatalf("configure with string-typed options: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := p.classify(ctx, canonicalPayload("x"))
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	authMu.Lock()
	auth := gotAuth
	authMu.Unlock()
	if auth != "Bearer sk-resolved-secret-value" {
		t.Fatalf("Authorization header = %q, want the api_key value as bearer token", auth)
	}
	if res["verdict"] != "ham" {
		t.Fatalf("verdict = %v, want ham (score 0.42 below string-configured threshold 0.9)", res["verdict"])
	}
}

// TestConfigure_APIKeyEnvResolvedSecretRejected exercises the ticket's
// compatibility rule for api_key_env: because its key also matches
// sysconfig's secret-key heuristic, system.toml forces its value to a
// "$VAR"/"file:" reference and the server resolves it to the secret
// itself before the plugin sees it — not the name of an environment
// variable. When that resolved value does not look like a variable name,
// OnConfigure must fail with a message pointing at api_key rather than
// silently doing an os.Getenv lookup on the secret's own value (re #302).
func TestConfigure_APIKeyEnvResolvedSecretRejected(t *testing.T) {
	bin := buildPlugin(t)
	p := spawnPlugin(t, bin)
	defer p.close()

	p.initialize(t)
	err := p.configure(t, map[string]any{
		"endpoint":    "http://localhost:11434/v1",
		"api_key_env": "sk-resolved-secret-with-dashes!",
	})
	if err == nil {
		t.Fatalf("expected configure to fail when api_key_env carries a resolved secret")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("error %v does not point at api_key", err)
	}
}
