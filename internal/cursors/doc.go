// Package cursors provides shared cursor-persistence helpers for
// change-feed workers.
//
// Workers that maintain an in-memory cursor advance it every tick and
// periodically write it to the durable store. On shutdown the last
// in-memory value must reach disk even when the run context is already
// cancelled. Use ShutdownFlusher rather than rolling your own flush: it
// uses a fresh context, applies a sensible deadline, and centralises the
// log key names so future audits read one place.
//
// Rule: workers with an in-memory change-feed cursor flush on shutdown
// via cursors.ShutdownFlusher. Do not roll your own.
package cursors
