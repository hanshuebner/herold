package storesqlite

import "context"

// SetConsolidateSnoozeHook installs fn to run inside ConsolidateSnooze's
// transaction between the write that places the reminder on the
// canonical membership and the write that clears it on the previous
// holder. Test seam for asserting that a concurrent wake sweep, which
// sees only committed state, observes exactly one holder; nil removes
// the hook.
func (s *Store) SetConsolidateSnoozeHook(fn func(ctx context.Context)) {
	if fn == nil {
		s.consolidateHook.Store(nil)
		return
	}
	s.consolidateHook.Store(&fn)
}
