package storesqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata seen-address methods (REQ-MAIL-11e..m)
// for the SQLite backend.  Schema commentary lives in
// migrations/0030_seen_addresses.sql.

const seenAddrSelectCols = `id, principal_id, email, display_name,
	first_seen_at_us, last_used_at_us, send_count, received_count`

func scanSeenAddress(row rowLike) (store.SeenAddress, error) {
	var (
		id, pid, firstUs, lastUs, sendCnt, recvCnt int64
		email, displayName                         string
	)
	if err := row.Scan(&id, &pid, &email, &displayName,
		&firstUs, &lastUs, &sendCnt, &recvCnt); err != nil {
		return store.SeenAddress{}, mapErr(err)
	}
	return store.SeenAddress{
		ID:            store.SeenAddressID(id),
		PrincipalID:   store.PrincipalID(pid),
		Email:         email,
		DisplayName:   displayName,
		FirstSeenAt:   fromMicros(firstUs),
		LastUsedAt:    fromMicros(lastUs),
		SendCount:     sendCnt,
		ReceivedCount: recvCnt,
	}, nil
}

func (m *metadata) UpsertSeenAddress(
	ctx context.Context,
	principalID store.PrincipalID,
	email, displayName string,
	sendDelta, receiveDelta int64,
) (store.SeenAddress, bool, error) {
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	var (
		out   store.SeenAddress
		isNew bool
	)
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Attempt a pure INSERT first. If it succeeds, the row is new.
		// If it conflicts, do the UPDATE.
		dn := displayName
		res, txerr := tx.ExecContext(ctx, `
			INSERT INTO seen_addresses
			  (principal_id, email, display_name, first_seen_at_us, last_used_at_us,
			   send_count, received_count)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(principal_id, email) DO NOTHING`,
			int64(principalID), email, dn, nowUs, nowUs, sendDelta, receiveDelta)
		if txerr != nil {
			return mapErr(txerr)
		}
		affected, _ := res.RowsAffected()
		if affected == 1 {
			isNew = true
		} else {
			// Row exists — update it.
			if displayName != "" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE seen_addresses SET
					  display_name    = ?,
					  last_used_at_us = ?,
					  send_count      = send_count + ?,
					  received_count  = received_count + ?
					WHERE principal_id = ? AND email = ?`,
					displayName, nowUs, sendDelta, receiveDelta,
					int64(principalID), email); err != nil {
					return mapErr(err)
				}
			} else {
				if _, err := tx.ExecContext(ctx, `
					UPDATE seen_addresses SET
					  last_used_at_us = ?,
					  send_count      = send_count + ?,
					  received_count  = received_count + ?
					WHERE principal_id = ? AND email = ?`,
					nowUs, sendDelta, receiveDelta,
					int64(principalID), email); err != nil {
					return mapErr(err)
				}
			}
		}

		// Fetch the upserted row.
		row := tx.QueryRowContext(ctx,
			`SELECT `+seenAddrSelectCols+` FROM seen_addresses WHERE principal_id = ? AND email = ?`,
			int64(principalID), email)
		sa, err := scanSeenAddress(row)
		if err != nil {
			return err
		}
		out = sa

		// Cap enforcement: if count > 500 delete the oldest-by-last_used_at row.
		var cnt int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM seen_addresses WHERE principal_id = ?`,
			int64(principalID)).Scan(&cnt); err != nil {
			return mapErr(err)
		}
		if cnt > 500 {
			// Find the oldest row (excluding the one we just upserted).
			var evictID int64
			evictErr := tx.QueryRowContext(ctx, `
				SELECT id FROM seen_addresses
				WHERE principal_id = ? AND id != ?
				ORDER BY last_used_at_us ASC
				LIMIT 1`, int64(principalID), int64(out.ID)).Scan(&evictID)
			if evictErr == nil {
				if _, err := tx.ExecContext(ctx,
					`DELETE FROM seen_addresses WHERE id = ?`, evictID); err != nil {
					return mapErr(err)
				}
				if err := appendStateChange(ctx, tx, principalID,
					store.EntityKindSeenAddress, uint64(evictID), 0,
					store.ChangeOpDestroyed, now); err != nil {
					return err
				}
			}
		}

		// Append state-change for the upserted row.
		op := store.ChangeOpCreated
		if !isNew {
			op = store.ChangeOpUpdated
		}
		return appendStateChange(ctx, tx, principalID,
			store.EntityKindSeenAddress, uint64(out.ID), 0, op, now)
	})
	if err != nil {
		return store.SeenAddress{}, false, err
	}
	// Bump the JMAP state counter outside the write tx (IncrementJMAPState opens
	// its own transaction).
	if _, err := m.IncrementJMAPState(ctx, principalID, store.JMAPStateKindSeenAddress); err != nil {
		return out, isNew, fmt.Errorf("storesqlite: bump seen_address_state: %w", err)
	}
	return out, isNew, nil
}

