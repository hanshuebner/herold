package store

import "time"

// This file declares the client-log ring-buffer entity
// (REQ-OPS-206, REQ-OPS-206a, REQ-OPS-219).
//
// Client events (browser errors, console logs, Web Vitals) arrive at
// POST /api/v1/clientlog and POST /api/v1/clientlog/public.  After
// validation, sanitisation, and enrichment the handler stores one
// ClientLogRow per event in the ring-buffer table so an operator with
// no external collector can still inspect recent client activity from
// the admin UI.
//
// Two logical slices share the same table: "auth" (authenticated events
// from Suite and Admin SPA sessions) and "public" (pre-auth / anonymous
// events from the public endpoint).  Each slice is bounded independently
// by row count and age; see ClientLogEvictOptions.

// ClientLogSlice is one of the two ring-buffer partitions.
type ClientLogSlice string

const (
	// ClientLogSliceAuth holds events from authenticated sessions
	// (cookie-scoped to the originating listener).
	ClientLogSliceAuth ClientLogSlice = "auth"
	// ClientLogSlicePublic holds anonymous events from
	// POST /api/v1/clientlog/public.  Content is fully
	// attacker-controlled; admin-viewer rendering is text-only.
	ClientLogSlicePublic ClientLogSlice = "public"
)

// ClientLogRow is one event as stored in the ring-buffer table.
// Every field that is nullable in the SQL schema is a pointer here so
// zero and absent are distinguished (important for user_id and
// request_id which have semantic meaning when absent).
type ClientLogRow struct {
	// ID is the autoincrement primary key.  Monotonically increasing;
	// used as the pagination cursor component.
	ID int64
	// Slice is "auth" or "public".
	Slice ClientLogSlice
	// ServerTS is the server-side arrival time (UTC).
	ServerTS time.Time
	// ClientTS is the browser wall-clock time as reported in the event.
	ClientTS time.Time
	// ClockSkewMS is the signed difference server_ts_ms - client_ts_ms.
	ClockSkewMS int64
	// App is "suite" or "admin".
	App string
	// Kind is "error", "log", or "vital".
	Kind string
	// Level is "trace", "debug", "info", "warn", or "error".
	Level string
	// UserID is non-nil for auth-slice rows; nil for public-slice rows.
	UserID *string
	// SessionID is nil for public-slice rows and for auth rows where
	// the SPA did not supply one.
	SessionID *string
	// PageID is the per-page-load UUID from the event.
	PageID string
	// RequestID is the correlated X-Request-Id, when present.
	RequestID *string
	// Route is the SPA route at the time of the event.
	Route *string
	// BuildSHA is the SPA build identifier.
	BuildSHA string
	// UA is the User-Agent header, capped at 256 chars.
	UA string
	// Msg is the human-readable event summary.
	Msg string
	// Stack is the raw unsymbolicated stack trace; nil for non-error events.
	Stack *string
	// PayloadJSON is the full enriched event record for admin replay.
	PayloadJSON string
}

// ClientLogFilter narrows ListByCursor and ListByRequestID results.
// All fields are optional; unset fields do not constrain the query.
type ClientLogFilter struct {
	// Slice, when non-empty, limits results to that slice.
	Slice ClientLogSlice
	// App limits to a specific SPA ("suite" or "admin").
	App string
	// Kind limits to a specific event kind ("error", "log", "vital").
	Kind string
	// Level limits to a specific level.
	Level string
	// Since, when non-zero, excludes rows with server_ts before this time.
	Since time.Time
	// Until, when non-zero, excludes rows with server_ts after this time.
	Until time.Time
	// UserID limits to rows for this user (auth slice).
	UserID string
	// SessionID limits to rows for this session.
	SessionID string
	// RequestID limits to rows correlated with this request.
	RequestID string
	// Route limits to rows from this SPA route.
	Route string
	// MsgSubstring, when non-empty, restricts to rows whose msg or
	// stack contains this substring (case-sensitive LIKE match).
	MsgSubstring string
}

// ClientLogCursorOptions controls a paginated ListByCursor call.
type ClientLogCursorOptions struct {
	// Filter narrows the result set.
	Filter ClientLogFilter
	// Cursor is the opaque pagination token returned by a prior call.
	// Empty string starts from the newest row.
	Cursor string
	// Limit is the maximum number of rows to return.  A value <= 0
	// is treated as the package-default (100).
	Limit int
}

// ClientLogEvictOptions controls one eviction pass for a single slice.
type ClientLogEvictOptions struct {
	// Slice is the partition to evict from.
	Slice ClientLogSlice
	// CapRows is the maximum number of rows to retain.  Rows with ids
	// below (max_id - CapRows) are deleted.
	CapRows int
	// MaxAge is the maximum row age; rows older than now-MaxAge are
	// deleted.
	MaxAge time.Duration
	// BatchSize limits how many rows are deleted in a single statement.
	// Default 1000.
	BatchSize int
}
