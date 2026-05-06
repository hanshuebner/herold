package admin

import "github.com/hanshuebner/herold/internal/store"

// ftsOverride wraps a store.Store so FTS() returns a caller-supplied
// implementation instead of the backend's default. Used to swap the
// per-backend substring stub for a storefts.Composite that routes
// queries to the Bleve index while keeping the backend's SQL-bound
// change-feed read intact.
type ftsOverride struct {
	store.Store
	fts store.FTS
}

func (o ftsOverride) FTS() store.FTS { return o.fts }
