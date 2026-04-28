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

// This file implements the Phase 2 store.Metadata methods (queue,
// DKIM keys, ACME, webhooks, DMARC, mailbox ACL, JMAP states,
// TLS-RPT) against the SQLite backend. The Phase 1 methods live in
// metadata.go alongside the original implementation; the split keeps
// diff review readable. Helpers shared with metadata.go (mapErr,
// runTx, usMicros, fromMicros) come from the same package.

// -- queue ------------------------------------------------------------

// scanQueueItem reads a row produced by a queue SELECT into a
// QueueItem. The scan order MUST match the column order used by the
// callers that build the SELECT.
func scanQueueItem(row rowLike) (store.QueueItem, error) {
	var (
		id, principalID                       sql.NullInt64
		state                                 int64
		attempts                              int64
		lastAttemptUs, nextAttemptUs          int64
		dsnFlags, dsnRet                      int64
		createdUs                             int64
		mailFrom, rcptTo, envID, bodyHash     string
		headersHash, lastErr, dsnEnvID, orcpt string
		idemp                                 sql.NullString
	)
	err := row.Scan(&id, &principalID, &mailFrom, &rcptTo, &envID,
		&bodyHash, &headersHash, &state, &attempts, &lastAttemptUs,
		&nextAttemptUs, &lastErr, &dsnFlags, &dsnRet, &dsnEnvID, &orcpt,
		&idemp, &createdUs)
	if err != nil {
		return store.QueueItem{}, mapErr(err)
	}
	q := store.QueueItem{
		ID:              store.QueueItemID(id.Int64),
		MailFrom:        mailFrom,
		RcptTo:          rcptTo,
		EnvelopeID:      store.EnvelopeID(envID),
		BodyBlobHash:    bodyHash,
		HeadersBlobHash: headersHash,
		State:           store.QueueState(state),
		Attempts:        int32(attempts),
		LastAttemptAt:   fromMicros(lastAttemptUs),
		NextAttemptAt:   fromMicros(nextAttemptUs),
		LastError:       lastErr,
		DSNNotify:       store.DSNNotifyFlags(dsnFlags),
		DSNRet:          store.DSNRet(dsnRet),
		DSNEnvID:        dsnEnvID,
		DSNOrcpt:        orcpt,
		CreatedAt:       fromMicros(createdUs),
	}
	if principalID.Valid {
		q.PrincipalID = store.PrincipalID(principalID.Int64)
	}
	if idemp.Valid {
		q.IdempotencyKey = idemp.String
	}
	return q, nil
}

const queueSelectColumns = `
	id, principal_id, mail_from, rcpt_to, envelope_id,
	body_blob_hash, headers_blob_hash, state, attempts, last_attempt_at_us,
	next_attempt_at_us, last_error, dsn_notify_flags, dsn_ret, dsn_envid,
	dsn_orcpt, idempotency_key, created_at_us`

