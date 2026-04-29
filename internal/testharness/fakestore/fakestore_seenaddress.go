package fakestore

import (
	"context"
	"fmt"
	"sort"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata seen-address methods (REQ-MAIL-11e..m)
// against the in-memory fakestore.

// seenAddressData holds the in-memory seen-address state.
type seenAddressData struct {
	rows      map[store.SeenAddressID]store.SeenAddress
	nextID    store.SeenAddressID
	byEmail   map[string]store.SeenAddressID // "pid:email" -> id
}

func newSeenAddressData() *seenAddressData {
	return &seenAddressData{
		rows:    make(map[store.SeenAddressID]store.SeenAddress),
		nextID:  1,
		byEmail: make(map[string]store.SeenAddressID),
	}
}

func seenAddrKey(pid store.PrincipalID, email string) string {
	return fmt.Sprintf("%d:%s", pid, email)
}

func (s *Store) ensureSeenAddr() *seenAddressData {
	if s.seenAddresses == nil {
		s.seenAddresses = newSeenAddressData()
	}
	return s.seenAddresses
}

func (m *metaFace) UpsertSeenAddress(
	ctx context.Context,
	principalID store.PrincipalID,
	email, displayName string,
	sendDelta, receiveDelta int64,
) (store.SeenAddress, bool, error) {
	if err := ctx.Err(); err != nil {
		return store.SeenAddress{}, false, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clk.Now()
	d := s.ensureSeenAddr()
	key := seenAddrKey(principalID, email)
	isNew := false
	var sa store.SeenAddress
	if id, ok := d.byEmail[key]; ok {
		sa = d.rows[id]
		sa.LastUsedAt = now
		sa.SendCount += sendDelta
		sa.ReceivedCount += receiveDelta
		if displayName != "" {
			sa.DisplayName = displayName
		}
		d.rows[id] = sa
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID: principalID,
			Kind:        store.EntityKindSeenAddress,
			EntityID:    uint64(sa.ID),
			Op:          store.ChangeOpUpdated,
			ProducedAt:  now,
		})
	} else {
		sa = store.SeenAddress{
			ID:            d.nextID,
			PrincipalID:   principalID,
			Email:         email,
			DisplayName:   displayName,
			FirstSeenAt:   now,
			LastUsedAt:    now,
			SendCount:     sendDelta,
			ReceivedCount: receiveDelta,
		}
		d.nextID++
		d.rows[sa.ID] = sa
		d.byEmail[key] = sa.ID
		isNew = true
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID: principalID,
			Kind:        store.EntityKindSeenAddress,
			EntityID:    uint64(sa.ID),
			Op:          store.ChangeOpCreated,
			ProducedAt:  now,
		})
	}

	// Cap enforcement: collect principal's rows, sort by last_used_at asc,
	// evict the oldest if count > 500.
	var pidRows []store.SeenAddress
	for _, r := range d.rows {
		if r.PrincipalID == principalID {
			pidRows = append(pidRows, r)
		}
	}
	if len(pidRows) > 500 {
		sort.Slice(pidRows, func(i, j int) bool {
			return pidRows[i].LastUsedAt.Before(pidRows[j].LastUsedAt)
		})
		// Evict the oldest row that is NOT the one we just upserted.
		var evict store.SeenAddress
		for _, r := range pidRows {
			if r.ID != sa.ID {
				evict = r
				break
			}
		}
		if evict.ID == 0 {
			// Fallback: evict the absolute oldest.
			evict = pidRows[0]
		}
		delete(d.rows, evict.ID)
		delete(d.byEmail, seenAddrKey(principalID, evict.Email))
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID: principalID,
			Kind:        store.EntityKindSeenAddress,
			EntityID:    uint64(evict.ID),
			Op:          store.ChangeOpDestroyed,
			ProducedAt:  now,
		})
	}

	// Bump JMAP state counter.
	s.ensurePhase2()
	st := s.phase2.jmapStates[principalID]
	st.PrincipalID = principalID
	st.SeenAddress++
	s.phase2.jmapStates[principalID] = st

	return sa, isNew, nil
}

