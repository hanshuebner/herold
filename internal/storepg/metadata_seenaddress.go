package storepg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata seen-address methods (REQ-MAIL-11e..m)
// for the Postgres backend.  Schema commentary lives in
// migrations/0030_seen_addresses.sql.

const seenAddrSelectColsPG = `id, principal_id, email, display_name,
	first_seen_at_us, last_used_at_us, send_count, received_count`

func scanSeenAddressPG(row pgx.Row) (store.SeenAddress, error) {
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		// Try INSERT ON CONFLICT DO NOTHING to detect new vs update.
		res, txerr := tx.Exec(ctx, `
			INSERT INTO seen_addresses
			  (principal_id, email, display_name, first_seen_at_us, last_used_at_us,
			   send_count, received_count)
			VALUES ($1, $2, $3, $4, $4, $5, $6)
			ON CONFLICT (principal_id, email) DO NOTHING`,
			int64(principalID), email, displayName, nowUs, sendDelta, receiveDelta)
		if txerr != nil {
			return mapErr(txerr)
		}
		if res.RowsAffected() == 1 {
			isNew = true
		} else {
			// Row exists — update it.
			if displayName != "" {
				if _, err := tx.Exec(ctx, `
					UPDATE seen_addresses SET
					  display_name    = $1,
					  last_used_at_us = $2,
					  send_count      = send_count + $3,
					  received_count  = received_count + $4
					WHERE principal_id = $5 AND email = $6`,
					displayName, nowUs, sendDelta, receiveDelta,
					int64(principalID), email); err != nil {
					return mapErr(err)
				}
			} else {
				if _, err := tx.Exec(ctx, `
					UPDATE seen_addresses SET
					  last_used_at_us = $1,
					  send_count      = send_count + $2,
					  received_count  = received_count + $3
					WHERE principal_id = $4 AND email = $5`,
					nowUs, sendDelta, receiveDelta,
					int64(principalID), email); err != nil {
					return mapErr(err)
				}
			}
		}

		// Fetch the upserted row.
		row := tx.QueryRow(ctx,
			`SELECT `+seenAddrSelectColsPG+` FROM seen_addresses WHERE principal_id = $1 AND email = $2`,
			int64(principalID), email)
		sa, err := scanSeenAddressPG(row)
		if err != nil {
			return err
		}
		out = sa

		// Cap enforcement: if count > 500 delete the oldest-by-last_used_at row.
		var cnt int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM seen_addresses WHERE principal_id = $1`,
			int64(principalID)).Scan(&cnt); err != nil {
			return mapErr(err)
		}
		if cnt > 500 {
			var evictID int64
			evictErr := tx.QueryRow(ctx, `
				SELECT id FROM seen_addresses
				WHERE principal_id = $1 AND id != $2
				ORDER BY last_used_at_us ASC
				LIMIT 1`, int64(principalID), int64(out.ID)).Scan(&evictID)
			if evictErr == nil {
				if _, err := tx.Exec(ctx,
					`DELETE FROM seen_addresses WHERE id = $1`, evictID); err != nil {
					return mapErr(err)
				}
				if err := appendStateChange(ctx, tx, principalID,
					store.EntityKindSeenAddress, uint64(evictID), 0,
					store.ChangeOpDestroyed, now); err != nil {
					return err
				}
			}
		}

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
	if _, err := m.IncrementJMAPState(ctx, principalID, store.JMAPStateKindSeenAddress); err != nil {
		return out, isNew, fmt.Errorf("storepg: bump seen_address_state: %w", err)
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
	rows, err := m.s.pool.Query(ctx, `
		SELECT `+seenAddrSelectColsPG+`
		  FROM seen_addresses
		 WHERE principal_id = $1
		 ORDER BY last_used_at_us DESC
		 LIMIT $2`,
		int64(principalID), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.SeenAddress
	for rows.Next() {
		sa, err := scanSeenAddressPG(rows)
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
	row := m.s.pool.QueryRow(ctx, `
		SELECT `+seenAddrSelectColsPG+`
		  FROM seen_addresses
		 WHERE principal_id = $1 AND email = $2`,
		int64(principalID), email)
	return scanSeenAddressPG(row)
}

func (m *metadata) DestroySeenAddress(
	ctx context.Context,
	principalID store.PrincipalID,
	id store.SeenAddressID,
) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM seen_addresses WHERE id = $1`,
			int64(id)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		if store.PrincipalID(pid) != principalID {
			return store.ErrNotFound
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM seen_addresses WHERE id = $1`, int64(id)); err != nil {
			return mapErr(err)
		}
		if err := appendStateChange(ctx, tx, principalID,
			store.EntityKindSeenAddress, uint64(id), 0, store.ChangeOpDestroyed, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE jmap_states SET seen_address_state = seen_address_state + 1, updated_at_us = $1
			 WHERE principal_id = $2`, usMicros(now), int64(principalID))
		return mapErr(err)
	})
}

func (m *metadata) DestroySeenAddressByEmail(
	ctx context.Context,
	principalID store.PrincipalID,
	email string,
) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx,
			`SELECT id FROM seen_addresses WHERE principal_id = $1 AND email = $2`,
			int64(principalID), email).Scan(&id); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM seen_addresses WHERE id = $1`, id); err != nil {
			return mapErr(err)
		}
		if err := appendStateChange(ctx, tx, principalID,
			store.EntityKindSeenAddress, uint64(id), 0, store.ChangeOpDestroyed, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE jmap_states SET seen_address_state = seen_address_state + 1, updated_at_us = $1
			 WHERE principal_id = $2`, usMicros(now), int64(principalID))
		return mapErr(err)
	})
}

func (m *metadata) PurgeSeenAddressesByPrincipal(
	ctx context.Context,
	principalID store.PrincipalID,
) (int, error) {
	now := m.s.clock.Now().UTC()
	var n int
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM seen_addresses WHERE principal_id = $1`, int64(principalID))
		if err != nil {
			return mapErr(err)
		}
		n = int(res.RowsAffected())
		if n == 0 {
			return nil
		}
		if err := appendStateChange(ctx, tx, principalID,
			store.EntityKindSeenAddress, 0, 0, store.ChangeOpDestroyed, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE jmap_states SET seen_address_state = seen_address_state + 1, updated_at_us = $1
			 WHERE principal_id = $2`, usMicros(now), int64(principalID))
		return mapErr(err)
	})
	return n, err
}
