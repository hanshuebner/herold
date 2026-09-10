package storepg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the sub-account promotion store primitives (issue
// #227, REQ-SUBACCT-09, REQ-IMAP-IMP-106/107) for the Postgres backend.
// Mirrors storesqlite/metadata_subaccount.go; schema commentary lives in
// migrations/0104_subaccount_migrations.sql. Orchestration
// (SeparateIdentity / RunSubAccountMigration / RemoveSubAccount) lives in
// internal/store/subaccount.go and calls only the Metadata methods here
// plus the pre-existing ones.

const subAccountMigrationSelectColsPG = `
	id, parent_principal_id, sub_principal_id, identity_id, status,
	messages_total, messages_moved, messages_copied, last_error,
	created_at_us, updated_at_us`

func scanSubAccountMigrationPG(row pgx.Row) (store.SubAccountMigration, error) {
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
		id, err = newOpaqueIDPG(m.s.randReader)
		if err != nil {
			return store.SubAccountMigration{}, err
		}
	}
	status := mig.Status
	if status == "" {
		status = store.SubAccountMigrationStatusPending
	}
	now := m.s.clock.Now().UTC()
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO subaccount_migrations
			  (id, parent_principal_id, sub_principal_id, identity_id, status,
			   messages_total, messages_moved, messages_copied, last_error,
			   created_at_us, updated_at_us)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
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
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+subAccountMigrationSelectColsPG+` FROM subaccount_migrations WHERE id = $1`, id)
	return scanSubAccountMigrationPG(row)
}

func (m *metadata) GetSubAccountMigrationByIdentity(ctx context.Context, identityID string) (store.SubAccountMigration, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+subAccountMigrationSelectColsPG+` FROM subaccount_migrations WHERE identity_id = $1`, identityID)
	return scanSubAccountMigrationPG(row)
}

func (m *metadata) GetSubAccountMigrationBySubPrincipal(ctx context.Context, subID store.PrincipalID) (store.SubAccountMigration, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+subAccountMigrationSelectColsPG+` FROM subaccount_migrations WHERE sub_principal_id = $1`, int64(subID))
	return scanSubAccountMigrationPG(row)
}

func (m *metadata) ListPendingSubAccountMigrations(ctx context.Context) ([]store.SubAccountMigration, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+subAccountMigrationSelectColsPG+`
		   FROM subaccount_migrations
		  WHERE status IN ('pending','running')
		  ORDER BY created_at_us ASC, id ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.SubAccountMigration
	for rows.Next() {
		mig, err := scanSubAccountMigrationPG(rows)
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var totalArg int64
		if totalIfUnset != nil {
			totalArg = *totalIfUnset
		}
		tag, err := tx.Exec(ctx, `
			UPDATE subaccount_migrations
			   SET status = $1,
			       messages_moved = messages_moved + $2,
			       messages_copied = messages_copied + $3,
			       messages_total = CASE WHEN messages_total = 0 THEN $4 ELSE messages_total END,
			       last_error = $5,
			       updated_at_us = $6
			 WHERE id = $7`,
			string(status), movedDelta, copiedDelta, totalArg, lastError, usMicros(now), id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) RebindJMAPIdentityPrincipal(ctx context.Context, identityID string, newPrincipalID store.PrincipalID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE jmap_identities SET principal_id = $1 WHERE id = $2`,
			int64(newPrincipalID), identityID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) RebindIMAPImportAccountPrincipal(ctx context.Context, accountID string, newPrincipalID store.PrincipalID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE imapimport_account SET principal_id = $1, updated_at = $2 WHERE id = $3`,
			int64(newPrincipalID), usMicros(now), accountID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// ReparentMessage: see store.Metadata for the contract.
func (m *metadata) ReparentMessage(ctx context.Context, msgID store.MessageID, newPrincipalID store.PrincipalID, mailboxMoves map[store.MailboxID]store.MailboxID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var oldPid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM messages WHERE id = $1`, int64(msgID)).Scan(&oldPid); err != nil {
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
			var snoozedUs *int64
			var receivedTo string
			err := tx.QueryRow(ctx, `
				SELECT flags, keywords_csv, snoozed_until_us, received_to
				  FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
				int64(msgID), int64(fromMB)).Scan(&flags, &keywords, &snoozedUs, &receivedTo)
			if err == pgx.ErrNoRows {
				// Nothing to move here (already moved by an earlier partial
				// run, or the caller passed a stale mapping); tolerate.
				continue
			}
			if err != nil {
				return mapErr(err)
			}

			var tgtUIDNext, tgtHighest int64
			if err := tx.QueryRow(ctx,
				`SELECT uidnext, highest_modseq FROM mailboxes WHERE id = $1`,
				int64(toMB)).Scan(&tgtUIDNext, &tgtHighest); err != nil {
				return mapErr(err)
			}
			newUID := tgtUIDNext
			newModSeq := tgtHighest + 1

			var snoozedArg any
			if snoozedUs != nil {
				snoozedArg = *snoozedUs
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO message_mailboxes
				  (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us, received_to)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				int64(msgID), int64(toMB), newUID, newModSeq, flags, keywords, snoozedArg, receivedTo); err != nil {
				return mapErr(err)
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
				int64(msgID), int64(fromMB)); err != nil {
				return mapErr(err)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
				newModSeq, usMicros(now), int64(toMB)); err != nil {
				return mapErr(err)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE mailboxes SET highest_modseq = highest_modseq + 1, updated_at_us = $1 WHERE id = $2`,
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
		if _, err := tx.Exec(ctx,
			`UPDATE messages SET principal_id = $1 WHERE id = $2`,
			int64(newPrincipalID), int64(msgID)); err != nil {
			return mapErr(err)
		}
		return nil
	})
}

// CopyImportedMessageToSubAccount: see store.Metadata for the contract.
func (m *metadata) CopyImportedMessageToSubAccount(ctx context.Context, req store.CopyImportedMessageRequest) (store.CopyImportedMessageResult, error) {
	if len(req.Targets) == 0 {
		return store.CopyImportedMessageResult{}, fmt.Errorf("storepg: CopyImportedMessageToSubAccount: targets must not be empty")
	}
	now := m.s.clock.Now().UTC()
	var result store.CopyImportedMessageResult
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var existing int64
		err := tx.QueryRow(ctx, `
			SELECT copied_message_id FROM imapimport_message_state
			 WHERE account_id = $1 AND herold_message_id = $2 AND copied_message_id != 0
			 LIMIT 1`,
			req.AccountID, int64(req.SourceMessageID)).Scan(&existing)
		if err != nil && err != pgx.ErrNoRows {
			return mapErr(err)
		}
		if err == nil && existing != 0 {
			result.MessageID = store.MessageID(existing)
			result.AlreadyDone = true
			return nil
		}

		newID, _, _, ierr := m.insertMessageTx(ctx, tx, req.NewMessage, req.Targets, now, true)
		if ierr != nil {
			return ierr
		}
		if _, err := tx.Exec(ctx, `
			UPDATE imapimport_message_state
			   SET copied_message_id = $1
			 WHERE account_id = $2 AND herold_message_id = $3`,
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
