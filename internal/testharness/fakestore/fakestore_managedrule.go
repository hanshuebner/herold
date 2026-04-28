package fakestore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// -- Wave 3.15 ManagedRule + user Sieve script (REQ-FLT-01..31) --

func (m *metaFace) InsertManagedRule(ctx context.Context, rule store.ManagedRule) (store.ManagedRule, error) {
	if err := ctx.Err(); err != nil {
		return store.ManagedRule{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.managedRuleSeq++
	rule.ID = store.ManagedRuleID(s.managedRuleSeq)
	now := time.Now().UTC()
	rule.CreatedAt = now
	rule.UpdatedAt = now
	s.managedRules[rule.ID] = rule
	return rule, nil
}

func (m *metaFace) UpdateManagedRule(ctx context.Context, rule store.ManagedRule) (store.ManagedRule, error) {
	if err := ctx.Err(); err != nil {
		return store.ManagedRule{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.managedRules[rule.ID]
	if !ok {
		return store.ManagedRule{}, fmt.Errorf("fakestore: %w", store.ErrNotFound)
	}
	rule.CreatedAt = existing.CreatedAt
	rule.UpdatedAt = time.Now().UTC()
	s.managedRules[rule.ID] = rule
	return rule, nil
}

func (m *metaFace) DeleteManagedRule(ctx context.Context, id store.ManagedRuleID, pid store.PrincipalID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, ok := s.managedRules[id]
	if !ok || rule.PrincipalID != pid {
		return fmt.Errorf("fakestore: %w", store.ErrNotFound)
	}
	delete(s.managedRules, id)
	return nil
}

func (m *metaFace) GetManagedRule(ctx context.Context, id store.ManagedRuleID, pid store.PrincipalID) (store.ManagedRule, error) {
	if err := ctx.Err(); err != nil {
		return store.ManagedRule{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	rule, ok := s.managedRules[id]
	if !ok || rule.PrincipalID != pid {
		return store.ManagedRule{}, fmt.Errorf("fakestore: %w", store.ErrNotFound)
	}
	return rule, nil
}

func (m *metaFace) ListManagedRules(ctx context.Context, pid store.PrincipalID, filter store.ManagedRuleFilter) ([]store.ManagedRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = 256
	}
	var out []store.ManagedRule
	for _, rule := range s.managedRules {
		if rule.PrincipalID != pid {
			continue
		}
		if filter.AfterID > 0 && rule.ID <= filter.AfterID {
			continue
		}
		out = append(out, rule)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetUserSieveScript returns the user-written Sieve script half for pid.
// The fakestore keeps this separate from the active script so callers can
// test the two-source composition path (preamble + user script = effective).
func (m *metaFace) GetUserSieveScript(ctx context.Context, pid store.PrincipalID) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.userSieveScripts[pid], nil
}

// SetUserSieveScript upserts the user-written Sieve script half for pid.
func (m *metaFace) SetUserSieveScript(ctx context.Context, pid store.PrincipalID, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		delete(s.userSieveScripts, pid)
		return nil
	}
	s.userSieveScripts[pid] = text
	return nil
}
