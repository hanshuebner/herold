package storepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the file-shares store methods for the Postgres
// backend (REQ-SHARE-01..23, REQ-SHARE-10..12, REQ-SHARE-50).
// Schema lives in migrations/0055_file_shares.sql.
// Mirrors internal/storesqlite/metadata_fileshares.go.
// Password and token helpers live in store.HashSharePassword,
// store.VerifySharePassword, store.NewCapabilityToken.

const fileShareSelectColsPG = `
	id, principal_id, blob_hash, blob_size, filename, content_type,
	created_at_us, expires_at_us, max_downloads, download_count,
	password_hash, state, last_downloaded_at_us, revoked_at_us,
	source_message_id, source_subject, source_recipients`

func scanFileSharePG(row rowLike) (store.FileShare, error) {
	var (
		id, blobHash, filename, contentType, state string
		pid                                        int64
		blobSize, createdUs, expiresUs             int64
		downloadCount                              int64
		maxDownloads                               *int64
		passwordHash                               *string
		lastDownloadedUs                           *int64
		revokedUs                                  *int64
		srcMessageID                               *string
		srcSubject                                 *string
		srcRecipients                              *string
	)
	if err := row.Scan(
		&id, &pid, &blobHash, &blobSize, &filename, &contentType,
		&createdUs, &expiresUs, &maxDownloads, &downloadCount,
		&passwordHash, &state, &lastDownloadedUs, &revokedUs,
		&srcMessageID, &srcSubject, &srcRecipients,
	); err != nil {
		return store.FileShare{}, mapErr(err)
	}
	fs := store.FileShare{
		ID:            id,
		PrincipalID:   store.PrincipalID(pid),
		BlobHash:      blobHash,
		BlobSize:      blobSize,
		Filename:      filename,
		ContentType:   contentType,
		CreatedAt:     fromMicros(createdUs),
		ExpiresAt:     fromMicros(expiresUs),
		DownloadCount: downloadCount,
		State:         store.FileShareState(state),
	}
	if maxDownloads != nil {
		v := *maxDownloads
		fs.MaxDownloads = &v
	}
	if passwordHash != nil {
		fs.PasswordHash = *passwordHash
	}
	if lastDownloadedUs != nil {
		t := fromMicros(*lastDownloadedUs)
		fs.LastDownloadedAt = &t
	}
	if revokedUs != nil {
		t := fromMicros(*revokedUs)
		fs.RevokedAt = &t
	}
	if srcMessageID != nil {
		fs.SourceMessageID = *srcMessageID
	}
	if srcSubject != nil {
		fs.SourceSubject = *srcSubject
	}
	if srcRecipients != nil && *srcRecipients != "" {
		var recipients []string
		if err := json.Unmarshal([]byte(*srcRecipients), &recipients); err != nil {
			return store.FileShare{}, fmt.Errorf("storepg: decode source_recipients: %w", err)
		}
		fs.SourceRecipients = recipients
	}
	return fs, nil
}

