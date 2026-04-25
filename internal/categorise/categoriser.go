package categorise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/netguard"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultBodyExcerptBytes is the per-call cap on the body excerpt
// passed to the LLM, per REQ-FILT-214 (~2 KB plain text).
const DefaultBodyExcerptBytes = 2 * 1024

// DefaultTimeout is applied when neither the per-account TimeoutSec
// override nor the operator-supplied DefaultTimeout is set.
const DefaultTimeout = 5 * time.Second

// Options configures a Categoriser. Store, Logger, and Clock are
// required; the remaining fields supply operator-level defaults that
// per-account configuration rows may override.
type Options struct {
	// Store is the metadata repository the categoriser reads
	// per-account configuration from.
	Store store.Store
	// Logger receives structured log lines.
	Logger *slog.Logger
	// Clock is the time source; tests inject a deterministic clock.
	Clock clock.Clock
	// HTTPClient is the shared HTTP client used for chat-completions
	// calls. Nil falls back to a fresh client with a 30s overall
	// transport timeout. Tests inject one pointing at httptest.
	HTTPClient *http.Client
	// DefaultEndpoint is the operator-configured chat-completions
	// endpoint URL (e.g. "http://localhost:11434/v1"). Per-account
	// rows override; empty disables the categoriser unless every
	// account row supplies its own endpoint.
	DefaultEndpoint string
	// DefaultModel is the operator-configured model name.
	DefaultModel string
	// DefaultAPIKey is the resolved Bearer token (operator pulls
	// from env at startup); per-account APIKeyEnv overrides.
	DefaultAPIKey string
	// DefaultTimeout bounds a single chat-completions call when the
	// per-account TimeoutSec is zero. Zero applies DefaultTimeout
	// (5s).
	DefaultTimeout time.Duration
	// AllowedEndpointHosts is the closed set of hostnames a per-account
	// endpoint override is permitted to use. Empty means "only the
	// operator-default endpoint is allowed" — every per-account override
	// then falls back to DefaultEndpoint and the override row is logged
	// at warn. The match is exact and case-insensitive on the host
	// component of the URL; localhost variants are always permitted in
	// addition to whatever the operator lists.
	AllowedEndpointHosts []string
}

// Categoriser orchestrates the per-message LLM categorisation call.
// One *Categoriser is constructed at server startup and reused across
// deliveries; every method is safe for concurrent use.
type Categoriser struct {
	store        store.Store
	logger       *slog.Logger
	clock        clock.Clock
	httpClient   *http.Client
	endpoint     string
	model        string
	apiKey       string
	timeout      time.Duration
	allowedHosts map[string]struct{}
}

// New returns a Categoriser configured against opts. A nil Store is a
// programmer error; the constructor returns nil to signal that the
// caller should not wire categorisation into the delivery pipeline.
func New(opts Options) *Categoriser {
	if opts.Store == nil {
		return nil
	}
	observe.RegisterCategoriseMetrics()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	timeout := opts.DefaultTimeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	allowed := make(map[string]struct{}, len(opts.AllowedEndpointHosts))
	for _, h := range opts.AllowedEndpointHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			allowed[h] = struct{}{}
		}
	}
	return &Categoriser{
		store:        opts.Store,
		logger:       logger,
		clock:        clk,
		httpClient:   httpClient,
		endpoint:     strings.TrimRight(opts.DefaultEndpoint, "/"),
		model:        opts.DefaultModel,
		apiKey:       opts.DefaultAPIKey,
		timeout:      timeout,
		allowedHosts: allowed,
	}
}

