package fakestore

// Phase 3 Wave 3.9: Email reactions fakestore implementation
// (REQ-PROTO-100..103, REQ-FLOW-100..108).

import (
	"context"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// reactionKey identifies one reaction row.
type reactionKey struct {
	emailID     store.MessageID
	emoji       string
	principalID store.PrincipalID
}

// reactionsData holds the fakestore reaction rows. Lazily initialised on
// the first Add call.
type reactionsData struct {
	rows map[reactionKey]time.Time
}

func (s *Store) reactionStore() *reactionsData {
	if s.reactions == nil {
		s.reactions = &reactionsData{rows: make(map[reactionKey]time.Time)}
	}
	return s.reactions
}

// AddEmailReaction inserts a reaction row; duplicate is silently ignored.
func (m *metaFace) AddEmailReaction(
	ctx context.Context,
	emailID store.MessageID,
	emoji string,
	principalID store.PrincipalID,
	createdAt time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	k := reactionKey{emailID: emailID, emoji: emoji, principalID: principalID}
	s.reactionStore().rows[k] = createdAt
	return nil
}

// RemoveEmailReaction deletes the reaction row. Idempotent.
func (m *metaFace) RemoveEmailReaction(
	ctx context.Context,
	emailID store.MessageID,
	emoji string,
	principalID store.PrincipalID,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reactionStore().rows, reactionKey{emailID: emailID, emoji: emoji, principalID: principalID})
	return nil
}

// ListEmailReactions returns all reactions on emailID.
func (m *metaFace) ListEmailReactions(
	ctx context.Context,
	emailID store.MessageID,
) (map[string]map[store.PrincipalID]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]map[store.PrincipalID]struct{})
	if s.reactions == nil {
		return out, nil
	}
	for k := range s.reactions.rows {
		if k.emailID != emailID {
			continue
		}
		if out[k.emoji] == nil {
			out[k.emoji] = make(map[store.PrincipalID]struct{})
		}
		out[k.emoji][k.principalID] = struct{}{}
	}
	return out, nil
}

// BatchListEmailReactions returns reactions for every id in emailIDs.
func (m *metaFace) BatchListEmailReactions(
	ctx context.Context,
	emailIDs []store.MessageID,
) (map[store.MessageID]map[string]map[store.PrincipalID]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[store.MessageID]map[string]map[store.PrincipalID]struct{}, len(emailIDs))
	if s.reactions == nil {
		return out, nil
	}
	wanted := make(map[store.MessageID]struct{}, len(emailIDs))
	for _, id := range emailIDs {
		wanted[id] = struct{}{}
	}
	for k := range s.reactions.rows {
		if _, ok := wanted[k.emailID]; !ok {
			continue
		}
		if out[k.emailID] == nil {
			out[k.emailID] = make(map[string]map[store.PrincipalID]struct{})
		}
		if out[k.emailID][k.emoji] == nil {
			out[k.emailID][k.emoji] = make(map[store.PrincipalID]struct{})
		}
		out[k.emailID][k.emoji][k.principalID] = struct{}{}
	}
	return out, nil
}

// GetMessageByMessageIDHeader looks up a message by its envelope Message-ID.
func (m *metaFace) GetMessageByMessageIDHeader(
	ctx context.Context,
	principalID store.PrincipalID,
	msgIDHeader string,
) (store.Message, error) {
	if err := ctx.Err(); err != nil {
		return store.Message{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Identify mailboxes owned by principalID.
	owned := make(map[store.MailboxID]struct{})
	for _, mb := range s.mailboxes {
		if mb.PrincipalID == principalID {
			owned[mb.ID] = struct{}{}
		}
	}
	// Normalise: strip angle brackets for comparison.
	header := strings.Trim(msgIDHeader, "<>")
	for _, msg := range s.messages {
		if _, ok := owned[msg.MailboxID]; !ok {
			continue
		}
		env := strings.Trim(msg.Envelope.MessageID, "<>")
		if env == header {
			return msg, nil
		}
	}
	return store.Message{}, store.ErrNotFound
}
