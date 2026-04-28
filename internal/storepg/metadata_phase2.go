package storepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase 2 store.Metadata methods (queue,
// DKIM, ACME, webhooks, DMARC, mailbox ACL, JMAP states, TLS-RPT)
// against Postgres. Phase 1 methods stay in metadata.go; the split
// keeps Wave 2.0 review focused.

// -- queue ------------------------------------------------------------

const queueSelectColumnsPG = `
	id, principal_id, mail_from, rcpt_to, envelope_id,
	body_blob_hash, headers_blob_hash, state, attempts, last_attempt_at_us,
	next_attempt_at_us, last_error, dsn_notify_flags, dsn_ret, dsn_envid,
	dsn_orcpt, idempotency_key, created_at_us`

func scanQueueItemPG(row pgx.Row) (store.QueueItem, error) {
	var (
		id                                    int64
		principalID                           *int64
		state                                 int32
		attempts                              int32
		lastUs                                int64
		nextUs                                int64
		dsnFlags                              int32
		dsnRet                                int32
		createdUs                             int64
		mailFrom, rcptTo, envID, bodyHash     string
		headersHash, lastErr, dsnEnvID, orcpt string
		idempKey                              *string
	)
	err := row.Scan(&id, &principalID, &mailFrom, &rcptTo, &envID,
		&bodyHash, &headersHash, &state, &attempts, &lastUs,
		&nextUs, &lastErr, &dsnFlags, &dsnRet, &dsnEnvID, &orcpt,
		&idempKey, &createdUs)
	if err != nil {
		return store.QueueItem{}, mapErr(err)
	}
	q := store.QueueItem{
		ID:              store.QueueItemID(id),
		MailFrom:        mailFrom,
		RcptTo:          rcptTo,
		EnvelopeID:      store.EnvelopeID(envID),
		BodyBlobHash:    bodyHash,
		HeadersBlobHash: headersHash,
		State:           store.QueueState(state),
		Attempts:        attempts,
		LastAttemptAt:   fromMicros(lastUs),
		NextAttemptAt:   fromMicros(nextUs),
		LastError:       lastErr,
		DSNNotify:       store.DSNNotifyFlags(dsnFlags),
		DSNRet:          store.DSNRet(dsnRet),
		DSNEnvID:        dsnEnvID,
		DSNOrcpt:        orcpt,
		CreatedAt:       fromMicros(createdUs),
	}
	if principalID != nil {
		q.PrincipalID = store.PrincipalID(*principalID)
	}
	if idempKey != nil {
		q.IdempotencyKey = *idempKey
	}
	return q, nil
}

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
	var pid *int64
	if item.PrincipalID != 0 {
		v := int64(item.PrincipalID)
		pid = &v
	}
	var idempKey *string
	if item.IdempotencyKey != "" {
		v := item.IdempotencyKey
		idempKey = &v
	}
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		if item.IdempotencyKey != "" {
			var prior int64
			err := tx.QueryRow(ctx,
				`SELECT id FROM queue WHERE idempotency_key = $1`,
				item.IdempotencyKey).Scan(&prior)
			if err == nil {
				id = prior
				return fmt.Errorf("queue idempotency key %q: %w", item.IdempotencyKey, store.ErrConflict)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return mapErr(err)
			}
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO queue (principal_id, mail_from, rcpt_to, envelope_id,
			  body_blob_hash, headers_blob_hash, state, attempts, last_attempt_at_us,
			  next_attempt_at_us, last_error, dsn_notify_flags, dsn_ret, dsn_envid,
			  dsn_orcpt, idempotency_key, created_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			RETURNING id`,
			pid, strings.ToLower(item.MailFrom), strings.ToLower(item.RcptTo),
			string(item.EnvelopeID), item.BodyBlobHash, item.HeadersBlobHash,
			int32(item.State), int32(item.Attempts), usMicros(item.LastAttemptAt),
			usMicros(item.NextAttemptAt), item.LastError,
			int32(item.DSNNotify), int32(item.DSNRet), item.DSNEnvID, item.DSNOrcpt,
			idempKey, usMicros(item.CreatedAt)).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		if item.BodyBlobHash != "" {
			if err := incRef(ctx, tx, item.BodyBlobHash, 0, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
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
	nowUs := usMicros(now)
	var out []store.QueueItem
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+queueSelectColumnsPG+`
			  FROM queue
			 WHERE state IN ($1, $2) AND next_attempt_at_us <= $3
			 ORDER BY next_attempt_at_us ASC, id ASC
			 LIMIT $4`,
			int32(store.QueueStateQueued), int32(store.QueueStateDeferred), nowUs, max)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			q, err := scanQueueItemPG(rows)
			if err != nil {
				return err
			}
			out = append(out, q)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		for i := range out {
			if _, err := tx.Exec(ctx, `
				UPDATE queue SET state = $1, last_attempt_at_us = $2 WHERE id = $3`,
				int32(store.QueueStateInflight), nowUs, int64(out[i].ID)); err != nil {
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var bodyHash string
		err := tx.QueryRow(ctx,
			`SELECT body_blob_hash FROM queue WHERE id = $1`, int64(id)).Scan(&bodyHash)
		if err != nil {
			return mapErr(err)
		}
		newState := store.QueueStateDone
		if !success {
			newState = store.QueueStateFailed
		}
		res, err := tx.Exec(ctx, `
			UPDATE queue SET state = $1, last_error = $2, last_attempt_at_us = $3
			 WHERE id = $4`,
			int32(newState), errMsg, usMicros(now), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE queue
			   SET state = $1, attempts = attempts + 1,
			       next_attempt_at_us = $2, last_error = $3
			 WHERE id = $4`,
			int32(store.QueueStateDeferred), usMicros(nextAttempt),
			errMsg, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) HoldQueueItem(ctx context.Context, id store.QueueItemID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`UPDATE queue SET state = $1 WHERE id = $2`,
			int32(store.QueueStateHeld), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ReleaseQueueItem(ctx context.Context, id store.QueueItemID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`UPDATE queue SET state = $1, next_attempt_at_us = $2 WHERE id = $3`,
			int32(store.QueueStateQueued), usMicros(now), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) DeleteQueueItem(ctx context.Context, id store.QueueItemID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var bodyHash string
		err := tx.QueryRow(ctx,
			`SELECT body_blob_hash FROM queue WHERE id = $1`, int64(id)).Scan(&bodyHash)
		if err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM queue WHERE id = $1`, int64(id)); err != nil {
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
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+queueSelectColumnsPG+` FROM queue WHERE id = $1`, int64(id))
	return scanQueueItemPG(row)
}

func (m *metadata) ListQueueItems(ctx context.Context, filter store.QueueFilter) ([]store.QueueItem, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var where []string
	var args []any
	pos := 1
	add := func(cond string, v any) {
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", pos)))
		args = append(args, v)
		pos++
	}
	if filter.AfterID != 0 {
		add("id > ?", int64(filter.AfterID))
	}
	if filter.State != store.QueueStateUnknown {
		add("state = ?", int32(filter.State))
	}
	if filter.PrincipalID != 0 {
		add("principal_id = ?", int64(filter.PrincipalID))
	}
	if filter.EnvelopeID != "" {
		add("envelope_id = ?", string(filter.EnvelopeID))
	}
	if filter.RecipientDomain != "" {
		add("rcpt_to LIKE ?", "%@"+strings.ToLower(filter.RecipientDomain))
	}
	q := `SELECT ` + queueSelectColumnsPG + ` FROM queue`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", pos)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.QueueItem
	for rows.Next() {
		q, err := scanQueueItemPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (m *metadata) CountQueueByState(ctx context.Context) (map[store.QueueState]int, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT state, COUNT(*) FROM queue GROUP BY state`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make(map[store.QueueState]int)
	for rows.Next() {
		var state int32
		var n int64
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO dkim_keys (domain, selector, algorithm, private_key_pem,
			  public_key_b64, status, created_at_us, rotated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (domain, selector) DO UPDATE SET
			  algorithm = EXCLUDED.algorithm,
			  private_key_pem = EXCLUDED.private_key_pem,
			  public_key_b64 = EXCLUDED.public_key_b64,
			  status = EXCLUDED.status,
			  rotated_at_us = $9`,
			strings.ToLower(key.Domain), key.Selector, int32(key.Algorithm),
			key.PrivateKeyPEM, key.PublicKeyB64, int32(key.Status),
			usMicros(now), usMicros(now), usMicros(now))
		return mapErr(err)
	})
}

func scanDKIMKeyPG(row pgx.Row) (store.DKIMKey, error) {
	var (
		id                   int64
		domain, selector     string
		algorithm, status    int32
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
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, domain, selector, algorithm, private_key_pem, public_key_b64,
		       status, created_at_us, rotated_at_us
		  FROM dkim_keys
		 WHERE domain = $1 AND status = $2
		 ORDER BY id DESC LIMIT 1`,
		strings.ToLower(domain), int32(store.DKIMKeyStatusActive))
	return scanDKIMKeyPG(row)
}

func (m *metadata) ListDKIMKeys(ctx context.Context, domain string) ([]store.DKIMKey, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, domain, selector, algorithm, private_key_pem, public_key_b64,
		       status, created_at_us, rotated_at_us
		  FROM dkim_keys WHERE domain = $1 ORDER BY selector`,
		strings.ToLower(domain))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.DKIMKey
	for rows.Next() {
		k, err := scanDKIMKeyPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (m *metadata) RotateDKIMKey(ctx context.Context, domain, oldSelector string, newKey store.DKIMKey) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE dkim_keys SET status = $1, rotated_at_us = $2
			 WHERE domain = $3 AND selector = $4`,
			int32(store.DKIMKeyStatusRetiring), usMicros(now),
			strings.ToLower(domain), oldSelector)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return fmt.Errorf("dkim retire %q/%q: %w", domain, oldSelector, store.ErrNotFound)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO dkim_keys (domain, selector, algorithm, private_key_pem,
			  public_key_b64, status, created_at_us, rotated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (domain, selector) DO UPDATE SET
			  algorithm = EXCLUDED.algorithm,
			  private_key_pem = EXCLUDED.private_key_pem,
			  public_key_b64 = EXCLUDED.public_key_b64,
			  status = EXCLUDED.status,
			  rotated_at_us = $9`,
			strings.ToLower(newKey.Domain), newKey.Selector, int32(newKey.Algorithm),
			newKey.PrivateKeyPEM, newKey.PublicKeyB64,
			int32(store.DKIMKeyStatusActive), usMicros(now), usMicros(now), usMicros(now))
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id FROM acme_accounts WHERE directory_url = $1 AND contact_email = $2`,
			acc.DirectoryURL, strings.ToLower(acc.ContactEmail)).Scan(&id)
		if err == nil {
			_, err = tx.Exec(ctx, `
				UPDATE acme_accounts SET account_key_pem = $1, kid = $2 WHERE id = $3`,
				acc.AccountKeyPEM, acc.KID, id)
			return mapErr(err)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return mapErr(err)
		}
		return tx.QueryRow(ctx, `
			INSERT INTO acme_accounts (directory_url, contact_email,
			  account_key_pem, kid, created_at_us)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			acc.DirectoryURL, strings.ToLower(acc.ContactEmail),
			acc.AccountKeyPEM, acc.KID, usMicros(acc.CreatedAt)).Scan(&id)
	})
	if err != nil {
		return store.ACMEAccount{}, err
	}
	acc.ID = store.ACMEAccountID(id)
	acc.ContactEmail = strings.ToLower(acc.ContactEmail)
	return acc, nil
}

func scanACMEAccountPG(row pgx.Row) (store.ACMEAccount, error) {
	var (
		id                                 int64
		directoryURL, contactEmail, accKey string
		kid                                string
		createdUs                          int64
	)
	err := row.Scan(&id, &directoryURL, &contactEmail, &accKey, &kid, &createdUs)
	if err != nil {
		return store.ACMEAccount{}, mapErr(err)
	}
	return store.ACMEAccount{
		ID:            store.ACMEAccountID(id),
		DirectoryURL:  directoryURL,
		ContactEmail:  contactEmail,
		AccountKeyPEM: accKey,
		KID:           kid,
		CreatedAt:     fromMicros(createdUs),
	}, nil
}

func (m *metadata) GetACMEAccount(ctx context.Context, directoryURL, contactEmail string) (store.ACMEAccount, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, directory_url, contact_email, account_key_pem, kid, created_at_us
		  FROM acme_accounts WHERE directory_url = $1 AND contact_email = $2`,
		directoryURL, strings.ToLower(contactEmail))
	return scanACMEAccountPG(row)
}

func (m *metadata) ListACMEAccounts(ctx context.Context) ([]store.ACMEAccount, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, directory_url, contact_email, account_key_pem, kid, created_at_us
		  FROM acme_accounts ORDER BY id ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ACMEAccount
	for rows.Next() {
		a, err := scanACMEAccountPG(rows)
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
		return store.ACMEOrder{}, fmt.Errorf("storepg: encode acme hostnames: %w", err)
	}
	var id int64
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO acme_orders (account_id, hostnames_json, status, order_url,
			  finalize_url, certificate_url, challenge_type, updated_at_us, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			int64(order.AccountID), string(hostnamesJSON), int32(order.Status),
			order.OrderURL, order.FinalizeURL, order.CertificateURL,
			int32(order.ChallengeType), usMicros(order.UpdatedAt), order.Error).Scan(&id)
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
		return fmt.Errorf("storepg: encode acme hostnames: %w", err)
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE acme_orders
			   SET hostnames_json = $1, status = $2, order_url = $3, finalize_url = $4,
			       certificate_url = $5, challenge_type = $6, updated_at_us = $7, error = $8
			 WHERE id = $9`,
			string(hostnamesJSON), int32(order.Status), order.OrderURL,
			order.FinalizeURL, order.CertificateURL, int32(order.ChallengeType),
			usMicros(now), order.Error, int64(order.ID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func scanACMEOrderPG(row pgx.Row) (store.ACMEOrder, error) {
	var (
		id, accountID                          int64
		hostnamesJSON                          string
		status, challengeType                  int32
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
			return store.ACMEOrder{}, fmt.Errorf("storepg: decode hostnames: %w", err)
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
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, account_id, hostnames_json, status, order_url, finalize_url,
		       certificate_url, challenge_type, updated_at_us, error
		  FROM acme_orders WHERE id = $1`, int64(id))
	return scanACMEOrderPG(row)
}

func (m *metadata) ListACMEOrdersByStatus(ctx context.Context, status store.ACMEOrderStatus) ([]store.ACMEOrder, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, account_id, hostnames_json, status, order_url, finalize_url,
		       certificate_url, challenge_type, updated_at_us, error
		  FROM acme_orders WHERE status = $1 ORDER BY updated_at_us ASC, id ASC`,
		int32(status))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ACMEOrder
	for rows.Next() {
		o, err := scanACMEOrderPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (m *metadata) UpsertACMECert(ctx context.Context, cert store.ACMECert) error {
	var orderID *int64
	if cert.OrderID != 0 {
		v := int64(cert.OrderID)
		orderID = &v
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO acme_certs (hostname, chain_pem, private_key_pem,
			  not_before_us, not_after_us, issuer, order_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (hostname) DO UPDATE SET
			  chain_pem = EXCLUDED.chain_pem,
			  private_key_pem = EXCLUDED.private_key_pem,
			  not_before_us = EXCLUDED.not_before_us,
			  not_after_us = EXCLUDED.not_after_us,
			  issuer = EXCLUDED.issuer,
			  order_id = EXCLUDED.order_id`,
			strings.ToLower(cert.Hostname), cert.ChainPEM, cert.PrivateKeyPEM,
			usMicros(cert.NotBefore), usMicros(cert.NotAfter), cert.Issuer, orderID)
		return mapErr(err)
	})
}

