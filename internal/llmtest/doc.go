// Package llmtest provides deterministic test doubles for LLM calls
// made by internal/categorise and internal/spam.
//
// Two implementations are provided:
//
//   - Recorder wraps a real LLM client; on every call it forwards the
//     request to the upstream endpoint and appends a fixture line to a
//     JSONL file. Used only by the capture script (scripts/llm-capture.sh);
//     never exercised in CI.
//
//   - Replayer loads a previously recorded fixture file and serves
//     responses from its in-memory map keyed by (kind, prompt_hash).
//     Unknown keys return ErrFixtureMissing. This is the default for CI.
//
// Fixture file format: line-delimited JSON at
// internal/llmtest/fixtures/<kind>/<test-package>.jsonl. Each line is a
// FixtureEntry with schema version v=1.
//
// Usage in a test:
//
//	client := llmtest.LoadReplayer(t, "categorise")
//	// pass client to categorise.Options.LLMClient
//
// When HEROLD_LLM_CAPTURE=1, callers should substitute a Recorder
// (see scripts/llm-capture.sh). The build tags llm_replay (default)
// and llm_capture control which implementation compiles.
//
// Ownership: plugin-platform-implementor.
package llmtest
