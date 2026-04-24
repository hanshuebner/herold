package fakestore

import (
	"context"
	"fmt"
	"sort"

	"github.com/hanshuebner/herold/internal/store"
)

// GetMailboxByName satisfies the protoimap MailboxRepo seam: return the
// mailbox with the given name owned by principalID. Wave 3 folds this
// into store.Metadata; see the interface declaration in
// internal/protoimap/repo.go.
func (s *Store) GetMailboxByName(ctx context.Context, principalID store.PrincipalID, name string) (store.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return store.Mailbox{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, mb := range s.mailboxes {
		if mb.PrincipalID == principalID && mb.Name == name {
			return mb, nil
		}
	}
	return store.Mailbox{}, fmt.Errorf("mailbox %q: %w", name, store.ErrNotFound)
}

// ListMessages returns every message in the mailbox ordered by UID
// ascending. Copies the internal state so the caller may mutate freely.
func (s *Store) ListMessages(ctx context.Context, mailboxID store.MailboxID) ([]store.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Message
	for _, m := range s.messages {
		if m.MailboxID == mailboxID {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// SetMailboxSubscribed toggles the MailboxAttrSubscribed bit.
func (s *Store) SetMailboxSubscribed(ctx context.Context, mailboxID store.MailboxID, subscribed bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok {
		return fmt.Errorf("mailbox %d: %w", mailboxID, store.ErrNotFound)
	}
	if subscribed {
		mb.Attributes |= store.MailboxAttrSubscribed
	} else {
		mb.Attributes &^= store.MailboxAttrSubscribed
	}
	mb.UpdatedAt = s.clk.Now()
	s.mailboxes[mailboxID] = mb
	return nil
}

// RenameMailbox updates the Name field, returning store.ErrConflict if the
// destination collides with an existing mailbox for the same principal.
func (s *Store) RenameMailbox(ctx context.Context, mailboxID store.MailboxID, newName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok {
		return fmt.Errorf("mailbox %d: %w", mailboxID, store.ErrNotFound)
	}
	for id, existing := range s.mailboxes {
		if id == mailboxID {
			continue
		}
		if existing.PrincipalID == mb.PrincipalID && existing.Name == newName {
			return fmt.Errorf("mailbox %q: %w", newName, store.ErrConflict)
		}
	}
	mb.Name = newName
	mb.UpdatedAt = s.clk.Now()
	s.mailboxes[mailboxID] = mb
	return nil
}
