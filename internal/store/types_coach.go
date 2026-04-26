package store

import "time"

// This file declares the Phase 3 Wave 3.10 types backing the JMAP
// ShortcutCoachStat datatype (REQ-PROTO-110..112). Schema commentary
// lives in storesqlite/migrations/0020_coach.sql; this is the Go
// companion.
//
// Storage strategy. Event data lives in coach_events (one row per
// flush batch-entry; aggregated at read time). Dismiss data lives in
// coach_dismiss (upserted directly; not windowed). The JMAP stat
// object is assembled by joining the two tables.

// CoachInputMethod identifies the invocation device for one event.
// The set is closed per the CHECK constraint in 0020_coach.sql.
type CoachInputMethod string

const (
	// CoachInputMethodKeyboard records a keyboard-shortcut invocation.
	CoachInputMethodKeyboard CoachInputMethod = "keyboard"
	// CoachInputMethodMouse records a mouse / pointer invocation.
	CoachInputMethodMouse CoachInputMethod = "mouse"
)

// CoachEvent is one row in the coach_events table: a batch of
// invocations of action by method at occurred_at. tabard typically
// batches multiple invocations from its 60-second ring-buffer flush
// into a single event_count > 1 record.
type CoachEvent struct {
	// ID is the server-assigned primary key.
	ID int64
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Action is the free-form action identifier (e.g. "archive").
	Action string
	// Method is either keyboard or mouse.
	Method CoachInputMethod
	// Count is the number of invocations represented by this row.
	Count int
	// OccurredAt is the client-supplied event time (UTC).
	OccurredAt time.Time
	// RecordedAt is the server-assigned receipt time (UTC).
	RecordedAt time.Time
}

// CoachDismiss is one row in the coach_dismiss table: per-principal,
// per-action dismissal bookkeeping. Not windowed; counters accumulate.
type CoachDismiss struct {
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Action is the free-form action identifier.
	Action string
	// DismissCount is the total number of hint dismissals for this action.
	DismissCount int
	// DismissUntil is the optional suppression deadline (nil = no suppression).
	DismissUntil *time.Time
	// UpdatedAt is the last mutation timestamp.
	UpdatedAt time.Time
}

// CoachStat is the assembled per-(principal, action) stat object that
// the JMAP layer surfaces as a ShortcutCoachStat. The windowed counters
// are computed by the store at read time from the event log.
type CoachStat struct {
	// Action is the free-form action identifier. Used as the JMAP id
	// since it is unique per principal.
	Action string
	// KeyboardCount14d is the sum of keyboard event_counts in the
	// trailing 14-day window ending at the query's now time.
	KeyboardCount14d int
	// MouseCount14d is the sum of mouse event_counts in the trailing
	// 14-day window.
	MouseCount14d int
	// KeyboardCount90d is the sum of keyboard event_counts in the
	// trailing 90-day window.
	KeyboardCount90d int
	// MouseCount90d is the sum of mouse event_counts in the trailing
	// 90-day window.
	MouseCount90d int
	// LastKeyboardAt is the maximum occurred_at among keyboard events,
	// nil when no keyboard events exist in the 90-day window.
	LastKeyboardAt *time.Time
	// LastMouseAt is the maximum occurred_at among mouse events,
	// nil when no mouse events exist in the 90-day window.
	LastMouseAt *time.Time
	// DismissCount is the total hint-dismiss count (not windowed).
	DismissCount int
	// DismissUntil is the optional suppression deadline, nil when not set.
	DismissUntil *time.Time
}