func scanACMECertPG(row pgx.Row) (store.ACMECert, error) {
	var (
		hostname, chain, key, issuer string
		notBeforeUs, notAfterUs      int64
		orderID                      *int64
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
	if orderID != nil {
		c.OrderID = store.ACMEOrderID(*orderID)
	}
	return c, nil
}

func (m *metadata) GetACMECert(ctx context.Context, hostname string) (store.ACMECert, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT hostname, chain_pem, private_key_pem, not_before_us, not_after_us,
		       issuer, order_id
		  FROM acme_certs WHERE hostname = $1`, strings.ToLower(hostname))
	return scanACMECertPG(row)
}

func (m *metadata) ListACMECertsExpiringBefore(ctx context.Context, t time.Time) ([]store.ACMECert, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT hostname, chain_pem, private_key_pem, not_before_us, not_after_us,
		       issuer, order_id
		  FROM acme_certs WHERE not_after_us < $1 ORDER BY not_after_us ASC`,
		usMicros(t))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ACMECert
	for rows.Next() {
		c, err := scanACMECertPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// -- webhooks ---------------------------------------------------------

func encodeRetryPolicyPG(p store.RetryPolicy) (string, error) {
	if (p == store.RetryPolicy{}) {
		return "", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("storepg: encode retry policy: %w", err)
	}
	return string(b), nil
}

func decodeRetryPolicyPG(s string) (store.RetryPolicy, error) {
	if s == "" {
		return store.RetryPolicy{}, nil
	}
	var p store.RetryPolicy
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return store.RetryPolicy{}, fmt.Errorf("storepg: decode retry policy: %w", err)
	}
	return p, nil
}

func (m *metadata) InsertWebhook(ctx context.Context, w store.Webhook) (store.Webhook, error) {
	now := m.s.clock.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now
	rpJSON, err := encodeRetryPolicyPG(w.RetryPolicy)
	if err != nil {
		return store.Webhook{}, err
	}
	var id int64
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO webhooks (owner_kind, owner_id, target_url, hmac_secret,
			  delivery_mode, retry_policy_json, active, created_at_us, updated_at_us,
			  target_kind, body_mode, extracted_text_max_bytes, text_required)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			RETURNING id`,
			int32(w.OwnerKind), w.OwnerID, w.TargetURL, w.HMACSecret,
			int32(w.DeliveryMode), rpJSON, w.Active,
			usMicros(now), usMicros(now),
			int32(w.TargetKind), int32(w.BodyMode), w.ExtractedTextMaxBytes,
			w.TextRequired).Scan(&id)
	})
	if err != nil {
		return store.Webhook{}, err
	}
	w.ID = store.WebhookID(id)
	return w, nil
}

func (m *metadata) UpdateWebhook(ctx context.Context, w store.Webhook) error {
	now := m.s.clock.Now().UTC()
	rpJSON, err := encodeRetryPolicyPG(w.RetryPolicy)
	if err != nil {
		return err
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE webhooks
			   SET owner_kind = $1, owner_id = $2, target_url = $3, hmac_secret = $4,
			       delivery_mode = $5, retry_policy_json = $6, active = $7, updated_at_us = $8,
			       target_kind = $9, body_mode = $10, extracted_text_max_bytes = $11,
			       text_required = $12
			 WHERE id = $13`,
			int32(w.OwnerKind), w.OwnerID, w.TargetURL, w.HMACSecret,
			int32(w.DeliveryMode), rpJSON, w.Active,
			usMicros(now),
			int32(w.TargetKind), int32(w.BodyMode), w.ExtractedTextMaxBytes,
			w.TextRequired,
			int64(w.ID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) DeleteWebhook(ctx context.Context, id store.WebhookID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM webhooks WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func scanWebhookPG(row pgx.Row) (store.Webhook, error) {
	var (
		id                         int64
		ownerKind, deliveryMode    int32
		ownerID, targetURL, rpJSON string
		hmac                       []byte
		active                     bool
		createdUs, updatedUs       int64
		targetKind, bodyMode       int32
		txtMax                     int64
		textRequired               bool
	)
	err := row.Scan(&id, &ownerKind, &ownerID, &targetURL, &hmac,
		&deliveryMode, &rpJSON, &active, &createdUs, &updatedUs,
		&targetKind, &bodyMode, &txtMax, &textRequired)
	if err != nil {
		return store.Webhook{}, mapErr(err)
	}
	rp, err := decodeRetryPolicyPG(rpJSON)
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
		TextRequired:          textRequired,
		RetryPolicy:           rp,
		Active:                active,
		CreatedAt:             fromMicros(createdUs),
		UpdatedAt:             fromMicros(updatedUs),
	}, nil
}

const webhookSelectColumnsPG = `id, owner_kind, owner_id, target_url, hmac_secret, delivery_mode,
		retry_policy_json, active, created_at_us, updated_at_us,
		target_kind, body_mode, extracted_text_max_bytes, text_required`

func (m *metadata) GetWebhook(ctx context.Context, id store.WebhookID) (store.Webhook, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT `+webhookSelectColumnsPG+`
		  FROM webhooks WHERE id = $1`, int64(id))
	return scanWebhookPG(row)
}

func (m *metadata) ListWebhooks(ctx context.Context, kind store.WebhookOwnerKind, ownerID string) ([]store.Webhook, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if kind == store.WebhookOwnerUnknown {
		rows, err = m.s.pool.Query(ctx, `
			SELECT `+webhookSelectColumnsPG+`
			  FROM webhooks ORDER BY id ASC`)
	} else {
		rows, err = m.s.pool.Query(ctx, `
			SELECT `+webhookSelectColumnsPG+`
			  FROM webhooks WHERE owner_kind = $1 AND owner_id = $2 ORDER BY id ASC`,
			int32(kind), ownerID)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Webhook
	for rows.Next() {
		w, err := scanWebhookPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (m *metadata) ListActiveWebhooksForDomain(ctx context.Context, domain string) ([]store.Webhook, error) {
	dom := strings.ToLower(domain)
	// See storesqlite.ListActiveWebhooksForDomain for the union shape;
	// kept identical here so SQLite and Postgres surface the same row
	// set for a given input.
	rows, err := m.s.pool.Query(ctx, `
		SELECT `+webhookSelectColumnsPG+`
		  FROM webhooks w
		 WHERE w.active = TRUE AND (
		   (w.owner_kind = $1 AND lower(w.owner_id) = $2)
		   OR (w.target_kind = $3 AND lower(w.owner_id) = $4)
		   OR (w.owner_kind = $5 AND w.owner_id IN (
		     SELECT CAST(p.id AS TEXT) FROM principals p
		      WHERE substring(p.canonical_email FROM position('@' IN p.canonical_email) + 1) = $6))
		 )
		 ORDER BY w.id ASC`,
		int32(store.WebhookOwnerDomain), dom,
		int32(store.WebhookTargetSynthetic), dom,
		int32(store.WebhookOwnerPrincipal), dom)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Webhook
	for rows.Next() {
		w, err := scanWebhookPG(rows)
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO dmarc_reports_raw (received_at_us, reporter_email, reporter_org,
			  report_id, domain, date_begin_us, date_end_us, xml_blob_hash, parsed_ok,
			  parse_error)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			usMicros(report.ReceivedAt), strings.ToLower(report.ReporterEmail),
			report.ReporterOrg, report.ReportID, strings.ToLower(report.Domain),
			usMicros(report.DateBegin), usMicros(report.DateEnd),
			report.XMLBlobHash, report.ParsedOK, report.ParseError).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		for _, r := range drows {
			if _, err := tx.Exec(ctx, `
				INSERT INTO dmarc_rows (report_id, source_ip, count, disposition,
				  spf_aligned, dkim_aligned, spf_result, dkim_result, header_from,
				  envelope_from, envelope_to)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				id, r.SourceIP, r.Count, int32(r.Disposition),
				r.SPFAligned, r.DKIMAligned, r.SPFResult, r.DKIMResult,
				strings.ToLower(r.HeaderFrom), strings.ToLower(r.EnvelopeFrom),
				strings.ToLower(r.EnvelopeTo)); err != nil {
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

func scanDMARCReportPG(row pgx.Row) (store.DMARCReport, error) {
	var (
		id                                           int64
		receivedUs, dateBeginUs, dateEndUs           int64
		reporterEmail, reporterOrg, reportID, domain string
		xmlHash, parseErr                            string
		parsedOK                                     bool
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
		ParsedOK:      parsedOK,
		ParseError:    parseErr,
	}, nil
}

func (m *metadata) GetDMARCReport(ctx context.Context, id store.DMARCReportID) (store.DMARCReport, []store.DMARCRow, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, received_at_us, reporter_email, reporter_org, report_id, domain,
		       date_begin_us, date_end_us, xml_blob_hash, parsed_ok, parse_error
		  FROM dmarc_reports_raw WHERE id = $1`, int64(id))
	rep, err := scanDMARCReportPG(row)
	if err != nil {
		return store.DMARCReport{}, nil, err
	}
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, report_id, source_ip, count, disposition, spf_aligned,
		       dkim_aligned, spf_result, dkim_result, header_from, envelope_from,
		       envelope_to
		  FROM dmarc_rows WHERE report_id = $1 ORDER BY id ASC`, int64(id))
	if err != nil {
		return rep, nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.DMARCRow
	for rows.Next() {
		var (
			rid, repID                int64
			count                     int64
			disp                      int32
			spfAligned, dkimAligned   bool
			sourceIP, spfRes, dkimRes string
			hdrFrom, envFrom, envTo   string
		)
		if err := rows.Scan(&rid, &repID, &sourceIP, &count, &disp, &spfAligned,
			&dkimAligned, &spfRes, &dkimRes, &hdrFrom, &envFrom, &envTo); err != nil {
			return rep, nil, mapErr(err)
		}
		out = append(out, store.DMARCRow{
			ID:           store.DMARCRowID(rid),
			ReportID:     store.DMARCReportID(repID),
			SourceIP:     sourceIP,
			Count:        count,
			Disposition:  disp,
			SPFAligned:   spfAligned,
			DKIMAligned:  dkimAligned,
			SPFResult:    spfRes,
			DKIMResult:   dkimRes,
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
	pos := 1
	add := func(cond string, v any) {
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", pos)))
		args = append(args, v)
		pos++
	}
	if filter.AfterID != 0 {
		add("id > ?", int64(filter.AfterID))
	}
	if filter.Domain != "" {
		add("domain = ?", strings.ToLower(filter.Domain))
	}
	if !filter.Since.IsZero() {
		add("date_begin_us >= ?", usMicros(filter.Since))
	}
	if !filter.Until.IsZero() {
		add("date_begin_us < ?", usMicros(filter.Until))
	}
	q := `SELECT id, received_at_us, reporter_email, reporter_org, report_id, domain,
	             date_begin_us, date_end_us, xml_blob_hash, parsed_ok, parse_error
	        FROM dmarc_reports_raw`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", pos)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.DMARCReport
	for rows.Next() {
		r, err := scanDMARCReportPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) DMARCAggregate(ctx context.Context, domain string, since, until time.Time) ([]store.DMARCAggregateRow, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT r.header_from, r.disposition,
		       SUM(r.count) AS total,
		       SUM(CASE WHEN r.spf_aligned THEN r.count ELSE 0 END) AS spf_pass,
		       SUM(CASE WHEN r.dkim_aligned THEN r.count ELSE 0 END) AS dkim_pass
		  FROM dmarc_rows r
		  JOIN dmarc_reports_raw rep ON rep.id = r.report_id
		 WHERE rep.domain = $1
		   AND rep.date_begin_us >= $2
		   AND rep.date_begin_us < $3
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
			disp     int32
			total    int64
			spfPass  int64
			dkimPass int64
		)
		if err := rows.Scan(&hf, &disp, &total, &spfPass, &dkimPass); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.DMARCAggregateRow{
			HeaderFrom:  hf,
			Disposition: disp,
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var existsQ string
		var existsArgs []any
		var pidArg *int64
		if principalID == nil {
			existsQ = `SELECT id FROM mailbox_acl WHERE mailbox_id = $1 AND principal_id IS NULL`
			existsArgs = []any{int64(mailboxID)}
		} else {
			v := int64(*principalID)
			pidArg = &v
			existsQ = `SELECT id FROM mailbox_acl WHERE mailbox_id = $1 AND principal_id = $2`
			existsArgs = []any{int64(mailboxID), int64(*principalID)}
		}
		var existing int64
		err := tx.QueryRow(ctx, existsQ, existsArgs...).Scan(&existing)
		if err == nil {
			_, err = tx.Exec(ctx,
				`UPDATE mailbox_acl SET rights_mask = $1, granted_by = $2 WHERE id = $3`,
				int64(rights), int64(grantedBy), existing)
			return mapErr(err)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return mapErr(err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO mailbox_acl (mailbox_id, principal_id, rights_mask, granted_by, created_at_us)
			VALUES ($1, $2, $3, $4, $5)`,
			int64(mailboxID), pidArg, int64(rights), int64(grantedBy), usMicros(now))
		return mapErr(err)
	})
}

func (m *metadata) GetMailboxACL(ctx context.Context, mailboxID store.MailboxID) ([]store.MailboxACL, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, mailbox_id, principal_id, rights_mask, granted_by, created_at_us
		  FROM mailbox_acl WHERE mailbox_id = $1
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
			pid                                  *int64
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
		if pid != nil {
			pp := store.PrincipalID(*pid)
			acl.PrincipalID = &pp
		}
		out = append(out, acl)
	}
	return out, rows.Err()
}

func (m *metadata) ListMailboxesAccessibleBy(ctx context.Context, pid store.PrincipalID) ([]store.Mailbox, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT mb.id, mb.principal_id, mb.parent_id, mb.name, mb.attributes,
		       mb.uidvalidity, mb.uidnext, mb.highest_modseq, mb.created_at_us,
		       mb.updated_at_us, mb.color_hex, mb.sort_order
		  FROM mailboxes mb
		 WHERE mb.id IN (
		   SELECT mailbox_id FROM mailbox_acl
		    WHERE (principal_id = $1 OR principal_id IS NULL)
		      AND (rights_mask & $2) = $2
		 )
		 ORDER BY mb.name ASC`,
		int64(pid), int64(store.ACLRightLookup))
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			cmdTag pgconn.CommandTag
			err    error
		)
		if principalID == nil {
			cmdTag, err = tx.Exec(ctx,
				`DELETE FROM mailbox_acl WHERE mailbox_id = $1 AND principal_id IS NULL`,
				int64(mailboxID))
		} else {
			cmdTag, err = tx.Exec(ctx,
				`DELETE FROM mailbox_acl WHERE mailbox_id = $1 AND principal_id = $2`,
				int64(mailboxID), int64(*principalID))
		}
		if err != nil {
			return mapErr(err)
		}
		if cmdTag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- JMAP states ------------------------------------------------------

func (m *metadata) GetJMAPStates(ctx context.Context, pid store.PrincipalID) (store.JMAPStates, error) {
	now := m.s.clock.Now().UTC()
	var out store.JMAPStates
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO jmap_states (principal_id, updated_at_us)
			VALUES ($1, $2) ON CONFLICT (principal_id) DO NOTHING`,
			int64(pid), usMicros(now)); err != nil {
			return mapErr(err)
		}
		var (
			ppid, mb, em, th, ide, es, vr, sv, ab, ct, cal, ce int64
			conv, msgChat, memb                                int64
			pushSub, coach, catSettings, managedRule           int64
			updatedUs                                          int64
		)
		err := tx.QueryRow(ctx, `
			SELECT principal_id, mailbox_state, email_state, thread_state,
			       identity_state, email_submission_state, vacation_response_state,
			       sieve_state, address_book_state, contact_state,
			       calendar_state, calendar_event_state,
			       conversation_state, message_chat_state, membership_state,
			       push_subscription_state, shortcut_coach_state,
			       category_settings_state, managed_rule_state,
			       updated_at_us
			  FROM jmap_states WHERE principal_id = $1`, int64(pid)).Scan(
			&ppid, &mb, &em, &th, &ide, &es, &vr, &sv, &ab, &ct, &cal, &ce,
			&conv, &msgChat, &memb, &pushSub, &coach, &catSettings, &managedRule, &updatedUs)
		if err != nil {
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
	col, err := jmapStateColumnPG(kind)
	if err != nil {
		return 0, err
	}
	now := m.s.clock.Now().UTC()
	var newVal int64
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO jmap_states (principal_id, updated_at_us)
			VALUES ($1, $2) ON CONFLICT (principal_id) DO NOTHING`,
			int64(pid), usMicros(now)); err != nil {
			return mapErr(err)
		}
		// Atomic increment + read in one statement using RETURNING.
		// The column name is from a closed enum so direct concatenation
		// is safe.
		return tx.QueryRow(ctx, fmt.Sprintf(`
			UPDATE jmap_states SET %s = %s + 1, updated_at_us = $1
			 WHERE principal_id = $2 RETURNING %s`, col, col, col),
			usMicros(now), int64(pid)).Scan(&newVal)
	})
	if err != nil {
		return 0, err
	}
	return newVal, nil
}

func jmapStateColumnPG(kind store.JMAPStateKind) (string, error) {
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
		return "", fmt.Errorf("storepg: unknown JMAPStateKind %d", kind)
	}
}

// -- TLS-RPT ----------------------------------------------------------

func (m *metadata) AppendTLSRPTFailure(ctx context.Context, f store.TLSRPTFailure) error {
	if f.RecordedAt.IsZero() {
		f.RecordedAt = m.s.clock.Now().UTC()
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tlsrpt_failures (recorded_at_us, policy_domain,
			  receiving_mta_hostname, failure_type, failure_code, failure_detail_json)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			usMicros(f.RecordedAt), strings.ToLower(f.PolicyDomain),
			f.ReceivingMTAHostname, int32(f.FailureType), f.FailureCode,
			f.FailureDetailJSON)
		return mapErr(err)
	})
}

func (m *metadata) ListTLSRPTFailures(ctx context.Context, policyDomain string, since, until time.Time) ([]store.TLSRPTFailure, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, recorded_at_us, policy_domain, receiving_mta_hostname,
		       failure_type, failure_code, failure_detail_json
		  FROM tlsrpt_failures
		 WHERE policy_domain = $1
		   AND recorded_at_us >= $2
		   AND recorded_at_us < $3
		 ORDER BY recorded_at_us ASC, id ASC`,
		strings.ToLower(policyDomain), usMicros(since), usMicros(until))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.TLSRPTFailure
	for rows.Next() {
		var (
			id, recordedUs        int64
			fType                 int32
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

func scanEmailSubmissionPG(row pgx.Row) (store.EmailSubmissionRow, error) {
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

const emailSubmissionSelectColumnsPG = `
	id, envelope_id, principal_id, identity_id, email_id, thread_id,
	send_at_us, created_at_us, undo_status, properties`

func (m *metadata) InsertEmailSubmission(ctx context.Context, row store.EmailSubmissionRow) error {
	if row.ID == "" {
		return fmt.Errorf("storepg: InsertEmailSubmission: empty id")
	}
	now := m.s.clock.Now().UTC()
	if row.CreatedAtUs == 0 {
		row.CreatedAtUs = usMicros(now)
	}
	if row.SendAtUs == 0 {
		row.SendAtUs = row.CreatedAtUs
	}
	props := row.Properties
	if props == nil {
		props = []byte{}
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO jmap_email_submissions
			  (id, envelope_id, principal_id, identity_id, email_id, thread_id,
			   send_at_us, created_at_us, undo_status, properties)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			row.ID, string(row.EnvelopeID), int64(row.PrincipalID),
			row.IdentityID, int64(row.EmailID), row.ThreadID,
			row.SendAtUs, row.CreatedAtUs, row.UndoStatus, props)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return fmt.Errorf("email submission %q: %w", row.ID, store.ErrConflict)
			}
			return mapErr(err)
		}
		return nil
	})
}

func (m *metadata) GetEmailSubmission(ctx context.Context, id string) (store.EmailSubmissionRow, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+emailSubmissionSelectColumnsPG+` FROM jmap_email_submissions WHERE id = $1`, id)
	return scanEmailSubmissionPG(row)
}

func (m *metadata) ListEmailSubmissions(ctx context.Context, principal store.PrincipalID, filter store.EmailSubmissionFilter) ([]store.EmailSubmissionRow, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	where := []string{"principal_id = $1"}
	args := []any{int64(principal)}
	idx := 2
	if filter.AfterID != "" {
		where = append(where, fmt.Sprintf("id > $%d", idx))
		args = append(args, filter.AfterID)
		idx++
	}
	if len(filter.IdentityIDs) > 0 {
		ph := make([]string, len(filter.IdentityIDs))
		for i, v := range filter.IdentityIDs {
			ph[i] = fmt.Sprintf("$%d", idx)
			args = append(args, v)
			idx++
		}
		where = append(where, "identity_id IN ("+strings.Join(ph, ",")+")")
	}
	if len(filter.EmailIDs) > 0 {
		ph := make([]string, len(filter.EmailIDs))
		for i, v := range filter.EmailIDs {
			ph[i] = fmt.Sprintf("$%d", idx)
			args = append(args, int64(v))
			idx++
		}
		where = append(where, "email_id IN ("+strings.Join(ph, ",")+")")
	}
	if len(filter.ThreadIDs) > 0 {
		ph := make([]string, len(filter.ThreadIDs))
		for i, v := range filter.ThreadIDs {
			ph[i] = fmt.Sprintf("$%d", idx)
			args = append(args, v)
			idx++
		}
		where = append(where, "thread_id IN ("+strings.Join(ph, ",")+")")
	}
	if filter.UndoStatus != "" {
		where = append(where, fmt.Sprintf("undo_status = $%d", idx))
		args = append(args, filter.UndoStatus)
		idx++
	}
	if filter.AfterUs != 0 {
		where = append(where, fmt.Sprintf("send_at_us > $%d", idx))
		args = append(args, filter.AfterUs)
		idx++
	}
	if filter.BeforeUs != 0 {
		where = append(where, fmt.Sprintf("send_at_us < $%d", idx))
		args = append(args, filter.BeforeUs)
		idx++
	}
	q := `SELECT ` + emailSubmissionSelectColumnsPG + ` FROM jmap_email_submissions WHERE `
	q += strings.Join(where, " AND ")
	q += fmt.Sprintf(" ORDER BY send_at_us ASC, id ASC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.EmailSubmissionRow
	for rows.Next() {
		r, err := scanEmailSubmissionPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateEmailSubmissionUndoStatus(ctx context.Context, id, undoStatus string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE jmap_email_submissions SET undo_status = $1 WHERE id = $2`,
			undoStatus, id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) DeleteEmailSubmission(ctx context.Context, id string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM jmap_email_submissions WHERE id = $1`, id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- JMAP Identity overlay -------------------------------------------

func scanJMAPIdentityPG(row pgx.Row) (store.JMAPIdentity, error) {
	var (
		id, name, email, textSig, htmlSig string
		principalID                       int64
		replyTo, bcc                      []byte
		mayDelete                         bool
		createdAtUs, updatedAtUs          int64
		signature                         *string
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
		MayDelete:     mayDelete,
		CreatedAtUs:   createdAtUs,
		UpdatedAtUs:   updatedAtUs,
	}
	if signature != nil {
		v := *signature
		out.Signature = &v
	}
	return out, nil
}

const jmapIdentitySelectColumnsPG = `
	id, principal_id, name, email, reply_to_json, bcc_json,
	text_signature, html_signature, may_delete, created_at_us, updated_at_us,
	signature`

func (m *metadata) InsertJMAPIdentity(ctx context.Context, row store.JMAPIdentity) error {
	if row.ID == "" {
		return fmt.Errorf("storepg: InsertJMAPIdentity: empty id")
	}
	now := m.s.clock.Now().UTC()
	if row.CreatedAtUs == 0 {
		row.CreatedAtUs = usMicros(now)
	}
	if row.UpdatedAtUs == 0 {
		row.UpdatedAtUs = row.CreatedAtUs
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO jmap_identities
			  (id, principal_id, name, email, reply_to_json, bcc_json,
			   text_signature, html_signature, may_delete, created_at_us, updated_at_us,
			   signature)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			row.ID, int64(row.PrincipalID), row.Name, row.Email,
			replyTo, bcc, row.TextSignature, row.HTMLSignature,
			row.MayDelete, row.CreatedAtUs, row.UpdatedAtUs, sig)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return fmt.Errorf("jmap identity %q: %w", row.ID, store.ErrConflict)
			}
			return mapErr(err)
		}
		return nil
	})
}

func (m *metadata) GetJMAPIdentity(ctx context.Context, id string) (store.JMAPIdentity, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+jmapIdentitySelectColumnsPG+` FROM jmap_identities WHERE id = $1`, id)
	return scanJMAPIdentityPG(row)
}

func (m *metadata) ListJMAPIdentities(ctx context.Context, principal store.PrincipalID) ([]store.JMAPIdentity, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+jmapIdentitySelectColumnsPG+`
		   FROM jmap_identities WHERE principal_id = $1
		  ORDER BY created_at_us ASC, id ASC`,
		int64(principal))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.JMAPIdentity
	for rows.Next() {
		r, err := scanJMAPIdentityPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateJMAPIdentity(ctx context.Context, row store.JMAPIdentity) error {
	now := m.s.clock.Now().UTC()
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE jmap_identities
			   SET name = $1, reply_to_json = $2, bcc_json = $3,
			       text_signature = $4, html_signature = $5, signature = $6,
			       updated_at_us = $7
			 WHERE id = $8`,
			row.Name, replyTo, bcc,
			row.TextSignature, row.HTMLSignature, sig, usMicros(now), row.ID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) DeleteJMAPIdentity(ctx context.Context, id string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM jmap_identities WHERE id = $1`, id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- LLM categorisation (REQ-FILT-200..221) --------------------------

// defaultCategorySet is retained for seeding existing rows and the legacy
// CategorySet field; new code reads DerivedCategories. Kept here so the
// pg backend can seed the row on first read without importing higher-level
// packages.
var defaultCategorySet = []store.CategoryDef{
	{Name: "primary", Description: "Personal correspondence and important messages from people you know."},
	{Name: "social", Description: "Messages from social networks and dating sites."},
	{Name: "promotions", Description: "Marketing emails, offers, deals, newsletters."},
	{Name: "updates", Description: "Receipts, confirmations, statements, account notices."},
	{Name: "forums", Description: "Mailing-list digests, online community discussions."},
}

// defaultCategorisationPrompt is the seeded system prompt (REQ-FILT-211).
// The prompt is the single source of truth for the category vocabulary.
// Must stay in sync with categorise.DefaultPrompt in internal/categorise/prompt.go.
const defaultCategorisationPrompt = `You are an email-categorisation assistant. Classify the message into one of the following categories:

- primary: Direct correspondence and important messages from people you know, plus anything that does not fit the categories below.
- social: Notifications and messages from social networks, dating sites, and messaging apps.
- promotions: Marketing emails, deals, offers, coupons, and newsletters from retailers or services.
- updates: Automated notifications — receipts, statements, confirmations, package tracking, and account alerts.
- forums: Mailing-list discussions, online community threads, and group digests.

Respond ONLY with a JSON object of the shape {"categories":["primary","social","promotions","updates","forums"],"assigned":"<name>"} where:
- "categories" lists every category defined above (always all five, in the order listed).
- "assigned" is the single category name that best fits this message, or null if no category fits.
Do not include any other text.`

func (m *metadata) GetCategorisationConfig(ctx context.Context, pid store.PrincipalID) (store.CategorisationConfig, error) {
	cfg, found, err := m.readCategorisationRow(ctx, pid)
	if err != nil {
		return store.CategorisationConfig{}, err
	}
	if found {
		return cfg, nil
	}
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

func (m *metadata) readCategorisationRow(ctx context.Context, pid store.PrincipalID) (store.CategorisationConfig, bool, error) {
	var (
		prompt                string
		guardrail             string
		categorySetJSON       []byte
		derivedCategoriesJSON *string
		derivedEpoch          int64
		endpointURL           *string
		model                 *string
		apiKeyEnv             *string
		timeoutSec            int32
		enabled               bool
		updatedAtUs           int64
	)
	err := m.s.pool.QueryRow(ctx, `
		SELECT prompt, guardrail, category_set_json, derived_categories_json,
		       derived_categories_epoch,
		       endpoint_url, model, api_key_env,
		       timeout_sec, enabled, updated_at_us
		  FROM jmap_categorisation_config
		 WHERE principal_id = $1`, int64(pid)).Scan(
		&prompt, &guardrail, &categorySetJSON, &derivedCategoriesJSON,
		&derivedEpoch,
		&endpointURL, &model, &apiKeyEnv,
		&timeoutSec, &enabled, &updatedAtUs)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.CategorisationConfig{}, false, nil
		}
		return store.CategorisationConfig{}, false, mapErr(err)
	}
	cfg := store.CategorisationConfig{
		PrincipalID:            pid,
		Prompt:                 prompt,
		Guardrail:              guardrail,
		Endpoint:               endpointURL,
		Model:                  model,
		APIKeyEnv:              apiKeyEnv,
		TimeoutSec:             int(timeoutSec),
		Enabled:                enabled,
		UpdatedAtUs:            updatedAtUs,
		DerivedCategoriesEpoch: derivedEpoch,
	}
	if len(categorySetJSON) > 0 {
		if err := json.Unmarshal(categorySetJSON, &cfg.CategorySet); err != nil {
			return store.CategorisationConfig{}, false, fmt.Errorf("storepg: decode category_set_json: %w", err)
		}
	}
	if derivedCategoriesJSON != nil && *derivedCategoriesJSON != "" {
		if err := json.Unmarshal([]byte(*derivedCategoriesJSON), &cfg.DerivedCategories); err != nil {
			return store.CategorisationConfig{}, false, fmt.Errorf("storepg: decode derived_categories_json: %w", err)
		}
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
		return fmt.Errorf("storepg: encode category_set_json: %w", err)
	}
	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		// When the caller updates the prompt, clear derived_categories_json
		// so the next classifier call refills it (REQ-FILT-217), and bump
		// the epoch so any in-flight classifier call with the old epoch
		// cannot overwrite the NULL via SetDerivedCategories.
		var storedPrompt *string
		var storedEpoch int64
		_ = tx.QueryRow(ctx,
			`SELECT prompt, derived_categories_epoch FROM jmap_categorisation_config WHERE principal_id = $1`,
			int64(cfg.PrincipalID)).Scan(&storedPrompt, &storedEpoch)
		promptChanged := storedPrompt == nil || *storedPrompt != cfg.Prompt
		var derivedJSON *string
		newEpoch := storedEpoch
		if promptChanged {
			// Increment the epoch so SetDerivedCategories calls that read
			// the previous epoch become no-ops.
			newEpoch = storedEpoch + 1
		} else if len(cfg.DerivedCategories) > 0 {
			b, jerr := json.Marshal(cfg.DerivedCategories)
			if jerr != nil {
				return fmt.Errorf("storepg: encode derived_categories_json: %w", jerr)
			}
			s := string(b)
			derivedJSON = &s
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO jmap_categorisation_config
			  (principal_id, prompt, guardrail, category_set_json, derived_categories_json,
			   derived_categories_epoch,
			   endpoint_url, model, api_key_env, timeout_sec, enabled, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (principal_id) DO UPDATE SET
			  prompt = EXCLUDED.prompt,
			  guardrail = EXCLUDED.guardrail,
			  category_set_json = EXCLUDED.category_set_json,
			  derived_categories_json = EXCLUDED.derived_categories_json,
			  derived_categories_epoch = EXCLUDED.derived_categories_epoch,
			  endpoint_url = EXCLUDED.endpoint_url,
			  model = EXCLUDED.model,
			  api_key_env = EXCLUDED.api_key_env,
			  timeout_sec = EXCLUDED.timeout_sec,
			  enabled = EXCLUDED.enabled,
			  updated_at_us = EXCLUDED.updated_at_us`,
			int64(cfg.PrincipalID), cfg.Prompt, cfg.Guardrail, js, derivedJSON,
			newEpoch,
			cfg.Endpoint, cfg.Model, cfg.APIKeyEnv,
			int32(timeoutSec), cfg.Enabled, usMicros(now))
		return mapErr(err)
	})
}

