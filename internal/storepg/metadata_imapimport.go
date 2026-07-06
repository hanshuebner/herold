package storepg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the IMAP-import store methods (issue #25,
// REQ-IMAP-IMP-02, -15..19, -34, -42, -70, -74) for the Postgres backend.
// Schema commentary lives in migrations/0057_imap_import.sql.

const imapImportAccountSelectColsPG = `
	id, identity_id, principal_id, account_name, host, port, tls_mode,
	username, auth_method, backfill_floor_date,
	credential_ct, state, last_success_at, last_error,
	delete_propagates, provenance_mailbox_id, debug_log, created_at, updated_at`

func scanIMAPImportAccountPG(row pgx.Row) (store.IMAPImportAccount, error) {
	var (
		id, accountName, host, tlsMode, username, authMethod, state, lastError string
		identityID                                                             *string
		pid, port                                                              int64
		backfillFloorUs, lastSuccessUs                                         *int64
		credentialCT                                                           []byte
		deletePropagates                                                       bool
		provenanceMailboxID                                                    *int64
		debugLog                                                               bool
		createdUs, updatedUs                                                   int64
	)
	err := row.Scan(
		&id, &identityID, &pid, &accountName, &host, &port, &tlsMode,
		&username, &authMethod, &backfillFloorUs,
		&credentialCT, &state, &lastSuccessUs, &lastError,
		&deletePropagates, &provenanceMailboxID, &debugLog, &createdUs, &updatedUs,
	)
	if err != nil {
		return store.IMAPImportAccount{}, mapErr(err)
	}
	acc := store.IMAPImportAccount{
		ID:                  id,
		IdentityID:          strDeref(identityID),
		ProvenanceMailboxID: store.MailboxID(int64Deref(provenanceMailboxID)),
		PrincipalID:         store.PrincipalID(pid),
		AccountName:         accountName,
		Host:                host,
		Port:                int(port),
		TLSMode:             store.IMAPImportTLSMode(tlsMode),
		Username:            username,
		AuthMethod:          store.IMAPImportAuthMethod(authMethod),
		CredentialCT:        nilSafeBytes(credentialCT),
		State:               store.IMAPImportAccountState(state),
		LastError:           lastError,
		DeletePropagates:    deletePropagates,
		DebugLog:            debugLog,
		CreatedAt:           fromMicros(createdUs),
		UpdatedAt:           fromMicros(updatedUs),
	}
	if backfillFloorUs != nil {
		t := fromMicros(*backfillFloorUs)
		acc.BackfillFloorDate = &t
	}
	if lastSuccessUs != nil {
		t := fromMicros(*lastSuccessUs)
		acc.LastSuccessAt = &t
	}
	return acc, nil
}

func (m *metadata) CreateIMAPImportAccount(ctx context.Context, create store.IMAPImportAccountCreate) (store.IMAPImportAccount, error) {
	if err := store.ValidateIMAPImportCredentialCT(create.CredentialCT); err != nil {
		return store.IMAPImportAccount{}, err
	}
	id, err := newOpaqueIDPG(m.s.randReader)
	if err != nil {
		return store.IMAPImportAccount{}, err
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	state := create.State
	if state == "" {
		state = store.IMAPImportAccountStateEnabled
	}
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO imapimport_account
			  (id, identity_id, principal_id, account_name, host, port, tls_mode,
			   username, auth_method, backfill_floor_date,
			   credential_ct, state, last_success_at, last_error,
			   delete_propagates, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULL,'',$13,$14,$15)`,
			id, nullStringOrNilPG(create.IdentityID), int64(create.PrincipalID),
			create.AccountName, create.Host, create.Port,
			string(create.TLSMode), create.Username, string(create.AuthMethod),
			nullOrMicrosPG(create.BackfillFloorDate),
			create.CredentialCT, string(state),
			create.DeletePropagates, nowUs, nowUs,
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

	err := m.runTx(ctx, func(tx pgx.Tx) error {
		// Build the query dynamically: always-present columns first, then
		// optional columns (credential_ct, debug_log) appended only when
		// the caller supplied them so existing values are preserved.
		args := []any{
			update.AccountName, update.Host, update.Port,
			string(update.TLSMode), update.Username, string(update.AuthMethod),
			nullOrMicrosPG(update.BackfillFloorDate),
			string(update.State), update.DeletePropagates, nowUs,
			nullStringOrNilPG(update.IdentityID),
		}
		// $1..$7 are the always-present non-credential columns, $8=state,
		// $9=delete_propagates, $10=updated_at, $11=identity_id.
		setParts := `
			  account_name = $1, host = $2, port = $3, tls_mode = $4,
			  username = $5, auth_method = $6,
			  backfill_floor_date = $7,
			  state = $8, delete_propagates = $9, updated_at = $10,
			  identity_id = $11`
		n := 11 // count of args so far
		if len(update.CredentialCT) > 0 {
			n++
			setParts += fmt.Sprintf(", credential_ct = $%d", n)
			args = append(args, update.CredentialCT)
		}
		if update.DebugLog != nil {
			n++
			setParts += fmt.Sprintf(", debug_log = $%d", n)
			args = append(args, *update.DebugLog)
		}
		n++ // WHERE id
		idParam := fmt.Sprintf("$%d", n)
		args = append(args, update.ID)
		n++ // WHERE principal_id
		pidParam := fmt.Sprintf("$%d", n)
		args = append(args, int64(update.PrincipalID))

		query := "UPDATE imapimport_account SET " + setParts +
			" WHERE id = " + idParam + " AND principal_id = " + pidParam
		res, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
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
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+imapImportAccountSelectColsPG+`
		   FROM imapimport_account WHERE id = $1`, id)
	return scanIMAPImportAccountPG(row)
}

func (m *metadata) ListIMAPImportAccountsByPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.IMAPImportAccount, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+imapImportAccountSelectColsPG+`
		   FROM imapimport_account WHERE principal_id = $1
		   ORDER BY created_at ASC`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return collectIMAPImportAccountRowsPG(rows)
}

func (m *metadata) ListEnabledIMAPImportAccounts(ctx context.Context) ([]store.IMAPImportAccount, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+imapImportAccountSelectColsPG+`
		   FROM imapimport_account WHERE state IN ('enabled','migrating')
		   ORDER BY created_at ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return collectIMAPImportAccountRowsPG(rows)
}

func collectIMAPImportAccountRowsPG(rows pgx.Rows) ([]store.IMAPImportAccount, error) {
	var out []store.IMAPImportAccount
	for rows.Next() {
		acc, err := scanIMAPImportAccountPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteIMAPImportAccount(ctx context.Context, principalID store.PrincipalID, id string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM imapimport_account WHERE id = $1 AND principal_id = $2`,
			id, int64(principalID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) SetIMAPImportAccountState(ctx context.Context, id string, state store.IMAPImportAccountState, lastError string, lastSuccessAt *time.Time) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		now := m.s.clock.Now().UTC()
		tag, err := tx.Exec(ctx, `
			UPDATE imapimport_account SET
			  state = $1, last_error = $2,
			  last_success_at = COALESCE($3, last_success_at),
			  updated_at = $4
			WHERE id = $5`,
			string(state), lastError,
			nullOrMicrosPG(lastSuccessAt),
			usMicros(now), id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) SetIMAPImportProvenanceMailbox(ctx context.Context, id string, mailboxID store.MailboxID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE imapimport_account SET provenance_mailbox_id = $1, updated_at = $2
			   WHERE id = $3`,
			int64(mailboxID), usMicros(m.s.clock.Now().UTC()), id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListIMAPImportMessageStatesByAccount(ctx context.Context, accountID string) ([]store.IMAPImportMessageState, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state WHERE account_id = $1`,
		accountID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return collectIMAPImportMessageStateRowsPG(rows)
}

func (m *metadata) ListIMAPImportMessageStatesByMessage(ctx context.Context, heroldMessageID store.MessageID) ([]store.IMAPImportMessageState, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state WHERE herold_message_id = $1`,
		int64(heroldMessageID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return collectIMAPImportMessageStateRowsPG(rows)
}

func collectIMAPImportMessageStateRowsPG(rows pgx.Rows) ([]store.IMAPImportMessageState, error) {
	var out []store.IMAPImportMessageState
	for rows.Next() {
		s, err := scanIMAPImportMessageStatePG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// int64Deref returns 0 when p is nil, otherwise *p.
func int64Deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// nullOrMicrosPG returns nil when t is nil or zero, otherwise usMicros(*t).
func nullOrMicrosPG(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return usMicros(*t)
}

// nullStringOrNilPG returns nil when s is empty (written as SQL NULL) and the
// string otherwise. Used for the nullable identity_id column.
func nullStringOrNilPG(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// strDeref returns the empty string when p is nil, otherwise *p. Used to scan
// a nullable TEXT column into a non-pointer store field.
func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// -- folder map -----------------------------------------------------------

func (m *metadata) GetIMAPImportFolderMap(ctx context.Context, accountID string) ([]store.IMAPImportFolderMapEntry, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT account_id, upstream_folder, herold_mailbox_name
		   FROM imapimport_folder_map WHERE account_id = $1
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM imapimport_folder_map WHERE account_id = $1`, accountID); err != nil {
			return mapErr(err)
		}
		for _, e := range entries {
			if _, err := tx.Exec(ctx,
				`INSERT INTO imapimport_folder_map (account_id, upstream_folder, herold_mailbox_name)
				 VALUES ($1, $2, $3)`,
				accountID, e.UpstreamFolder, e.HeroldMailboxName); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

// -- folder cursor --------------------------------------------------------

func (m *metadata) GetIMAPImportFolderCursor(ctx context.Context, accountID, upstreamFolder string) (store.IMAPImportFolderCursor, bool, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT account_id, upstream_folder, uidvalidity, uidnext,
		        low_water_uid, high_water_uid, highest_modseq
		   FROM imapimport_folder_cursor
		  WHERE account_id = $1 AND upstream_folder = $2`,
		accountID, upstreamFolder)
	c, err := scanIMAPImportFolderCursorPG(row)
	if err == store.ErrNotFound {
		return store.IMAPImportFolderCursor{}, false, nil
	}
	if err != nil {
		return store.IMAPImportFolderCursor{}, false, err
	}
	return c, true, nil
}

func scanIMAPImportFolderCursorPG(row pgx.Row) (store.IMAPImportFolderCursor, error) {
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO imapimport_folder_cursor
			  (account_id, upstream_folder, uidvalidity, uidnext,
			   low_water_uid, high_water_uid, highest_modseq)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (account_id, upstream_folder) DO UPDATE SET
			  uidvalidity    = EXCLUDED.uidvalidity,
			  uidnext        = EXCLUDED.uidnext,
			  low_water_uid  = EXCLUDED.low_water_uid,
			  high_water_uid = EXCLUDED.high_water_uid,
			  highest_modseq = EXCLUDED.highest_modseq`,
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
	row := m.s.pool.QueryRow(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state
		  WHERE account_id = $1 AND upstream_folder = $2 AND upstream_uid = $3`,
		accountID, upstreamFolder, int64(upstreamUID))
	s, err := scanIMAPImportMessageStatePG(row)
	if err == store.ErrNotFound {
		return store.IMAPImportMessageState{}, false, nil
	}
	if err != nil {
		return store.IMAPImportMessageState{}, false, err
	}
	return s, true, nil
}

func (m *metadata) GetIMAPImportMessageStateByMessage(ctx context.Context, accountID string, heroldMessageID store.MessageID) (store.IMAPImportMessageState, bool, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT account_id, upstream_folder, upstream_uid,
		        herold_message_id, herold_mailbox_id, last_synced_flags
		   FROM imapimport_message_state
		  WHERE account_id = $1 AND herold_message_id = $2`,
		accountID, int64(heroldMessageID))
	s, err := scanIMAPImportMessageStatePG(row)
	if err == store.ErrNotFound {
		return store.IMAPImportMessageState{}, false, nil
	}
	if err != nil {
		return store.IMAPImportMessageState{}, false, err
	}
	return s, true, nil
}

func scanIMAPImportMessageStatePG(row pgx.Row) (store.IMAPImportMessageState, error) {
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO imapimport_message_state
			  (account_id, upstream_folder, upstream_uid,
			   herold_message_id, herold_mailbox_id, last_synced_flags)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (account_id, upstream_folder, upstream_uid) DO UPDATE SET
			  herold_message_id = EXCLUDED.herold_message_id,
			  herold_mailbox_id = EXCLUDED.herold_mailbox_id,
			  last_synced_flags = EXCLUDED.last_synced_flags`,
			state.AccountID, state.UpstreamFolder, int64(state.UpstreamUID),
			int64(state.HeroldMessageID), int64(state.HeroldMailboxID),
			int32(state.LastSyncedFlags),
		)
		return mapErr(err)
	})
}
