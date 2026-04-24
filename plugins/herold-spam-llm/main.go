// Command herold-spam-llm is a first-party spam classifier plugin that
// forwards each classification request to an OpenAI-compatible
// chat-completions endpoint (defaults to a local Ollama instance, per
// REQ-FILT-05 / REQ-FILT-11).
//
// The plugin is deliberately stateless beyond its configured options and
// the shared *http.Client. It never retries on its own: the server owns
// retry and circuit-breaker policy (REQ-FILT-41, REQ-FILT-52).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

// Default configuration values. The operator can override any of these at
// Configure time. Keep the defaults conservative: local Ollama, small
// model, short timeout. Cloud endpoints are opt-in (REQ-FILT-61).
const (
	defaultEndpoint     = "http://localhost:11434/v1"
	defaultModel        = "llama3.2"
	defaultTimeoutSec   = 5
	defaultThreshold    = 0.7
	defaultMaxBodyChars = 4000
)

// builtinSystemPrompt is the classifier instruction. Keep it terse and
// structured: small local models follow short prompts better than long
// ones. Operators may override via the system_prompt_override option.
const builtinSystemPrompt = `You are a spam classifier. Return ONLY a single JSON object with this shape:
{"verdict": "spam" | "ham", "score": 0.0..1.0, "reason": "..."}
Do not include any other text.
Score is your confidence that the message is spam.
Consider: authentication results (DKIM/SPF/DMARC), subject, from, body text.`

// knownOptions enumerates every option key the plugin accepts. Any other
// key in the configure map is rejected so typos surface immediately
// (REQ-PLUG-21).
var knownOptions = map[string]struct{}{
	"endpoint":               {},
	"model":                  {},
	"api_key_env":            {},
	"timeout_sec":            {},
	"spam_threshold":         {},
	"system_prompt_override": {},
	"max_body_chars":         {},
	"log_samples":            {},
}

// options holds the validated configuration. It is populated in
// OnConfigure and read without locking afterwards — Configure runs
// before any classify calls per REQ-PLUG lifecycle.
type options struct {
	endpoint      string
	model         string
	apiKey        string // resolved from api_key_env at Configure time
	apiKeyEnv     string
	timeout       time.Duration
	spamThreshold float64
	systemPrompt  string
	maxBodyChars  int
	logSamples    bool
}

type handler struct {
	mu         sync.RWMutex
	opts       options
	httpClient *http.Client
	inflight   sync.WaitGroup
	// callCount counts classify calls across the plugin's lifetime.
	// Every 100th call is logged at info (sampled) so operators get a
	// light heartbeat without leaking mail content.
	callCount atomic.Uint64
}

// newHandler returns a handler with a pooled HTTP client. Transport
// settings match Go's http.DefaultTransport except for pool knobs:
// the plugin only talks to one endpoint, so few idle conns are needed.
func newHandler() *handler {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &handler{
		httpClient: &http.Client{Transport: transport},
	}
}

