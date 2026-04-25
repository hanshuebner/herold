// Package protomanagesieve implements the ManageSieve listener (RFC 5804)
// on 4190/tcp.
//
// Phase 2 Wave 2.2 scope (REQ-PROTO-50..52):
//
//   - Listener with mandatory STARTTLS before any non-STARTTLS /
//     CAPABILITY / LOGOUT command (RFC 5804 §1.5).
//   - SASL via internal/sasl (PLAIN, LOGIN, SCRAM-SHA-256,
//     SCRAM-SHA-256-PLUS over TLS, OAUTHBEARER, XOAUTH2).
//   - Commands: STARTTLS, AUTHENTICATE, LOGOUT, CAPABILITY,
//     HAVESPACE, PUTSCRIPT, LISTSCRIPTS, SETACTIVE, GETSCRIPT,
//     DELETESCRIPT, RENAMESCRIPT, CHECKSCRIPT, NOOP.
//   - Script validation uses internal/sieve.Parse + Validate so the
//     "accepted" and "runnable" bars are the same parser
//     (REQ-PROTO-51).
//   - SIEVE capability list pulled dynamically from
//     sieve.SupportedExtensions; new extensions advertise
//     automatically as the interpreter learns them.
//
// Phase-2 limitation, documented for operator visibility: the metadata
// store keeps one Sieve script per principal (store.SetSieveScript /
// GetSieveScript). RFC 5804 LISTSCRIPTS / SETACTIVE / RENAMESCRIPT
// permit multiple named scripts; this server presents a single
// implicit slot named "active". SETACTIVE is a no-op when the named
// script is already the only one, and RENAMESCRIPT on the active slot
// returns OK without changing storage. Multi-script support lands in
// a follow-up wave alongside a schema change.
//
// Ownership: sieve-implementor (this listener), shared with the
// sieve-implementor for the script parser (internal/sieve).
package protomanagesieve
