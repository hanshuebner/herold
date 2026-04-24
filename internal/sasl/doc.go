// Package sasl implements the SASL mechanisms Herold offers on SMTP
// submission, IMAP LOGIN, and HTTP Basic admin paths.
//
// Wave-1 mechanisms: PLAIN, LOGIN, SCRAM-SHA-256 (and SCRAM-SHA-256-PLUS
// with tls-server-end-point channel binding, RFC 5929), OAUTHBEARER
// (RFC 7628), and XOAUTH2 (Google/Microsoft compat). The package is a
// pure library: no I/O, no network state, no imports beyond stdlib and
// golang.org/x/crypto. Callers drive the state machine by calling Start
// once with the client's initial response (possibly empty) and then
// Next for each subsequent client line; each call returns a server
// challenge, a done flag, and an optional error.
//
// Plain-text mechanisms (PLAIN, LOGIN, OAUTHBEARER, XOAUTH2) refuse to
// Start unless the caller marks the underlying transport as TLS via
// WithTLS on ctx. SCRAM is safe in cleartext but PLUS variants require
// a non-empty channel-binding value.
//
// Ownership: directory-auth-implementor.
package sasl