// OnConfigure validates the options map and stashes the result. Unknown
// keys fail loud (REQ-PLUG-21). Numeric ranges are checked. api_key_env
// is resolved here so a misconfigured env var is caught at startup.
func (h *handler) OnConfigure(ctx context.Context, opts map[string]any) error {
	for k := range opts {
		if _, ok := knownOptions[k]; !ok {
			return fmt.Errorf("unknown option %q", k)
		}
	}

	cfg := options{
		endpoint:      defaultEndpoint,
		model:         defaultModel,
		timeout:       time.Duration(defaultTimeoutSec) * time.Second,
		spamThreshold: defaultThreshold,
		systemPrompt:  builtinSystemPrompt,
		maxBodyChars:  defaultMaxBodyChars,
	}

	if v, ok := opts["endpoint"]; ok {
		s, err := asString(v, "endpoint")
		if err != nil {
			return err
		}
		s = strings.TrimRight(strings.TrimSpace(s), "/")
		if s == "" {
			return errors.New("endpoint must be non-empty")
		}
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			return fmt.Errorf("endpoint must be http(s) URL, got %q", s)
		}
		cfg.endpoint = s
	}
	if v, ok := opts["model"]; ok {
		s, err := asString(v, "model")
		if err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			return errors.New("model must be non-empty")
		}
		cfg.model = s
	}
	if v, ok := opts["api_key_env"]; ok {
		s, err := asString(v, "api_key_env")
		if err != nil {
			return err
		}
		cfg.apiKeyEnv = strings.TrimSpace(s)
		if cfg.apiKeyEnv != "" {
			key := os.Getenv(cfg.apiKeyEnv)
			if key == "" {
				return fmt.Errorf("api_key_env=%s is set but environment variable is empty", cfg.apiKeyEnv)
			}
			cfg.apiKey = key
		}
	}
	if v, ok := opts["timeout_sec"]; ok {
		n, err := asInt(v, "timeout_sec")
		if err != nil {
			return err
		}
		if n <= 0 || n > 300 {
			return fmt.Errorf("timeout_sec out of range (1..300): %d", n)
		}
		cfg.timeout = time.Duration(n) * time.Second
	}
	if v, ok := opts["spam_threshold"]; ok {
		f, err := asFloat(v, "spam_threshold")
		if err != nil {
			return err
		}
		if f < 0 || f > 1 {
			return fmt.Errorf("spam_threshold out of range [0..1]: %v", f)
		}
		cfg.spamThreshold = f
	}
	if v, ok := opts["system_prompt_override"]; ok {
		s, err := asString(v, "system_prompt_override")
		if err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s != "" {
			cfg.systemPrompt = s
		}
	}
	if v, ok := opts["max_body_chars"]; ok {
		n, err := asInt(v, "max_body_chars")
		if err != nil {
			return err
		}
		if n <= 0 || n > 1_000_000 {
			return fmt.Errorf("max_body_chars out of range (1..1_000_000): %d", n)
		}
		cfg.maxBodyChars = n
	}
	if v, ok := opts["log_samples"]; ok {
		b, err := asBool(v, "log_samples")
		if err != nil {
			return err
		}
		cfg.logSamples = b
	}

	h.mu.Lock()
	h.opts = cfg
	h.mu.Unlock()

	sdk.Logf("info", "herold-spam-llm configured endpoint=%s model=%s timeout=%s threshold=%.2f",
		cfg.endpoint, cfg.model, cfg.timeout, cfg.spamThreshold)
	return nil
}

// OnHealth performs a lightweight reachability probe against {endpoint}/models.
// It is intentionally cheap: the server uses this to decide whether to
// mark the plugin healthy, not to validate correctness.
func (h *handler) OnHealth(ctx context.Context) error {
	h.mu.RLock()
	endpoint := h.opts.endpoint
	apiKey := h.opts.apiKey
	h.mu.RUnlock()
	if endpoint == "" {
		// Not yet configured — report healthy so the supervisor does not
		// restart us before Configure arrives.
		return nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint+"/models", nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health GET %s/models: %w", endpoint, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("health GET %s/models: status %d", endpoint, resp.StatusCode)
	}
	return nil
}

// OnShutdown waits for in-flight classify calls to return, bounded by
// the context deadline the SDK supplies.
func (h *handler) OnShutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		h.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SpamClassify satisfies sdk.SpamHandler. The received SpamClassifyParams
// carries whatever the server's current payload decodes into; the plugin
// re-marshals it and hands it to the LLM as the user turn. Any error
// returned here is surfaced to the server as an RPC error; the server's
// Classifier.parseClassification maps that to Unclassified (REQ-FILT-40).
func (h *handler) SpamClassify(ctx context.Context, in sdk.SpamClassifyParams) (sdk.SpamClassifyResult, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	h.mu.RLock()
	opts := h.opts
	h.mu.RUnlock()
	if opts.endpoint == "" {
		return sdk.SpamClassifyResult{}, errors.New("plugin not configured")
	}

	// Per-request deadline: use the shorter of the configured timeout
	// and the inherited ctx deadline. The ctx deadline (if any) wins
	// whenever it is tighter, because the supervisor already decided
	// how long it is willing to wait.
	callCtx, cancel := withBoundedDeadline(ctx, opts.timeout)
	defer cancel()

	payload := trimPayload(in, opts.maxBodyChars)
	userJSON, err := json.Marshal(payload)
	if err != nil {
		return sdk.SpamClassifyResult{}, fmt.Errorf("marshal user payload: %w", err)
	}

	started := time.Now()
	verdict, score, reason, err := h.callLLM(callCtx, opts, userJSON)
	elapsed := time.Since(started)

	labels := map[string]string{"model": opts.model}
	if err != nil {
		labels["result"] = "error"
		sdk.Metric("spam.latency_ms", labels, float64(elapsed.Milliseconds()))
		return sdk.SpamClassifyResult{}, err
	}

	final := sdk.SpamClassifyResult{
		Verdict:    verdict,
		Confidence: score,
		Reason:     reason,
	}
	// Apply threshold: if the LLM reported "spam" with a score below the
	// operator's threshold, downgrade to ham. Likewise a "ham" verdict
	// with an alarmingly high spam score is promoted. This keeps the
	// threshold a single operator-visible knob.
	if score >= opts.spamThreshold {
		final.Verdict = "spam"
	} else {
		final.Verdict = "ham"
	}
	labels["verdict"] = final.Verdict
	sdk.Metric("spam.latency_ms", labels, float64(elapsed.Milliseconds()))

	n := h.callCount.Add(1)
	if n%100 == 0 {
		sdk.Logf("info", "herold-spam-llm classify samples=%d latest_verdict=%s latest_latency_ms=%d",
			n, final.Verdict, elapsed.Milliseconds())
	}
	return final, nil
}

