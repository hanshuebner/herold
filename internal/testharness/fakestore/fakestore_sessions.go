package fakestore

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata session methods (REQ-OPS-208,
// REQ-CLOG-06) for the in-memory fakestore.  Sessions are stored in a plain
// map keyed by SessionID.  Expiry eviction is triggered only by explicit
// EvictExpiredSessions calls (no background goroutine).

func (m *metaFace) UpsertSession(ctx context.Context, row store.SessionRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[row.SessionID] = row
	return nil
}

func (m *metaFace) GetSession(ctx context.Context, sessionID string) (store.SessionRow, error) {
	if err := ctx.Err(); err != nil {
		return store.SessionRow{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.sessions[sessionID]
	if !ok {
		return store.SessionRow{}, store.ErrNotFound
	}
	return row, nil
}

func (m *metaFace) DeleteSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return store.ErrNotFound
	}
	delete(s.sessions, sessionID)
	return nil
}

func (m *metaFace) UpdateSessionTelemetry(ctx context.Context, sessionID string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.sessions[sessionID]
	if !ok {
		return store.ErrNotFound
	}
	row.ClientlogTelemetryEnabled = enabled
	s.sessions[sessionID] = row
	return nil
}

func (m *metaFace) EvictExpiredSessions(ctx context.Context, nowMicros int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int
	for id, row := range s.sessions {
		if row.ExpiresAt.UnixMicro() <= nowMicros {
			delete(s.sessions, id)
			deleted++
		}
	}
	return deleted, nil
}

func (m *metaFace) ClearExpiredLivetail(ctx context.Context, nowMicros int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	var cleared int
	for id, row := range s.sessions {
		if row.ClientlogLivetailUntil != nil && row.ClientlogLivetailUntil.UnixMicro() <= nowMicros {
			row.ClientlogLivetailUntil = nil
			s.sessions[id] = row
			cleared++
		}
	}
	return cleared, nil
}
