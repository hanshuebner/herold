package storesqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements store.Metadata's app methods for SQLite
// (REQ-APP-01..13, migration 0118).

const appSelectCols = `
	id, app_key, origin, manifest_json, manifest_digest,
	pending_manifest_json, pending_manifest_digest, secret_sealed,
	state, all_principals, created_at_us, updated_at_us, last_invoked_at_us`

func scanApp(row rowLike) (store.App, error) {
	var (
		id                            int64
		appKey, origin                string
		manifest, pending             sql.NullString
		digest, pendingDigest, state  string
		secret                        []byte
		allPrincipals                 int64
		createdUs, updatedUs, invoked int64
	)
	if err := row.Scan(&id, &appKey, &origin, &manifest, &digest,
		&pending, &pendingDigest, &secret, &state, &allPrincipals,
		&createdUs, &updatedUs, &invoked); err != nil {
		return store.App{}, mapErr(err)
	}
	a := store.App{
		ID:                    store.AppID(id),
		AppKey:                appKey,
		Origin:                origin,
		ManifestDigest:        digest,
		PendingManifestDigest: pendingDigest,
		SecretSealed:          secret,
		State:                 store.AppState(state),
		AllPrincipals:         allPrincipals != 0,
		CreatedAt:             fromMicros(createdUs),
		UpdatedAt:             fromMicros(updatedUs),
		LastInvokedAt:         fromMicros(invoked),
	}
	if manifest.Valid {
		a.ManifestJSON = []byte(manifest.String)
	}
	if pending.Valid {
		a.PendingManifestJSON = []byte(pending.String)
	}
	return a, nil
}