func (m *metadata) EnqueueMessage(ctx context.Context, item store.QueueItem) (store.QueueItemID, error) {
	now := m.s.clock.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.NextAttemptAt.IsZero() {
		item.NextAttemptAt = now
	}
	if item.State == store.QueueStateUnknown {
		item.State = store.QueueStateQueued
	}
	var id int64
	var pid any
	if item.PrincipalID != 0 {
		pid = int64(item.PrincipalID)
	}
	var idemp any
	if item.IdempotencyKey != "" {
		idemp = item.IdempotencyKey
	}
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Idempotency dedupe: when a row with the same key exists,
		// surface ErrConflict carrying the existing id.
		if item.IdempotencyKey != "" {
			var prior int64
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM queue WHERE idempotency_key = ?`,
				item.IdempotencyKey).Scan(&prior)
			if err == nil {
				id = prior
				return fmt.Errorf("queue idempotency key %q: %w", item.IdempotencyKey, store.ErrConflict)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return mapErr(err)
			}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO queue (principal_id, mail_from, rcpt_to, envelope_id,
			  body_blob_hash, headers_blob_hash, state, attempts, last_attempt_at_us,
			  next_attempt_at_us, last_error, dsn_notify_flags, dsn_ret, dsn_envid,
			  dsn_orcpt, idempotency_key, created_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pid, strings.ToLower(item.MailFrom), strings.ToLower(item.RcptTo),
			string(item.EnvelopeID), item.BodyBlobHash, item.HeadersBlobHash,
			int64(item.State), int64(item.Attempts), usMicros(item.LastAttemptAt),
			usMicros(item.NextAttemptAt), item.LastError,
			int64(item.DSNNotify), int64(item.DSNRet), item.DSNEnvID, item.DSNOrcpt,
			idemp, usMicros(item.CreatedAt))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		// Increment body blob refcount.
		if item.BodyBlobHash != "" {
			if err := incRef(ctx, tx, item.BodyBlobHash, 0, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Idempotency-conflict path: id is already populated.
		if errors.Is(err, store.ErrConflict) && item.IdempotencyKey != "" {
			return store.QueueItemID(id), err
		}
		return 0, err
	}
	return store.QueueItemID(id), nil
}

func (m *metadata) ClaimDueQueueItems(ctx context.Context, now time.Time, max int) ([]store.QueueItem, error) {
	if max <= 0 {
		max = 100
	}
	var out []store.QueueItem
	nowUs := usMicros(now)
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT `+queueSelectColumns+`
			  FROM queue
			 WHERE state IN (?, ?) AND next_attempt_at_us <= ?
			 ORDER BY next_attempt_at_us ASC, id ASC
			 LIMIT ?`,
			int64(store.QueueStateQueued), int64(store.QueueStateDeferred), nowUs, max)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			q, err := scanQueueItem(rows)
			if err != nil {
				return err
			}
			out = append(out, q)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		// Transition each claimed row to inflight in the same tx.
		for i := range out {
			if _, err := tx.ExecContext(ctx, `
				UPDATE queue SET state = ?, last_attempt_at_us = ?
				 WHERE id = ?`,
				int64(store.QueueStateInflight), nowUs, int64(out[i].ID)); err != nil {
				return mapErr(err)
			}
			out[i].State = store.QueueStateInflight
			out[i].LastAttemptAt = now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (m *metadata) CompleteQueueItem(ctx context.Context, id store.QueueItemID, success bool, errMsg string) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var bodyHash string
		err := tx.QueryRowContext(ctx,
			`SELECT body_blob_hash FROM queue WHERE id = ?`, int64(id)).Scan(&bodyHash)
		if err != nil {
			return mapErr(err)
		}
		newState := store.QueueStateDone
		if !success {
			newState = store.QueueStateFailed
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE queue SET state = ?, last_error = ?, last_attempt_at_us = ?
			 WHERE id = ?`,
			int64(newState), errMsg, usMicros(now), int64(id))
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
		if bodyHash != "" {
			if err := decRef(ctx, tx, bodyHash, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *metadata) RescheduleQueueItem(ctx context.Context, id store.QueueItemID, nextAttempt time.Time, errMsg string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE queue
			   SET state = ?, attempts = attempts + 1,
			       next_attempt_at_us = ?, last_error = ?
			 WHERE id = ?`,
			int64(store.QueueStateDeferred), usMicros(nextAttempt),
			errMsg, int64(id))
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

func (m *metadata) HoldQueueItem(ctx context.Context, id store.QueueItemID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE queue SET state = ? WHERE id = ?`,
			int64(store.QueueStateHeld), int64(id))
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

func (m *metadata) ReleaseQueueItem(ctx context.Context, id store.QueueItemID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE queue SET state = ?, next_attempt_at_us = ? WHERE id = ?`,
			int64(store.QueueStateQueued), usMicros(now), int64(id))
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

func (m *metadata) DeleteQueueItem(ctx context.Context, id store.QueueItemID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var bodyHash string
		err := tx.QueryRowContext(ctx,
			`SELECT body_blob_hash FROM queue WHERE id = ?`, int64(id)).Scan(&bodyHash)
		if err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM queue WHERE id = ?`, int64(id)); err != nil {
			return mapErr(err)
		}
		if bodyHash != "" {
			if err := decRef(ctx, tx, bodyHash, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *metadata) GetQueueItem(ctx context.Context, id store.QueueItemID) (store.QueueItem, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+queueSelectColumns+` FROM queue WHERE id = ?`, int64(id))
	return scanQueueItem(row)
}

func (m *metadata) ListQueueItems(ctx context.Context, filter store.QueueFilter) ([]store.QueueItem, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var where []string
	var args []any
	if filter.AfterID != 0 {
		where = append(where, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	if filter.State != store.QueueStateUnknown {
		where = append(where, "state = ?")
		args = append(args, int64(filter.State))
	}
	if filter.PrincipalID != 0 {
		where = append(where, "principal_id = ?")
		args = append(args, int64(filter.PrincipalID))
	}
	if filter.EnvelopeID != "" {
		where = append(where, "envelope_id = ?")
		args = append(args, string(filter.EnvelopeID))
	}
	if filter.RecipientDomain != "" {
		where = append(where, "instr(rcpt_to, ?) = (length(rcpt_to) - length(?) + 1)")
		dom := "@" + strings.ToLower(filter.RecipientDomain)
		args = append(args, dom, dom)
	}
	q := `SELECT ` + queueSelectColumns + ` FROM queue`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.QueueItem
	for rows.Next() {
		q, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (m *metadata) CountQueueByState(ctx context.Context) (map[store.QueueState]int, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT state, COUNT(*) FROM queue GROUP BY state`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make(map[store.QueueState]int)
	for rows.Next() {
		var state, n int64
		if err := rows.Scan(&state, &n); err != nil {
			return nil, mapErr(err)
		}
		out[store.QueueState(state)] = int(n)
	}
	return out, rows.Err()
}

// -- DKIM keys --------------------------------------------------------

func (m *metadata) UpsertDKIMKey(ctx context.Context, key store.DKIMKey) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO dkim_keys (domain, selector, algorithm, private_key_pem,
			  public_key_b64, status, created_at_us, rotated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(domain, selector) DO UPDATE SET
			  algorithm = excluded.algorithm,
			  private_key_pem = excluded.private_key_pem,
			  public_key_b64 = excluded.public_key_b64,
			  status = excluded.status,
			  rotated_at_us = ?`,
			strings.ToLower(key.Domain), key.Selector, int64(key.Algorithm),
			key.PrivateKeyPEM, key.PublicKeyB64, int64(key.Status),
			usMicros(now), usMicros(now), usMicros(now))
		return mapErr(err)
	})
}

func scanDKIMKey(row rowLike) (store.DKIMKey, error) {
	var (
		id                   int64
		domain, selector     string
		algorithm, status    int64
		privPEM, pubB64      string
		createdUs, rotatedUs int64
	)
	err := row.Scan(&id, &domain, &selector, &algorithm, &privPEM, &pubB64,
		&status, &createdUs, &rotatedUs)
	if err != nil {
		return store.DKIMKey{}, mapErr(err)
	}
	return store.DKIMKey{
		ID:            store.DKIMKeyID(id),
		Domain:        domain,
		Selector:      selector,
		Algorithm:     store.DKIMAlgorithm(algorithm),
		PrivateKeyPEM: privPEM,
		PublicKeyB64:  pubB64,
		Status:        store.DKIMKeyStatus(status),
		CreatedAt:     fromMicros(createdUs),
		RotatedAt:     fromMicros(rotatedUs),
	}, nil
}

func (m *metadata) GetActiveDKIMKey(ctx context.Context, domain string) (store.DKIMKey, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, domain, selector, algorithm, private_key_pem, public_key_b64,
		       status, created_at_us, rotated_at_us
		  FROM dkim_keys
		 WHERE domain = ? AND status = ?
		 ORDER BY id DESC LIMIT 1`,
		strings.ToLower(domain), int64(store.DKIMKeyStatusActive))
	return scanDKIMKey(row)
}

func (m *metadata) ListDKIMKeys(ctx context.Context, domain string) ([]store.DKIMKey, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, domain, selector, algorithm, private_key_pem, public_key_b64,
		       status, created_at_us, rotated_at_us
		  FROM dkim_keys WHERE domain = ? ORDER BY selector`,
		strings.ToLower(domain))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.DKIMKey
	for rows.Next() {
		k, err := scanDKIMKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (m *metadata) RotateDKIMKey(ctx context.Context, domain, oldSelector string, newKey store.DKIMKey) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Retire the old key.
		res, err := tx.ExecContext(ctx, `
			UPDATE dkim_keys
			   SET status = ?, rotated_at_us = ?
			 WHERE domain = ? AND selector = ?`,
			int64(store.DKIMKeyStatusRetiring), usMicros(now),
			strings.ToLower(domain), oldSelector)
		if err != nil {
			return mapErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("dkim retire %q/%q: %w", domain, oldSelector, store.ErrNotFound)
		}
		// Insert the new key as active.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO dkim_keys (domain, selector, algorithm, private_key_pem,
			  public_key_b64, status, created_at_us, rotated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(domain, selector) DO UPDATE SET
			  algorithm = excluded.algorithm,
			  private_key_pem = excluded.private_key_pem,
			  public_key_b64 = excluded.public_key_b64,
			  status = excluded.status,
			  rotated_at_us = ?`,
			strings.ToLower(newKey.Domain), newKey.Selector, int64(newKey.Algorithm),
			newKey.PrivateKeyPEM, newKey.PublicKeyB64,
			int64(store.DKIMKeyStatusActive), usMicros(now), usMicros(now), usMicros(now))
		return mapErr(err)
	})
}

// -- ACME -------------------------------------------------------------

func (m *metadata) UpsertACMEAccount(ctx context.Context, acc store.ACMEAccount) (store.ACMEAccount, error) {
	now := m.s.clock.Now().UTC()
	if acc.CreatedAt.IsZero() {
		acc.CreatedAt = now
	}
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// First check whether the row exists.
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM acme_accounts WHERE directory_url = ? AND contact_email = ?`,
			acc.DirectoryURL, strings.ToLower(acc.ContactEmail)).Scan(&id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `
				UPDATE acme_accounts
				   SET account_key_pem = ?, kid = ?
				 WHERE id = ?`, acc.AccountKeyPEM, acc.KID, id)
			return mapErr(err)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO acme_accounts (directory_url, contact_email,
			  account_key_pem, kid, created_at_us)
			VALUES (?, ?, ?, ?, ?)`,
			acc.DirectoryURL, strings.ToLower(acc.ContactEmail),
			acc.AccountKeyPEM, acc.KID, usMicros(acc.CreatedAt))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.ACMEAccount{}, err
	}
	acc.ID = store.ACMEAccountID(id)
	acc.ContactEmail = strings.ToLower(acc.ContactEmail)
	return acc, nil
}

func scanACMEAccount(row rowLike) (store.ACMEAccount, error) {
	var (
		id                         int64
		directoryURL, contactEmail string
		accountKeyPEM, kid         string
		createdUs                  int64
	)
	err := row.Scan(&id, &directoryURL, &contactEmail, &accountKeyPEM, &kid, &createdUs)
	if err != nil {
		return store.ACMEAccount{}, mapErr(err)
	}
	return store.ACMEAccount{
		ID:            store.ACMEAccountID(id),
		DirectoryURL:  directoryURL,
		ContactEmail:  contactEmail,
		AccountKeyPEM: accountKeyPEM,
		KID:           kid,
		CreatedAt:     fromMicros(createdUs),
	}, nil
}

