// Package protoimap implements the IMAP4rev2 / rev1 server per
// docs/design/server/requirements/01-protocols.md §IMAP.
//
// Phase 1 scope (REQ-PROTO-20..31): LOGIN, AUTHENTICATE, LIST, LSUB,
// SELECT/EXAMINE, FETCH (envelope, flags, body sections, RFC822.SIZE, UID,
// BODYSTRUCTURE conservative), STORE, APPEND (UIDPLUS), EXPUNGE (UID
// EXPUNGE), IDLE, basic SEARCH + ESEARCH, STATUS, NAMESPACE, ID, ENABLE.
// Phase 2 adds CONDSTORE/QRESYNC, MOVE, ACL, NOTIFY.
//
// Ownership: imap-implementor.
//
// Implementation note: per docs/design/server/notes/spike-imap-library.md we take the
// "middle path" — depend on github.com/emersion/go-imap/v2 (top-level
// types only: AST, capability tokens, NumSet, SearchCriteria, FetchOptions,
// StoreFlags, Envelope, Flag) and write our own session + parser +
// formatter on top. We do not import v2/imapserver. The parser/formatter
// in this package is hand-written because emersion's wire layer lives in
// an internal/ path not exposed for reuse.
package protoimap
