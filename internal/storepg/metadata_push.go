package storepg

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase 3 Wave 3.8a JMAP PushSubscription
// store.Metadata methods (REQ-PROTO-120..122) against Postgres.
// Mirrors metadata_push.go on the SQLite side; commentary lives there.

const pushSubscriptionSelectColumnsPG = `
	id, principal_id, device_client_id, url, p256dh, auth,
	expires_at_us, types_csv, verification_code, verified,
	vapid_key_at_registration, notification_rules_json,
	quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
	created_at_us, updated_at_us`

func scanPushSubscriptionPG(row pgx.Row) (store.PushSubscription, error) {
	var (
		id, pid                                       int64
		device, url, typesCSV, verCode, vapidKey, qhTZ string
		p256dh, authKey, rulesJSON                    []byte
		verified                                      bool
		expiresAtUs                                   *int64
		quietStart, quietEnd                          *int32
		createdUs, updatedUs                          int64
	)
	err := row.Scan(&id, &pid, &device, &url, &p256dh, &authKey,
		&expiresAtUs, &typesCSV, &verCode, &verified,
		&vapidKey, &rulesJSON,
		&quietStart, &quietEnd, &qhTZ,
		&createdUs, &updatedUs)
	if err != nil {
		return store.PushSubscription{}, mapErr(err)
	}
	ps := store.PushSubscription{
		ID:                     store.PushSubscriptionID(id),
		PrincipalID:            store.PrincipalID(pid),
		DeviceClientID:         device,
		URL:                    url,
		P256DH:                 p256dh,
		Auth:                   authKey,
		Types:                  splitTypesCSVPG(typesCSV),
		VerificationCode:       verCode,
		Verified:               verified,
		VAPIDKeyAtRegistration: vapidKey,
		NotificationRulesJSON:  rulesJSON,
		QuietHoursTZ:           qhTZ,
		CreatedAt:              fromMicros(createdUs),
		UpdatedAt:              fromMicros(updatedUs),
	}
	if expiresAtUs != nil {
		t := fromMicros(*expiresAtUs)
		ps.Expires = &t
	}
	if quietStart != nil {
		v := int(*quietStart)
		ps.QuietHoursStartLocal = &v
	}
	if quietEnd != nil {
		v := int(*quietEnd)
		ps.QuietHoursEndLocal = &v
	}
	return ps, nil
}

func splitTypesCSVPG(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func joinTypesCSVPG(types []string) string {
	if len(types) == 0 {
		return ""
	}
	return strings.Join(types, ",")
}

func (m *metadata) InsertPushSubscription(ctx context.Context, ps store.PushSubscription) (store.PushSubscriptionID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		expiresArg := pushExpiresArg(ps.Expires)
		qhStart := pushQuietArg(ps.QuietHoursStartLocal)
		qhEnd := pushQuietArg(ps.QuietHoursEndLocal)
		var rulesArg any
		if ps.NotificationRulesJSON != nil {
			rulesArg = ps.NotificationRulesJSON
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO push_subscription (
				principal_id, device_client_id, url, p256dh, auth,
				expires_at_us, types_csv, verification_code, verified,
				vapid_key_at_registration, notification_rules_json,
				quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
				created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			RETURNING id`,
			int64(ps.PrincipalID), ps.DeviceClientID, ps.URL, ps.P256DH, ps.Auth,
			expiresArg, joinTypesCSVPG(ps.Types), ps.VerificationCode, ps.Verified,
			ps.VAPIDKeyAtRegistration, rulesArg,
			qhStart, qhEnd, ps.QuietHoursTZ,
			usMicros(now), usMicros(now)).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, ps.PrincipalID,
			store.EntityKindPushSubscription, uint64(id), 0, store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.PushSubscriptionID(id), nil
}

func (m *metadata) GetPushSubscription(ctx context.Context, id store.PushSubscriptionID) (store.PushSubscription, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+pushSubscriptionSelectColumnsPG+` FROM push_subscription WHERE id = $1`,
		int64(id))
	return scanPushSubscriptionPG(row)
}

func (m *metadata) ListPushSubscriptionsByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.PushSubscription, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+pushSubscriptionSelectColumnsPG+` FROM push_subscription
		  WHERE principal_id = $1 ORDER BY id ASC`, int64(pid))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.PushSubscription
	for rows.Next() {
		ps, err := scanPushSubscriptionPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

func (m *metadata) UpdatePushSubscription(ctx context.Context, ps store.PushSubscription) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM push_subscription WHERE id = $1`,
			int64(ps.ID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		expiresArg := pushExpiresArg(ps.Expires)
		qhStart := pushQuietArg(ps.QuietHoursStartLocal)
		qhEnd := pushQuietArg(ps.QuietHoursEndLocal)
		var rulesArg any
		if ps.NotificationRulesJSON != nil {
			rulesArg = ps.NotificationRulesJSON
		}
		ct, err := tx.Exec(ctx, `
			UPDATE push_subscription SET
				expires_at_us = $1,
				types_csv = $2,
				verification_code = $3,
				verified = $4,
				notification_rules_json = $5,
				quiet_hours_start_local = $6,
				quiet_hours_end_local = $7,
				quiet_hours_tz = $8,
				updated_at_us = $9
			 WHERE id = $10`,
			expiresArg, joinTypesCSVPG(ps.Types), ps.VerificationCode,
			ps.Verified, rulesArg,
			qhStart, qhEnd, ps.QuietHoursTZ,
			usMicros(now), int64(ps.ID))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindPushSubscription, uint64(ps.ID), 0, store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeletePushSubscription(ctx context.Context, id store.PushSubscriptionID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM push_subscription WHERE id = $1`,
			int64(id)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		ct, err := tx.Exec(ctx,
			`DELETE FROM push_subscription WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindPushSubscription, uint64(id), 0, store.ChangeOpDestroyed, now)
	})
}

// pushExpiresArg returns the SQL placeholder argument for a
// *time.Time expires field: nil when the pointer is nil so the
// column stores NULL, the unix-micros otherwise. Returning an
// untyped nil through any() is the canonical pgx pattern for
// optional columns.
func pushExpiresArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return usMicros(*t)
}

// pushQuietArg adapts a *int (0..23 hour-of-day) to the pgx
// argument shape: nil for absent, int32 for present (the column is
// declared INTEGER on the Postgres side).
func pushQuietArg(v *int) any {
	if v == nil {
		return nil
	}
	return int32(*v)
}