func (m *metaFace) ListSeenAddressesByPrincipal(
	ctx context.Context,
	principalID store.PrincipalID,
	limit int,
) ([]store.SeenAddress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.seenAddresses == nil {
		return nil, nil
	}
	var out []store.SeenAddress
	for _, r := range s.seenAddresses.rows {
		if r.PrincipalID == principalID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastUsedAt.After(out[j].LastUsedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) GetSeenAddressByEmail(
	ctx context.Context,
	principalID store.PrincipalID,
	email string,
) (store.SeenAddress, error) {
	if err := ctx.Err(); err != nil {
		return store.SeenAddress{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.seenAddresses == nil {
		return store.SeenAddress{}, fmt.Errorf("seen_address %s: %w", email, store.ErrNotFound)
	}
	key := seenAddrKey(principalID, email)
	id, ok := s.seenAddresses.byEmail[key]
	if !ok {
		return store.SeenAddress{}, fmt.Errorf("seen_address %s: %w", email, store.ErrNotFound)
	}
	return s.seenAddresses.rows[id], nil
}

func (m *metaFace) DestroySeenAddress(
	ctx context.Context,
	principalID store.PrincipalID,
	id store.SeenAddressID,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenAddresses == nil {
		return fmt.Errorf("seen_address %d: %w", id, store.ErrNotFound)
	}
	sa, ok := s.seenAddresses.rows[id]
	if !ok || sa.PrincipalID != principalID {
		return fmt.Errorf("seen_address %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	delete(s.seenAddresses.rows, id)
	delete(s.seenAddresses.byEmail, seenAddrKey(principalID, sa.Email))
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: principalID,
		Kind:        store.EntityKindSeenAddress,
		EntityID:    uint64(id),
		Op:          store.ChangeOpDestroyed,
		ProducedAt:  now,
	})
	s.ensurePhase2()
	st := s.phase2.jmapStates[principalID]
	st.PrincipalID = principalID
	st.SeenAddress++
	s.phase2.jmapStates[principalID] = st
	return nil
}

func (m *metaFace) DestroySeenAddressByEmail(
	ctx context.Context,
	principalID store.PrincipalID,
	email string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenAddresses == nil {
		return fmt.Errorf("seen_address %q: %w", email, store.ErrNotFound)
	}
	key := seenAddrKey(principalID, email)
	id, ok := s.seenAddresses.byEmail[key]
	if !ok {
		return fmt.Errorf("seen_address %q: %w", email, store.ErrNotFound)
	}
	now := s.clk.Now()
	delete(s.seenAddresses.rows, id)
	delete(s.seenAddresses.byEmail, key)
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: principalID,
		Kind:        store.EntityKindSeenAddress,
		EntityID:    uint64(id),
		Op:          store.ChangeOpDestroyed,
		ProducedAt:  now,
	})
	s.ensurePhase2()
	st := s.phase2.jmapStates[principalID]
	st.PrincipalID = principalID
	st.SeenAddress++
	s.phase2.jmapStates[principalID] = st
	return nil
}

func (m *metaFace) PurgeSeenAddressesByPrincipal(
	ctx context.Context,
	principalID store.PrincipalID,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenAddresses == nil {
		return 0, nil
	}
	now := s.clk.Now()
	n := 0
	for id, sa := range s.seenAddresses.rows {
		if sa.PrincipalID == principalID {
			delete(s.seenAddresses.rows, id)
			delete(s.seenAddresses.byEmail, seenAddrKey(principalID, sa.Email))
			n++
		}
	}
	if n > 0 {
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID: principalID,
			Kind:        store.EntityKindSeenAddress,
			EntityID:    0,
			Op:          store.ChangeOpDestroyed,
			ProducedAt:  now,
		})
		s.ensurePhase2()
		st := s.phase2.jmapStates[principalID]
		st.PrincipalID = principalID
		st.SeenAddress++
		s.phase2.jmapStates[principalID] = st
	}
	return n, nil
}