// Categorise runs the per-message classification (REQ-FILT-200..215).
// principal identifies the recipient whose configuration row drives
// the call; msg / auth / spamVerdict are passed through to the prompt
// for context. The returned categoryName is either the bare category
// name (no "$category-" prefix) or "" when no category applies, the
// LLM returned "none", or any error path was taken (REQ-FILT-202,
// REQ-FILT-215, REQ-FILT-230). Errors are NEVER returned to the
// delivery caller — failures collapse to ("", nil) plus a warn log so
// delivery never blocks.
func (c *Categoriser) Categorise(
	ctx context.Context,
	principal store.PrincipalID,
	msg mailparse.Message,
	auth *mailauth.AuthResults,
	spamVerdict spam.Verdict,
) (string, error) {
	if c == nil {
		return "", nil
	}
	cfg, err := c.store.Meta().GetCategorisationConfig(ctx, principal)
	if err != nil {
		c.logger.WarnContext(ctx, "categorise: load config",
			slog.Uint64("principal_id", uint64(principal)),
			slog.String("err", err.Error()))
		return "", nil
	}
	if !cfg.Enabled {
		return "", nil
	}
	endpoint := c.endpoint
	overrideEndpoint := false
	if cfg.Endpoint != nil && *cfg.Endpoint != "" {
		endpoint = strings.TrimRight(*cfg.Endpoint, "/")
		overrideEndpoint = true
	}
	if endpoint == "" {
		// No endpoint configured at any level — leave the message
		// uncategorised. Logged at debug because steady-state
		// operations on a server without LLM should not spam warn.
		c.logger.DebugContext(ctx, "categorise: no endpoint configured",
			slog.Uint64("principal_id", uint64(principal)))
		return "", nil
	}
	host, attachOperatorKey, err := c.validateEndpoint(ctx, endpoint, overrideEndpoint)
	if err != nil {
		c.logger.WarnContext(ctx, "categorise: endpoint rejected",
			slog.Uint64("principal_id", uint64(principal)),
			slog.String("endpoint", endpoint),
			slog.String("err", err.Error()))
		observe.CategoriseCallsTotal.WithLabelValues("endpoint_rejected").Inc()
		return "", nil
	}
	_ = host
	model := c.model
	if cfg.Model != nil && *cfg.Model != "" {
		model = *cfg.Model
	}
	if model == "" {
		c.logger.WarnContext(ctx, "categorise: no model configured",
			slog.Uint64("principal_id", uint64(principal)))
		observe.CategoriseCallsTotal.WithLabelValues("endpoint_rejected").Inc()
		return "", nil
	}
	apiKey := c.apiKey
	if cfg.APIKeyEnv != nil && *cfg.APIKeyEnv != "" {
		// API-key resolution is the operator's responsibility at
		// startup time; we do NOT read os.Getenv here because the
		// store row is mutable from admin tooling and a typo must not
		// leak environment names into a runtime panic. Per-account
		// overrides therefore require the operator to plumb the key
		// through DefaultAPIKey or the HTTPClient transport. We log
		// at debug to make the override visible during diagnostics.
		c.logger.DebugContext(ctx, "categorise: per-account api_key_env override ignored at runtime; configure via operator default",
			slog.Uint64("principal_id", uint64(principal)),
			slog.String("api_key_env", *cfg.APIKeyEnv))
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = c.timeout
	}

	callCtx, cancel := withBoundedDeadline(ctx, c.clock, timeout)
	defer cancel()

	prompt := renderSystemPrompt(cfg.Prompt, cfg.CategorySet)
	user := buildUserPayload(msg, auth, spamVerdict)
	if !attachOperatorKey {
		// Per-account override pointing at a host the operator did not
		// allowlist: never attach the operator-default API key. The
		// account row could be operator-set to point at a localhost
		// Ollama, in which case no Bearer token is appropriate either.
		apiKey = ""
	}
	start := c.clock.Now()
	cat, callErr := c.callLLM(callCtx, endpoint, model, apiKey, prompt, user)
	observe.CategoriseCallDurationSeconds.Observe(c.clock.Now().Sub(start).Seconds())
	if callErr != nil {
		c.logger.WarnContext(ctx, "categorise: chat completion failed",
			slog.Uint64("principal_id", uint64(principal)),
			slog.String("endpoint", endpoint),
			slog.String("model", model),
			slog.String("err", callErr.Error()))
		if errors.Is(callErr, context.DeadlineExceeded) {
			observe.CategoriseCallsTotal.WithLabelValues("timeout").Inc()
		} else {
			observe.CategoriseCallsTotal.WithLabelValues("http_error").Inc()
		}
		return "", nil
	}
	if cat == "" || strings.EqualFold(cat, "none") {
		observe.CategoriseCallsTotal.WithLabelValues("none").Inc()
		return "", nil
	}
	if !categoryInSet(cat, cfg.CategorySet) {
		c.logger.WarnContext(ctx, "categorise: model returned unknown category",
			slog.Uint64("principal_id", uint64(principal)),
			slog.String("category", cat))
		observe.CategoriseCallsTotal.WithLabelValues("unknown_category").Inc()
		return "", nil
	}
	observe.CategoriseCallsTotal.WithLabelValues("categorised").Inc()
	observe.CategoriseCategoriesAssignedTotal.WithLabelValues(cat).Inc()
	return cat, nil
}

