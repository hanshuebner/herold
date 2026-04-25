// Package snooze implements the JMAP snooze extension wake-up worker
// (REQ-PROTO-49). One goroutine polls Metadata.ListDueSnoozedMessages
// at a configurable cadence and clears the snoozed-until column /
// "$snoozed" keyword pair atomically through Metadata.SetSnooze.
//
// The worker enforces only the wake side of the snooze contract; the
// JMAP and IMAP handlers maintain the (SnoozedUntil != nil) iff
// (Keywords contains "$snoozed") atomicity invariant on the set side.
//
// Lifecycle: construct with NewWorker; call Run(ctx) in a single
// goroutine owned by the server lifecycle (admin/server.go's
// errgroup); cancel ctx to drain and return.
package snooze
