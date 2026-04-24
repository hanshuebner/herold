package storesqlite_test

import (
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// Compile-time check that *storesqlite.Store satisfies store.Store.
var _ store.Store = (*storesqlite.Store)(nil)