func (m *metadata) CreateFileShare(ctx context.Context, req store.FileShareCreate) (store.FileShare, error) {
	if req.BlobHash == "" {
		return store.FileShare{}, fmt.Errorf("%w: file share: empty blob_hash", store.ErrInvalidArgument)
	}
	if req.Filename == "" {
		return store.FileShare{}, fmt.Errorf("%w: file share: empty filename", store.ErrInvalidArgument)
	}
	if req.ContentType == "" {
		return store.FileShare{}, fmt.Errorf("%w: file share: empty content_type", store.ErrInvalidArgument)
	}
	if req.PendingTTL <= 0 {
		return store.FileShare{}, fmt.Errorf("%w: file share: non-positive pending_ttl", store.ErrInvalidArgument)
	}

	token, err := store.NewCapabilityToken(m.s.randReader)
	if err != nil {
		return store.FileShare{}, err
	}

	var passwordHash *string
	if req.Password != "" {
		h, err := store.HashSharePassword(m.s.randReader, req.Password)
		if err != nil {
			return store.FileShare{}, err
		}
		passwordHash = &h
	}

	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	expiresAt := now.Add(req.PendingTTL)
	expiresAtUs := usMicros(expiresAt)

	var fs store.FileShare
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		// Cap enforcement (REQ-SHARE-12).
		var count int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM file_shares
			  WHERE principal_id = $1 AND state IN ('pending','active')`,
			int64(req.PrincipalID)).Scan(&count); err != nil {
			return mapErr(err)
		}
		if count >= req.Config.MaxSharesPerPrincipal {
			return fmt.Errorf("principal %d already holds %d file shares: %w",
				int64(req.PrincipalID), count, store.ErrTooManyShares)
		}

		// Quota enforcement (REQ-SHARE-50): dedup-aware.
		if req.Config.ShareQuotaPerPrincipal > 0 {
			var alreadyRef int64
			if err := tx.QueryRow(ctx,
				`SELECT COUNT(*) FROM file_shares
				  WHERE principal_id = $1 AND blob_hash = $2`,
				int64(req.PrincipalID), req.BlobHash).Scan(&alreadyRef); err != nil {
				return mapErr(err)
			}
			if alreadyRef == 0 {
				var currentQuota int64
				if err := tx.QueryRow(ctx,
					`SELECT COALESCE(SUM(DISTINCT blob_size), 0)
					   FROM file_shares
					  WHERE principal_id = $1`,
					int64(req.PrincipalID)).Scan(&currentQuota); err != nil {
					return mapErr(err)
				}
				if currentQuota+req.BlobSize > req.Config.ShareQuotaPerPrincipal {
					return fmt.Errorf("principal %d share quota exceeded (%d + %d > %d): %w",
						int64(req.PrincipalID), currentQuota, req.BlobSize,
						req.Config.ShareQuotaPerPrincipal, store.ErrQuotaExceeded)
				}
			}
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO file_shares
			  (id, principal_id, blob_hash, blob_size, filename, content_type,
			   created_at_us, expires_at_us, max_downloads, download_count,
			   password_hash, state, last_downloaded_at_us, revoked_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 0, $10, 'pending', NULL, NULL)`,
			token, int64(req.PrincipalID), req.BlobHash, req.BlobSize,
			req.Filename, req.ContentType,
			nowUs, expiresAtUs, req.MaxDownloads, passwordHash); err != nil {
			return mapErr(err)
		}

		// Install the blob reference (REQ-SHARE-01, REQ-STORE-12).
		if err := incRef(ctx, tx, req.BlobHash, req.BlobSize, now); err != nil {
			return err
		}

		var phStr string
		if passwordHash != nil {
			phStr = *passwordHash
		}
		fs = store.FileShare{
			ID:            token,
			PrincipalID:   req.PrincipalID,
			BlobHash:      req.BlobHash,
			BlobSize:      req.BlobSize,
			Filename:      req.Filename,
			ContentType:   req.ContentType,
			CreatedAt:     now,
			ExpiresAt:     expiresAt,
			MaxDownloads:  req.MaxDownloads,
			DownloadCount: 0,
			PasswordHash:  phStr,
			State:         store.FileShareStatePending,
		}
		return nil
	})
	if err != nil {
		return store.FileShare{}, err
	}
	return fs, nil
}

