package storesqlite

// Phase 3 Wave 3.9: Email reactions store methods (REQ-PROTO-100..103).
// Schema: migrations/0019_email_reactions.sql.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// AddEmailReaction inserts a reaction row. Duplicate inserts are silently
// ignored (ON CONFLICT DO NOTHING).
func (m *metadata) AddEmailReaction(
	ctx context.Context,
	emailID store.MessageID,
	emoji string,
	principalID store.PrincipalID,
	createdAt time.Time,
) error {
	const q = `
		INSERT INTO email_reactions (email_id, emoji, principal_id, created_at_us)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(email_id, emoji, principal_id) DO NOTHING`
	_, err := m.s.db.ExecContext(ctx, q,
		int64(emailID), emoji, int64(principalID), usMicros(createdAt))
	return err
}

// RemoveEmailReaction deletes the reaction row. Idempotent: no error
// when the row is already absent.
func (m *metadata) RemoveEmailReaction(
	ctx context.Context,
	emailID store.MessageID,
	emoji string,
	principalID store.PrincipalID,
) error {
	const q = `
		DELETE FROM email_reactions
		WHERE email_id = ? AND emoji = ? AND principal_id = ?`
	_, err := m.s.db.ExecContext(ctx, q,
		int64(emailID), emoji, int64(principalID))
	return err
}

// ListEmailReactions returns all reactions on a single email as
// emoji → set-of-principal-ids.
func (m *metadata) ListEmailReactions(
	ctx context.Context,
	emailID store.MessageID,
) (map[string]map[store.PrincipalID]struct{}, error) {
	const q = `
		SELECT emoji, principal_id
		FROM email_reactions
		WHERE email_id = ?
		ORDER BY created_at_us`
	rows, err := m.s.db.QueryContext(ctx, q, int64(emailID))
	if err != nil {
		return nil, fmt.Errorf("storesqlite: list email reactions: %w", err)
	}
	defer rows.Close()

	out := make(map[string]map[store.PrincipalID]struct{})
	for rows.Next() {
		var emoji string
		var pid int64
		if err := rows.Scan(&emoji, &pid); err != nil {
			return nil, fmt.Errorf("storesqlite: scan reaction: %w", err)
		}
		if out[emoji] == nil {
			out[emoji] = make(map[store.PrincipalID]struct{})
		}
		out[emoji][store.PrincipalID(pid)] = struct{}{}
	}
	return out, rows.Err()
}

// BatchListEmailReactions returns reactions for every id in emailIDs.
func (m *metadata) BatchListEmailReactions(
	ctx context.Context,
	emailIDs []store.MessageID,
) (map[store.MessageID]map[string]map[store.PrincipalID]struct{}, error) {
	if len(emailIDs) == 0 {
		return map[store.MessageID]map[string]map[store.PrincipalID]struct{}{}, nil
	}
	// Build an IN(...) query with individual placeholders.
	args := make([]any, len(emailIDs))
	phs := make([]byte, 0, len(emailIDs)*2)
	for i, id := range emailIDs {
		if i > 0 {
			phs = append(phs, ',')
		}
		phs = append(phs, '?')
		args[i] = int64(id)
	}
	q := "SELECT email_id, emoji, principal_id FROM email_reactions WHERE email_id IN (" + string(phs) + ") ORDER BY created_at_us"
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("storesqlite: batch list email reactions: %w", err)
	}
	defer rows.Close()

	out := make(map[store.MessageID]map[string]map[store.PrincipalID]struct{}, len(emailIDs))
	for rows.Next() {
		var eid, pid int64
		var emoji string
		if err := rows.Scan(&eid, &emoji, &pid); err != nil {
			return nil, fmt.Errorf("storesqlite: scan batch reaction: %w", err)
		}
		mid := store.MessageID(eid)
		if out[mid] == nil {
			out[mid] = make(map[string]map[store.PrincipalID]struct{})
		}
		if out[mid][emoji] == nil {
			out[mid][emoji] = make(map[store.PrincipalID]struct{})
		}
		out[mid][emoji][store.PrincipalID(pid)] = struct{}{}
	}
	return out, rows.Err()
}

// GetMessageByMessageIDHeader looks up a message for principalID whose
// cached envelope Message-ID (env_message_id column) equals msgIDHeader
// (without angle brackets).
func (m *metadata) GetMessageByMessageIDHeader(
	ctx context.Context,
	principalID store.PrincipalID,
	msgIDHeader string,
) (store.Message, error) {
	// Use messages.principal_id for the lookup now that the column is
	// directly on messages (Wave 3.11 migration).
	const q = `
		SELECT id FROM messages
		WHERE principal_id = ?
		  AND env_message_id = ?
		LIMIT 1`
	var msgID int64
	err := m.s.db.QueryRowContext(ctx, q, int64(principalID), msgIDHeader).Scan(&msgID)
	if err == sql.ErrNoRows {
		return store.Message{}, store.ErrNotFound
	}
	if err != nil {
		return store.Message{}, fmt.Errorf("storesqlite: get message by message-id header: %w", err)
	}
	return m.GetMessage(ctx, store.MessageID(msgID))
}
