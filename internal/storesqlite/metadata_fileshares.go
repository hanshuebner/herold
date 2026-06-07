package storesqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the file-shares store methods for the SQLite
// backend (REQ-SHARE-01..23, REQ-SHARE-10..12, REQ-SHARE-50).
// Schema lives in migrations/0055_file_shares.sql.
// Password and token helpers live in store.HashSharePassword,
// store.VerifySharePassword, store.NewCapabilityToken.

const fileShareSelectCols = `
	id, principal_id, blob_hash, blob_size, filename, content_type,
	created_at_us, expires_at_us, max_downloads, download_count,
	password_hash, state, last_downloaded_at_us, revoked_at_us,
	source_message_id, source_subject, source_recipients`

// scanFileShare scans one row of file_shares into a store.FileShare.
// Column order must match fileShareSelectCols verbatim.
func scanFileShare(row rowLike) (store.FileShare, error) {
	var (
		id, blobHash, filename, contentType, state string
		pid                                        int64
		blobSize, createdUs, expiresUs             int64
		downloadCount                              int64
		maxDownloads                               sql.NullInt64
		passwordHash                               sql.NullString
		lastDownloadedUs                           sql.NullInt64
		revokedUs                                  sql.NullInt64
		srcMessageID                               sql.NullString
		srcSubject                                 sql.NullString
		srcRecipients                              sql.NullString
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
	if maxDownloads.Valid {
		v := maxDownloads.Int64
		fs.MaxDownloads = &v
	}
	if passwordHash.Valid {
		fs.PasswordHash = passwordHash.String
	}
	if lastDownloadedUs.Valid {
		t := fromMicros(lastDownloadedUs.Int64)
		fs.LastDownloadedAt = &t
	}
	if revokedUs.Valid {
		t := fromMicros(revokedUs.Int64)
		fs.RevokedAt = &t
	}
	if srcMessageID.Valid {
		fs.SourceMessageID = srcMessageID.String
	}
	if srcSubject.Valid {
		fs.SourceSubject = srcSubject.String
	}
	if srcRecipients.Valid && srcRecipients.String != "" {
		var recipients []string
		if err := json.Unmarshal([]byte(srcRecipients.String), &recipients); err != nil {
			return store.FileShare{}, fmt.Errorf("storesqlite: decode source_recipients: %w", err)
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

	var passwordHash string
	if req.Password != "" {
		h, err := store.HashSharePassword(m.s.randReader, req.Password)
		if err != nil {
			return store.FileShare{}, err
		}
		passwordHash = h
	}

	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	expiresAt := now.Add(req.PendingTTL)
	expiresAtUs := usMicros(expiresAt)

	var fs store.FileShare
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		// Cap enforcement (REQ-SHARE-12): count active+pending shares for
		// this principal inside the writer transaction to prevent races.
		var count int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_shares
			  WHERE principal_id = ? AND state IN ('pending','active')`,
			int64(req.PrincipalID)).Scan(&count); err != nil {
			return mapErr(err)
		}
		if count >= req.Config.MaxSharesPerPrincipal {
			return fmt.Errorf("principal %d already holds %d file shares: %w",
				int64(req.PrincipalID), count, store.ErrTooManyShares)
		}

		// Quota enforcement (REQ-SHARE-50): measure over distinct blob_hash
		// values referenced by the principal's non-deleted shares. The new
		// blob_hash is only counted if not already present (dedup).
		if req.Config.ShareQuotaPerPrincipal > 0 {
			// Is the blob already referenced by another share of this principal?
			var alreadyRef int64
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM file_shares
				  WHERE principal_id = ? AND blob_hash = ?`,
				int64(req.PrincipalID), req.BlobHash).Scan(&alreadyRef); err != nil {
				return mapErr(err)
			}
			if alreadyRef == 0 {
				// Blob is new to this principal's share set; check quota.
				var currentQuota int64
				if err := tx.QueryRowContext(ctx,
					`SELECT COALESCE(SUM(DISTINCT blob_size), 0)
					   FROM file_shares
					  WHERE principal_id = ?`,
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

		var maxDL any
		if req.MaxDownloads != nil {
			maxDL = *req.MaxDownloads
		}
		var ph any
		if passwordHash != "" {
			ph = passwordHash
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO file_shares
			  (id, principal_id, blob_hash, blob_size, filename, content_type,
			   created_at_us, expires_at_us, max_downloads, download_count,
			   password_hash, state, last_downloaded_at_us, revoked_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'pending', NULL, NULL)`,
			token, int64(req.PrincipalID), req.BlobHash, req.BlobSize,
			req.Filename, req.ContentType,
			nowUs, expiresAtUs, maxDL, ph); err != nil {
			return mapErr(err)
		}

		// Install the blob reference so the GC keeps the blob alive
		// (REQ-SHARE-01, REQ-STORE-12).
		if err := incRef(ctx, tx, req.BlobHash, req.BlobSize, now); err != nil {
			return err
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
			PasswordHash:  passwordHash,
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
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx,
			`SELECT `+fileShareSelectCols+` FROM file_shares WHERE id = ? AND principal_id = ?`,
			id, int64(principalID))
		var err error
		fs, err = scanFileShare(row)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.ErrShareNotConfirmable
			}
			return err
		}
		switch fs.State {
		case store.FileShareStateActive:
			// Idempotent: already active (REQ-SHARE-21). Update source
			// columns only when a non-zero source is provided and the
			// stored source is currently empty, to avoid wiping an existing
			// context with an accidental empty re-confirm.
			if !source.IsZero() {
				if err := updateFileShareSourceSQLite(ctx, tx, id, source, &fs); err != nil {
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
		newExpiresUs := usMicros(newExpires)

		if !source.IsZero() {
			// Encode recipients as JSON; empty slice -> "[]".
			recipientsJSON, err := json.Marshal(source.Recipients)
			if err != nil {
				return fmt.Errorf("storesqlite: encode source_recipients: %w", err)
			}
			var srcMsgID, srcSubj, srcRcp any
			if source.MessageID != "" {
				srcMsgID = source.MessageID
			}
			if source.Subject != "" {
				srcSubj = source.Subject
			}
			if len(source.Recipients) > 0 {
				srcRcp = string(recipientsJSON)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE file_shares
				    SET state = 'active', expires_at_us = ?,
				        source_message_id = ?, source_subject = ?, source_recipients = ?
				  WHERE id = ?`,
				newExpiresUs, srcMsgID, srcSubj, srcRcp, id); err != nil {
				return mapErr(err)
			}
			fs.SourceMessageID = source.MessageID
			fs.SourceSubject = source.Subject
			if len(source.Recipients) > 0 {
				fs.SourceRecipients = source.Recipients
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`UPDATE file_shares
				    SET state = 'active', expires_at_us = ?
				  WHERE id = ?`,
				newExpiresUs, id); err != nil {
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

// updateFileShareSourceSQLite writes source columns on an already-active
// share when the caller provides a non-zero source on a re-confirm.
func updateFileShareSourceSQLite(ctx context.Context, tx *sql.Tx, id string, source store.FileShareSource, fs *store.FileShare) error {
	recipientsJSON, err := json.Marshal(source.Recipients)
	if err != nil {
		return fmt.Errorf("storesqlite: encode source_recipients: %w", err)
	}
	var srcMsgID, srcSubj, srcRcp any
	if source.MessageID != "" {
		srcMsgID = source.MessageID
	}
	if source.Subject != "" {
		srcSubj = source.Subject
	}
	if len(source.Recipients) > 0 {
		srcRcp = string(recipientsJSON)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE file_shares
		    SET source_message_id = ?, source_subject = ?, source_recipients = ?
		  WHERE id = ?`,
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
	return m.runTx(ctx, func(tx *sql.Tx) error {
		now := m.s.clock.Now().UTC()
		res, err := tx.ExecContext(ctx,
			`UPDATE file_shares
			    SET state = 'revoked', revoked_at_us = ?
			  WHERE id = ? AND principal_id = ?`,
			usMicros(now), id, int64(principalID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) DestroyFileShare(ctx context.Context, principalID store.PrincipalID, id string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var blobHash string
		err := tx.QueryRowContext(ctx,
			`SELECT blob_hash FROM file_shares WHERE id = ? AND principal_id = ?`,
			id, int64(principalID)).Scan(&blobHash)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM file_shares WHERE id = ?`, id); err != nil {
			return mapErr(err)
		}
		// Release the blob reference (REQ-SHARE-01).
		return decRef(ctx, tx, blobHash, m.s.clock.Now())
	})
}

func (m *metadata) GetFileShareByID(ctx context.Context, id string) (store.FileShare, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+fileShareSelectCols+` FROM file_shares WHERE id = ?`, id)
	return scanFileShare(row)
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
	var where []string
	where = append(where, "principal_id = ?")
	args = append(args, int64(principalID))

	if filter.State != "" {
		where = append(where, "state = ?")
		args = append(args, string(filter.State))
	}
	if filter.AfterID != "" {
		where = append(where, "id > ?")
		args = append(args, filter.AfterID)
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	args = append(args, limit)
	q := `SELECT ` + fileShareSelectCols + ` FROM file_shares` + whereSQL +
		` ORDER BY created_at_us DESC LIMIT ?`

	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.FileShare
	for rows.Next() {
		fs, err := scanFileShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

func (m *metadata) RecordFileShareDownload(ctx context.Context, id string) (store.FileShare, error) {
	var fs store.FileShare
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		now := m.s.clock.Now().UTC()
		nowUs := usMicros(now)
		// Atomic increment under the writer lock (single-writer SQLite).
		res, err := tx.ExecContext(ctx,
			`UPDATE file_shares
			    SET download_count = download_count + 1,
			        last_downloaded_at_us = ?
			  WHERE id = ?`,
			nowUs, id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		// Return the post-increment row.
		row := tx.QueryRowContext(ctx,
			`SELECT `+fileShareSelectCols+` FROM file_shares WHERE id = ?`, id)
		fs, err = scanFileShare(row)
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

	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// 1. Pending shares older than pending_ttl.
		rows, err := tx.QueryContext(ctx,
			`SELECT id, blob_hash FROM file_shares
			  WHERE state = 'pending' AND created_at_us < ?`,
			pendingCutoff)
		if err != nil {
			return mapErr(err)
		}
		hashes, ids, err := collectIDsAndHashes(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := deleteSharesAndDecRef(ctx, tx, ids, hashes, now); err != nil {
				return err
			}
			stats.DeletedPending = int64(len(ids))
		}

		// 2. Active shares past expires_at.
		rows, err = tx.QueryContext(ctx,
			`SELECT id, blob_hash FROM file_shares
			  WHERE state = 'active' AND expires_at_us < ?`,
			activeCutoff)
		if err != nil {
			return mapErr(err)
		}
		hashes, ids, err = collectIDsAndHashes(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := deleteSharesAndDecRef(ctx, tx, ids, hashes, now); err != nil {
				return err
			}
			stats.DeletedExpired = int64(len(ids))
		}

		// 3. Revoked shares past revoked_grace.
		rows, err = tx.QueryContext(ctx,
			`SELECT id, blob_hash FROM file_shares
			  WHERE state = 'revoked' AND revoked_at_us < ?`,
			revokedCutoff)
		if err != nil {
			return mapErr(err)
		}
		hashes, ids, err = collectIDsAndHashes(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := deleteSharesAndDecRef(ctx, tx, ids, hashes, now); err != nil {
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

// collectIDsAndHashes drains a (id, blob_hash) result set into parallel slices.
func collectIDsAndHashes(rows *sql.Rows) (hashes []string, ids []string, err error) {
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, nil, mapErr(err)
		}
		ids = append(ids, id)
		hashes = append(hashes, hash)
	}
	return hashes, ids, rows.Err()
}

// deleteSharesAndDecRef deletes file_shares rows by id and decrements
// the blob_refs refcount for each hash. Runs inside an existing tx.
func deleteSharesAndDecRef(ctx context.Context, tx *sql.Tx, ids []string, hashes []string, now time.Time) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM file_shares WHERE id = ?`, id); err != nil {
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
	err := m.s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file_shares WHERE blob_hash = ?`, hash).Scan(&count)
	if err != nil {
		return false, mapErr(err)
	}
	return count > 0, nil
}

func (m *metadata) UpdateFileShareExpiry(ctx context.Context, principalID store.PrincipalID, id string, newExpiry time.Time) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			expiresUs int64
			state     string
		)
		err := tx.QueryRowContext(ctx,
			`SELECT expires_at_us, state FROM file_shares WHERE id = ? AND principal_id = ?`,
			id, int64(principalID)).Scan(&expiresUs, &state)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
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
		_, err = tx.ExecContext(ctx,
			`UPDATE file_shares SET expires_at_us = ? WHERE id = ?`,
			usMicros(newExpiry), id)
		return mapErr(err)
	})
}

func (m *metadata) UpdateFileShareMaxDownloads(ctx context.Context, principalID store.PrincipalID, id string, newMax int64) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			maxDownloads  sql.NullInt64
			downloadCount int64
			state         string
		)
		err := tx.QueryRowContext(ctx,
			`SELECT max_downloads, download_count, state FROM file_shares WHERE id = ? AND principal_id = ?`,
			id, int64(principalID)).Scan(&maxDownloads, &downloadCount, &state)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
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
		if maxDownloads.Valid && newMax >= maxDownloads.Int64 {
			return fmt.Errorf("share %s: newMax %d >= current max_downloads %d: %w",
				id, newMax, maxDownloads.Int64, store.ErrShareMaxDownloadsNotLowerable)
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE file_shares SET max_downloads = ? WHERE id = ?`,
			newMax, id)
		return mapErr(err)
	})
}
