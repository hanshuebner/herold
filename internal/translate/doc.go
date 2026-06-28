// Package translate provides a typed HTTP client for proxying translation
// requests to configured third-party backend APIs. It abstracts over
// provider differences (MyMemory and DeepL) behind a single Translate
// method, enforces input size limits, and surfaces provider errors as
// typed sentinel values so callers can distinguish "feature disabled"
// from "upstream failure" without parsing error strings.
package translate
