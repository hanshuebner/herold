package coach

import (
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// GCWindow is the retention window for coach_events rows. Rows older than
// this are eligible for deletion by the nightly GC tick in admin/server.go.
// The 90-day window matches the longest counter window the JMAP layer
// surfaces (keyboardCount90d / mouseCount90d per REQ-PROTO-110).
const GCWindow = 90 * 24 * time.Hour

// jmapID is the wire form of a JMAP id. ShortcutCoachStat ids are the
// action string itself (unique per principal; actions are free-form
// identifiers the suite chooses such as "archive" or "reply").
type jmapID = string

// coachIDFromJMAP returns the action string unchanged; the JMAP id IS
// the action. An empty string is invalid; callers surface "notFound".
func coachIDFromJMAP(id jmapID) (string, bool) {
	if id == "" {
		return "", false
	}
	return id, true
}

// jmapCoachStat is the wire-form ShortcutCoachStat object (the suite
// server-contract.md § Shortcut coach). Field names match the suite
// contract verbatim.
type jmapCoachStat struct {
	ID               jmapID  `json:"id"`
	Action           string  `json:"action"`
	KeyboardCount14d int     `json:"keyboardCount14d"`
	MouseCount14d    int     `json:"mouseCount14d"`
	KeyboardCount90d int     `json:"keyboardCount90d"`
	MouseCount90d    int     `json:"mouseCount90d"`
	LastKeyboardAt   *string `json:"lastKeyboardAt,omitempty"`
	LastMouseAt      *string `json:"lastMouseAt,omitempty"`
	DismissCount     int     `json:"dismissCount"`
	DismissUntil     *string `json:"dismissUntil,omitempty"`
}

// renderStat projects a store.CoachStat to the wire object. The JMAP
// id is the action string.
func renderStat(s store.CoachStat) jmapCoachStat {
	out := jmapCoachStat{
		ID:               s.Action,
		Action:           s.Action,
		KeyboardCount14d: s.KeyboardCount14d,
		MouseCount14d:    s.MouseCount14d,
		KeyboardCount90d: s.KeyboardCount90d,
		MouseCount90d:    s.MouseCount90d,
		DismissCount:     s.DismissCount,
	}
	if s.LastKeyboardAt != nil {
		v := utcDate(*s.LastKeyboardAt)
		out.LastKeyboardAt = &v
	}
	if s.LastMouseAt != nil {
		v := utcDate(*s.LastMouseAt)
		out.LastMouseAt = &v
	}
	if s.DismissUntil != nil {
		v := utcDate(*s.DismissUntil)
		out.DismissUntil = &v
	}
	return out
}