func (m *metadata) ConfirmFileShare(ctx context.Context, principalID store.PrincipalID, id string, cfg store.FileSharesConfig, source store.FileShareSource) (store.FileShare, error) {
	var fs store.FileShare
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT `+fileShareSelectColsPG+` FROM file_shares WHERE id = $1 AND principal_id = $2`,
			id, int64(principalID))
		var err error
		fs, err = scanFileSharePG(row)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.ErrShareNotConfirmable
			}
			return err
		}
		switch fs.State {
		case store.FileShareStateActive:
			// Idempotent (REQ-SHARE-21). Update source columns when a
			// non-zero source is provided on a re-confirm.
			if !source.IsZero() {
				if err := updateFileShareSourcePG(ctx, tx, id, source, &fs); err != nil {
					return err
				}
			}
			return nil
		case store.FileShareStateRevoked:
			return fmt.Errorf("share %s is revoked: %w", id, store.ErrShareNotConfirmable)
		case store.FileShareStatePending:
			// proceed
		default:
			return fmt.Errorf("share %s has unknown state %q: %w", id, fs.State, store.ErrShareNotConfirmable)
		}

		now := m.s.clock.Now().UTC()
		newExpires := now.Add(cfg.DefaultTTL)
		maxExpires := now.Add(cfg.MaxTTL)
		if newExpires.After(maxExpires) {
			newExpires = maxExpires
		}

		if !source.IsZero() {
			recipientsJSON, err := json.Marshal(source.Recipients)
			if err != nil {
				return fmt.Errorf("storepg: encode source_recipients: %w", err)
			}
			var srcMsgID, srcSubj, srcRcp *string
			if source.MessageID != "" {
				srcMsgID = &source.MessageID
			}
			if source.Subject != "" {
				srcSubj = &source.Subject
			}
			if len(source.Recipients) > 0 {
				s := string(recipientsJSON)
				srcRcp = &s
			}
			if _, err := tx.Exec(ctx,
				`UPDATE file_shares
				    SET state = 'active', expires_at_us = $1,
				        source_message_id = $2, source_subject = $3, source_recipients = $4
				  WHERE id = $5`,
				usMicros(newExpires), srcMsgID, srcSubj, srcRcp, id); err != nil {
				return mapErr(err)
			}
			fs.SourceMessageID = source.MessageID
			fs.SourceSubject = source.Subject
			if len(source.Recipients) > 0 {
				fs.SourceRecipients = source.Recipients
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE file_shares SET state = 'active', expires_at_us = $1 WHERE id = $2`,
				usMicros(newExpires), id); err != nil {
				return mapErr(err)
			}
		}
		fs.State = store.FileShareStateActive
		fs.ExpiresAt = newExpires
		return nil
	})
	if err != nil {
		return store.FileShare{}, err
	}
	return fs, nil
}

// updateFileShareSourcePG writes source columns on an already-active share
// when the caller provides a non-zero source on a re-confirm.
func updateFileShareSourcePG(ctx context.Context, tx pgx.Tx, id string, source store.FileShareSource, fs *store.FileShare) error {
	recipientsJSON, err := json.Marshal(source.Recipients)
	if err != nil {
		return fmt.Errorf("storepg: encode source_recipients: %w", err)
	}
	var srcMsgID, srcSubj, srcRcp *string
	if source.MessageID != "" {
		srcMsgID = &source.MessageID
	}
	if source.Subject != "" {
		srcSubj = &source.Subject
	}
	if len(source.Recipients) > 0 {
		s := string(recipientsJSON)
		srcRcp = &s
	}
	if _, err := tx.Exec(ctx,
		`UPDATE file_shares
		    SET source_message_id = $1, source_subject = $2, source_recipients = $3
		  WHERE id = $4`,
		srcMsgID, srcSubj, srcRcp, id); err != nil {
		return mapErr(err)
	}
	fs.SourceMessageID = source.MessageID
	fs.SourceSubject = source.Subject
	if len(source.Recipients) > 0 {
		fs.SourceRecipients = source.Recipients
	}
	return nil
}