// nullText maps a nil/empty byte slice to SQL NULL and anything else to
// its string form.
func nullText(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func validateAppRow(app store.App) error {
	if app.AppKey == "" || app.Origin == "" {
		return fmt.Errorf("%w: app_key and origin are required", store.ErrInvalidArgument)
	}
	if !app.State.Valid() {
		return fmt.Errorf("%w: app state %q", store.ErrInvalidArgument, app.State)
	}
	if len(app.SecretSealed) == 0 {
		return fmt.Errorf("%w: app secret is required", store.ErrInvalidArgument)
	}
	return nil
}

func (m *metadata) InsertApp(ctx context.Context, app store.App) (store.App, error) {
	defer m.s.epochs.BumpAccounts()
	if app.State == "" {
		app.State = store.AppStatePendingApproval
	}
	if err := validateAppRow(app); err != nil {
		return store.App{}, err
	}
	nowUs := usMicros(m.s.clock.Now().UTC())
	var out store.App
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO apps
			  (app_key, origin, manifest_json, manifest_digest,
			   pending_manifest_json, pending_manifest_digest, secret_sealed,
			   state, all_principals, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			app.AppKey, app.Origin, nullText(app.ManifestJSON), app.ManifestDigest,
			nullText(app.PendingManifestJSON), app.PendingManifestDigest, app.SecretSealed,
			string(app.State), boolInt(app.AllPrincipals), nowUs, nowUs)
		if err != nil {
			return mapErr(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("apps insert: %w", err)
		}
		out, err = scanApp(tx.QueryRowContext(ctx,
			`SELECT `+appSelectCols+` FROM apps WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return store.App{}, err
	}
	return out, nil
}

func (m *metadata) GetApp(ctx context.Context, id store.AppID) (store.App, error) {
	return scanApp(m.s.db.QueryRowContext(ctx,
		`SELECT `+appSelectCols+` FROM apps WHERE id = ?`, int64(id)))
}

func (m *metadata) queryApps(ctx context.Context, q string, args ...any) ([]store.App, error) {
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (m *metadata) ListApps(ctx context.Context) ([]store.App, error) {
	return m.queryApps(ctx, `SELECT `+appSelectCols+` FROM apps ORDER BY id ASC`)
}

func (m *metadata) UpdateApp(ctx context.Context, app store.App) (store.App, error) {
	defer m.s.epochs.BumpAccounts()
	if err := validateAppRow(app); err != nil {
		return store.App{}, err
	}
	nowUs := usMicros(m.s.clock.Now().UTC())
	var out store.App
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE apps SET
			  origin = ?, manifest_json = ?, manifest_digest = ?,
			  pending_manifest_json = ?, pending_manifest_digest = ?,
			  secret_sealed = ?, state = ?, all_principals = ?,
			  updated_at_us = ?
			WHERE id = ?`,
			app.Origin, nullText(app.ManifestJSON), app.ManifestDigest,
			nullText(app.PendingManifestJSON), app.PendingManifestDigest,
			app.SecretSealed, string(app.State), boolInt(app.AllPrincipals),
			nowUs, int64(app.ID))
		if err != nil {
			return mapErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("apps update: %w", err)
		} else if n == 0 {
			return store.ErrNotFound
		}
		out, err = scanApp(tx.QueryRowContext(ctx,
			`SELECT `+appSelectCols+` FROM apps WHERE id = ?`, int64(app.ID)))
		return err
	})
	if err != nil {
		return store.App{}, err
	}
	return out, nil
}

func (m *metadata) ApproveAppManifest(ctx context.Context, id store.AppID, digest string) (store.App, error) {
	defer m.s.epochs.BumpAccounts()
	nowUs := usMicros(m.s.clock.Now().UTC())
	var out store.App
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		cur, err := scanApp(tx.QueryRowContext(ctx,
			`SELECT `+appSelectCols+` FROM apps WHERE id = ?`, int64(id)))
		if err != nil {
			return err
		}
		if cur.PendingManifestDigest == "" || cur.PendingManifestDigest != digest {
			return store.ErrConflict
		}
		state := cur.State
		if state == store.AppStatePendingApproval {
			state = store.AppStateActive
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE apps SET
			  manifest_json = pending_manifest_json,
			  manifest_digest = pending_manifest_digest,
			  pending_manifest_json = NULL,
			  pending_manifest_digest = '',
			  state = ?, updated_at_us = ?
			WHERE id = ?`, string(state), nowUs, int64(id)); err != nil {
			return mapErr(err)
		}
		out, err = scanApp(tx.QueryRowContext(ctx,
			`SELECT `+appSelectCols+` FROM apps WHERE id = ?`, int64(id)))
		return err
	})
	if err != nil {
		return store.App{}, err
	}
	return out, nil
}

func (m *metadata) DeleteApp(ctx context.Context, id store.AppID) error {
	defer m.s.epochs.BumpAccounts()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("apps delete: %w", err)
		} else if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) SetAppPrincipals(ctx context.Context, id store.AppID, principals []store.PrincipalID) error {
	defer m.s.epochs.BumpAccounts()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var one int64
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM apps WHERE id = ?`, int64(id)).Scan(&one); err != nil {
			return mapErr(err)
		}
		for _, pid := range principals {
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM principals WHERE id = ?`, int64(pid)).Scan(&one); err != nil {
				if errors.Is(mapErr(err), store.ErrNotFound) {
					return fmt.Errorf("principal %d: %w", pid, store.ErrNotFound)
				}
				return mapErr(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_principals WHERE app_id = ?`, int64(id)); err != nil {
			return mapErr(err)
		}
		for _, pid := range principals {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO app_principals (app_id, principal_id) VALUES (?, ?)
				ON CONFLICT DO NOTHING`, int64(id), int64(pid)); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (m *metadata) ListAppPrincipals(ctx context.Context, id store.AppID) ([]store.PrincipalID, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT principal_id FROM app_principals WHERE app_id = ? ORDER BY principal_id ASC`, int64(id))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.PrincipalID
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.PrincipalID(pid))
	}
	return out, rows.Err()
}

func (m *metadata) ListAppsForPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.App, error) {
	return m.queryApps(ctx, `
		SELECT `+appSelectCols+` FROM apps
		 WHERE state = ?
		   AND (all_principals = 1 OR EXISTS (
		         SELECT 1 FROM app_principals ap
		          WHERE ap.app_id = apps.id AND ap.principal_id = ?))
		 ORDER BY id ASC`, string(store.AppStateActive), int64(pid))
}

func (m *metadata) RecordAppInvoked(ctx context.Context, id store.AppID, at time.Time) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET last_invoked_at_us = ? WHERE id = ?`, usMicros(at), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("apps record invoked: %w", err)
		} else if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}
