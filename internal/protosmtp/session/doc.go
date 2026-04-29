// Package session provides a reusable SMTP client session layer used by
// both the outbound MX/smart-host delivery path (internal/protosmtp) and the
// external-submission engine (internal/extsubmit).
//
// A Session wraps a single net.Conn and exposes typed methods for each
// phase of an outbound SMTP exchange: greeting, EHLO, STARTTLS upgrade,
// AUTH (PLAIN, LOGIN, XOAUTH2), MAIL FROM, RCPT TO, DATA, and QUIT.
//
// Session is not safe for concurrent use. One Session corresponds to one
// SMTP connection; callers create a new Session for each dial.
//
// The reply parser is exposed as a typed Reply struct so that callers can
// make routing decisions on numeric codes and enhanced status codes without
// string parsing, and so that a fuzz target can exercise the parser in
// isolation.
package session
