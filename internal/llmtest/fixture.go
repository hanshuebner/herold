package llmtest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// FixtureKind identifies the subsystem that produced or consumes a
// fixture. The value is embedded in every fixture line and is used as
// the first component of the lookup key.
type FixtureKind string

const (
	// KindCategorise is used by internal/categorise.
	KindCategorise FixtureKind = "categorise"
	// KindSpamClassify is used by internal/spam.
	KindSpamClassify FixtureKind = "spam-classify"
)

// FixtureEntry is the on-disk JSON record for a single LLM call. The
// schema version v=1 is stable; any incompatible change bumps v.
type FixtureEntry struct {
	// V is the schema version. Currently 1.
	V int `json:"v"`
	// Kind identifies the subsystem (categorise | spam-classify).
	Kind FixtureKind `json:"kind"`
	// PromptHash is the SHA-256 hex of the full prompt text. It is the
	// lookup key. A prompt change invalidates the fixture intentionally
	// (REQ-FILT-301).
	PromptHash string `json:"prompt_hash"`
	// Prompt is the full prompt stored for human debugging only. It is
	// never used as a lookup key.
	Prompt string `json:"prompt"`
	// Response is the raw JSON object the LLM returned.
	Response json.RawMessage `json:"response"`
	// Model is the model identifier reported by the LLM endpoint.
	Model string `json:"model"`
	// CapturedAt is the UTC time the fixture was recorded.
	CapturedAt time.Time `json:"captured_at"`
}

// FixtureKey is the (kind, prompt_hash) pair used for map lookups.
type FixtureKey struct {
	Kind FixtureKind
	Hash string
}

// HashPrompt returns the hex-encoded SHA-256 of the prompt string.
func HashPrompt(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return fmt.Sprintf("%x", sum)
}
