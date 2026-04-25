package fakestore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.6 JMAP for Contacts surface
// (REQ-PROTO-55) against the fakestore. The schema-side commentary lives
// in internal/storesqlite/migrations/0010_contacts.sql; the type
// definitions are in internal/store/types_contacts.go.

// -- AddressBook ------------------------------------------------------

func (m *metaFace) InsertAddressBook(ctx context.Context, ab store.AddressBook) (store.AddressBookID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	// (principal_id, name) uniqueness — JMAP allows duplicate names, but
	// the storage surface mirrors mailbox name uniqueness for v1
	// consistency. Tests can always pick distinct names; the unique
	// constraint is a backstop, not a restriction the JMAP layer
	// exposes to clients.
	for _, e := range s.phase2.addressBooks {
		if e.PrincipalID == ab.PrincipalID && e.Name == ab.Name {
			return 0, fmt.Errorf("address book %q for principal %d: %w", ab.Name, ab.PrincipalID, store.ErrConflict)
		}
	}
	if ab.IsDefault {
		for id, e := range s.phase2.addressBooks {
			if e.PrincipalID == ab.PrincipalID && e.IsDefault {
				e.IsDefault = false
				e.UpdatedAt = now
				s.phase2.addressBooks[id] = e
			}
		}
	}
	ab.ID = s.phase2.nextAddressBook
	s.phase2.nextAddressBook++
	if ab.CreatedAt.IsZero() {
		ab.CreatedAt = now
	}
	ab.UpdatedAt = now
	ab.ModSeq = 1
	if ab.Color != nil {
		v := *ab.Color
		ab.Color = &v
	}
	s.phase2.addressBooks[ab.ID] = ab
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: ab.PrincipalID,
		Kind:        store.EntityKindAddressBook,
		EntityID:    uint64(ab.ID),
		Op:          store.ChangeOpCreated,
		ProducedAt:  now,
	})
	return ab.ID, nil
}

func (m *metaFace) GetAddressBook(ctx context.Context, id store.AddressBookID) (store.AddressBook, error) {
	if err := ctx.Err(); err != nil {
		return store.AddressBook{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.AddressBook{}, fmt.Errorf("address book %d: %w", id, store.ErrNotFound)
	}
	ab, ok := s.phase2.addressBooks[id]
	if !ok {
		return store.AddressBook{}, fmt.Errorf("address book %d: %w", id, store.ErrNotFound)
	}
	if ab.Color != nil {
		v := *ab.Color
		ab.Color = &v
	}
	return ab, nil
}

func (m *metaFace) ListAddressBooks(ctx context.Context, filter store.AddressBookFilter) ([]store.AddressBook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.AddressBook
	for _, ab := range s.phase2.addressBooks {
		if filter.PrincipalID != nil && ab.PrincipalID != *filter.PrincipalID {
			continue
		}
		if filter.AfterModSeq != 0 && ab.ModSeq <= filter.AfterModSeq {
			continue
		}
		if filter.AfterID != 0 && ab.ID <= filter.AfterID {
			continue
		}
		if ab.Color != nil {
			v := *ab.Color
			ab.Color = &v
		}
		out = append(out, ab)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) UpdateAddressBook(ctx context.Context, ab store.AddressBook) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.addressBooks[ab.ID]
	if !ok {
		return fmt.Errorf("address book %d: %w", ab.ID, store.ErrNotFound)
	}
	now := s.clk.Now()
	if ab.IsDefault && !cur.IsDefault {
		for id, e := range s.phase2.addressBooks {
			if id == ab.ID || e.PrincipalID != cur.PrincipalID {
				continue
			}
			if e.IsDefault {
				e.IsDefault = false
				e.UpdatedAt = now
				s.phase2.addressBooks[id] = e
			}
		}
	}
	cur.Name = ab.Name
	cur.Description = ab.Description
	if ab.Color != nil {
		v := *ab.Color
		cur.Color = &v
	} else {
		cur.Color = nil
	}
	cur.SortOrder = ab.SortOrder
	cur.IsSubscribed = ab.IsSubscribed
	cur.IsDefault = ab.IsDefault
	cur.RightsMask = ab.RightsMask
	cur.UpdatedAt = now
	cur.ModSeq++
	s.phase2.addressBooks[ab.ID] = cur
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: cur.PrincipalID,
		Kind:        store.EntityKindAddressBook,
		EntityID:    uint64(ab.ID),
		Op:          store.ChangeOpUpdated,
		ProducedAt:  now,
	})
	return nil
}

func (m *metaFace) DeleteAddressBook(ctx context.Context, id store.AddressBookID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.addressBooks[id]
	if !ok {
		return fmt.Errorf("address book %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	// Cascade: remove every contact owned by this book and append per-row
	// destroyed state-change entries.
	var contactIDs []store.ContactID
	for cid, c := range s.phase2.contacts {
		if c.AddressBookID == id {
			contactIDs = append(contactIDs, cid)
		}
	}
	sort.Slice(contactIDs, func(i, j int) bool { return contactIDs[i] < contactIDs[j] })
	for _, cid := range contactIDs {
		c := s.phase2.contacts[cid]
		delete(s.phase2.contacts, cid)
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID:    c.PrincipalID,
			Kind:           store.EntityKindContact,
			EntityID:       uint64(cid),
			ParentEntityID: uint64(id),
			Op:             store.ChangeOpDestroyed,
			ProducedAt:     now,
		})
	}
	delete(s.phase2.addressBooks, id)
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: cur.PrincipalID,
		Kind:        store.EntityKindAddressBook,
		EntityID:    uint64(id),
		Op:          store.ChangeOpDestroyed,
		ProducedAt:  now,
	})
	return nil
}

