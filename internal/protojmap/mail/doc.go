// Package mail wires the JMAP Mail (RFC 8621) datatype handlers into the
// shared CapabilityRegistry owned by the protojmap Core dispatcher.
//
// The Wave 2.2 surface owned by this package and its sub-packages is the
// two largest types:
//
//   - internal/protojmap/mail/mailbox — Mailbox/get|changes|query|queryChanges|set
//   - internal/protojmap/mail/email   — Email/get|changes|query|queryChanges|set|copy|import|parse
//
// Smaller Mail types (EmailSubmission, Identity, Thread, SearchSnippet,
// VacationResponse) ship from a parallel agent in a sibling sub-package.
//
// Each sub-package exposes its own Register; this top-level package's
// Register is a convenience that calls every sub-package's Register in
// turn so the boot path in internal/admin/server.go has a single hook.
package mail
