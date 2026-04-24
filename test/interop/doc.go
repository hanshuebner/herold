// Package interop hosts cross-package interop and scenario tests that run
// against a full server spun up from internal/testharness. Conformance
// suites (imaptest, Pigeonhole, DKIM/DMARC/ARC vectors, scripted SMTP vs
// Postfix/Exim in Docker) are wired in here.
//
// Ownership: conformance-fuzz-engineer.
package interop
