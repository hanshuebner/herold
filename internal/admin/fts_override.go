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

// internalizeNotifySetter is the optional interface storesqlite.Store
// and storepg.Store satisfy so admin/server.go can register the
// extimg-internalize-worker's wake hook (REQ-EXTIMG-BG-08).
type internalizeNotifySetter interface {
	SetInternalizeNotifyHook(func())
}

// registerInternalizeNotify walks down the wrapper chain (ftsOverride
// embeds store.Store) and calls SetInternalizeNotifyHook on the first
// concrete store that supports it. A no-op when the chain runs out
// without finding the setter (e.g. the test fakestore).
func registerInternalizeNotify(s store.Store, hook func()) {
	for {
		if setter, ok := s.(internalizeNotifySetter); ok {
			setter.SetInternalizeNotifyHook(hook)
			return
		}
		over, ok := s.(ftsOverride)
		if !ok {
			return
		}
		s = over.Store
	}
}
