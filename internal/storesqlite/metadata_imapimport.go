package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the IMAP-import store methods (issue #25,
// REQ-IMAP-IMP-02, -15..19, -34, -42, -70, -74) for the SQLite backend.
// Schema commentary lives in migrations/0057_imap_import.sql.

const imapImportAccountSelectCols = `
	id, identity_id, principal_id, account_name, host, port, tls_mode,
	username, auth_method, backfill_floor_date,
	credential_ct, state, last_success_at, last_error,
	delete_propagates, provenance_mailbox_id, created_at, updated_at`

func scanIMAPImportAccount(row rowLike) (store.IMAPImportAccount, error) {
	var (
		id, accountName, host, tlsMode, username, authMethod, state, lastError string
		identityID                                                             sql.NullString
		pid, port                                                              int64
		backfillFloorUs, lastSuccessUs                                         sql.NullInt64
		credentialCT                                                           []byte
		deletePropagates                                                       int64
		provenanceMailboxID                                                    sql.NullInt64
		createdUs, updatedUs                                                   int64
	)
	err := row.Scan(
		&id, &identityID, &pid, &accountName, &host, &port, &tlsMode,
		&username, &authMethod, &backfillFloorUs,
		&credentialCT, &state, &lastSuccessUs, &lastError,
		&deletePropagates, &provenanceMailboxID, &createdUs, &updatedUs,
	)
	if err != nil {
		return store.IMAPImportAccount{}, mapErr(err)
	}
	acc := store.IMAPImportAccount{
		ID:                  id,
		IdentityID:          identityID.String,
		ProvenanceMailboxID: store.MailboxID(provenanceMailboxID.Int64),
		PrincipalID:         store.PrincipalID(pid),
		AccountName:         accountName,
		Host:                host,
		Port:                int(port),
		TLSMode:             store.IMAPImportTLSMode(tlsMode),
		Username:            username,
		AuthMethod:          store.IMAPImportAuthMethod(authMethod),
		CredentialCT:        nullableBytes(credentialCT),
		State:               store.IMAPImportAccountState(state),
		LastError:           lastError,
		DeletePropagates:    deletePropagates != 0,
		CreatedAt:           fromMicros(createdUs),
		UpdatedAt:           fromMicros(updatedUs),
	}
	if backfillFloorUs.Valid {
		t := fromMicros(backfillFloorUs.Int64)
		acc.BackfillFloorDate = &t
	}
	if lastSuccessUs.Valid {
		t := fromMicros(lastSuccessUs.Int64)
		acc.LastSuccessAt = &t
	}
	return acc, nil
}

func (m *metadata) CreateIMAPImportAccount(ctx context.Context, create store.IMAPImportAccountCreate) (store.IMAPImportAccount, error) {
	if err := store.ValidateIMAPImportCredentialCT(create.CredentialCT); err != nil {
		return store.IMAPImportAccount{}, err
	}
	id, err := newOpaqueID(m.s.randReader)
	if err != nil {
		return store.IMAPImportAccount{}, err
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	state := create.State
	if state == "" {
		state = store.IMAPImportAccountStateEnabled
	}
	var deletePropagates int64
	if create.DeletePropagates {
		deletePropagates = 1
	}
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO imapimport_account
			  (id, identity_id, principal_id, account_name, host, port, tls_mode,
			   username, auth_method, backfill_floor_date,
			   credential_ct, state, last_success_at, last_error,
			   delete_propagates, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,NULL,'',?,?,?)`,
			id, nullStringOrNil(create.IdentityID), int64(create.PrincipalID),
			create.AccountName, create.Host, create.Port,
			string(create.TLSMode), create.Username, string(create.AuthMethod),
			nullOrMicros(ptrTimeOrZero(create.BackfillFloorDate)),
			create.CredentialCT, string(state),
			deletePropagates, nowUs, nowUs,
		)
		return mapErr(err)
	})
	if err != nil {
		return store.IMAPImportAccount{}, err
	}
	acc := store.IMAPImportAccount{
		ID:                id,
		IdentityID:        create.IdentityID,
		PrincipalID:       create.PrincipalID,
		AccountName:       create.AccountName,
		Host:              create.Host,
		Port:              create.Port,
		TLSMode:           create.TLSMode,
		Username:          create.Username,
		AuthMethod:        create.AuthMethod,
		BackfillFloorDate: create.BackfillFloorDate,
		CredentialCT:      create.CredentialCT,
		State:             state,
		LastError:         "",
		DeletePropagates:  create.DeletePropagates,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	return acc, nil
}

func (m *metadata) UpdateIMAPImportAccount(ctx context.Context, update store.IMAPImportAccountUpdate) (store.IMAPImportAccount, error) {
	if err := store.ValidateIMAPImportCredentialCT(update.CredentialCT); err != nil {
		return store.IMAPImportAccount{}, err
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	var deletePropagates int64
	if update.DeletePropagates {
		deletePropagates = 1
	}
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Build args in query column order so positional ? placeholders align.
		args := []any{
			nullStringOrNil(update.IdentityID),
			update.AccountName, update.Host, update.Port,
			string(update.TLSMode), update.Username, string(update.AuthMethod),
			nullOrMicros(ptrTimeOrZero(update.BackfillFloorDate)),
		}
		var credExpr string
		if len(update.CredentialCT) > 0 {
			credExpr = "credential_ct = ?,"
			args = append(args, update.CredentialCT)
		}
		args = append(args,
			string(update.State), deletePropagates, nowUs,
			update.ID, int64(update.PrincipalID),
		)
		res, err := tx.ExecContext(ctx, `
			UPDATE imapimport_account SET
			  identity_id = ?,
			  account_name = ?, host = ?, port = ?, tls_mode = ?,
			  username = ?, auth_method = ?,
			  backfill_floor_date = ?,
			  `+credExpr+`
			  state = ?, delete_propagates = ?, updated_at = ?
			WHERE id = ? AND principal_id = ?`,
			args...,
		)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: UpdateIMAPImportAccount rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return store.IMAPImportAccount{}, err
	}
	return m.GetIMAPImportAccount(ctx, update.ID)
}

func (m *metadata) GetIMAPImportAccount(ctx context.Context, id string) (store.IMAPImportAccount, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+imapImportAccountSelectCols+`
		   FROM imapimport_account WHERE id = ?`, id)
	return scanIMAPImportAccount(row)
}

func (m *metadata) ListIMAPImportAccountsByPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.IMAPImportAccount, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+imapImportAccountSelectCols+`
		   FROM imapimport_account WHERE principal_id = ?
		   ORDER BY created_at ASC`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanIMAPImportAccountRows(rows)
}

func (m *metadata) ListEnabledIMAPImportAccounts(ctx context.Context) ([]store.IMAPImportAccount, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+imapImportAccountSelectCols+`
		   FROM imapimport_account WHERE state = 'enabled'
		   ORDER BY created_at ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanIMAPImportAccountRows(rows)
}

func scanIMAPImportAccountRows(rows *sql.Rows) ([]store.IMAPImportAccount, error) {
	var out []store.IMAPImportAccount
	for rows.Next() {
		acc, err := scanIMAPImportAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteIMAPImportAccount(ctx context.Context, principalID store.PrincipalID, id string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM imapimport_account WHERE id = ? AND principal_id = ?`,
			id, int64(principalID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: DeleteIMAPImportAccount rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) SetIMAPImportAccountState(ctx context.Context, id string, state store.IMAPImportAccountState, lastError string, lastSuccessAt *time.Time) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		now := m.s.clock.Now().UTC()
		var lastSuccessUs any
		if lastSuccessAt != nil && !lastSuccessAt.IsZero() {
			lastSuccessUs = usMicros(*lastSuccessAt)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE imapimport_account SET
			  state = ?, last_error = ?,
			  last_success_at = COALESCE(?, last_success_at),
			  updated_at = ?
			WHERE id = ?`,
			string(state), lastError, lastSuccessUs, usMicros(now), id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: SetIMAPImportAccountState rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) SetIMAPImportProvenanceMailbox(ctx context.Context, id string, mailboxID store.MailboxID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE imapimport_account SET provenance_mailbox_id = ?, updated_at = ?
			   WHERE id = ?`,
			int64(mailboxID), usMicros(m.s.clock.Now().UTC()), id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: SetIMAPImportProvenanceMailbox rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListIMAPImportMessageStatesByAccount(ctx context.Context, accountID string) ([]store.IMAPImportMessageState, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state WHERE account_id = ?`,
		accountID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanIMAPImportMessageStateRows(rows)
}

func (m *metadata) ListIMAPImportMessageStatesByMessage(ctx context.Context, heroldMessageID store.MessageID) ([]store.IMAPImportMessageState, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state WHERE herold_message_id = ?`,
		int64(heroldMessageID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanIMAPImportMessageStateRows(rows)
}

func scanIMAPImportMessageStateRows(rows *sql.Rows) ([]store.IMAPImportMessageState, error) {
	var out []store.IMAPImportMessageState
	for rows.Next() {
		s, err := scanIMAPImportMessageState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// -- folder map -----------------------------------------------------------

func (m *metadata) GetIMAPImportFolderMap(ctx context.Context, accountID string) ([]store.IMAPImportFolderMapEntry, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT account_id, upstream_folder, herold_mailbox_name
		   FROM imapimport_folder_map WHERE account_id = ?
		   ORDER BY upstream_folder ASC`,
		accountID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.IMAPImportFolderMapEntry
	for rows.Next() {
		var e store.IMAPImportFolderMapEntry
		if err := rows.Scan(&e.AccountID, &e.UpstreamFolder, &e.HeroldMailboxName); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (m *metadata) SetIMAPImportFolderMap(ctx context.Context, accountID string, entries []store.IMAPImportFolderMapEntry) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM imapimport_folder_map WHERE account_id = ?`, accountID); err != nil {
			return mapErr(err)
		}
		for _, e := range entries {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO imapimport_folder_map (account_id, upstream_folder, herold_mailbox_name)
				 VALUES (?, ?, ?)`,
				accountID, e.UpstreamFolder, e.HeroldMailboxName); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

// -- folder cursor --------------------------------------------------------

func (m *metadata) GetIMAPImportFolderCursor(ctx context.Context, accountID, upstreamFolder string) (store.IMAPImportFolderCursor, bool, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT account_id, upstream_folder, uidvalidity, uidnext,
		        low_water_uid, high_water_uid, highest_modseq
		   FROM imapimport_folder_cursor
		  WHERE account_id = ? AND upstream_folder = ?`,
		accountID, upstreamFolder)
	c, err := scanIMAPImportFolderCursor(row)
	if err == store.ErrNotFound {
		return store.IMAPImportFolderCursor{}, false, nil
	}
	if err != nil {
		return store.IMAPImportFolderCursor{}, false, err
	}
	return c, true, nil
}

func scanIMAPImportFolderCursor(row rowLike) (store.IMAPImportFolderCursor, error) {
	var (
		accountID, upstreamFolder   string
		uidvalidity, uidnext        int64
		lowWater, highWater, modseq int64
	)
	err := row.Scan(&accountID, &upstreamFolder,
		&uidvalidity, &uidnext, &lowWater, &highWater, &modseq)
	if err != nil {
		return store.IMAPImportFolderCursor{}, mapErr(err)
	}
	return store.IMAPImportFolderCursor{
		AccountID:      accountID,
		UpstreamFolder: upstreamFolder,
		UIDValidity:    uint64(uidvalidity),
		UIDNext:        uint64(uidnext),
		LowWaterUID:    uint64(lowWater),
		HighWaterUID:   uint64(highWater),
		HighestModSeq:  uint64(modseq),
	}, nil
}

func (m *metadata) UpsertIMAPImportFolderCursor(ctx context.Context, cursor store.IMAPImportFolderCursor) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO imapimport_folder_cursor
			  (account_id, upstream_folder, uidvalidity, uidnext,
			   low_water_uid, high_water_uid, highest_modseq)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(account_id, upstream_folder) DO UPDATE SET
			  uidvalidity     = excluded.uidvalidity,
			  uidnext         = excluded.uidnext,
			  low_water_uid   = excluded.low_water_uid,
			  high_water_uid  = excluded.high_water_uid,
			  highest_modseq  = excluded.highest_modseq`,
			cursor.AccountID, cursor.UpstreamFolder,
			int64(cursor.UIDValidity), int64(cursor.UIDNext),
			int64(cursor.LowWaterUID), int64(cursor.HighWaterUID),
			int64(cursor.HighestModSeq),
		)
		return mapErr(err)
	})
}

// -- message state --------------------------------------------------------

func (m *metadata) GetIMAPImportMessageState(ctx context.Context, accountID, upstreamFolder string, upstreamUID uint32) (store.IMAPImportMessageState, bool, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state
		  WHERE account_id = ? AND upstream_folder = ? AND upstream_uid = ?`,
		accountID, upstreamFolder, int64(upstreamUID))
	s, err := scanIMAPImportMessageState(row)
	if err == store.ErrNotFound {
		return store.IMAPImportMessageState{}, false, nil
	}
	if err != nil {
		return store.IMAPImportMessageState{}, false, err
	}
	return s, true, nil
}

func (m *metadata) GetIMAPImportMessageStateByMessage(ctx context.Context, accountID string, heroldMessageID store.MessageID) (store.IMAPImportMessageState, bool, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state
		  WHERE account_id = ? AND herold_message_id = ?`,
		accountID, int64(heroldMessageID))
	s, err := scanIMAPImportMessageState(row)
	if err == store.ErrNotFound {
		return store.IMAPImportMessageState{}, false, nil
	}
	if err != nil {
		return store.IMAPImportMessageState{}, false, err
	}
	return s, true, nil
}

func scanIMAPImportMessageState(row rowLike) (store.IMAPImportMessageState, error) {
	var (
		accountID, upstreamFolder string
		upstreamUID               int64
		heroldMessageID           int64
		heroldMailboxID           int64
		lastSyncedFlags           int32
	)
	err := row.Scan(&accountID, &upstreamFolder, &upstreamUID,
		&heroldMessageID, &heroldMailboxID, &lastSyncedFlags)
	if err != nil {
		return store.IMAPImportMessageState{}, mapErr(err)
	}
	return store.IMAPImportMessageState{
		AccountID:       accountID,
		UpstreamFolder:  upstreamFolder,
		UpstreamUID:     uint32(upstreamUID),
		HeroldMessageID: store.MessageID(heroldMessageID),
		HeroldMailboxID: store.MailboxID(heroldMailboxID),
		LastSyncedFlags: store.IMAPImportSyncedFlags(lastSyncedFlags),
	}, nil
}

func (m *metadata) UpsertIMAPImportMessageState(ctx context.Context, state store.IMAPImportMessageState) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO imapimport_message_state
			  (account_id, upstream_folder, upstream_uid,
			   herold_message_id, herold_mailbox_id, last_synced_flags)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(account_id, upstream_folder, upstream_uid) DO UPDATE SET
			  herold_message_id = excluded.herold_message_id,
			  herold_mailbox_id = excluded.herold_mailbox_id,
			  last_synced_flags = excluded.last_synced_flags`,
			state.AccountID, state.UpstreamFolder, int64(state.UpstreamUID),
			int64(state.HeroldMessageID), int64(state.HeroldMailboxID),
			int32(state.LastSyncedFlags),
		)
		return mapErr(err)
	})
}

// nullStringOrNil returns nil when s is empty (so the column is written as
// SQL NULL) and the string otherwise. Used for the nullable identity_id
// column on imapimport_account.
func nullStringOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ptrTimeOrZero returns the time.Time value pointed to by t, or the zero
// value when t is nil. Used by nullOrMicros to encode nullable timestamps.
func ptrTimeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
