// Package spam builds the spam-classifier prompt and invokes the
// spam-classifier plugin. The plugin performs the actual model call
// (Ollama, llama.cpp, cloud API, or a custom deterministic plugin for
// tests); this package is pure prompt-construction plus one RPC
// invocation.
//
// Design:
//
//   - Classify builds a JSON payload from a mailparse.Message plus an
//     AuthResultsReader (defined locally, see authresults.go for the
//     "interface at the consumer" rationale).
//   - The payload is sent via a PluginInvoker — a small interface that
//     internal/plugin.*Manager satisfies naturally — under the method
//     name "spam.classify".
//   - The plugin returns a verdict ("ham" | "spam" | "unclassified") and a
//     [0,1] confidence score. Parse / timeout / RPC failures all collapse
//     to Classification{Verdict: Unclassified} plus a non-nil error; the
//     delivery path treats Unclassified as not spam per REQ-FILT default.
//
// This package does not know about LLM endpoints, model names, or prompt
// templates beyond the canonical JSON schema. The plugin owns the model-
// specific details per REQ-FILT-13.
//
// Ownership: sieve-implementor.
package spam
