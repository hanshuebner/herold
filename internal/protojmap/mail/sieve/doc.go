// Package sieve implements the JMAP Sieve datatype (RFC 9007), the
// urn:ietf:params:jmap:sieve capability that wraps Herold's existing
// Sieve interpreter and per-principal script storage so JMAP clients
// can read, write, and validate scripts without speaking ManageSieve
// (REQ-PROTO-50..53).
//
// One-script-per-principal: Phase 1's ManageSieve surface stores a
// single active Sieve script per principal (REQ-PROTO-51); the JMAP
// datatype keeps the same model. The wire-form Sieve/get response is
// therefore always a singleton list — when a script is on file the
// list contains one entry whose isActive is true; when no script is
// on file the list is empty. The id surfaced to JMAP is the principal
// id stringified, since there is exactly one row.
//
// Sieve/set accepts create / update / destroy operations. Create and
// update reference the script body via blobId — the client uploads
// the source through POST /jmap/upload first and then passes the
// returned blobId here. The handler parses + validates the body via
// internal/sieve.Parse and Validate (the same parser the runtime
// uses, per REQ-PROTO-51), persists the active text via
// store.Metadata.SetSieveScript, and bumps the principal's
// JMAPStateKindSieve counter.
//
// Sieve/validate is the parse-only sibling: it runs the same parser
// + validator pass without persisting anything. The suite's compose UI
// uses /validate for syntax-check-as-you-type.
package sieve
