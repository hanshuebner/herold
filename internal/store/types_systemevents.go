package store

import "time"

// This file declares the system-events ring-buffer entity (REQ-ADM-304).
//
// System-initiated operational events (SMTP acceptance, recipient resolution,
// inbound receipt, webhook dispatch outcomes) are recorded in this bounded
// table, separate from the audit log. The audit log is the security/compliance
// record of actor-initiated actions; system events are operational telemetry.
//
// The table is bounded by row-count and age, both configured via
// SystemEventEvictOptions. Oldest rows are evicted by a periodic sweeper or
// by inline calls from AppendSystemEvent callers. The table is excluded from
// herold diag backup by default (REQ-ADM-304).

// SystemEventID is the autoincrement primary key of a system_events row.
type SystemEventID int64

// SystemEvent is one row in the system_events ring-buffer table (REQ-ADM-304).
//
// The Domain field enables server-side scope filtering per REQ-ADM-307:
// super-admins see all events; domain-scoped operators see only events whose
// Domain matches one of their managed domains. Callers populate Domain from
// the recipient address or other per-message context when available; events
// without a clear domain association leave Domain empty (visible only to
// super-admins).
type SystemEvent struct {
	// ID is the autoincrement primary key; populated by the store on insert.
	ID SystemEventID
	// At is the event instant (UTC). Callers supply this via their clock;
	// the store persists it verbatim so tests with a FakeClock are
	// deterministic.
	At time.Time
	// Action is the dotted action identifier, e.g. "smtp.accept",
	// "smtp.rcpt.resolve", "ses_inbound_received". Same namespace as the
	// audit log action field.
	Action string
	// ActorID identifies the subsystem that emitted the event: "smtp",
	// "ses_inbound", "system", etc.
	ActorID string
	// Subject names the entity acted upon, e.g. "message:<hash>",
	// "rcpt:<addr>", "webhook:<id>".
	Subject string
	// RemoteAddr is the originating IP for network events; empty otherwise.
	RemoteAddr string
	// Outcome is OutcomeSuccess or OutcomeFailure.
	Outcome AuditOutcome
	// Message is the human-readable summary.
	Message string
	// Domain is the managed-domain key for REQ-ADM-307 scope filtering.
	// Derived from the recipient address where applicable; empty when no
	// per-domain context is available.
	Domain string
	// Metadata carries typed key/value tags. The store serialises the map
	// deterministically (sorted keys).
	Metadata map[string]string
}

// SystemEventFilter narrows a ListSystemEvents read. Unset (zero) fields
// are treated as "no constraint". Limit is capped at 1000 server-side.
type SystemEventFilter struct {
	// Action, when non-empty, matches the Action column exactly.
	Action string
	// ActorID, when non-empty, matches the ActorID column exactly.
	ActorID string
	// Domains, when non-nil, restricts results to events whose Domain
	// matches any entry in the slice. An empty (non-nil) slice returns
	// nothing (fail-closed per REQ-ADM-307).
	Domains []string
	// Since, when non-zero, excludes rows with At < Since.
	Since time.Time
	// Until, when non-zero, excludes rows with At >= Until.
	Until time.Time
	// Limit caps the returned slice length. 0 means the default (100).
	// Values above 1000 are silently lowered to 1000.
	Limit int
	// BeforeID is the keyset cursor: return rows with ID < BeforeID.
	BeforeID SystemEventID
}

// SystemEventEvictOptions controls one eviction pass.
type SystemEventEvictOptions struct {
	// CapRows is the maximum number of rows to retain. Rows with IDs below
	// (max_id - CapRows) are deleted.
	CapRows int
	// MaxAge is the maximum row age; rows older than now-MaxAge are deleted.
	MaxAge time.Duration
	// BatchSize limits how many rows are deleted in one statement. Default 1000.
	BatchSize int
}