func (m *metadata) GetACMEAccount(ctx context.Context, directoryURL, contactEmail string) (store.ACMEAccount, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, directory_url, contact_email, account_key_pem, kid, created_at_us
		  FROM acme_accounts
		 WHERE directory_url = ? AND contact_email = ?`,
		directoryURL, strings.ToLower(contactEmail))
	return scanACMEAccount(row)
}

func (m *metadata) ListACMEAccounts(ctx context.Context) ([]store.ACMEAccount, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, directory_url, contact_email, account_key_pem, kid, created_at_us
		  FROM acme_accounts ORDER BY id ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ACMEAccount
	for rows.Next() {
		a, err := scanACMEAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (m *metadata) InsertACMEOrder(ctx context.Context, order store.ACMEOrder) (store.ACMEOrder, error) {
	now := m.s.clock.Now().UTC()
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = now
	}
	hostnamesJSON, err := json.Marshal(order.Hostnames)
	if err != nil {
		return store.ACMEOrder{}, fmt.Errorf("storesqlite: encode acme hostnames: %w", err)
	}
	var id int64
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO acme_orders (account_id, hostnames_json, status, order_url,
			  finalize_url, certificate_url, challenge_type, updated_at_us, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			int64(order.AccountID), string(hostnamesJSON), int64(order.Status),
			order.OrderURL, order.FinalizeURL, order.CertificateURL,
			int64(order.ChallengeType), usMicros(order.UpdatedAt), order.Error)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.ACMEOrder{}, err
	}
	order.ID = store.ACMEOrderID(id)
	return order, nil
}

func (m *metadata) UpdateACMEOrder(ctx context.Context, order store.ACMEOrder) error {
	now := m.s.clock.Now().UTC()
	hostnamesJSON, err := json.Marshal(order.Hostnames)
	if err != nil {
		return fmt.Errorf("storesqlite: encode acme hostnames: %w", err)
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE acme_orders
			   SET hostnames_json = ?, status = ?, order_url = ?, finalize_url = ?,
			       certificate_url = ?, challenge_type = ?, updated_at_us = ?, error = ?
			 WHERE id = ?`,
			string(hostnamesJSON), int64(order.Status), order.OrderURL,
			order.FinalizeURL, order.CertificateURL, int64(order.ChallengeType),
			usMicros(now), order.Error, int64(order.ID))
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

func scanACMEOrder(row rowLike) (store.ACMEOrder, error) {
	var (
		id, accountID                          int64
		hostnamesJSON                          string
		status, challengeType                  int64
		orderURL, finalizeURL, certURL, errStr string
		updatedUs                              int64
	)
	err := row.Scan(&id, &accountID, &hostnamesJSON, &status, &orderURL,
		&finalizeURL, &certURL, &challengeType, &updatedUs, &errStr)
	if err != nil {
		return store.ACMEOrder{}, mapErr(err)
	}
	var hostnames []string
	if hostnamesJSON != "" {
		if err := json.Unmarshal([]byte(hostnamesJSON), &hostnames); err != nil {
			return store.ACMEOrder{}, fmt.Errorf("storesqlite: decode hostnames: %w", err)
		}
	}
	return store.ACMEOrder{
		ID:             store.ACMEOrderID(id),
		AccountID:      store.ACMEAccountID(accountID),
		Hostnames:      hostnames,
		Status:         store.ACMEOrderStatus(status),
		OrderURL:       orderURL,
		FinalizeURL:    finalizeURL,
		CertificateURL: certURL,
		ChallengeType:  store.ChallengeType(challengeType),
		UpdatedAt:      fromMicros(updatedUs),
		Error:          errStr,
	}, nil
}

