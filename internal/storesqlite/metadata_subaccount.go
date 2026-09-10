package storesqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the sub-account promotion store primitives (issue
// #227, REQ-SUBACCT-09, REQ-IMAP-IMP-106/107) for the SQLite backend.
// Schema commentary lives in migrations/0104_subaccount_migrations.sql.
// Orchestration (SeparateIdentity / RunSubAccountMigration /
// RemoveSubAccount) lives in internal/store/subaccount.go and calls only
// the Metadata methods here plus the pre-existing ones.

const subAccountMigrationSelectCols = `
	id, parent_principal_id, sub_principal_id, identity_id, status,
	messages_total, messages_moved, messages_copied, last_error,
	created_at_us, updated_at_us`

func scanSubAccountMigration(row rowLike) (store.SubAccountMigration, error) {
	var (
		id, identityID, status, lastError string
		parentID, subID                   int64
		total, moved, copied              int64
		createdUs, updatedUs              int64
	)
	if err := row.Scan(&id, &parentID, &subID, &identityID, &status,
		&total, &moved, &copied, &lastError, &createdUs, &updatedUs); err != nil {
		return store.SubAccountMigration{}, mapErr(err)
	}
	return store.SubAccountMigration{
		ID:                id,
		ParentPrincipalID: store.PrincipalID(parentID),
		SubPrincipalID:    store.PrincipalID(subID),
		IdentityID:        identityID,
		Status:            store.SubAccountMigrationStatus(status),
		MessagesTotal:     total,
		MessagesMoved:     moved,
		MessagesCopied:    copied,
		LastError:         lastError,
		CreatedAt:         fromMicros(createdUs),
		UpdatedAt:         fromMicros(updatedUs),
	}, nil
}

func (m *metadata) InsertSubAccountMigration(ctx context.Context, mig store.SubAccountMigration) (store.SubAccountMigration, error) {
	id := mig.ID
	if id == "" {
		var err error
		id, err = newOpaqueID(m.s.randReader)
		if err != nil {
			return store.SubAccountMigration{}, err
		}
	}
	status := mig.Status
	if status == "" {
		status = store.SubAccountMigrationStatusPending
	}
	now := m.s.clock.Now().UTC()
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO subaccount_migrations
			  (id, parent_principal_id, sub_principal_id, identity_id, status,
			   messages_total, messages_moved, messages_copied, last_error,
			   created_at_us, updated_at_us)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			id, int64(mig.ParentPrincipalID), int64(mig.SubPrincipalID), mig.IdentityID, string(status),
			mig.MessagesTotal, mig.MessagesMoved, mig.MessagesCopied, mig.LastError,
			usMicros(now), usMicros(now))
		return mapErr(err)
	})
	if err != nil {
		return store.SubAccountMigration{}, err
	}
	return m.GetSubAccountMigration(ctx, id)
}

func (m *metadata) GetSubAccountMigration(ctx context.Context, id string) (store.SubAccountMigration, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+subAccountMigrationSelectCols+` FROM subaccount_migrations WHERE id = ?`, id)
	return scanSubAccountMigration(row)
}

func (m *metadata) GetSubAccountMigrationByIdentity(ctx context.Context, identityID string) (store.SubAccountMigration, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+subAccountMigrationSelectCols+` FROM subaccount_migrations WHERE identity_id = ?`, identityID)
	return scanSubAccountMigration(row)
}

func (m *metadata) GetSubAccountMigrationBySubPrincipal(ctx context.Context, subID store.PrincipalID) (store.SubAccountMigration, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+subAccountMigrationSelectCols+` FROM subaccount_migrations WHERE sub_principal_id = ?`, int64(subID))
	return scanSubAccountMigration(row)
}