func (m *metadata) ListSeenAddressesByPrincipal(
	ctx context.Context,
	principalID store.PrincipalID,
	limit int,
) ([]store.SeenAddress, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT `+seenAddrSelectCols+`
		  FROM seen_addresses
		 WHERE principal_id = ?
		 ORDER BY last_used_at_us DESC
		 LIMIT ?`,
		int64(principalID), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.SeenAddress
	for rows.Next() {
		sa, err := scanSeenAddress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sa)
	}
	return out, rows.Err()
}

func (m *metadata) GetSeenAddressByEmail(
	ctx context.Context,
	principalID store.PrincipalID,
	email string,
) (store.SeenAddress, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT `+seenAddrSelectCols+`
		  FROM seen_addresses
		 WHERE principal_id = ? AND email = ?`,
		int64(principalID), email)
	return scanSeenAddress(row)
}

func (m *metadata) DestroySeenAddress(
	ctx context.Context,
	principalID store.PrincipalID,
	id store.SeenAddressID,
) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Verify ownership before delete.
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM seen_addresses WHERE id = ?`,
			int64(id)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		if store.PrincipalID(pid) != principalID {
			return store.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM seen_addresses WHERE id = ?`, int64(id)); err != nil {
			return mapErr(err)
		}
		if err := appendStateChange(ctx, tx, principalID,
			store.EntityKindSeenAddress, uint64(id), 0, store.ChangeOpDestroyed, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE jmap_states SET seen_address_state = seen_address_state + 1, updated_at_us = ?
			 WHERE principal_id = ?`, usMicros(now), int64(principalID))
		return mapErr(err)
	})
}

func (m *metadata) DestroySeenAddressByEmail(
	ctx context.Context,
	principalID store.PrincipalID,
	email string,
) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM seen_addresses WHERE principal_id = ? AND email = ?`,
			int64(principalID), email).Scan(&id); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM seen_addresses WHERE id = ?`, id); err != nil {
			return mapErr(err)
		}
		if err := appendStateChange(ctx, tx, principalID,
			store.EntityKindSeenAddress, uint64(id), 0, store.ChangeOpDestroyed, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE jmap_states SET seen_address_state = seen_address_state + 1, updated_at_us = ?
			 WHERE principal_id = ?`, usMicros(now), int64(principalID))
		return mapErr(err)
	})
}

func (m *metadata) PurgeSeenAddressesByPrincipal(
	ctx context.Context,
	principalID store.PrincipalID,
) (int, error) {
	now := m.s.clock.Now().UTC()
	var n int
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM seen_addresses WHERE principal_id = ?`, int64(principalID))
		if err != nil {
			return mapErr(err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: purge rows affected: %w", err)
		}
		n = int(rows)
		if n == 0 {
			return nil
		}
		// One synthetic state-change entry covering the purge.
		if err := appendStateChange(ctx, tx, principalID,
			store.EntityKindSeenAddress, 0, 0, store.ChangeOpDestroyed, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE jmap_states SET seen_address_state = seen_address_state + 1, updated_at_us = ?
			 WHERE principal_id = ?`, usMicros(now), int64(principalID))
		return mapErr(err)
	})
	return n, err
}