func (m *metadata) GetACMEOrder(ctx context.Context, id store.ACMEOrderID) (store.ACMEOrder, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, account_id, hostnames_json, status, order_url, finalize_url,
		       certificate_url, challenge_type, updated_at_us, error
		  FROM acme_orders WHERE id = ?`, int64(id))
	return scanACMEOrder(row)
}

func (m *metadata) ListACMEOrdersByStatus(ctx context.Context, status store.ACMEOrderStatus) ([]store.ACMEOrder, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, account_id, hostnames_json, status, order_url, finalize_url,
		       certificate_url, challenge_type, updated_at_us, error
		  FROM acme_orders WHERE status = ? ORDER BY updated_at_us ASC, id ASC`,
		int64(status))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ACMEOrder
	for rows.Next() {
		o, err := scanACMEOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (m *metadata) UpsertACMECert(ctx context.Context, cert store.ACMECert) error {
	var orderID any
	if cert.OrderID != 0 {
		orderID = int64(cert.OrderID)
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO acme_certs (hostname, chain_pem, private_key_pem,
			  not_before_us, not_after_us, issuer, order_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(hostname) DO UPDATE SET
			  chain_pem = excluded.chain_pem,
			  private_key_pem = excluded.private_key_pem,
			  not_before_us = excluded.not_before_us,
			  not_after_us = excluded.not_after_us,
			  issuer = excluded.issuer,
			  order_id = excluded.order_id`,
			strings.ToLower(cert.Hostname), cert.ChainPEM, cert.PrivateKeyPEM,
			usMicros(cert.NotBefore), usMicros(cert.NotAfter), cert.Issuer, orderID)
		return mapErr(err)
	})
}

func scanACMECert(row rowLike) (store.ACMECert, error) {
	var (
		hostname, chain, key, issuer string
		notBeforeUs, notAfterUs      int64
		orderID                      sql.NullInt64
	)
	err := row.Scan(&hostname, &chain, &key, &notBeforeUs, &notAfterUs, &issuer, &orderID)
	if err != nil {
		return store.ACMECert{}, mapErr(err)
	}
	c := store.ACMECert{
		Hostname:      hostname,
		ChainPEM:      chain,
		PrivateKeyPEM: key,
		NotBefore:     fromMicros(notBeforeUs),
		NotAfter:      fromMicros(notAfterUs),
		Issuer:        issuer,
	}
	if orderID.Valid {
		c.OrderID = store.ACMEOrderID(orderID.Int64)
	}
	return c, nil
}

func (m *metadata) GetACMECert(ctx context.Context, hostname string) (store.ACMECert, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT hostname, chain_pem, private_key_pem, not_before_us, not_after_us,
		       issuer, order_id
		  FROM acme_certs WHERE hostname = ?`, strings.ToLower(hostname))
	return scanACMECert(row)
}

func (m *metadata) ListACMECertsExpiringBefore(ctx context.Context, t time.Time) ([]store.ACMECert, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT hostname, chain_pem, private_key_pem, not_before_us, not_after_us,
		       issuer, order_id
		  FROM acme_certs WHERE not_after_us < ? ORDER BY not_after_us ASC`,
		usMicros(t))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ACMECert
	for rows.Next() {
		c, err := scanACMECert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// -- webhooks ---------------------------------------------------------

func encodeRetryPolicy(p store.RetryPolicy) (string, error) {
	if (p == store.RetryPolicy{}) {
		return "", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("storesqlite: encode retry policy: %w", err)
	}
	return string(b), nil
}

func decodeRetryPolicy(s string) (store.RetryPolicy, error) {
	if s == "" {
		return store.RetryPolicy{}, nil
	}
	var p store.RetryPolicy
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return store.RetryPolicy{}, fmt.Errorf("storesqlite: decode retry policy: %w", err)
	}
	return p, nil
}

func (m *metadata) InsertWebhook(ctx context.Context, w store.Webhook) (store.Webhook, error) {
	now := m.s.clock.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now
	rpJSON, err := encodeRetryPolicy(w.RetryPolicy)
	if err != nil {
		return store.Webhook{}, err
	}
	var id int64
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO webhooks (owner_kind, owner_id, target_url, hmac_secret,
			  delivery_mode, retry_policy_json, active, created_at_us, updated_at_us,
			  target_kind, body_mode, extracted_text_max_bytes, text_required)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			int64(w.OwnerKind), w.OwnerID, w.TargetURL, w.HMACSecret,
			int64(w.DeliveryMode), rpJSON, boolToInt(w.Active),
			usMicros(now), usMicros(now),
			int64(w.TargetKind), int64(w.BodyMode), w.ExtractedTextMaxBytes,
			boolToInt(w.TextRequired))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.Webhook{}, err
	}
	w.ID = store.WebhookID(id)
	return w, nil
}

func (m *metadata) UpdateWebhook(ctx context.Context, w store.Webhook) error {
	now := m.s.clock.Now().UTC()
	rpJSON, err := encodeRetryPolicy(w.RetryPolicy)
	if err != nil {
		return err
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE webhooks
			   SET owner_kind = ?, owner_id = ?, target_url = ?, hmac_secret = ?,
			       delivery_mode = ?, retry_policy_json = ?, active = ?, updated_at_us = ?,
			       target_kind = ?, body_mode = ?, extracted_text_max_bytes = ?,
			       text_required = ?
			 WHERE id = ?`,
			int64(w.OwnerKind), w.OwnerID, w.TargetURL, w.HMACSecret,
			int64(w.DeliveryMode), rpJSON, boolToInt(w.Active),
			usMicros(now),
			int64(w.TargetKind), int64(w.BodyMode), w.ExtractedTextMaxBytes,
			boolToInt(w.TextRequired),
			int64(w.ID))
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

func (m *metadata) DeleteWebhook(ctx context.Context, id store.WebhookID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM webhooks WHERE id = ?`, int64(id))
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

func scanWebhook(row rowLike) (store.Webhook, error) {
	var (
		id                              int64
		ownerKind, deliveryMode, active int64
		ownerID, targetURL, rpJSON      string
		hmac                            []byte
		createdUs, updatedUs            int64
		targetKind, bodyMode, txtMax    int64
		textRequired                    int64
	)
	err := row.Scan(&id, &ownerKind, &ownerID, &targetURL, &hmac,
		&deliveryMode, &rpJSON, &active, &createdUs, &updatedUs,
		&targetKind, &bodyMode, &txtMax, &textRequired)
	if err != nil {
		return store.Webhook{}, mapErr(err)
	}
	rp, err := decodeRetryPolicy(rpJSON)
	if err != nil {
		return store.Webhook{}, err
	}
	return store.Webhook{
		ID:                    store.WebhookID(id),
		OwnerKind:             store.WebhookOwnerKind(ownerKind),
		OwnerID:               ownerID,
		TargetKind:            store.WebhookTargetKind(targetKind),
		TargetURL:             targetURL,
		HMACSecret:            hmac,
		DeliveryMode:          store.DeliveryMode(deliveryMode),
		BodyMode:              store.WebhookBodyMode(bodyMode),
		ExtractedTextMaxBytes: txtMax,
		TextRequired:          textRequired != 0,
		RetryPolicy:           rp,
		Active:                active != 0,
		CreatedAt:             fromMicros(createdUs),
		UpdatedAt:             fromMicros(updatedUs),
	}, nil
}

const webhookSelectColumns = `id, owner_kind, owner_id, target_url, hmac_secret, delivery_mode,
		retry_policy_json, active, created_at_us, updated_at_us,
		target_kind, body_mode, extracted_text_max_bytes, text_required`

func (m *metadata) GetWebhook(ctx context.Context, id store.WebhookID) (store.Webhook, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT `+webhookSelectColumns+`
		  FROM webhooks WHERE id = ?`, int64(id))
	return scanWebhook(row)
}

func (m *metadata) ListWebhooks(ctx context.Context, kind store.WebhookOwnerKind, ownerID string) ([]store.Webhook, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if kind == store.WebhookOwnerUnknown {
		rows, err = m.s.db.QueryContext(ctx, `
			SELECT `+webhookSelectColumns+`
			  FROM webhooks ORDER BY id ASC`)
	} else {
		rows, err = m.s.db.QueryContext(ctx, `
			SELECT `+webhookSelectColumns+`
			  FROM webhooks WHERE owner_kind = ? AND owner_id = ? ORDER BY id ASC`,
			int64(kind), ownerID)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (m *metadata) ListActiveWebhooksForDomain(ctx context.Context, domain string) ([]store.Webhook, error) {
	dom := strings.ToLower(domain)
	// Match:
	//   - domain webhooks directly (legacy owner_kind=domain or new
	//     target_kind=domain).
	//   - synthetic-target webhooks (target_kind=synthetic) whose
	//     owner_id matches the recipient domain.  These never have a
	//     legacy owner_kind=domain row, so they sit in the union path.
	//   - principal webhooks whose principal canonical_email's domain
	//     == dom.
	// Synthetic and address kinds are stored with owner_kind=domain
	// (best legacy fallback) so the dispatcher can filter further.
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT `+webhookSelectColumns+`
		  FROM webhooks w
		 WHERE w.active = 1 AND (
		   (w.owner_kind = ? AND lower(w.owner_id) = ?)
		   OR (w.target_kind = ? AND lower(w.owner_id) = ?)
		   OR (w.owner_kind = ? AND w.owner_id IN (
		     SELECT CAST(p.id AS TEXT) FROM principals p
		      WHERE substr(p.canonical_email, instr(p.canonical_email, '@') + 1) = ?))
		 )
		 ORDER BY w.id ASC`,
		int64(store.WebhookOwnerDomain), dom,
		int64(store.WebhookTargetSynthetic), dom,
		int64(store.WebhookOwnerPrincipal), dom)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// -- DMARC ------------------------------------------------------------

func (m *metadata) InsertDMARCReport(ctx context.Context, report store.DMARCReport, drows []store.DMARCRow) (store.DMARCReportID, error) {
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO dmarc_reports_raw (received_at_us, reporter_email, reporter_org,
			  report_id, domain, date_begin_us, date_end_us, xml_blob_hash, parsed_ok,
			  parse_error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			usMicros(report.ReceivedAt), strings.ToLower(report.ReporterEmail),
			report.ReporterOrg, report.ReportID, strings.ToLower(report.Domain),
			usMicros(report.DateBegin), usMicros(report.DateEnd),
			report.XMLBlobHash, boolToInt(report.ParsedOK), report.ParseError)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		for _, r := range drows {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO dmarc_rows (report_id, source_ip, count, disposition,
				  spf_aligned, dkim_aligned, spf_result, dkim_result, header_from,
				  envelope_from, envelope_to)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, r.SourceIP, r.Count, int64(r.Disposition),
				boolToInt(r.SPFAligned), boolToInt(r.DKIMAligned),
				r.SPFResult, r.DKIMResult, strings.ToLower(r.HeaderFrom),
				strings.ToLower(r.EnvelopeFrom), strings.ToLower(r.EnvelopeTo)); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return store.DMARCReportID(id), nil
}

func scanDMARCReport(row rowLike) (store.DMARCReport, error) {
	var (
		id                                           int64
		receivedUs, dateBeginUs, dateEndUs           int64
		reporterEmail, reporterOrg, reportID, domain string
		xmlHash, parseErr                            string
		parsedOK                                     int64
	)
	err := row.Scan(&id, &receivedUs, &reporterEmail, &reporterOrg, &reportID,
		&domain, &dateBeginUs, &dateEndUs, &xmlHash, &parsedOK, &parseErr)
	if err != nil {
		return store.DMARCReport{}, mapErr(err)
	}
	return store.DMARCReport{
		ID:            store.DMARCReportID(id),
		ReceivedAt:    fromMicros(receivedUs),
		ReporterEmail: reporterEmail,
		ReporterOrg:   reporterOrg,
		ReportID:      reportID,
		Domain:        domain,
		DateBegin:     fromMicros(dateBeginUs),
		DateEnd:       fromMicros(dateEndUs),
		XMLBlobHash:   xmlHash,
		ParsedOK:      parsedOK != 0,
		ParseError:    parseErr,
	}, nil
}

func (m *metadata) GetDMARCReport(ctx context.Context, id store.DMARCReportID) (store.DMARCReport, []store.DMARCRow, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, received_at_us, reporter_email, reporter_org, report_id, domain,
		       date_begin_us, date_end_us, xml_blob_hash, parsed_ok, parse_error
		  FROM dmarc_reports_raw WHERE id = ?`, int64(id))
	rep, err := scanDMARCReport(row)
	if err != nil {
		return store.DMARCReport{}, nil, err
	}
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, report_id, source_ip, count, disposition, spf_aligned,
		       dkim_aligned, spf_result, dkim_result, header_from, envelope_from,
		       envelope_to
		  FROM dmarc_rows WHERE report_id = ? ORDER BY id ASC`, int64(id))
	if err != nil {
		return rep, nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.DMARCRow
	for rows.Next() {
		var (
			rid, repID                      int64
			count                           int64
			disp                            int64
			spfAligned, dkimAligned         int64
			sourceIP, spfResult, dkimResult string
			hdrFrom, envFrom, envTo         string
		)
		if err := rows.Scan(&rid, &repID, &sourceIP, &count, &disp, &spfAligned,
			&dkimAligned, &spfResult, &dkimResult, &hdrFrom, &envFrom, &envTo); err != nil {
			return rep, nil, mapErr(err)
		}
		out = append(out, store.DMARCRow{
			ID:           store.DMARCRowID(rid),
			ReportID:     store.DMARCReportID(repID),
			SourceIP:     sourceIP,
			Count:        count,
			Disposition:  int32(disp),
			SPFAligned:   spfAligned != 0,
			DKIMAligned:  dkimAligned != 0,
			SPFResult:    spfResult,
			DKIMResult:   dkimResult,
			HeaderFrom:   hdrFrom,
			EnvelopeFrom: envFrom,
			EnvelopeTo:   envTo,
		})
	}
	return rep, out, rows.Err()
}

func (m *metadata) ListDMARCReports(ctx context.Context, filter store.DMARCReportFilter) ([]store.DMARCReport, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var where []string
	var args []any
	if filter.AfterID != 0 {
		where = append(where, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	if filter.Domain != "" {
		where = append(where, "domain = ?")
		args = append(args, strings.ToLower(filter.Domain))
	}
	if !filter.Since.IsZero() {
		where = append(where, "date_begin_us >= ?")
		args = append(args, usMicros(filter.Since))
	}
	if !filter.Until.IsZero() {
		where = append(where, "date_begin_us < ?")
		args = append(args, usMicros(filter.Until))
	}
	q := `SELECT id, received_at_us, reporter_email, reporter_org, report_id, domain,
	             date_begin_us, date_end_us, xml_blob_hash, parsed_ok, parse_error
	        FROM dmarc_reports_raw`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.DMARCReport
	for rows.Next() {
		r, err := scanDMARCReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) DMARCAggregate(ctx context.Context, domain string, since, until time.Time) ([]store.DMARCAggregateRow, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT r.header_from, r.disposition,
		       SUM(r.count) AS total,
		       SUM(CASE WHEN r.spf_aligned = 1 THEN r.count ELSE 0 END) AS spf_pass,
		       SUM(CASE WHEN r.dkim_aligned = 1 THEN r.count ELSE 0 END) AS dkim_pass
		  FROM dmarc_rows r
		  JOIN dmarc_reports_raw rep ON rep.id = r.report_id
		 WHERE rep.domain = ?
		   AND rep.date_begin_us >= ?
		   AND rep.date_begin_us < ?
		 GROUP BY r.header_from, r.disposition
		 ORDER BY r.header_from, r.disposition`,
		strings.ToLower(domain), usMicros(since), usMicros(until))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.DMARCAggregateRow
	for rows.Next() {
		var (
			hf       string
			disp     int64
			total    int64
			spfPass  int64
			dkimPass int64
		)
		if err := rows.Scan(&hf, &disp, &total, &spfPass, &dkimPass); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.DMARCAggregateRow{
			HeaderFrom:  hf,
			Disposition: int32(disp),
			Count:       total,
			PassedSPF:   spfPass,
			PassedDKIM:  dkimPass,
		})
	}
	return out, rows.Err()
}