// validateEndpoint enforces the categoriser's outbound-call policy:
// (a) https only, except http://localhost / 127.0.0.1 / [::1] which is
// permitted for the developer-Ollama loopback case; (b) the host must
// not resolve to a private/loopback/link-local/CGNAT/multicast IP UNLESS
// it is the operator-allowlisted set OR a localhost literal; (c) when
// the endpoint is a per-account override pointing at a host the
// operator did NOT allowlist, return attachOperatorKey=false so the
// caller does not leak the operator-default API key to that endpoint.
//
// The operator-default endpoint (overrideEndpoint==false) is trusted
// unconditionally — it is configured in the system.toml or via the
// admin REST surface, both of which are operator-controlled.
func (c *Categoriser) validateEndpoint(ctx context.Context, endpoint string, overrideEndpoint bool) (host string, attachOperatorKey bool, err error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", false, fmt.Errorf("parse endpoint: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	if host == "" {
		return "", false, errors.New("endpoint has no host")
	}
	if u.User != nil {
		return "", false, errors.New("endpoint must not embed credentials")
	}
	switch scheme {
	case "https":
		// fine
	case "http":
		if !netguard.IsLocalhost(host) {
			return "", false, fmt.Errorf("http scheme not permitted for non-localhost host %q", host)
		}
	default:
		return "", false, fmt.Errorf("unsupported scheme %q", scheme)
	}
	allowed := false
	if !overrideEndpoint {
		allowed = true
	} else if _, ok := c.allowedHosts[host]; ok {
		allowed = true
	} else if netguard.IsLocalhost(host) {
		// Localhost is acceptable as a destination but is not the
		// operator-default; do NOT attach the operator API key in that
		// case (caller drops it via attachOperatorKey=false).
		allowed = false
	}
	if !netguard.IsLocalhost(host) {
		// Resolve and refuse private ranges. Localhost short-circuits
		// because IsLoopback covers it without a DNS round-trip.
		if err := netguard.CheckHost(ctx, nil, host); err != nil {
			return "", false, err
		}
	}
	return host, allowed, nil
}

// userPayload is the JSON body the LLM sees as the "user" turn.
// Privacy posture matches REQ-FILT-214: envelope summary plus a
// trimmed body excerpt; no attachment content, no full HTML, no raw
// header soup.
type userPayload struct {
	From                 []string `json:"from"`
	To                   []string `json:"to"`
	Cc                   []string `json:"cc,omitempty"`
	Subject              string   `json:"subject"`
	ListID               string   `json:"list_id,omitempty"`
	ListUnsubscribe      string   `json:"list_unsubscribe,omitempty"`
	AuthenticationResult string   `json:"authentication_results,omitempty"`
	SpamVerdict          string   `json:"spam_verdict,omitempty"`
	BodyExcerpt          string   `json:"body_excerpt,omitempty"`
}

