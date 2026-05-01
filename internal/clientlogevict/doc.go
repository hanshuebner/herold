// Package clientlogevict implements the background eviction goroutine for the
// clientlog ring-buffer table (REQ-OPS-206, REQ-OPS-219).
//
// The eviction goroutine wakes every TickInterval (default 60 s), runs one
// eviction pass per slice ("auth" and "public"), and deletes rows that are
// older than the configured age limit or that exceed the configured row-count
// cap.  Each pass is bounded by BatchSize to avoid long-held write locks on
// SQLite.
//
// Usage:
//
//	ev := &clientlogevict.Evictor{Store: store.Meta(), Logger: logger}
//	g.Go(func() error { return ev.Run(ctx) })
//
// The Evictor blocks until ctx is cancelled and returns nil on clean shutdown.
// All configuration fields have documented defaults so a zero-value Evictor
// with only Store set is usable.
package clientlogevict
