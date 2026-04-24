package sieve

import (
	"context"
	"sync"
	"time"
)

// InMemoryVacationStore is a simple VacationStore backed by a map. It is
// used by tests and as a reference implementation; production wires a
// persistent store (the `store` package's vacation table) that satisfies
// the same interface.
type InMemoryVacationStore struct {
	mu    sync.Mutex
	sends map[string]time.Time // key -> last sent timestamp
}

// NewInMemoryVacationStore returns an empty store.
func NewInMemoryVacationStore() *InMemoryVacationStore {
	return &InMemoryVacationStore{sends: map[string]time.Time{}}
}

func vacationKey(handle, sender string) string {
	return handle + "\x00" + sender
}

// ShouldSend reports whether a vacation reply for (handle, sender) is due.
// If days <= 0 the store treats it as one day.
func (s *InMemoryVacationStore) ShouldSend(_ context.Context, handle, sender string, days int, now time.Time) (bool, error) {
	if days <= 0 {
		days = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.sends[vacationKey(handle, sender)]
	if !ok {
		return true, nil
	}
	if now.Sub(last) >= time.Duration(days)*24*time.Hour {
		return true, nil
	}
	return false, nil
}

// Record stores the send timestamp.
func (s *InMemoryVacationStore) Record(_ context.Context, handle, sender string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends[vacationKey(handle, sender)] = now
	return nil
}

// InMemoryDuplicateStore is a reference DuplicateStore implementation. It
// purges entries older than the longest observed window on every Mark
// call. Tests use this directly; production uses the store package's
// duplicate table.
type InMemoryDuplicateStore struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

// NewInMemoryDuplicateStore returns an empty store.
func NewInMemoryDuplicateStore() *InMemoryDuplicateStore {
	return &InMemoryDuplicateStore{entries: map[string]time.Time{}}
}

// SeenAndMark reports whether (handle, value) was observed inside window
// and atomically updates the timestamp.
func (s *InMemoryDuplicateStore) SeenAndMark(_ context.Context, handle, value string, window time.Duration, now time.Time) (bool, error) {
	key := handle + "\x00" + value
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.entries[key]
	s.entries[key] = now
	if !ok {
		return false, nil
	}
	return now.Sub(prev) < window, nil
}