// SpamHealth reuses OnHealth but surfaces its result structurally. The
// server reads OK/latency to decide whether to route traffic here.
func (h *handler) SpamHealth(ctx context.Context) (sdk.SpamHealthResult, error) {
	started := time.Now()
	err := h.OnHealth(ctx)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return sdk.SpamHealthResult{OK: false, LatencyMsP: latency}, err
	}
	return sdk.SpamHealthResult{OK: true, LatencyMsP: latency}, nil
}

// trimPayload returns a sanitized copy of the inbound params, with the
// body excerpt capped at maxBody. The map-typed fields are copied
// shallowly — we do not mutate the server's payload.
func trimPayload(in sdk.SpamClassifyParams, maxBody int) map[string]any {
	out := map[string]any{}
	if len(in.Envelope) > 0 {
		out["envelope"] = in.Envelope
	}
	if len(in.Headers) > 0 {
		out["headers"] = in.Headers
	}
	if len(in.AuthResults) > 0 {
		out["auth_results"] = in.AuthResults
	}
	if len(in.Context) > 0 {
		out["context"] = in.Context
	}
	body := in.BodyExcerpt
	if maxBody > 0 && len(body) > maxBody {
		body = body[:maxBody]
	}
	if body != "" {
		out["body_excerpt"] = body
	}
	return out
}

// chatMessage is one entry in an OpenAI chat-completions request.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the request body sent to {endpoint}/chat/completions.
type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
}

// chatResponse is the subset of the OpenAI chat-completions response we
// need to parse. Both OpenAI and Ollama populate Choices[0].Message.Content
// with the model's textual output.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// modelVerdict is the JSON shape the model is instructed to emit.
type modelVerdict struct {
	Verdict string  `json:"verdict"`
	Score   float64 `json:"score"`
	Reason  string  `json:"reason"`
}

// jsonObjectRE finds the first balanced-looking JSON object in a string.
// Models wrap the verdict in prose often enough that a tolerant match
// pays off; parsing failures still fall through to the plugin's
// structured error path.
var jsonObjectRE = regexp.MustCompile(`(?s)\{.*\}`)

// callLLM performs the HTTP POST and parses the model's reply. It
// returns (verdict, score, reason, error); error is non-nil on any
// transport, HTTP, or parse failure.
func (h *handler) callLLM(ctx context.Context, opts options, userJSON []byte) (string, float64, string, error) {
	body := chatRequest{
		Model: opts.model,
		Messages: []chatMessage{
			{Role: "system", Content: opts.systemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
		Temperature:    0.0,
		ResponseFormat: map[string]any{"type": "json_object"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", 0, "", fmt.Errorf("marshal chat request: %w", err)
	}
	if opts.logSamples {
		sdk.Logf("debug", "herold-spam-llm request bytes=%d", len(raw))
	}

	url := opts.endpoint + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", 0, "", fmt.Errorf("build chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+opts.apiKey)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", 0, "", fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, "", fmt.Errorf("read chat response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		prefix := string(respBytes)
		if len(prefix) > 256 {
			prefix = prefix[:256]
		}
		sdk.Logf("warn", "herold-spam-llm non-200 status=%d body_prefix=%q", resp.StatusCode, prefix)
		return "", 0, "", fmt.Errorf("chat completions HTTP %d: %s", resp.StatusCode, prefix)
	}

	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return "", 0, "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", 0, "", errors.New("chat completions returned no choices")
	}
	content := cr.Choices[0].Message.Content
	if opts.logSamples {
		sdk.Logf("debug", "herold-spam-llm response bytes=%d", len(content))
	}

	mv, err := parseModelVerdict(content)
	if err != nil {
		return "", 0, "", err
	}
	return mv.Verdict, mv.Score, mv.Reason, nil
}

