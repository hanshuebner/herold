// Package coach implements the JMAP ShortcutCoachStat datatype
// (REQ-PROTO-110..112 / tabard 23-shortcut-coach.md).
//
// Capability URI: https://tabard.dev/jmap/shortcut-coach
//
// One stat row per (principal, action) pair; counters are derived at
// read time from a per-row event log (coach_events). Privacy:
// a principal can only read/write their own rows; admin/operator reads
// are disallowed (REQ-PROTO-112 / REQ-COACH-04).
package coach
