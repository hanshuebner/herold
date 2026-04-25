// Package categorise implements server-side LLM categorisation of
// inbound mail (REQ-FILT-200..231). It runs after the spam classifier
// and Sieve filters, only on messages destined for the principal's
// inbox, and labels each message with at most one "$category-<name>"
// keyword chosen from the per-account configured set.
//
// Operationally separate from the spam classifier (REQ-FILT-213): we
// call an OpenAI-compatible chat-completions endpoint directly from
// the server rather than dispatch through a plugin. This keeps the
// per-account knobs (prompt, category set, endpoint override) close
// to the store row that owns them and removes one round-trip from the
// delivery hot path; operators may promote the call into a plugin
// later by writing one against the same configured contract.
//
// Failure isolation: a categoriser error never blocks delivery
// (REQ-FILT-230). The package logs at warn and returns an empty
// category name; the caller leaves the message uncategorised.
package categorise
