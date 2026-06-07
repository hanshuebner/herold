// Package imapimport implements the inbound IMAP live-mirror feature:
// herold acting as an IMAP client that mirrors one or more upstream
// accounts into a principal's herold mailboxes. It is the client
// counterpart to internal/protoimap (the server).
//
// The top-level type is Pool, a per-instance supervisor that starts
// one accountWorker goroutine per enabled IMAPImportAccount, bounded
// by cfg.ConcurrentAccounts.
//
// Design: docs/design/server/architecture/12-imap-import.md
// Requirements: docs/design/server/requirements/19-imap-import.md
//
// Seam summary:
//   - Dialer: abstracts establishing an authenticated IMAP connection.
//     The production implementation is in dialer.go and imports
//     imapclient. Tests inject a fakeDialer backed by an in-process
//     imapserver started in testserver_test.go.
//   - Conn: the subset of imapclient operations the worker needs.
//     Currently only Capabilities and Close/Logout for 3a;
//     sub-steps 3b-3e will extend the interface.
//   - oauthTokenSource: abstracts the HTTP refresh-token exchange so
//     tests can inject a fake token endpoint.
//   - Categoriser: seam for the optional LLM categorisation step (3c).
//     Defined but unused in 3a.
//
// Security properties maintained across all sub-steps:
//   - Credentials are opened by the accountWorker immediately before
//     connect and never retained longer than the auth exchange.
//     (REQ-IMAP-IMP-70 / decision 2.)
//   - Tokens and auth payloads are never logged. (REQ-IMAP-IMP-71.)
//   - tls_mode = none is rejected both at config-time and here
//     defensively. The connection is always TLS. (REQ-IMAP-IMP-06.)
package imapimport