func (m *metadata) RevokeFileShare(ctx context.Context, principalID store.PrincipalID, id string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		now := m.s.clock.Now().UTC()
		tag, err := tx.Exec(ctx,
			`UPDATE file_shares
			    SET state = 'revoked', revoked_at_us = $1
			  WHERE id = $2 AND principal_id = $3`,
			usMicros(now), id, int64(principalID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) DestroyFileShare(ctx context.Context, principalID store.PrincipalID, id string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var blobHash string
		err := tx.QueryRow(ctx,
			`SELECT blob_hash FROM file_shares WHERE id = $1 AND principal_id = $2`,
			id, int64(principalID)).Scan(&blobHash)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM file_shares WHERE id = $1`, id); err != nil {
			return mapErr(err)
		}
		return decRef(ctx, tx, blobHash, m.s.clock.Now())
	})
}

func (m *metadata) GetFileShareByID(ctx context.Context, id string) (store.FileShare, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+fileShareSelectColsPG+` FROM file_shares WHERE id = $1`, id)
	return scanFileSharePG(row)
}

func (m *metadata) ListFileSharesByPrincipal(ctx context.Context, principalID store.PrincipalID, filter store.FileShareListFilter) ([]store.FileShare, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	var args []any
	paramIdx := 1
	var where []string

	where = append(where, fmt.Sprintf("principal_id = $%d", paramIdx))
	args = append(args, int64(principalID))
	paramIdx++

	if filter.State != "" {
		where = append(where, fmt.Sprintf("state = $%d", paramIdx))
		args = append(args, string(filter.State))
		paramIdx++
	}
	if filter.AfterID != "" {
		where = append(where, fmt.Sprintf("id > $%d", paramIdx))
		args = append(args, filter.AfterID)
		paramIdx++
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	args = append(args, limit)
	q := fmt.Sprintf(`SELECT %s FROM file_shares%s ORDER BY created_at_us DESC LIMIT $%d`,
		fileShareSelectColsPG, whereSQL, paramIdx)

	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.FileShare
	for rows.Next() {
		fs, err := scanFileSharePG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

func (m *metadata) RecordFileShareDownload(ctx context.Context, id string) (store.FileShare, error) {
	var fs store.FileShare
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		now := m.s.clock.Now().UTC()
		tag, err := tx.Exec(ctx,
			`UPDATE file_shares
			    SET download_count = download_count + 1,
			        last_downloaded_at_us = $1
			  WHERE id = $2`,
			usMicros(now), id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		row := tx.QueryRow(ctx,
			`SELECT `+fileShareSelectColsPG+` FROM file_shares WHERE id = $1`, id)
		fs, err = scanFileSharePG(row)
		return err
	})
	if err != nil {
		return store.FileShare{}, err
	}
	return fs, nil
}

func (m *metadata) SweepFileShares(ctx context.Context, now time.Time, cfg store.FileSharesConfig) (store.SweepStats, error) {
	var stats store.SweepStats
	pendingCutoff := usMicros(now.Add(-cfg.PendingTTL))
	activeCutoff := usMicros(now)
	revokedCutoff := usMicros(now.Add(-cfg.RevokedGrace))

	err := m.runTx(ctx, func(tx pgx.Tx) error {
		// 1. Pending shares older than pending_ttl.
		ids, hashes, err := collectIDsAndHashesPG(ctx, tx,
			`SELECT id, blob_hash FROM file_shares WHERE state = 'pending' AND created_at_us < $1`,
			pendingCutoff)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := deleteSharesAndDecRefPG(ctx, tx, ids, hashes, now); err != nil {
				return err
			}
			stats.DeletedPending = int64(len(ids))
		}

		// 2. Active shares past expires_at.
		ids, hashes, err = collectIDsAndHashesPG(ctx, tx,
			`SELECT id, blob_hash FROM file_shares WHERE state = 'active' AND expires_at_us < $1`,
			activeCutoff)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := deleteSharesAndDecRefPG(ctx, tx, ids, hashes, now); err != nil {
				return err
			}
			stats.DeletedExpired = int64(len(ids))
		}

		// 3. Revoked shares past revoked_grace.
		ids, hashes, err = collectIDsAndHashesPG(ctx, tx,
			`SELECT id, blob_hash FROM file_shares WHERE state = 'revoked' AND revoked_at_us < $1`,
			revokedCutoff)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := deleteSharesAndDecRefPG(ctx, tx, ids, hashes, now); err != nil {
				return err
			}
			stats.DeletedRevoked = int64(len(ids))
		}

		return nil
	})
	if err != nil {
		return store.SweepStats{}, err
	}
	return stats, nil
}

func collectIDsAndHashesPG(ctx context.Context, tx pgx.Tx, q string, arg any) (ids, hashes []string, err error) {
	rows, err := tx.Query(ctx, q, arg)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, nil, mapErr(err)
		}
		ids = append(ids, id)
		hashes = append(hashes, hash)
	}
	return ids, hashes, rows.Err()
}

func deleteSharesAndDecRefPG(ctx context.Context, tx pgx.Tx, ids []string, hashes []string, now time.Time) error {
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `DELETE FROM file_shares WHERE id = $1`, id); err != nil {
			return mapErr(err)
		}
	}
	for _, hash := range hashes {
		if err := decRef(ctx, tx, hash, now); err != nil {
			return err
		}
	}
	return nil
}

func (m *metadata) IsFileShareBlobReferenced(ctx context.Context, hash string) (bool, error) {
	var count int64
	err := m.s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM file_shares WHERE blob_hash = $1`, hash).Scan(&count)
	if err != nil {
		return false, mapErr(err)
	}
	return count > 0, nil
}

func (m *metadata) UpdateFileShareExpiry(ctx context.Context, principalID store.PrincipalID, id string, newExpiry time.Time) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			expiresUs int64
			state     string
		)
		err := tx.QueryRow(ctx,
			`SELECT expires_at_us, state FROM file_shares WHERE id = $1 AND principal_id = $2`,
			id, int64(principalID)).Scan(&expiresUs, &state)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		if store.FileShareState(state) == store.FileShareStateRevoked {
			return fmt.Errorf("share %s is revoked: %w", id, store.ErrShareNotConfirmable)
		}
		now := m.s.clock.Now().UTC()
		newExpiry = newExpiry.UTC()
		if !newExpiry.After(now) {
			return fmt.Errorf("share %s: newExpiry is not in the future: %w", id, store.ErrShareExpiryNotShortenable)
		}
		current := fromMicros(expiresUs)
		if !newExpiry.Before(current) {
			return fmt.Errorf("share %s: newExpiry %v is not before current %v: %w",
				id, newExpiry, current, store.ErrShareExpiryNotShortenable)
		}
		_, err = tx.Exec(ctx,
			`UPDATE file_shares SET expires_at_us = $1 WHERE id = $2`,
			usMicros(newExpiry), id)
		return mapErr(err)
	})
}

