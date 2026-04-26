package fakestore

import (
	"context"
	"fmt"
	"sort"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase 3 Wave 3.8a JMAP PushSubscription
// surface (REQ-PROTO-120..122) against the fakestore. Schema-side
// commentary lives in
// internal/storesqlite/migrations/0017_push_subscription.sql; the
// type definitions are in internal/store/types_push.go.

// clonePushSubscription deep-copies the byte-slice fields so the
// caller cannot mutate the in-memory store row by editing the
// returned copy.
func clonePushSubscription(ps store.PushSubscription) store.PushSubscription {
	if len(ps.P256DH) > 0 {
		b := make([]byte, len(ps.P256DH))
		copy(b, ps.P256DH)
		ps.P256DH = b
	}
	if len(ps.Auth) > 0 {
		b := make([]byte, len(ps.Auth))
		copy(b, ps.Auth)
		ps.Auth = b
	}
	if len(ps.NotificationRulesJSON) > 0 {
		b := make([]byte, len(ps.NotificationRulesJSON))
		copy(b, ps.NotificationRulesJSON)
		ps.NotificationRulesJSON = b
	}
	if len(ps.Types) > 0 {
		t := make([]string, len(ps.Types))
		copy(t, ps.Types)
		ps.Types = t
	}
	if ps.Expires != nil {
		t := *ps.Expires
		ps.Expires = &t
	}
	if ps.QuietHoursStartLocal != nil {
		v := *ps.QuietHoursStartLocal
		ps.QuietHoursStartLocal = &v
	}
	if ps.QuietHoursEndLocal != nil {
		v := *ps.QuietHoursEndLocal
		ps.QuietHoursEndLocal = &v
	}
	return ps
}

func (m *metaFace) InsertPushSubscription(ctx context.Context, ps store.PushSubscription) (store.PushSubscriptionID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	ps.ID = s.phase2.nextPushSubscription
	s.phase2.nextPushSubscription++
	if ps.CreatedAt.IsZero() {
		ps.CreatedAt = now
	}
	ps.UpdatedAt = now
	stored := clonePushSubscription(ps)
	s.phase2.pushSubscriptions[stored.ID] = stored
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: ps.PrincipalID,
		Kind:        store.EntityKindPushSubscription,
		EntityID:    uint64(stored.ID),
		Op:          store.ChangeOpCreated,
		ProducedAt:  now,
	})
	return stored.ID, nil
}

func (m *metaFace) GetPushSubscription(ctx context.Context, id store.PushSubscriptionID) (store.PushSubscription, error) {
	if err := ctx.Err(); err != nil {
		return store.PushSubscription{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.PushSubscription{}, fmt.Errorf("push subscription %d: %w", id, store.ErrNotFound)
	}
	ps, ok := s.phase2.pushSubscriptions[id]
	if !ok {
		return store.PushSubscription{}, fmt.Errorf("push subscription %d: %w", id, store.ErrNotFound)
	}
	return clonePushSubscription(ps), nil
}

func (m *metaFace) ListPushSubscriptionsByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.PushSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.PushSubscription
	for _, ps := range s.phase2.pushSubscriptions {
		if ps.PrincipalID != pid {
			continue
		}
		out = append(out, clonePushSubscription(ps))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *metaFace) UpdatePushSubscription(ctx context.Context, ps store.PushSubscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase2 == nil {
		return fmt.Errorf("push subscription %d: %w", ps.ID, store.ErrNotFound)
	}
	cur, ok := s.phase2.pushSubscriptions[ps.ID]
	if !ok {
		return fmt.Errorf("push subscription %d: %w", ps.ID, store.ErrNotFound)
	}
	now := s.clk.Now()
	// Apply mutable fields only; immutable ones (PrincipalID,
	// DeviceClientID, URL, P256DH, Auth, VAPIDKeyAtRegistration,
	// CreatedAt) come from the existing row so the JMAP /set update
	// path cannot accidentally mutate them through the store.
	cur.Expires = nil
	if ps.Expires != nil {
		t := *ps.Expires
		cur.Expires = &t
	}
	if len(ps.Types) == 0 {
		cur.Types = nil
	} else {
		t := make([]string, len(ps.Types))
		copy(t, ps.Types)
		cur.Types = t
	}
	cur.VerificationCode = ps.VerificationCode
	cur.Verified = ps.Verified
	if ps.NotificationRulesJSON == nil {
		cur.NotificationRulesJSON = nil
	} else {
		b := make([]byte, len(ps.NotificationRulesJSON))
		copy(b, ps.NotificationRulesJSON)
		cur.NotificationRulesJSON = b
	}
	cur.QuietHoursStartLocal = nil
	if ps.QuietHoursStartLocal != nil {
		v := *ps.QuietHoursStartLocal
		cur.QuietHoursStartLocal = &v
	}
	cur.QuietHoursEndLocal = nil
	if ps.QuietHoursEndLocal != nil {
		v := *ps.QuietHoursEndLocal
		cur.QuietHoursEndLocal = &v
	}
	cur.QuietHoursTZ = ps.QuietHoursTZ
	cur.UpdatedAt = now
	s.phase2.pushSubscriptions[cur.ID] = cur
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: cur.PrincipalID,
		Kind:        store.EntityKindPushSubscription,
		EntityID:    uint64(cur.ID),
		Op:          store.ChangeOpUpdated,
		ProducedAt:  now,
	})
	return nil
}

func (m *metaFace) DeletePushSubscription(ctx context.Context, id store.PushSubscriptionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase2 == nil {
		return fmt.Errorf("push subscription %d: %w", id, store.ErrNotFound)
	}
	cur, ok := s.phase2.pushSubscriptions[id]
	if !ok {
		return fmt.Errorf("push subscription %d: %w", id, store.ErrNotFound)
	}
	delete(s.phase2.pushSubscriptions, id)
	now := s.clk.Now()
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: cur.PrincipalID,
		Kind:        store.EntityKindPushSubscription,
		EntityID:    uint64(id),
		Op:          store.ChangeOpDestroyed,
		ProducedAt:  now,
	})
	return nil
}
