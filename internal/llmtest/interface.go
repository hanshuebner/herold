package llmtest

import (
	"context"
	"encoding/json"
)

// ChatCompleter is the LLM client interface used by internal/categorise.
// The Categoriser calls Complete with a full system prompt and a
// serialised user payload; the implementation returns the assistant's
// text content from the first choice.
//
// Production code constructs an HTTPChatCompleter (see http.go) that
// POSTs to an OpenAI-compatible endpoint. Tests substitute a Replayer.
type ChatCompleter interface {
	// Complete sends the supplied system prompt and user content to the
	// LLM and returns the assistant's raw text reply from the first
	// choice. The prompt string is used as the fixture lookup key via
	// its SHA-256 hash (PromptHash). ctx carries the call deadline.
	Complete(ctx context.Context, prompt, userContent string) (string, error)
}

// SpamInvoker is the LLM plugin-invoker interface used by
// internal/spam. It mirrors spam.PluginInvoker exactly so the llmtest
// Replayer can be passed directly to spam.New without an adapter.
type SpamInvoker interface {
	// Call dispatches a JSON-RPC request to the named plugin method.
	// params is marshalled as the request params; result is populated
	// from the response.
	Call(ctx context.Context, plugin, method string, params any, result any) error
}

// spamKey builds the lookup key for a spam-classify fixture. The
// key is the JSON-serialised params so that any change in the request
// shape invalidates the fixture.
func spamKey(params any) (string, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	return HashPrompt(string(b)), nil
}
