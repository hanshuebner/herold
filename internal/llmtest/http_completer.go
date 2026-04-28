package llmtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPChatCompleter is the production implementation of ChatCompleter.
// It POSTs to an OpenAI-compatible chat-completions endpoint and
// returns the assistant content from the first choice.
//
// Construct with NewHTTPChatCompleter; do not create directly.
type HTTPChatCompleter struct {
	client   *http.Client
	endpoint string
	model    string
	apiKey   string
}

// HTTPChatCompleterOptions configures an HTTPChatCompleter.
type HTTPChatCompleterOptions struct {
	// Endpoint is the base URL of the OpenAI-compatible endpoint
	// (e.g. "http://localhost:11434/v1"). Required.
	Endpoint string
	// Model is the model name passed in chat-completions requests.
	// Required.
	Model string
	// APIKey is the optional Bearer token.
	APIKey string
	// HTTPClient is the transport; nil uses a default 30s-timeout client.
	HTTPClient *http.Client
}

// NewHTTPChatCompleter returns an HTTPChatCompleter configured against
// opts.
func NewHTTPChatCompleter(opts HTTPChatCompleterOptions) *HTTPChatCompleter {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPChatCompleter{
		client:   client,
		endpoint: opts.Endpoint,
		model:    opts.Model,
		apiKey:   opts.APIKey,
	}
}

type httpChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type httpChatRequest struct {
	Model          string            `json:"model"`
	Messages       []httpChatMessage `json:"messages"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat map[string]any    `json:"response_format,omitempty"`
}

type httpChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Complete implements ChatCompleter. It sends a two-turn conversation
// (system + user) to the endpoint and returns the assistant's text
// content.
func (c *HTTPChatCompleter) Complete(ctx context.Context, prompt, userContent string) (string, error) {
	body := httpChatRequest{
		Model: c.model,
		Messages: []httpChatMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: userContent},
		},
		Temperature:    0,
		ResponseFormat: map[string]any{"type": "json_object"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("http_completer: marshal: %w", err)
	}
	url := c.endpoint + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("http_completer: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http_completer: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("http_completer: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		prefix := string(respBytes)
		if len(prefix) > 256 {
			prefix = prefix[:256]
		}
		return "", fmt.Errorf("http_completer: HTTP %d: %s", resp.StatusCode, prefix)
	}
	var cr httpChatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return "", fmt.Errorf("http_completer: decode response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("http_completer: no choices in response")
	}
	return cr.Choices[0].Message.Content, nil
}
