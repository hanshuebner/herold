package storepg

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the blob_part_index methods from store.Metadata
// for the Postgres backend (migration 0061, re #46).

func (m *metadata) PutBlobPartIndex(ctx context.Context, blobHash string, version int, partsJSON []byte, computedAtUS int64) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO blob_part_index (blob_hash, index_version, parts_json, computed_at_us)
			     VALUES ($1, $2, $3, $4)
			ON CONFLICT (blob_hash) DO UPDATE SET
			    index_version  = EXCLUDED.index_version,
			    parts_json     = EXCLUDED.parts_json,
			    computed_at_us = EXCLUDED.computed_at_us`,
			blobHash, int64(version), string(partsJSON), computedAtUS)
		return mapErr(err)
	})
}

func (m *metadata) GetBlobPartIndex(ctx context.Context, blobHash string) (int, []byte, error) {
	var version int64
	var partsJSON string
	err := m.s.pool.QueryRow(ctx,
		`SELECT index_version, parts_json FROM blob_part_index WHERE blob_hash = $1`,
		blobHash).Scan(&version, &partsJSON)
	if err != nil {
		return 0, nil, mapErr(err)
	}
	return int(version), []byte(partsJSON), nil
}

func (m *metadata) DeleteBlobPartIndex(ctx context.Context, blobHash string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		// DELETE is idempotent: deleting an absent row is not an error.
		_, err := tx.Exec(ctx,
			`DELETE FROM blob_part_index WHERE blob_hash = $1`, blobHash)
		return mapErr(err)
	})
}

func (m *metadata) ListMessagesNeedingPartIndex(ctx context.Context, beforeID store.MessageID, minVersion int, limit int) ([]store.MessageID, error) {
	if limit <= 0 {
		return nil, nil
	}
	upper := int64(beforeID)
	if upper == 0 {
		upper = math.MaxInt64
	}
	rows, err := m.s.pool.Query(ctx, `
		SELECT m.id
		  FROM messages m
		 WHERE NOT EXISTS (
		           SELECT 1 FROM blob_part_index bpi
		            WHERE bpi.blob_hash = m.blob_hash
		              AND bpi.index_version >= $1
		       )
		   AND m.id < $2
		 ORDER BY m.id DESC
		 LIMIT $3`,
		int64(minVersion), upper, limit)
	if err != nil {
		return nil, fmt.Errorf("storepg: list messages needing part index: %w", mapErr(err))
	}
	defer rows.Close()
	out := make([]store.MessageID, 0, limit)
	for rows.Next() {
		var mid int64
		if err := rows.Scan(&mid); err != nil {
			return nil, fmt.Errorf("storepg: list messages needing part index: scan: %w", err)
		}
		out = append(out, store.MessageID(mid))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storepg: list messages needing part index: rows: %w", err)
	}
	return out, nil
}
