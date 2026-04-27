// CONDSTORE / QRESYNC mode state on the IMAP session (RFC 7162).
//
// This file owns the session-level toggles for the two extensions and the
// helpers that adjust FETCH / STORE / SEARCH / STATUS output when the
// extensions are active. The parser additions for FETCH CHANGEDSINCE,
// STORE UNCHANGEDSINCE, and SEARCH MODSEQ live in parser_store_search.go
// and parser_fetch.go; this file is the runtime side.
//
// QRESYNC (qresync.go) implies CONDSTORE — both flags are flipped when the
// client issues `ENABLE QRESYNC` or `SELECT mbox (QRESYNC ...)`. Once
// CONDSTORE is on a session it stays on until the session ends; clients
// have no out-of-band way to disable it.
//
// MODSEQ retention strategy. RFC 7162 §3.1.6 requires the server to remember
// expunged UIDs and their expunge-modseq long enough for QRESYNC clients to
// resync without a UIDVALIDITY bump. Phase 2 Wave 2.2 lands the runtime
// surface but defers the persistent expunged_messages table to a follow-up
// (a 24h retention table coordinated with the storage migration cadence).
// Until that lands, qresync.go falls back to a hard-cutoff: clients more
// than 24h behind their last-seen ModSeq receive a UIDVALIDITY bump and
// resync from scratch. The cutoff is deterministic against the injected
// Clock so tests are reproducible.

package protoimap

import (
	"github.com/hanshuebner/herold/internal/store"
)

// condstoreState carries the per-session CONDSTORE / QRESYNC toggles plus
// the QRESYNC parameters captured at SELECT time. The zero value means
// "neither extension active".
type condstoreState struct {
	// condstoreEnabled is set by ENABLE CONDSTORE or any SELECT that
	// names CONDSTORE / QRESYNC, or any FETCH / STORE / SEARCH that uses
	// MODSEQ — RFC 7162 §3.1 promotes the session implicitly on first
	// use of a MODSEQ-bearing operation.
	condstoreEnabled bool
	// qresyncEnabled is set by ENABLE QRESYNC or SELECT-with-QRESYNC.
	// QRESYNC implies CONDSTORE; both bits travel together.
	qresyncEnabled bool
	// selectedHighestModSeq is the mailbox HighestModSeq captured at
	// SELECT time. It seeds the OK [HIGHESTMODSEQ ...] response and is
	// the boundary against which CHANGEDSINCE / UNCHANGEDSINCE are
	// initially compared (subsequent mutations advance the mailbox row,
	// not this seed).
	selectedHighestModSeq store.ModSeq
}

// enableCondstore promotes the session into CONDSTORE mode. Idempotent.
// Called from the ENABLE handler and from any FETCH / STORE / SEARCH path
// that observes a MODSEQ-bearing token, per RFC 7162 §3.1's "implicit
// promote on first MODSEQ use".
func (ses *session) enableCondstore() {
	ses.selMu.Lock()
	defer ses.selMu.Unlock()
	ses.cs.condstoreEnabled = true
}

// enableQresync promotes the session into QRESYNC (and CONDSTORE) mode.
// Per RFC 7162 §3.1, QRESYNC strictly implies CONDSTORE — flipping one
// flips the other.
func (ses *session) enableQresync() {
	ses.selMu.Lock()
	defer ses.selMu.Unlock()
	ses.cs.condstoreEnabled = true
	ses.cs.qresyncEnabled = true
}

// condstoreActive reports whether the session has been promoted to
// CONDSTORE mode (directly or via QRESYNC).
func (ses *session) condstoreActive() bool {
	ses.selMu.Lock()
	defer ses.selMu.Unlock()
	return ses.cs.condstoreEnabled
}

// qresyncActive reports whether QRESYNC is on the session. Implies
// condstoreActive().
func (ses *session) qresyncActive() bool {
	ses.selMu.Lock()
	defer ses.selMu.Unlock()
	return ses.cs.qresyncEnabled
}