func (m *metaFace) DefaultAddressBook(ctx context.Context, principalID store.PrincipalID) (store.AddressBook, error) {
	if err := ctx.Err(); err != nil {
		return store.AddressBook{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.AddressBook{}, fmt.Errorf("default address book for %d: %w", principalID, store.ErrNotFound)
	}
	for _, ab := range s.phase2.addressBooks {
		if ab.PrincipalID == principalID && ab.IsDefault {
			if ab.Color != nil {
				v := *ab.Color
				ab.Color = &v
			}
			return ab, nil
		}
	}
	return store.AddressBook{}, fmt.Errorf("default address book for %d: %w", principalID, store.ErrNotFound)
}

// -- Contact ----------------------------------------------------------

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func (m *metaFace) InsertContact(ctx context.Context, c store.Contact) (store.ContactID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	for _, e := range s.phase2.contacts {
		if e.AddressBookID == c.AddressBookID && e.UID == c.UID {
			return 0, fmt.Errorf("contact %q in book %d: %w", c.UID, c.AddressBookID, store.ErrConflict)
		}
	}
	now := s.clk.Now()
	c.ID = s.phase2.nextContact
	s.phase2.nextContact++
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.ModSeq = 1
	c.JSContactJSON = cloneBytes(c.JSContactJSON)
	c.PrimaryEmail = strings.ToLower(c.PrimaryEmail)
	c.SearchBlob = strings.ToLower(c.SearchBlob)
	s.phase2.contacts[c.ID] = c
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID:    c.PrincipalID,
		Kind:           store.EntityKindContact,
		EntityID:       uint64(c.ID),
		ParentEntityID: uint64(c.AddressBookID),
		Op:             store.ChangeOpCreated,
		ProducedAt:     now,
	})
	return c.ID, nil
}

func (m *metaFace) GetContact(ctx context.Context, id store.ContactID) (store.Contact, error) {
	if err := ctx.Err(); err != nil {
		return store.Contact{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.Contact{}, fmt.Errorf("contact %d: %w", id, store.ErrNotFound)
	}
	c, ok := s.phase2.contacts[id]
	if !ok {
		return store.Contact{}, fmt.Errorf("contact %d: %w", id, store.ErrNotFound)
	}
	c.JSContactJSON = cloneBytes(c.JSContactJSON)
	return c, nil
}

func (m *metaFace) ListContacts(ctx context.Context, filter store.ContactFilter) ([]store.Contact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	text := strings.ToLower(filter.Text)
	var hasEmail string
	if filter.HasEmail != nil {
		hasEmail = strings.ToLower(*filter.HasEmail)
	}
	var out []store.Contact
	for _, c := range s.phase2.contacts {
		if filter.AddressBookID != nil && c.AddressBookID != *filter.AddressBookID {
			continue
		}
		if filter.PrincipalID != nil && c.PrincipalID != *filter.PrincipalID {
			continue
		}
		if text != "" && !strings.Contains(c.SearchBlob, text) {
			continue
		}
		if filter.HasEmail != nil && c.PrimaryEmail != hasEmail {
			continue
		}
		if filter.UID != nil && c.UID != *filter.UID {
			continue
		}
		if filter.AfterModSeq != 0 && c.ModSeq <= filter.AfterModSeq {
			continue
		}
		if filter.AfterID != 0 && c.ID <= filter.AfterID {
			continue
		}
		c.JSContactJSON = cloneBytes(c.JSContactJSON)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) UpdateContact(ctx context.Context, c store.Contact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.contacts[c.ID]
	if !ok {
		return fmt.Errorf("contact %d: %w", c.ID, store.ErrNotFound)
	}
	now := s.clk.Now()
	cur.JSContactJSON = cloneBytes(c.JSContactJSON)
	cur.DisplayName = c.DisplayName
	cur.GivenName = c.GivenName
	cur.Surname = c.Surname
	cur.OrgName = c.OrgName
	cur.PrimaryEmail = strings.ToLower(c.PrimaryEmail)
	cur.SearchBlob = strings.ToLower(c.SearchBlob)
	cur.UpdatedAt = now
	cur.ModSeq++
	s.phase2.contacts[c.ID] = cur
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID:    cur.PrincipalID,
		Kind:           store.EntityKindContact,
		EntityID:       uint64(cur.ID),
		ParentEntityID: uint64(cur.AddressBookID),
		Op:             store.ChangeOpUpdated,
		ProducedAt:     now,
	})
	return nil
}

func (m *metaFace) DeleteContact(ctx context.Context, id store.ContactID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.contacts[id]
	if !ok {
		return fmt.Errorf("contact %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	delete(s.phase2.contacts, id)
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID:    cur.PrincipalID,
		Kind:           store.EntityKindContact,
		EntityID:       uint64(id),
		ParentEntityID: uint64(cur.AddressBookID),
		Op:             store.ChangeOpDestroyed,
		ProducedAt:     now,
	})
	return nil
}