func (m *metadata) UpdateFileShareMaxDownloads(ctx context.Context, principalID store.PrincipalID, id string, newMax int64) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			maxDownloads  *int64
			downloadCount int64
			state         string
		)
		err := tx.QueryRow(ctx,
			`SELECT max_downloads, download_count, state FROM file_shares WHERE id = $1 AND principal_id = $2`,
			id, int64(principalID)).Scan(&maxDownloads, &downloadCount, &state)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		if store.FileShareState(state) == store.FileShareStateRevoked {
			return fmt.Errorf("share %s is revoked: %w", id, store.ErrShareNotConfirmable)
		}
		// newMax must be >= download_count.
		if newMax < downloadCount {
			return fmt.Errorf("share %s: newMax %d < download_count %d: %w",
				id, newMax, downloadCount, store.ErrShareMaxDownloadsNotLowerable)
		}
		// newMax must be strictly less than current MaxDownloads, unless current is unlimited (nil).
		if maxDownloads != nil && newMax >= *maxDownloads {
			return fmt.Errorf("share %s: newMax %d >= current max_downloads %d: %w",
				id, newMax, *maxDownloads, store.ErrShareMaxDownloadsNotLowerable)
		}
		_, err = tx.Exec(ctx,
			`UPDATE file_shares SET max_downloads = $1 WHERE id = $2`,
			newMax, id)
		return mapErr(err)
	})
}
