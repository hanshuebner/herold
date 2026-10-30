package main_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureChat decodes a chat-completions request body into the system
// and user message contents.
func captureChat(t *testing.T, raw string) (system, user string) {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decode chat request: %v (raw=%s)", err, raw)
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			system = m.Content
		case "user":
			user = m.Content
		}
	}
	return system, user
}

// runCapturing spawns the plugin against a fake model, runs one request
// of the given method and returns the chat request the model received.
func runCapturing(t *testing.T, method string, payload map[string]any) (system, user string) {
	t.Helper()
	var captured string
	var mu sync.Mutex
	llm := newFakeLLM(t)
	llm.setHandler(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = string(body)
		mu.Unlock()
		replyJSON(w, `{"verdict":"ham","score":0.05,"reason":"ok"}`)
	})
	p := spawnPlugin(t, buildPlugin(t))
	defer p.close()
	p.initialize(t)
	if err := p.configure(t, map[string]any{
		"endpoint": llm.endpoint(), "model": "fake", "spam_threshold": 0.5,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if method == "mail.classify" {
		_, err = p.mailClassify(ctx, payload)
	} else {
		_, err = p.classify(ctx, payload)
	}
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	mu.Lock()
	defer mu.Unlock()
	return captureChat(t, captured)
}

// TestClassify_LinkHostMismatchAndAuthWordingReachLLM (re #552): the
// link_host_mismatch fact is forwarded to the model as a field, and both
// built-in prompts state it as a determined fact that is an input rather
// than a verdict, describe the "[link: host]" markers, and state that a
// DMARC pass proves who sent the message but is not on its own a ham
// signal while a passing sender is still never called spoofed.
func TestClassify_LinkHostMismatchAndAuthWordingReachLLM(t *testing.T) {
	for _, method := range []string{"spam.classify", "mail.classify"} {
		t.Run(method, func(t *testing.T) {
			payload := canonicalPayload("Transferir documento [link: files.docs-share.example]")
			payload["link_host_mismatch"] = true
			system, user := runCapturing(t, method, payload)
			if !strings.Contains(user, `"link_host_mismatch":true`) {
				t.Errorf("user turn lacks link_host_mismatch: %s", user)
			}
			for _, want := range []string{
				`"link_host_mismatch": true`,
				"herold has determined",
				"not a verdict",
				`"[link: host]" marker`,
				"is not on its own a ham signal",
				"never describe a sender with a DMARC pass as spoofed, forged, or impersonating",
			} {
				if !strings.Contains(system, want) {
					t.Errorf("system prompt lacks %q", want)
				}
			}
		})
	}
}

// TestClassify_LinkHostMismatchOmittedWhenFalse: the key is absent from
// the user turn unless herold determined the mismatch.
func TestClassify_LinkHostMismatchOmittedWhenFalse(t *testing.T) {
	_, user := runCapturing(t, "spam.classify", canonicalPayload("hello"))
	if strings.Contains(user, "link_host_mismatch") {
		t.Fatalf("user turn carries link_host_mismatch although unset: %s", user)
	}
}