// -- mailbox ACL ------------------------------------------------------

func (m *metadata) SetMailboxACL(ctx context.Context, mailboxID store.MailboxID, principalID *store.PrincipalID, rights store.ACLRights, grantedBy store.PrincipalID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// SQLite's UNIQUE on (mailbox_id, principal_id) does not match
		// rows with NULL principal_id (NULLs compare unequal); we
		// emulate the upsert by hand.
		var pidArg any
		var existsQuery string
		var existsArgs []any
		if principalID == nil {
			existsQuery = `SELECT id FROM mailbox_acl WHERE mailbox_id = ? AND principal_id IS NULL`
			existsArgs = []any{int64(mailboxID)}
		} else {
			pidArg = int64(*principalID)
			existsQuery = `SELECT id FROM mailbox_acl WHERE mailbox_id = ? AND principal_id = ?`
			existsArgs = []any{int64(mailboxID), int64(*principalID)}
		}
		var existing int64
		err := tx.QueryRowContext(ctx, existsQuery, existsArgs...).Scan(&existing)
		if err == nil {
			_, err = tx.ExecContext(ctx,
				`UPDATE mailbox_acl SET rights_mask = ?, granted_by = ? WHERE id = ?`,
				int64(rights), int64(grantedBy), existing)
			return mapErr(err)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return mapErr(err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO mailbox_acl (mailbox_id, principal_id, rights_mask, granted_by, created_at_us)
			VALUES (?, ?, ?, ?, ?)`,
			int64(mailboxID), pidArg, int64(rights), int64(grantedBy), usMicros(now))
		return mapErr(err)
	})
}

func (m *metadata) GetMailboxACL(ctx context.Context, mailboxID store.MailboxID) ([]store.MailboxACL, error) {
	// Anyone rows first (NULL principal_id), then per-principal rows
	// in ascending principal id.
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, mailbox_id, principal_id, rights_mask, granted_by, created_at_us
		  FROM mailbox_acl WHERE mailbox_id = ?
		 ORDER BY (principal_id IS NULL) DESC, principal_id ASC`,
		int64(mailboxID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.MailboxACL
	for rows.Next() {
		var (
			id, mb, rights, grantedBy, createdUs int64
			pid                                  sql.NullInt64
		)
		if err := rows.Scan(&id, &mb, &pid, &rights, &grantedBy, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		acl := store.MailboxACL{
			ID:        store.MailboxACLID(id),
			MailboxID: store.MailboxID(mb),
			Rights:    store.ACLRights(rights),
			GrantedBy: store.PrincipalID(grantedBy),
			CreatedAt: fromMicros(createdUs),
		}
		if pid.Valid {
			pp := store.PrincipalID(pid.Int64)
			acl.PrincipalID = &pp
		}
		out = append(out, acl)
	}
	return out, rows.Err()
}

func (m *metadata) ListMailboxesAccessibleBy(ctx context.Context, pid store.PrincipalID) ([]store.Mailbox, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT mb.id, mb.principal_id, mb.parent_id, mb.name, mb.attributes,
		       mb.uidvalidity, mb.uidnext, mb.highest_modseq, mb.created_at_us,
		       mb.updated_at_us, mb.color_hex, mb.sort_order
		  FROM mailboxes mb
		 WHERE mb.id IN (
		   SELECT mailbox_id FROM mailbox_acl
		    WHERE (principal_id = ? OR principal_id IS NULL)
		      AND (rights_mask & ?) = ?
		 )
		 ORDER BY mb.name ASC`,
		int64(pid), int64(store.ACLRightLookup), int64(store.ACLRightLookup))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Mailbox
	for rows.Next() {
		mb, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mb)
	}
	return out, rows.Err()
}

func (m *metadata) RemoveMailboxACL(ctx context.Context, mailboxID store.MailboxID, principalID *store.PrincipalID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			res sql.Result
			err error
		)
		if principalID == nil {
			res, err = tx.ExecContext(ctx,
				`DELETE FROM mailbox_acl WHERE mailbox_id = ? AND principal_id IS NULL`,
				int64(mailboxID))
		} else {
			res, err = tx.ExecContext(ctx,
				`DELETE FROM mailbox_acl WHERE mailbox_id = ? AND principal_id = ?`,
				int64(mailboxID), int64(*principalID))
		}
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

// -- JMAP states ------------------------------------------------------

func (m *metadata) GetJMAPStates(ctx context.Context, pid store.PrincipalID) (store.JMAPStates, error) {
	now := m.s.clock.Now().UTC()
	var out store.JMAPStates
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Lazy create.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO jmap_states (principal_id, updated_at_us)
			VALUES (?, ?)
			ON CONFLICT(principal_id) DO NOTHING`,
			int64(pid), usMicros(now))
		if err != nil {
			return mapErr(err)
		}
		row := tx.QueryRowContext(ctx, `
			SELECT principal_id, mailbox_state, email_state, thread_state,
			       identity_state, email_submission_state, vacation_response_state,
			       sieve_state, address_book_state, contact_state,
			       calendar_state, calendar_event_state,
			       conversation_state, message_chat_state, membership_state,
			       push_subscription_state, shortcut_coach_state,
			       category_settings_state, managed_rule_state,
			       updated_at_us
			  FROM jmap_states WHERE principal_id = ?`, int64(pid))
		var (
			ppid, mb, em, th, ide, es, vr, sv, ab, ct, cal, ce int64
			conv, msgChat, memb                                int64
			pushSub, coach, catSettings, managedRule           int64
			updatedUs                                          int64
		)
		if err := row.Scan(&ppid, &mb, &em, &th, &ide, &es, &vr, &sv, &ab, &ct, &cal, &ce,
			&conv, &msgChat, &memb, &pushSub, &coach, &catSettings, &managedRule, &updatedUs); err != nil {
			return mapErr(err)
		}
		out = store.JMAPStates{
			PrincipalID:      store.PrincipalID(ppid),
			Mailbox:          mb,
			Email:            em,
			Thread:           th,
			Identity:         ide,
			EmailSubmission:  es,
			VacationResponse: vr,
			Sieve:            sv,
			AddressBook:      ab,
			Contact:          ct,
			Calendar:         cal,
			CalendarEvent:    ce,
			Conversation:     conv,
			ChatMessage:      msgChat,
			Membership:       memb,
			PushSubscription: pushSub,
			ShortcutCoach:    coach,
			CategorySettings: catSettings,
			ManagedRule:      managedRule,
			UpdatedAt:        fromMicros(updatedUs),
		}
		return nil
	})
	if err != nil {
		return store.JMAPStates{}, err
	}
	return out, nil
}

func (m *metadata) IncrementJMAPState(ctx context.Context, pid store.PrincipalID, kind store.JMAPStateKind) (int64, error) {
	col, err := jmapStateColumn(kind)
	if err != nil {
		return 0, err
	}
	now := m.s.clock.Now().UTC()
	var newVal int64
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		// Lazy create.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO jmap_states (principal_id, updated_at_us)
			VALUES (?, ?)
			ON CONFLICT(principal_id) DO NOTHING`,
			int64(pid), usMicros(now)); err != nil {
			return mapErr(err)
		}
		// SQLite supports column-name binding only for values, not
		// identifiers; the column is from a closed enum so direct
		// concatenation is safe.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE jmap_states SET %s = %s + 1, updated_at_us = ?
			 WHERE principal_id = ?`, col, col),
			usMicros(now), int64(pid)); err != nil {
			return mapErr(err)
		}
		return tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT %s FROM jmap_states WHERE principal_id = ?`, col),
			int64(pid)).Scan(&newVal)
	})
	if err != nil {
		return 0, err
	}
	return newVal, nil
}

func jmapStateColumn(kind store.JMAPStateKind) (string, error) {
	switch kind {
	case store.JMAPStateKindMailbox:
		return "mailbox_state", nil
	case store.JMAPStateKindEmail:
		return "email_state", nil
	case store.JMAPStateKindThread:
		return "thread_state", nil
	case store.JMAPStateKindIdentity:
		return "identity_state", nil
	case store.JMAPStateKindEmailSubmission:
		return "email_submission_state", nil
	case store.JMAPStateKindVacationResponse:
		return "vacation_response_state", nil
	case store.JMAPStateKindSieve:
		return "sieve_state", nil
	case store.JMAPStateKindAddressBook:
		return "address_book_state", nil
	case store.JMAPStateKindContact:
		return "contact_state", nil
	case store.JMAPStateKindCalendar:
		return "calendar_state", nil
	case store.JMAPStateKindCalendarEvent:
		return "calendar_event_state", nil
	case store.JMAPStateKindConversation:
		return "conversation_state", nil
	case store.JMAPStateKindChatMessage:
		return "message_chat_state", nil
	case store.JMAPStateKindMembership:
		return "membership_state", nil
	case store.JMAPStateKindPushSubscription:
		return "push_subscription_state", nil
	case store.JMAPStateKindShortcutCoach:
		return "shortcut_coach_state", nil
	case store.JMAPStateKindCategorySettings:
		return "category_settings_state", nil
	case store.JMAPStateKindManagedRule:
		return "managed_rule_state", nil
	default:
		return "", fmt.Errorf("storesqlite: unknown JMAPStateKind %d", kind)
	}
}

// -- TLS-RPT ----------------------------------------------------------

func (m *metadata) AppendTLSRPTFailure(ctx context.Context, f store.TLSRPTFailure) error {
	if f.RecordedAt.IsZero() {
		f.RecordedAt = m.s.clock.Now().UTC()
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO tlsrpt_failures (recorded_at_us, policy_domain,
			  receiving_mta_hostname, failure_type, failure_code, failure_detail_json)
			VALUES (?, ?, ?, ?, ?, ?)`,
			usMicros(f.RecordedAt), strings.ToLower(f.PolicyDomain),
			f.ReceivingMTAHostname, int64(f.FailureType), f.FailureCode,
			f.FailureDetailJSON)
		return mapErr(err)
	})
}

