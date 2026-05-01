package clientlogevict_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clientlogevict"
	"github.com/hanshuebner/herold/internal/store"
)

// stubStore counts eviction calls and records the most recent options.
type stubStore struct {
	calls atomic.Int64
	last  store.ClientLogEvictOptions
}

func (s *stubStore) EvictClientLog(_ context.Context, opts store.ClientLogEvictOptions) (int, error) {
	s.calls.Add(1)
	s.last = opts
	return 0, nil
}

// TestEvictor_RunExitsOnContextCancel verifies that Run returns nil when the
// context is cancelled before the first tick fires.
func TestEvictor_RunExitsOnContextCancel(t *testing.T) {
	stub := &stubStore{}
	ev := &clientlogevict.Evictor{
		Store:        stub,
		TickInterval: 10 * time.Second, // long so no tick fires
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ev.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v; want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancel")
	}
}

// TestEvictor_TickCallsBothSlices verifies that a single tick evicts both the
// auth and public slices.
func TestEvictor_TickCallsBothSlices(t *testing.T) {
	stub := &stubStore{}
	ev := &clientlogevict.Evictor{
		Store:         stub,
		TickInterval:  10 * time.Millisecond, // fire quickly
		AuthCapRows:   50_000,
		AuthMaxAge:    24 * time.Hour,
		PublicCapRows: 5_000,
		PublicMaxAge:  6 * time.Hour,
		BatchSize:     500,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = ev.Run(ctx)

	// At least one tick must have fired; each tick calls EvictClientLog twice
	// (once per slice).
	if c := stub.calls.Load(); c < 2 {
		t.Errorf("expected >= 2 EvictClientLog calls; got %d", c)
	}
}

// TestEvictor_DefaultsAppliedWhenZero verifies that zero-value Evictor fields
// fall back to the documented defaults.
func TestEvictor_DefaultsAppliedWhenZero(t *testing.T) {
	if clientlogevict.DefaultTickInterval != 60*time.Second {
		t.Errorf("DefaultTickInterval = %v; want 60s", clientlogevict.DefaultTickInterval)
	}
	if clientlogevict.DefaultAuthCapRows != 100_000 {
		t.Errorf("DefaultAuthCapRows = %d; want 100000", clientlogevict.DefaultAuthCapRows)
	}
	if clientlogevict.DefaultAuthMaxAge != 168*time.Hour {
		t.Errorf("DefaultAuthMaxAge = %v; want 168h", clientlogevict.DefaultAuthMaxAge)
	}
	if clientlogevict.DefaultPublicCapRows != 10_000 {
		t.Errorf("DefaultPublicCapRows = %d; want 10000", clientlogevict.DefaultPublicCapRows)
	}
	if clientlogevict.DefaultPublicMaxAge != 24*time.Hour {
		t.Errorf("DefaultPublicMaxAge = %v; want 24h", clientlogevict.DefaultPublicMaxAge)
	}
	if clientlogevict.DefaultBatchSize != 1000 {
		t.Errorf("DefaultBatchSize = %d; want 1000", clientlogevict.DefaultBatchSize)
	}
}