func (m *metadata) ListPendingSubAccountMigrations(ctx context.Context) ([]store.SubAccountMigration, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+subAccountMigrationSelectCols+`
		   FROM subaccount_migrations
		  WHERE status IN ('pending','running')
		  ORDER BY created_at_us ASC, id ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.SubAccountMigration
	for rows.Next() {
		mig, err := scanSubAccountMigration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mig)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateSubAccountMigrationProgress(
	ctx context.Context, id string, status store.SubAccountMigrationStatus,
	movedDelta, copiedDelta int64, totalIfUnset *int64, lastError string,
) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var totalArg any
		if totalIfUnset != nil {
			totalArg = *totalIfUnset
		} else {
			totalArg = int64(0)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE subaccount_migrations
			   SET status = ?,
			       messages_moved = messages_moved + ?,
			       messages_copied = messages_copied + ?,
			       messages_total = CASE WHEN messages_total = 0 THEN ? ELSE messages_total END,
			       last_error = ?,
			       updated_at_us = ?
			 WHERE id = ?`,
			string(status), movedDelta, copiedDelta, totalArg, lastError, usMicros(now), id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: UpdateSubAccountMigrationProgress rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) RebindJMAPIdentityPrincipal(ctx context.Context, identityID string, newPrincipalID store.PrincipalID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE jmap_identities SET principal_id = ? WHERE id = ?`,
			int64(newPrincipalID), identityID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: RebindJMAPIdentityPrincipal rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) RebindIMAPImportAccountPrincipal(ctx context.Context, accountID string, newPrincipalID store.PrincipalID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE imapimport_account SET principal_id = ?, updated_at = ? WHERE id = ?`,
			int64(newPrincipalID), usMicros(now), accountID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: RebindIMAPImportAccountPrincipal rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// ReparentMessage: see store.Metadata for the contract.
func (m *metadata) ReparentMessage(ctx context.Context, msgID store.MessageID, newPrincipalID store.PrincipalID, mailboxMoves map[store.MailboxID]store.MailboxID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var oldPid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM messages WHERE id = ?`, int64(msgID)).Scan(&oldPid); err != nil {
			return mapErr(err)
		}
		if oldPid == int64(newPrincipalID) {
			// Idempotent no-op: a prior, possibly-interrupted run already
			// completed this reparent.
			return nil
		}
		for fromMB, toMB := range mailboxMoves {
			var flags int64
			var keywords string
			var snoozedUs sql.NullInt64
			var receivedTo string
			err := tx.QueryRowContext(ctx, `
				SELECT flags, keywords_csv, snoozed_until_us, received_to
				  FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
				int64(msgID), int64(fromMB)).Scan(&flags, &keywords, &snoozedUs, &receivedTo)
			if err == sql.ErrNoRows {
				// Nothing to move here (already moved by an earlier partial
				// run, or the caller passed a stale mapping); tolerate.
				continue
			}
			if err != nil {
				return mapErr(err)
			}

			var tgtUIDNext, tgtHighest int64
			if err := tx.QueryRowContext(ctx,
				`SELECT uidnext, highest_modseq FROM mailboxes WHERE id = ?`,
				int64(toMB)).Scan(&tgtUIDNext, &tgtHighest); err != nil {
				return mapErr(err)
			}
			newUID := tgtUIDNext
			newModSeq := tgtHighest + 1

			var snoozedArg any
			if snoozedUs.Valid {
				snoozedArg = snoozedUs.Int64
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO message_mailboxes
				  (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us, received_to)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				int64(msgID), int64(toMB), newUID, newModSeq, flags, keywords, snoozedArg, receivedTo); err != nil {
				return mapErr(err)
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
				int64(msgID), int64(fromMB)); err != nil {
				return mapErr(err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
				newModSeq, usMicros(now), int64(toMB)); err != nil {
				return mapErr(err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE mailboxes SET highest_modseq = highest_modseq + 1, updated_at_us = ? WHERE id = ?`,
				usMicros(now), int64(fromMB)); err != nil {
				return mapErr(err)
			}
			if err := appendStateChange(ctx, tx, store.PrincipalID(oldPid),
				store.EntityKindEmail, uint64(msgID), uint64(fromMB), store.ChangeOpDestroyed, now); err != nil {
				return err
			}
			if err := appendStateChange(ctx, tx, newPrincipalID,
				store.EntityKindEmail, uint64(msgID), uint64(toMB), store.ChangeOpCreated, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE messages SET principal_id = ? WHERE id = ?`,
			int64(newPrincipalID), int64(msgID)); err != nil {
			return mapErr(err)
		}
		return nil
	})
}

// CopyImportedMessageToSubAccount: see store.Metadata for the contract.
func (m *metadata) CopyImportedMessageToSubAccount(ctx context.Context, req store.CopyImportedMessageRequest) (store.CopyImportedMessageResult, error) {
	if len(req.Targets) == 0 {
		return store.CopyImportedMessageResult{}, fmt.Errorf("storesqlite: CopyImportedMessageToSubAccount: targets must not be empty")
	}
	now := m.s.clock.Now().UTC()
	var result store.CopyImportedMessageResult
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var existing sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT copied_message_id FROM imapimport_message_state
			 WHERE account_id = ? AND herold_message_id = ? AND copied_message_id != 0
			 LIMIT 1`,
			req.AccountID, int64(req.SourceMessageID)).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return mapErr(err)
		}
		if err == nil && existing.Valid && existing.Int64 != 0 {
			result.MessageID = store.MessageID(existing.Int64)
			result.AlreadyDone = true
			return nil
		}

		newID, _, _, ierr := m.insertMessageTx(ctx, tx, req.NewMessage, req.Targets, now, true)
		if ierr != nil {
			return ierr
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE imapimport_message_state
			   SET copied_message_id = ?
			 WHERE account_id = ? AND herold_message_id = ?`,
			int64(newID), req.AccountID, int64(req.SourceMessageID)); err != nil {
			return mapErr(err)
		}
		result.MessageID = newID
		result.AlreadyDone = false
		return nil
	})
	if err != nil {
		return store.CopyImportedMessageResult{}, err
	}
	return result, nil
}