func (m *metadata) ListTLSRPTFailures(ctx context.Context, policyDomain string, since, until time.Time) ([]store.TLSRPTFailure, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, recorded_at_us, policy_domain, receiving_mta_hostname,
		       failure_type, failure_code, failure_detail_json
		  FROM tlsrpt_failures
		 WHERE policy_domain = ?
		   AND recorded_at_us >= ?
		   AND recorded_at_us < ?
		 ORDER BY recorded_at_us ASC, id ASC`,
		strings.ToLower(policyDomain), usMicros(since), usMicros(until))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.TLSRPTFailure
	for rows.Next() {
		var (
			id, recordedUs, fType int64
			pd, mta, code, detail string
		)
		if err := rows.Scan(&id, &recordedUs, &pd, &mta, &fType, &code, &detail); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.TLSRPTFailure{
			ID:                   store.TLSRPTFailureID(id),
			RecordedAt:           fromMicros(recordedUs),
			PolicyDomain:         pd,
			ReceivingMTAHostname: mta,
			FailureType:          store.TLSRPTFailureType(fType),
			FailureCode:          code,
			FailureDetailJSON:    detail,
		})
	}
	return out, rows.Err()
}

// -- JMAP EmailSubmission --------------------------------------------

func scanEmailSubmission(row rowLike) (store.EmailSubmissionRow, error) {
	var (
		id, envID, identityID, threadID, undoStatus string
		principalID, emailID                        int64
		sendAtUs, createdAtUs                       int64
		props                                       []byte
	)
	err := row.Scan(&id, &envID, &principalID, &identityID, &emailID,
		&threadID, &sendAtUs, &createdAtUs, &undoStatus, &props)
	if err != nil {
		return store.EmailSubmissionRow{}, mapErr(err)
	}
	return store.EmailSubmissionRow{
		ID:          id,
		EnvelopeID:  store.EnvelopeID(envID),
		PrincipalID: store.PrincipalID(principalID),
		IdentityID:  identityID,
		EmailID:     store.MessageID(emailID),
		ThreadID:    threadID,
		SendAtUs:    sendAtUs,
		CreatedAtUs: createdAtUs,
		UndoStatus:  undoStatus,
		Properties:  props,
	}, nil
}

const emailSubmissionSelectColumns = `
	id, envelope_id, principal_id, identity_id, email_id, thread_id,
	send_at_us, created_at_us, undo_status, properties`

func (m *metadata) InsertEmailSubmission(ctx context.Context, row store.EmailSubmissionRow) error {
	if row.ID == "" {
		return fmt.Errorf("storesqlite: InsertEmailSubmission: empty id")
	}
	now := m.s.clock.Now().UTC()
	if row.CreatedAtUs == 0 {
		row.CreatedAtUs = usMicros(now)
	}
	if row.SendAtUs == 0 {
		row.SendAtUs = row.CreatedAtUs
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM jmap_email_submissions WHERE id = ?`, row.ID).Scan(&existing)
		if err == nil {
			return fmt.Errorf("email submission %q: %w", row.ID, store.ErrConflict)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return mapErr(err)
		}
		props := row.Properties
		if props == nil {
			props = []byte{}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO jmap_email_submissions
			  (id, envelope_id, principal_id, identity_id, email_id, thread_id,
			   send_at_us, created_at_us, undo_status, properties)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, string(row.EnvelopeID), int64(row.PrincipalID),
			row.IdentityID, int64(row.EmailID), row.ThreadID,
			row.SendAtUs, row.CreatedAtUs, row.UndoStatus, props)
		return mapErr(err)
	})
}

