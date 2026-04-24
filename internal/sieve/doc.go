// Package sieve parses, interprets, and sandboxes Sieve scripts per
// RFC 5228 plus the extension set listed in docs/requirements/01-protocols.md
// §Sieve (REQ-PROTO-60..68).
//
// The package is organised as three passes over a script:
//
//  1. Parse (grammar.go, parse.go) turns source bytes into an AST
//     (Script, Command, Test, Argument). The parser is a hand-written
//     recursive-descent; no parser generator is used.
//  2. Validate (validate.go) enforces the RFC 5228 semantic rules that are
//     not shape-level: "require" declarations must precede use, extensions
//     must be on our known list, argument types must match, nesting depth
//     is bounded.
//  3. Interpret (interp.go) evaluates the AST against a mailparse.Message
//     plus an Environment and returns an Outcome describing what the script
//     decided (keep, discard, fileinto, redirect, reject, vacation, flag
//     mutations, mailboxid override).
//
// Sandboxing (sandbox.go) applies bounds during interpretation: no
// filesystem or network I/O (the interpreter has no primitives for either),
// a bounded instruction counter, variable count cap, and per-value length
// caps. Every bound is exercised by a dedicated test.
//
// Extension support in Phase 1:
//
//   - Shipped in Phase 1: RFC 5228 base, fileinto (5228), reject (5429),
//     envelope (5228), imap4flags (5232), body (5173) — text matching against
//     decoded text parts, vacation (5230) + vacation-seconds (6131),
//     variables (5229), relational (5231), subaddress (5233), regex (de
//     facto), copy (3894), include (6609, inline only — no filesystem
//     includes), date (5260) with :zone, mailbox (5490), mailboxid (9042),
//     encoded-character (5228), editheader (5293), duplicate (7352),
//     spamtest/spamtestplus (5235), extlists (6134, :list placeholder),
//     enotify (5435) mailto-only.
//   - Deferred to Phase 1.5 with a parse-and-stub execution path
//     (recognised by the parser and validator, interpreter records intent
//     but does not fully evaluate): foreverypart (5703), mime (5703).
//
// ManageSieve (RFC 5804) lives in internal/protomanagesieve; this package
// is intentionally transport-agnostic.
//
// AuthResults seam: the Sieve spamtest/spamtestplus mapping reads a small
// subset of the final mail authentication verdict. Because the concrete
// mailauth.AuthResults type lands in a parallel wave, this package defines
// the minimum interface it needs locally (authresults.go). The production
// mailauth.AuthResults satisfies the interface by shape.
//
// Ownership: sieve-implementor.
package sieve
