package llmtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Recorder wraps a real LLM HTTP endpoint. On every call it forwards
// the request to the upstream and appends a fixture line to the
// configured output file (REQ-FILT-300, REQ-FILT-301).
//
// Construct with NewRecorder; do not create directly.
//
// The Recorder is used only by scripts/llm-capture.sh — never in CI.
type Recorder struct {
	mu        sync.Mutex
	kind      FixtureKind
	endpoint  string
	model     string
	apiKey    string
	client    *http.Client
	out       *os.File
	created   int
	updated   int
	unchanged int
	existing  map[FixtureKey]*FixtureEntry
}

// RecorderOptions configures a Recorder.
type RecorderOptions struct {
	// Kind is the fixture kind (KindCategorise or KindSpamClassify).
	Kind FixtureKind
	// Endpoint is the OpenAI-compatible endpoint base URL
	// (e.g. "http://localhost:11434/v1").
	Endpoint string
	// Model is the model name to pass in chat-completions requests.
	Model string
	// APIKey is the optional Bearer token.
	APIKey string
	// HTTPClient is the transport; nil uses a default 60s-timeout client.
	HTTPClient *http.Client
	// OutputFile is the JSONL file to write fixtures to. If it already
	// exists, existing entries are loaded so the Recorder can report
	// created/updated/unchanged statistics. Required.
	OutputFile string
}

// NewRecorder returns a Recorder ready to forward calls to the real
// endpoint and write fixtures.
func NewRecorder(opts RecorderOptions) (*Recorder, error) {
	if opts.OutputFile == "" {
		return nil, fmt.Errorf("llmtest Recorder: OutputFile is required")
	}
	if err := os.MkdirAll(filepath.Dir(opts.OutputFile), 0o755); err != nil {
		return nil, fmt.Errorf("llmtest Recorder: mkdir: %w", err)
	}

	existing := make(map[FixtureKey]*FixtureEntry)
	if data, err := os.ReadFile(opts.OutputFile); err == nil {
		for _, line := range splitLines(data) {
			var e FixtureEntry
			if json.Unmarshal(line, &e) == nil {
				k := FixtureKey{Kind: e.Kind, Hash: e.PromptHash}
				existing[k] = &e
			}
		}
	}

	f, err := os.OpenFile(opts.OutputFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("llmtest Recorder: open output: %w", err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Recorder{
		kind:     opts.Kind,
		endpoint: opts.Endpoint,
		model:    opts.Model,
		apiKey:   opts.APIKey,
		client:   client,
		out:      f,
		existing: existing,
	}, nil
}

// Close flushes and closes the output file. Must be called when capture
// is complete.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.Close()
}

// Stats returns the created/updated/unchanged counts accumulated since
// the Recorder was constructed.
func (r *Recorder) Stats() (created, updated, unchanged int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.created, r.updated, r.unchanged
}

// Complete implements ChatCompleter for internal/categorise.
func (r *Recorder) Complete(ctx context.Context, prompt, userContent string) (string, error) {
	combined := prompt + "\n" + userContent
	hash := HashPrompt(combined)

	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type chatRequest struct {
		Model          string         `json:"model"`
		Messages       []chatMessage  `json:"messages"`
		Temperature    float64        `json:"temperature"`
		ResponseFormat map[string]any `json:"response_format,omitempty"`
	}
	type chatResponse struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	reqBody := chatRequest{
		Model: r.model,
		Messages: []chatMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: userContent},
		},
		Temperature:    0,
		ResponseFormat: map[string]any{"type": "json_object"},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("llmtest recorder: marshal: %w", err)
	}
	url := r.endpoint + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("llmtest recorder: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llmtest recorder: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("llmtest recorder: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llmtest recorder: HTTP %d: %s", resp.StatusCode, truncate(respBytes, 256))
	}
	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return "", fmt.Errorf("llmtest recorder: decode response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("llmtest recorder: no choices in response")
	}

	// Store the assistant content as the fixture response.
	content := cr.Choices[0].Message.Content
	responseObj := map[string]any{"content": content}
	responseRaw, err := json.Marshal(responseObj)
	if err != nil {
		return "", fmt.Errorf("llmtest recorder: marshal response: %w", err)
	}

	model := cr.Model
	if model == "" {
		model = r.model
	}
	r.record(combined, hash, prompt, responseRaw, model)
	return content, nil
}

// Call implements SpamInvoker for internal/spam.
func (r *Recorder) Call(ctx context.Context, _, method string, params any, result any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("llmtest recorder: marshal params: %w", err)
	}
	hash := HashPrompt(string(paramsJSON))

	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type chatRequest struct {
		Model       string        `json:"model"`
		Messages    []chatMessage `json:"messages"`
		Temperature float64       `json:"temperature"`
	}
	type chatResponse struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	reqBody := chatRequest{
		Model: r.model,
		Messages: []chatMessage{
			{Role: "system", Content: "You are a spam classifier. Given email metadata, return JSON with fields: verdict (ham|spam), score (0.0-1.0), reason (string)."},
			{Role: "user", Content: string(paramsJSON)},
		},
		Temperature: 0,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("llmtest recorder: marshal request: %w", err)
	}
	url := r.endpoint + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("llmtest recorder: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("llmtest recorder: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("llmtest recorder: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llmtest recorder: HTTP %d: %s", resp.StatusCode, truncate(respBytes, 256))
	}
	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return fmt.Errorf("llmtest recorder: decode response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return fmt.Errorf("llmtest recorder: no choices")
	}

	content := cr.Choices[0].Message.Content
	// The spam plugin is expected to return a JSON object directly.
	// Wrap the content as a response map.
	var resultObj json.RawMessage
	if json.Unmarshal([]byte(content), &resultObj) != nil {
		// Content is not JSON; wrap it.
		resultObj, _ = json.Marshal(map[string]any{"raw": content})
	}

	model := cr.Model
	if model == "" {
		model = r.model
	}
	r.record(string(paramsJSON), hash, method+":"+string(paramsJSON), resultObj, model)

	if result != nil {
		if err := json.Unmarshal(resultObj, result); err != nil {
			return fmt.Errorf("llmtest recorder: unmarshal result: %w", err)
		}
	}
	return nil
}

// record appends or updates a fixture entry and updates statistics.
func (r *Recorder) record(_, hash, prompt string, response json.RawMessage, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := FixtureKey{Kind: r.kind, Hash: hash}
	entry := FixtureEntry{
		V:          1,
		Kind:       r.kind,
		PromptHash: hash,
		Prompt:     prompt,
		Response:   response,
		Model:      model,
		CapturedAt: time.Now().UTC(),
	}
	if _, exists := r.existing[key]; exists {
		r.updated++
	} else {
		r.created++
	}
	line, _ := json.Marshal(entry)
	line = append(line, '\n')
	_, _ = r.out.Write(line)
}

// splitLines splits a byte slice by newlines, skipping blank lines.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// truncate returns the first n bytes of b as a string.
func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