func (m *metadata) GetEmailSubmission(ctx context.Context, id string) (store.EmailSubmissionRow, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+emailSubmissionSelectColumns+` FROM jmap_email_submissions WHERE id = ?`, id)
	return scanEmailSubmission(row)
}

func (m *metadata) ListEmailSubmissions(ctx context.Context, principal store.PrincipalID, filter store.EmailSubmissionFilter) ([]store.EmailSubmissionRow, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	where := []string{"principal_id = ?"}
	args := []any{int64(principal)}
	if filter.AfterID != "" {
		where = append(where, "id > ?")
		args = append(args, filter.AfterID)
	}
	if len(filter.IdentityIDs) > 0 {
		ph := make([]string, len(filter.IdentityIDs))
		for i, v := range filter.IdentityIDs {
			ph[i] = "?"
			args = append(args, v)
		}
		where = append(where, "identity_id IN ("+strings.Join(ph, ",")+")")
	}
	if len(filter.EmailIDs) > 0 {
		ph := make([]string, len(filter.EmailIDs))
		for i, v := range filter.EmailIDs {
			ph[i] = "?"
			args = append(args, int64(v))
		}
		where = append(where, "email_id IN ("+strings.Join(ph, ",")+")")
	}
	if len(filter.ThreadIDs) > 0 {
		ph := make([]string, len(filter.ThreadIDs))
		for i, v := range filter.ThreadIDs {
			ph[i] = "?"
			args = append(args, v)
		}
		where = append(where, "thread_id IN ("+strings.Join(ph, ",")+")")
	}
	if filter.UndoStatus != "" {
		where = append(where, "undo_status = ?")
		args = append(args, filter.UndoStatus)
	}
	if filter.AfterUs != 0 {
		where = append(where, "send_at_us > ?")
		args = append(args, filter.AfterUs)
	}
	if filter.BeforeUs != 0 {
		where = append(where, "send_at_us < ?")
		args = append(args, filter.BeforeUs)
	}
	q := `SELECT ` + emailSubmissionSelectColumns + ` FROM jmap_email_submissions`
	q += " WHERE " + strings.Join(where, " AND ")
	q += " ORDER BY send_at_us ASC, id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.EmailSubmissionRow
	for rows.Next() {
		r, err := scanEmailSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateEmailSubmissionUndoStatus(ctx context.Context, id, undoStatus string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE jmap_email_submissions SET undo_status = ? WHERE id = ?`,
			undoStatus, id)
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

func (m *metadata) DeleteEmailSubmission(ctx context.Context, id string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM jmap_email_submissions WHERE id = ?`, id)
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

// -- JMAP Identity overlay -------------------------------------------

func scanJMAPIdentity(row rowLike) (store.JMAPIdentity, error) {
	var (
		id, name, email, textSig, htmlSig string
		principalID                       int64
		replyTo, bcc                      []byte
		mayDelete                         int64
		createdAtUs, updatedAtUs          int64
		signature                         sql.NullString
	)
	err := row.Scan(&id, &principalID, &name, &email, &replyTo, &bcc,
		&textSig, &htmlSig, &mayDelete, &createdAtUs, &updatedAtUs, &signature)
	if err != nil {
		return store.JMAPIdentity{}, mapErr(err)
	}
	out := store.JMAPIdentity{
		ID:            id,
		PrincipalID:   store.PrincipalID(principalID),
		Name:          name,
		Email:         email,
		ReplyToJSON:   replyTo,
		BccJSON:       bcc,
		TextSignature: textSig,
		HTMLSignature: htmlSig,
		MayDelete:     mayDelete != 0,
		CreatedAtUs:   createdAtUs,
		UpdatedAtUs:   updatedAtUs,
	}
	if signature.Valid {
		v := signature.String
		out.Signature = &v
	}
	return out, nil
}

const jmapIdentitySelectColumns = `
	id, principal_id, name, email, reply_to_json, bcc_json,
	text_signature, html_signature, may_delete, created_at_us, updated_at_us,
	signature`

func (m *metadata) InsertJMAPIdentity(ctx context.Context, row store.JMAPIdentity) error {
	if row.ID == "" {
		return fmt.Errorf("storesqlite: InsertJMAPIdentity: empty id")
	}
	now := m.s.clock.Now().UTC()
	if row.CreatedAtUs == 0 {
		row.CreatedAtUs = usMicros(now)
	}
	if row.UpdatedAtUs == 0 {
		row.UpdatedAtUs = row.CreatedAtUs
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM jmap_identities WHERE id = ?`, row.ID).Scan(&existing)
		if err == nil {
			return fmt.Errorf("jmap identity %q: %w", row.ID, store.ErrConflict)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return mapErr(err)
		}
		replyTo := row.ReplyToJSON
		if replyTo == nil {
			replyTo = []byte{}
		}
		bcc := row.BccJSON
		if bcc == nil {
			bcc = []byte{}
		}
		var sig any
		if row.Signature != nil {
			sig = *row.Signature
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO jmap_identities
			  (id, principal_id, name, email, reply_to_json, bcc_json,
			   text_signature, html_signature, may_delete, created_at_us, updated_at_us,
			   signature)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, int64(row.PrincipalID), row.Name, row.Email,
			replyTo, bcc, row.TextSignature, row.HTMLSignature,
			boolToInt(row.MayDelete), row.CreatedAtUs, row.UpdatedAtUs, sig)
		return mapErr(err)
	})
}

func (m *metadata) GetJMAPIdentity(ctx context.Context, id string) (store.JMAPIdentity, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+jmapIdentitySelectColumns+` FROM jmap_identities WHERE id = ?`, id)
	return scanJMAPIdentity(row)
}

func (m *metadata) ListJMAPIdentities(ctx context.Context, principal store.PrincipalID) ([]store.JMAPIdentity, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+jmapIdentitySelectColumns+`
		   FROM jmap_identities WHERE principal_id = ?
		  ORDER BY created_at_us ASC, id ASC`,
		int64(principal))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.JMAPIdentity
	for rows.Next() {
		r, err := scanJMAPIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateJMAPIdentity(ctx context.Context, row store.JMAPIdentity) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		replyTo := row.ReplyToJSON
		if replyTo == nil {
			replyTo = []byte{}
		}
		bcc := row.BccJSON
		if bcc == nil {
			bcc = []byte{}
		}
		var sig any
		if row.Signature != nil {
			sig = *row.Signature
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET name = ?, reply_to_json = ?, bcc_json = ?,
			       text_signature = ?, html_signature = ?, signature = ?,
			       updated_at_us = ?
			 WHERE id = ?`,
			row.Name, replyTo, bcc,
			row.TextSignature, row.HTMLSignature, sig, usMicros(now), row.ID)
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

func (m *metadata) DeleteJMAPIdentity(ctx context.Context, id string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM jmap_identities WHERE id = ?`, id)
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

// -- LLM categorisation (REQ-FILT-200..221) --------------------------

// defaultCategorySet matches the documented Gmail-style seed
// (REQ-FILT-201/210). Kept here instead of the categorise package so
// the store can seed the row on first read without pulling a
// higher-level dependency.
var defaultCategorySet = []store.CategoryDef{
	{Name: "primary", Description: "Personal correspondence and important messages from people you know."},
	{Name: "social", Description: "Messages from social networks and dating sites."},
	{Name: "promotions", Description: "Marketing emails, offers, deals, newsletters."},
	{Name: "updates", Description: "Receipts, confirmations, statements, account notices."},
	{Name: "forums", Description: "Mailing-list digests, online community discussions."},
}

// defaultCategorisationPrompt is the seeded system prompt
// (REQ-FILT-211). Kept terse: small local models follow short prompts
// better than long ones. Operators may override per-account.
const defaultCategorisationPrompt = `You are an email-categorisation assistant. Given an email envelope and a short body excerpt, choose exactly one category from the supplied list whose description best fits the message, or return "none" if no category is a clear match. Respond ONLY with a single JSON object of the form {"category":"<name>"} where <name> is one of the listed category names or the literal "none". Do not include any other text.`

func (m *metadata) GetCategorisationConfig(ctx context.Context, pid store.PrincipalID) (store.CategorisationConfig, error) {
	cfg, found, err := m.readCategorisationRow(ctx, pid)
	if err != nil {
		return store.CategorisationConfig{}, err
	}
	if found {
		return cfg, nil
	}
	// Seed the defaults. The seed write is best-effort: a backend
	// error here returns the in-memory defaults so the pipeline can
	// run on stock configuration.
	seed := store.CategorisationConfig{
		PrincipalID: pid,
		Prompt:      defaultCategorisationPrompt,
		CategorySet: append([]store.CategoryDef(nil), defaultCategorySet...),
		TimeoutSec:  5,
		Enabled:     true,
	}
	_ = m.UpdateCategorisationConfig(ctx, seed)
	cfg2, found2, err := m.readCategorisationRow(ctx, pid)
	if err != nil || !found2 {
		seed.UpdatedAtUs = usMicros(m.s.clock.Now().UTC())
		return seed, nil
	}
	return cfg2, nil
}

// readCategorisationRow returns the row for pid; the bool reports
// whether a row was found. A backend error is reported separately;
// "row absent" is not an error.
func (m *metadata) readCategorisationRow(ctx context.Context, pid store.PrincipalID) (store.CategorisationConfig, bool, error) {
	var (
		prompt          string
		categorySetJSON []byte
		endpointURL     sql.NullString
		model           sql.NullString
		apiKeyEnv       sql.NullString
		timeoutSec      int64
		enabled         int64
		updatedAtUs     int64
	)
	err := m.s.db.QueryRowContext(ctx, `
		SELECT prompt, category_set_json, endpoint_url, model, api_key_env,
		       timeout_sec, enabled, updated_at_us
		  FROM jmap_categorisation_config
		 WHERE principal_id = ?`, int64(pid)).Scan(
		&prompt, &categorySetJSON, &endpointURL, &model, &apiKeyEnv,
		&timeoutSec, &enabled, &updatedAtUs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.CategorisationConfig{}, false, nil
		}
		return store.CategorisationConfig{}, false, mapErr(err)
	}
	cfg := store.CategorisationConfig{
		PrincipalID: pid,
		Prompt:      prompt,
		TimeoutSec:  int(timeoutSec),
		Enabled:     enabled != 0,
		UpdatedAtUs: updatedAtUs,
	}
	if len(categorySetJSON) > 0 {
		if err := json.Unmarshal(categorySetJSON, &cfg.CategorySet); err != nil {
			return store.CategorisationConfig{}, false, fmt.Errorf("storesqlite: decode category_set_json: %w", err)
		}
	}
	if endpointURL.Valid {
		v := endpointURL.String
		cfg.Endpoint = &v
	}
	if model.Valid {
		v := model.String
		cfg.Model = &v
	}
	if apiKeyEnv.Valid {
		v := apiKeyEnv.String
		cfg.APIKeyEnv = &v
	}
	return cfg, true, nil
}

func (m *metadata) UpdateCategorisationConfig(ctx context.Context, cfg store.CategorisationConfig) error {
	now := m.s.clock.Now().UTC()
	if cfg.CategorySet == nil {
		cfg.CategorySet = []store.CategoryDef{}
	}
	js, err := json.Marshal(cfg.CategorySet)
	if err != nil {
		return fmt.Errorf("storesqlite: encode category_set_json: %w", err)
	}
	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO jmap_categorisation_config
			  (principal_id, prompt, category_set_json, endpoint_url, model,
			   api_key_env, timeout_sec, enabled, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(principal_id) DO UPDATE SET
			  prompt = excluded.prompt,
			  category_set_json = excluded.category_set_json,
			  endpoint_url = excluded.endpoint_url,
			  model = excluded.model,
			  api_key_env = excluded.api_key_env,
			  timeout_sec = excluded.timeout_sec,
			  enabled = excluded.enabled,
			  updated_at_us = excluded.updated_at_us`,
			int64(cfg.PrincipalID), cfg.Prompt, js,
			nullStringPtr(cfg.Endpoint), nullStringPtr(cfg.Model), nullStringPtr(cfg.APIKeyEnv),
			int64(timeoutSec), boolToInt(cfg.Enabled), usMicros(now))
		return mapErr(err)
	})
}

// nullStringPtr converts an optional string into a value suitable for
// sql binding: nil pointer → SQL NULL, non-nil → the bare string.
func nullStringPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