// buildUserPayload assembles the user-turn payload from a parsed
// message + auth + spam context. The body excerpt is capped at
// DefaultBodyExcerptBytes; HTML parts are stripped to text using the
// same naive tag stripper the spam classifier uses.
func buildUserPayload(msg mailparse.Message, auth *mailauth.AuthResults, spamVerdict spam.Verdict) userPayload {
	out := userPayload{
		From:            addrsToStrings(msg.Envelope.From),
		To:              addrsToStrings(msg.Envelope.To),
		Cc:              addrsToStrings(msg.Envelope.Cc),
		Subject:         msg.Envelope.Subject,
		ListID:          msg.Headers.Get("List-ID"),
		ListUnsubscribe: msg.Headers.Get("List-Unsubscribe"),
		BodyExcerpt:     collectTextBody(msg.Body, DefaultBodyExcerptBytes),
	}
	if auth != nil && auth.Raw != "" {
		out.AuthenticationResult = auth.Raw
	}
	if spamVerdict != spam.Unclassified {
		out.SpamVerdict = spamVerdict.String()
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

// chatResponse is the slice of the OpenAI response we need.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// modelVerdict is the JSON shape the model is instructed to emit.
type modelVerdict struct {
	Category string `json:"category"`
}

// jsonObjectRE finds the first balanced-looking JSON object in a
// string. Mirrors the spam plugin's tolerant fallback for prose-
// wrapped model output.
var jsonObjectRE = regexp.MustCompile(`(?s)\{.*\}`)

// callLLM POSTs the chat-completions request, parses the assistant
// reply, and returns the chosen category name (or "none"). Transport
// or parse failures are returned as wrapped errors.
func (c *Categoriser) callLLM(
	ctx context.Context,
	endpoint, model, apiKey, systemPrompt string,
	user userPayload,
) (string, error) {
	userJSON, err := json.Marshal(user)
	if err != nil {
		return "", fmt.Errorf("marshal user payload: %w", err)
	}
	body := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
		Temperature:    0.0,
		ResponseFormat: map[string]any{"type": "json_object"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}
	url := endpoint + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("build chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read chat response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		prefix := string(respBytes)
		if len(prefix) > 256 {
			prefix = prefix[:256]
		}
		return "", fmt.Errorf("chat completions HTTP %d: %s", resp.StatusCode, prefix)
	}
	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("chat completions returned no choices")
	}
	return parseModelCategory(cr.Choices[0].Message.Content)
}

// parseModelCategory extracts the category name from the assistant's
// textual content. Strict JSON first; tolerant {...} extraction next.
func parseModelCategory(text string) (string, error) {
	s := strings.TrimSpace(text)
	var mv modelVerdict
	if err := json.Unmarshal([]byte(s), &mv); err == nil && mv.Category != "" {
		return strings.ToLower(strings.TrimSpace(mv.Category)), nil
	}
	m := jsonObjectRE.FindString(s)
	if m == "" {
		return "", fmt.Errorf("no JSON object in model reply: %q", truncateForError(text))
	}
	if err := json.Unmarshal([]byte(m), &mv); err != nil {
		return "", fmt.Errorf("parse model JSON: %w (raw=%q)", err, truncateForError(m))
	}
	if mv.Category == "" {
		return "", fmt.Errorf("model JSON missing category: %q", truncateForError(m))
	}
	return strings.ToLower(strings.TrimSpace(mv.Category)), nil
}

// categoryInSet reports whether name (case-insensitive) is one of the
// configured category names.
func categoryInSet(name string, set []store.CategoryDef) bool {
	for _, c := range set {
		if strings.EqualFold(c.Name, name) {
			return true
		}
	}
	return false
}

// addrsToStrings flattens a list of mail.Address into "name <addr>"
// strings, falling back to the bare address when no display name is
// set. Empty input returns nil so the JSON marshaller omits the field.
func addrsToStrings(addrs []mail.Address) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Name == "" {
			out = append(out, a.Address)
			continue
		}
		out = append(out, fmt.Sprintf("%s <%s>", a.Name, a.Address))
	}
	return out
}

// collectTextBody concatenates decoded text/* parts up to cap bytes.
// HTML parts are rendered to plain text by stripping tags — the
// classifier prompt tolerates noise and avoiding an HTML parser keeps
// the package's dependency surface narrow (STANDARDS.md §3).
func collectTextBody(p mailparse.Part, cap int) string {
	if cap <= 0 {
		cap = DefaultBodyExcerptBytes
	}
	var b strings.Builder
	collectTextBodyInto(p, &b, cap)
	s := b.String()
	if len(s) > cap {
		s = s[:cap]
	}
	return s
}

func collectTextBodyInto(p mailparse.Part, b *strings.Builder, cap int) {
	if b.Len() >= cap {
		return
	}
	ct := strings.ToLower(p.ContentType)
	switch {
	case strings.HasPrefix(ct, "text/html"):
		b.WriteString(stripHTMLTags(p.Text))
		b.WriteByte('\n')
	case strings.HasPrefix(ct, "text/"):
		b.WriteString(p.Text)
		b.WriteByte('\n')
	}
	for _, c := range p.Children {
		if b.Len() >= cap {
			return
		}
		collectTextBodyInto(c, b, cap)
	}
}

// stripHTMLTags is the same naive stripper the spam classifier uses.
// Good enough for prompt context; URLs and mail addresses survive.
func stripHTMLTags(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	skip := 0
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch c {
		case '<':
			skip++
			continue
		case '>':
			if skip > 0 {
				skip--
			}
			continue
		}
		if skip == 0 {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// withBoundedDeadline returns a context whose deadline is the sooner
// of the inherited deadline and now+timeout, where now comes from the
// supplied Clock. The returned cancel must always be called.
//
// The Clock parameter exists so categorise's deterministic-time
// discipline (STANDARDS §5: no wall-clock reads in deterministic code)
// extends to deadline computation; tests inject a FakeClock and the
// derived deadline lines up with the rest of the package's time source.
func withBoundedDeadline(parent context.Context, clk clock.Clock, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	deadline := clk.Now().Add(timeout)
	if dl, ok := parent.Deadline(); ok && dl.Before(deadline) {
		// Parent deadline is tighter; let it win.
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, deadline)
}

// truncateForError bounds a string embedded in an error message so we
// do not surface unbounded model output back to the operator log.
func truncateForError(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