// SetDerivedCategories updates only the derived_categories_json column for
// pid (REQ-FILT-217). See store.Metadata.SetDerivedCategories for contract.
//
// Returns (true, nil) when the row was updated, (false, nil) when the epoch
// guard rejected the write (stale call — prompt changed under us).
func (m *metadata) SetDerivedCategories(ctx context.Context, pid store.PrincipalID, categories []string, expectedEpoch int64) (bool, error) {
	cats := sanitiseDerivedCategories(categories)
	var derivedJSON *string
	if len(cats) > 0 {
		b, err := json.Marshal(cats)
		if err != nil {
			return false, fmt.Errorf("storepg: encode derived_categories_json: %w", err)
		}
		s := string(b)
		derivedJSON = &s
	}
	var updated bool
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`UPDATE jmap_categorisation_config
			    SET derived_categories_json = $1
			  WHERE principal_id = $2
			    AND derived_categories_epoch = $3`,
			derivedJSON, int64(pid), expectedEpoch)
		if err != nil {
			return mapErr(err)
		}
		updated = res.RowsAffected() > 0
		return nil
	})
	return updated, err
}

// sanitiseDerivedCategories enforces the per-entry and total bounds defined
// by store.MaxDerivedCategoryEntries and store.MaxDerivedCategoryNameBytes.
func sanitiseDerivedCategories(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, name := range in {
		if len(out) >= store.MaxDerivedCategoryEntries {
			break
		}
		if len(name) > store.MaxDerivedCategoryNameBytes {
			name = name[:store.MaxDerivedCategoryNameBytes]
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// -- LLM classification records (REQ-FILT-66 / REQ-FILT-216) ----------

func (m *metadata) SetLLMClassification(ctx context.Context, rec store.LLMClassificationRecord) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO llm_classifications
			  (message_id, principal_id,
			   spam_verdict, spam_confidence, spam_reason,
			   spam_prompt_applied, spam_model, spam_classified_at_us,
			   category_assigned, category_prompt_applied,
			   category_model, category_classified_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (message_id) DO UPDATE SET
			  spam_verdict             = COALESCE(EXCLUDED.spam_verdict, llm_classifications.spam_verdict),
			  spam_confidence          = COALESCE(EXCLUDED.spam_confidence, llm_classifications.spam_confidence),
			  spam_reason              = COALESCE(EXCLUDED.spam_reason, llm_classifications.spam_reason),
			  spam_prompt_applied      = COALESCE(EXCLUDED.spam_prompt_applied, llm_classifications.spam_prompt_applied),
			  spam_model               = COALESCE(EXCLUDED.spam_model, llm_classifications.spam_model),
			  spam_classified_at_us    = COALESCE(EXCLUDED.spam_classified_at_us, llm_classifications.spam_classified_at_us),
			  category_assigned        = COALESCE(EXCLUDED.category_assigned, llm_classifications.category_assigned),
			  category_prompt_applied  = COALESCE(EXCLUDED.category_prompt_applied, llm_classifications.category_prompt_applied),
			  category_model           = COALESCE(EXCLUDED.category_model, llm_classifications.category_model),
			  category_classified_at_us = COALESCE(EXCLUDED.category_classified_at_us, llm_classifications.category_classified_at_us)`,
			int64(rec.MessageID), int64(rec.PrincipalID),
			rec.SpamVerdict, rec.SpamConfidence, rec.SpamReason,
			rec.SpamPromptApplied, rec.SpamModel, pgOptTimeToUs(rec.SpamClassifiedAt),
			rec.CategoryAssigned, rec.CategoryPromptApplied,
			rec.CategoryModel, pgOptTimeToUs(rec.CategoryClassifiedAt))
		return mapErr(err)
	})
}

func (m *metadata) GetLLMClassification(ctx context.Context, msgID store.MessageID) (store.LLMClassificationRecord, error) {
	recs, err := m.BatchGetLLMClassifications(ctx, []store.MessageID{msgID})
	if err != nil {
		return store.LLMClassificationRecord{}, err
	}
	r, ok := recs[msgID]
	if !ok {
		return store.LLMClassificationRecord{}, store.ErrNotFound
	}
	return r, nil
}

func (m *metadata) BatchGetLLMClassifications(ctx context.Context, msgIDs []store.MessageID) (map[store.MessageID]store.LLMClassificationRecord, error) {
	if len(msgIDs) == 0 {
		return map[store.MessageID]store.LLMClassificationRecord{}, nil
	}
	ph := make([]string, len(msgIDs))
	args := make([]any, len(msgIDs))
	for i, id := range msgIDs {
		ph[i] = fmt.Sprintf("$%d", i+1)
		args[i] = int64(id)
	}
	q := `SELECT message_id, principal_id,
		     spam_verdict, spam_confidence, spam_reason,
		     spam_prompt_applied, spam_model, spam_classified_at_us,
		     category_assigned, category_prompt_applied,
		     category_model, category_classified_at_us
		  FROM llm_classifications
		 WHERE message_id IN (` + strings.Join(ph, ",") + `)`
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make(map[store.MessageID]store.LLMClassificationRecord, len(msgIDs))
	for rows.Next() {
		var (
			msgIDInt, pidInt         int64
			spamVerdict              *string
			spamConfidence           *float64
			spamReason               *string
			spamPromptApplied        *string
			spamModel                *string
			spamClassifiedAtUs       *int64
			categoryAssigned         *string
			categoryPromptApplied    *string
			categoryModel            *string
			categoryClassifiedAtUs   *int64
		)
		if err := rows.Scan(&msgIDInt, &pidInt,
			&spamVerdict, &spamConfidence, &spamReason,
			&spamPromptApplied, &spamModel, &spamClassifiedAtUs,
			&categoryAssigned, &categoryPromptApplied,
			&categoryModel, &categoryClassifiedAtUs); err != nil {
			return nil, mapErr(err)
		}
		rec := store.LLMClassificationRecord{
			MessageID:             store.MessageID(msgIDInt),
			PrincipalID:           store.PrincipalID(pidInt),
			SpamVerdict:           spamVerdict,
			SpamConfidence:        spamConfidence,
			SpamReason:            spamReason,
			SpamPromptApplied:     spamPromptApplied,
			SpamModel:             spamModel,
			CategoryAssigned:      categoryAssigned,
			CategoryPromptApplied: categoryPromptApplied,
			CategoryModel:         categoryModel,
		}
		if spamClassifiedAtUs != nil {
			t := fromMicros(*spamClassifiedAtUs)
			rec.SpamClassifiedAt = &t
		}
		if categoryClassifiedAtUs != nil {
			t := fromMicros(*categoryClassifiedAtUs)
			rec.CategoryClassifiedAt = &t
		}
		out[store.MessageID(msgIDInt)] = rec
	}
	return out, mapErr(rows.Err())
}

// pgOptTimeToUs converts an optional *time.Time to a nullable int64 (Unix
// micros) for pgx binding. Nil input → nil (SQL NULL).
func pgOptTimeToUs(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := usMicros(*t)
	return &v
}
