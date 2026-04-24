package storepg_test

import (
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// Compile-time check that *storepg.Store satisfies store.Store.
var _ store.Store = (*storepg.Store)(nil)