// parseModelVerdict extracts a modelVerdict from the raw assistant text.
// Strategy: first try direct unmarshal (fast path when the model obeyed),
// then fall back to the first `{...}` block extracted by regex.
func parseModelVerdict(text string) (modelVerdict, error) {
	s := strings.TrimSpace(text)
	var mv modelVerdict
	if err := json.Unmarshal([]byte(s), &mv); err == nil && mv.Verdict != "" {
		return mv, nil
	}
	m := jsonObjectRE.FindString(s)
	if m == "" {
		return modelVerdict{}, fmt.Errorf("no JSON object in model reply: %q", truncateForError(text))
	}
	if err := json.Unmarshal([]byte(m), &mv); err != nil {
		return modelVerdict{}, fmt.Errorf("parse model JSON: %w (raw=%q)", err, truncateForError(m))
	}
	if mv.Verdict == "" {
		return modelVerdict{}, fmt.Errorf("model JSON missing verdict: %q", truncateForError(m))
	}
	return mv, nil
}

// truncateForError bounds the length of a string embedded in an error
// message. We never log message bodies (REQ-FILT-62), but we do surface
// small prefixes of model output to help operators diagnose parsing
// failures.
func truncateForError(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// withBoundedDeadline returns a context whose deadline is the sooner of
// the inherited ctx deadline and now+timeout. The returned cancel must
// always be called.
func withBoundedDeadline(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	deadline := time.Now().Add(timeout)
	if dl, ok := parent.Deadline(); ok && dl.Before(deadline) {
		// Parent deadline is tighter; honor it directly.
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, deadline)
}

// asString coerces a JSON-decoded value to string. JSON numbers are
// rejected so typos like timeout_sec="5" still validate cleanly elsewhere.
func asString(v any, name string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", name, v)
	}
	return s, nil
}

func asBool(v any, name string) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean, got %T", name, v)
	}
	return b, nil
}

// asInt accepts either a json.Number-ish float (the default decoding of
// integer literals through map[string]any) or an explicit int.
func asInt(v any, name string) (int, error) {
	switch t := v.(type) {
	case float64:
		if t != float64(int(t)) {
			return 0, fmt.Errorf("%s must be an integer, got %v", name, t)
		}
		return int(t), nil
	case int:
		return t, nil
	case int64:
		return int(t), nil
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", name, v)
	}
}

func asFloat(v any, name string) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	default:
		return 0, fmt.Errorf("%s must be a number, got %T", name, v)
	}
}

func main() {
	manifest := sdk.Manifest{
		Name:                  "herold-spam-llm",
		Version:               "0.1.0",
		Type:                  plug.TypeSpam,
		Lifecycle:             plug.LifecycleLongRunning,
		MaxConcurrentRequests: 16,
		ABIVersion:            plug.ABIVersion,
		ShutdownGraceSec:      10,
		HealthIntervalSec:     30,
		OptionsSchema: map[string]plug.OptionSchema{
			"endpoint":               {Type: "string", Default: defaultEndpoint},
			"model":                  {Type: "string", Default: defaultModel},
			"api_key_env":            {Type: "string"},
			"timeout_sec":            {Type: "integer", Default: defaultTimeoutSec},
			"spam_threshold":         {Type: "number", Default: defaultThreshold},
			"system_prompt_override": {Type: "string"},
			"max_body_chars":         {Type: "integer", Default: defaultMaxBodyChars},
			"log_samples":            {Type: "boolean", Default: false},
		},
	}
	if err := sdk.Run(manifest, newHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "herold-spam-llm: %v\n", err)
		os.Exit(1)
	}
}
