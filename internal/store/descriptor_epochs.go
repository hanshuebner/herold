package store

import "sync/atomic"

// DescriptorEpochs counts committed writes to the rows the JMAP session
// descriptor is assembled from (RFC 8620 §2, REQ-PROTO-43). protojmap
// caches the session-state digest per principal and per session and
// recomputes it when the matching counter has moved, so a POST /jmap
// response carries a current sessionState without re-reading those rows
// on every request.
//
// Accounts covers what decides a principal's accounts and per-principal
// capabilities: principal rows (names, deletion, sub-principals), mailbox
// grants and mailbox deletion (ACL-visible foreign accounts, REQ-PROTO-33,
// REQ-AC-50), and apps with their principal sets (REQ-APP-13, REQ-APP-50).
// Sessions covers the clientlog columns of session rows (REQ-OPS-211).
//
// Backends bump a counter after the write's transaction has returned, so
// a reader that loads the counter before reading the rows either sees the
// new rows or records a counter value that is already stale. The counters
// live in process memory: they cover writers inside this server process.
//
// The zero value is ready to use; a nil *DescriptorEpochs ignores bumps
// and reports zero.
type DescriptorEpochs struct {
	accounts atomic.Uint64
	sessions atomic.Uint64
}

// Accounts returns the current accounts counter.
func (e *DescriptorEpochs) Accounts() uint64 {
	if e == nil {
		return 0
	}
	return e.accounts.Load()
}

// Sessions returns the current sessions counter.
func (e *DescriptorEpochs) Sessions() uint64 {
	if e == nil {
		return 0
	}
	return e.sessions.Load()
}

// BumpAccounts records a write to principal, grant, mailbox-deletion or
// app rows.
func (e *DescriptorEpochs) BumpAccounts() {
	if e != nil {
		e.accounts.Add(1)
	}
}

// BumpSessions records a write to the clientlog columns of a session row.
func (e *DescriptorEpochs) BumpSessions() {
	if e != nil {
		e.sessions.Add(1)
	}
}

// DescriptorEpochSource is implemented by the Metadata of backends that
// maintain DescriptorEpochs. Callers type-assert Store.Meta() against it;
// a Metadata without it gives callers no change signal, so they read the
// rows on every request.
type DescriptorEpochSource interface {
	DescriptorEpochs() *DescriptorEpochs
}
