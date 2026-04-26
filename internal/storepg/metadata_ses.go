package storepg

// Phase 3 Wave 3.2: SES inbound dedupe store methods (REQ-HOOK-SES-02).
// Schema: migrations/0018_ses_seen_messages.sql.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// IsSESSeen returns true when messageID exists in ses_seen_messages.
func (m *metadata) IsSESSeen(ctx context.Context, messageID string) (bool, error) {
	const q = `SELECT 1 FROM ses_seen_messages WHERE message_id = $1 LIMIT 1`
	var one int
	err := m.s.pool.QueryRow(ctx, q, messageID).Scan(&one)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// InsertSESSeen records messageID as seen at seenAt.  Duplicate inserts
// are silently ignored.
func (m *metadata) InsertSESSeen(ctx context.Context, messageID string, seenAt time.Time) error {
	const q = `
		INSERT INTO ses_seen_messages (message_id, seen_at)
		VALUES ($1, $2)
		ON CONFLICT(message_id) DO NOTHING`
	_, err := m.s.pool.Exec(ctx, q, messageID, seenAt.UTC())
	return err
}

// GCOldSESSeen deletes rows whose seen_at is strictly before cutoff.
func (m *metadata) GCOldSESSeen(ctx context.Context, cutoff time.Time) error {
	const q = `DELETE FROM ses_seen_messages WHERE seen_at < $1`
	_, err := m.s.pool.Exec(ctx, q, cutoff.UTC())
	return err
}
