package storepg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements store.Metadata's app methods for Postgres
// (REQ-APP-01..13, migration 0118).

const appSelectColsPG = `
	id, app_key, origin, manifest_json, manifest_digest,
	pending_manifest_json, pending_manifest_digest, secret_sealed,
	state, all_principals, created_at_us, updated_at_us, last_invoked_at_us`

func scanAppPG(row rowLike) (store.App, error) {
	var (
		id                            int64
		appKey, origin                string
		manifest, pending             *string
		digest, pendingDigest, state  string
		secret                        []byte
		allPrincipals                 bool
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
		AllPrincipals:         allPrincipals,
		CreatedAt:             fromMicros(createdUs),
		UpdatedAt:             fromMicros(updatedUs),
		LastInvokedAt:         fromMicros(invoked),
	}
	if manifest != nil {
		a.ManifestJSON = []byte(*manifest)
	}
	if pending != nil {
		a.PendingManifestJSON = []byte(*pending)
	}
	return a, nil
}

// nullTextPG maps a nil/empty byte slice to SQL NULL and anything else
// to its string form.
func nullTextPG(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	return &s
}

func validateAppRowPG(app store.App) error {
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
	if err := validateAppRowPG(app); err != nil {
		return store.App{}, err
	}
	nowUs := usMicros(m.s.clock.Now().UTC())
	var out store.App
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanAppPG(tx.QueryRow(ctx, `
			INSERT INTO apps
			  (app_key, origin, manifest_json, manifest_digest,
			   pending_manifest_json, pending_manifest_digest, secret_sealed,
			   state, all_principals, created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+appSelectColsPG,
			app.AppKey, app.Origin, nullTextPG(app.ManifestJSON), app.ManifestDigest,
			nullTextPG(app.PendingManifestJSON), app.PendingManifestDigest, app.SecretSealed,
			string(app.State), app.AllPrincipals, nowUs, nowUs))
		return err
	})
	if err != nil {
		return store.App{}, err
	}
	return out, nil
}

func (m *metadata) GetApp(ctx context.Context, id store.AppID) (store.App, error) {
	return scanAppPG(m.s.pool.QueryRow(ctx,
		`SELECT `+appSelectColsPG+` FROM apps WHERE id = $1`, int64(id)))
}

func (m *metadata) queryAppsPG(ctx context.Context, q string, args ...any) ([]store.App, error) {
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.App
	for rows.Next() {
		a, err := scanAppPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (m *metadata) ListApps(ctx context.Context) ([]store.App, error) {
	return m.queryAppsPG(ctx, `SELECT `+appSelectColsPG+` FROM apps ORDER BY id ASC`)
}

func (m *metadata) UpdateApp(ctx context.Context, app store.App) (store.App, error) {
	defer m.s.epochs.BumpAccounts()
	if err := validateAppRowPG(app); err != nil {
		return store.App{}, err
	}
	nowUs := usMicros(m.s.clock.Now().UTC())
	var out store.App
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanAppPG(tx.QueryRow(ctx, `
			UPDATE apps SET
			  origin = $1, manifest_json = $2, manifest_digest = $3,
			  pending_manifest_json = $4, pending_manifest_digest = $5,
			  secret_sealed = $6, state = $7, all_principals = $8,
			  updated_at_us = $9
			WHERE id = $10
			RETURNING `+appSelectColsPG,
			app.Origin, nullTextPG(app.ManifestJSON), app.ManifestDigest,
			nullTextPG(app.PendingManifestJSON), app.PendingManifestDigest,
			app.SecretSealed, string(app.State), app.AllPrincipals,
			nowUs, int64(app.ID)))
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanAppPG(tx.QueryRow(ctx,
			`SELECT `+appSelectColsPG+` FROM apps WHERE id = $1 FOR UPDATE`, int64(id)))
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
		out, err = scanAppPG(tx.QueryRow(ctx, `
			UPDATE apps SET
			  manifest_json = pending_manifest_json,
			  manifest_digest = pending_manifest_digest,
			  pending_manifest_json = NULL,
			  pending_manifest_digest = '',
			  state = $1, updated_at_us = $2
			WHERE id = $3
			RETURNING `+appSelectColsPG, string(state), nowUs, int64(id)))
		return err
	})
	if err != nil {
		return store.App{}, err
	}
	return out, nil
}

func (m *metadata) DeleteApp(ctx context.Context, id store.AppID) error {
	defer m.s.epochs.BumpAccounts()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM apps WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) SetAppPrincipals(ctx context.Context, id store.AppID, principals []store.PrincipalID) error {
	defer m.s.epochs.BumpAccounts()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var one int64
		if err := tx.QueryRow(ctx, `SELECT 1 FROM apps WHERE id = $1 FOR UPDATE`, int64(id)).Scan(&one); err != nil {
			return mapErr(err)
		}
		for _, pid := range principals {
			if err := tx.QueryRow(ctx, `SELECT 1 FROM principals WHERE id = $1`, int64(pid)).Scan(&one); err != nil {
				if errors.Is(mapErr(err), store.ErrNotFound) {
					return fmt.Errorf("principal %d: %w", pid, store.ErrNotFound)
				}
				return mapErr(err)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_principals WHERE app_id = $1`, int64(id)); err != nil {
			return mapErr(err)
		}
		for _, pid := range principals {
			if _, err := tx.Exec(ctx, `
				INSERT INTO app_principals (app_id, principal_id) VALUES ($1, $2)
				ON CONFLICT DO NOTHING`, int64(id), int64(pid)); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (m *metadata) ListAppPrincipals(ctx context.Context, id store.AppID) ([]store.PrincipalID, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT principal_id FROM app_principals WHERE app_id = $1 ORDER BY principal_id ASC`, int64(id))
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
	return m.queryAppsPG(ctx, `
		SELECT `+appSelectColsPG+` FROM apps
		 WHERE state = $1
		   AND (all_principals OR EXISTS (
		         SELECT 1 FROM app_principals ap
		          WHERE ap.app_id = apps.id AND ap.principal_id = $2))
		 ORDER BY id ASC`, string(store.AppStateActive), int64(pid))
}

func (m *metadata) RecordAppInvoked(ctx context.Context, id store.AppID, at time.Time) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE apps SET last_invoked_at_us = $1 WHERE id = $2`, usMicros(at), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}
