package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase 3 Wave 3.8a JMAP PushSubscription
// store.Metadata methods (REQ-PROTO-120..122). The schema-side
// commentary lives in migrations/0017_push_subscription.sql; helpers
// (mapErr, runTx, usMicros, fromMicros, appendStateChange) come from
// metadata.go.
//
// The keys.p256dh / keys.auth byte slices are stored verbatim; the
// JMAP serializer base64url-decodes on input and base64url-encodes on
// output. types_csv is the comma-joined list of subscribed JMAP type
// names; an empty string means "all types".

const pushSubscriptionSelectColumns = `
	id, principal_id, device_client_id, url, p256dh, auth,
	expires_at_us, types_csv, verification_code, verified,
	vapid_key_at_registration, notification_rules_json,
	quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
	created_at_us, updated_at_us`

func scanPushSubscription(row rowLike) (store.PushSubscription, error) {
	var (
		id, pid                                                  int64
		device, url, typesCSV, verCode, vapidKey, qhTZ           string
		p256dh, authKey, rulesJSON                               []byte
		verified                                                 int64
		expiresAtUs                                              sql.NullInt64
		quietStart, quietEnd                                     sql.NullInt64
		createdUs, updatedUs                                     int64
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
		Types:                  splitTypesCSV(typesCSV),
		VerificationCode:       verCode,
		Verified:               verified != 0,
		VAPIDKeyAtRegistration: vapidKey,
		NotificationRulesJSON:  rulesJSON,
		QuietHoursTZ:           qhTZ,
		CreatedAt:              fromMicros(createdUs),
		UpdatedAt:              fromMicros(updatedUs),
	}
	if expiresAtUs.Valid {
		t := fromMicros(expiresAtUs.Int64)
		ps.Expires = &t
	}
	if quietStart.Valid {
		v := int(quietStart.Int64)
		ps.QuietHoursStartLocal = &v
	}
	if quietEnd.Valid {
		v := int(quietEnd.Int64)
		ps.QuietHoursEndLocal = &v
	}
	return ps, nil
}

// splitTypesCSV splits a comma-separated type list into a slice. An
// empty string returns nil (the JMAP serializer treats nil as "all
// types"); whitespace-only entries are skipped so a stray comma in
// the stored value does not produce empty type names.
func splitTypesCSV(csv string) []string {
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

// joinTypesCSV is the inverse of splitTypesCSV.
func joinTypesCSV(types []string) string {
	if len(types) == 0 {
		return ""
	}
	return strings.Join(types, ",")
}

func (m *metadata) InsertPushSubscription(ctx context.Context, ps store.PushSubscription) (store.PushSubscriptionID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var expiresArg any
		if ps.Expires != nil {
			expiresArg = usMicros(*ps.Expires)
		}
		var qhStart, qhEnd any
		if ps.QuietHoursStartLocal != nil {
			qhStart = int64(*ps.QuietHoursStartLocal)
		}
		if ps.QuietHoursEndLocal != nil {
			qhEnd = int64(*ps.QuietHoursEndLocal)
		}
		var rulesArg any
		if ps.NotificationRulesJSON != nil {
			rulesArg = ps.NotificationRulesJSON
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO push_subscription (
				principal_id, device_client_id, url, p256dh, auth,
				expires_at_us, types_csv, verification_code, verified,
				vapid_key_at_registration, notification_rules_json,
				quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
				created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			int64(ps.PrincipalID), ps.DeviceClientID, ps.URL, ps.P256DH, ps.Auth,
			expiresArg, joinTypesCSV(ps.Types), ps.VerificationCode, boolToInt(ps.Verified),
			ps.VAPIDKeyAtRegistration, rulesArg,
			qhStart, qhEnd, ps.QuietHoursTZ,
			usMicros(now), usMicros(now))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return appendStateChange(ctx, tx, ps.PrincipalID,
			store.EntityKindPushSubscription, uint64(id), 0, store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.PushSubscriptionID(id), nil
}

func (m *metadata) GetPushSubscription(ctx context.Context, id store.PushSubscriptionID) (store.PushSubscription, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+pushSubscriptionSelectColumns+` FROM push_subscription WHERE id = ?`,
		int64(id))
	return scanPushSubscription(row)
}

func (m *metadata) ListPushSubscriptionsByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.PushSubscription, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+pushSubscriptionSelectColumns+` FROM push_subscription
		  WHERE principal_id = ? ORDER BY id ASC`, int64(pid))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.PushSubscription
	for rows.Next() {
		ps, err := scanPushSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

func (m *metadata) UpdatePushSubscription(ctx context.Context, ps store.PushSubscription) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM push_subscription WHERE id = ?`,
			int64(ps.ID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		var expiresArg any
		if ps.Expires != nil {
			expiresArg = usMicros(*ps.Expires)
		}
		var qhStart, qhEnd any
		if ps.QuietHoursStartLocal != nil {
			qhStart = int64(*ps.QuietHoursStartLocal)
		}
		if ps.QuietHoursEndLocal != nil {
			qhEnd = int64(*ps.QuietHoursEndLocal)
		}
		var rulesArg any
		if ps.NotificationRulesJSON != nil {
			rulesArg = ps.NotificationRulesJSON
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE push_subscription SET
				expires_at_us = ?,
				types_csv = ?,
				verification_code = ?,
				verified = ?,
				notification_rules_json = ?,
				quiet_hours_start_local = ?,
				quiet_hours_end_local = ?,
				quiet_hours_tz = ?,
				updated_at_us = ?
			 WHERE id = ?`,
			expiresArg, joinTypesCSV(ps.Types), ps.VerificationCode,
			boolToInt(ps.Verified), rulesArg,
			qhStart, qhEnd, ps.QuietHoursTZ,
			usMicros(now), int64(ps.ID))
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
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindPushSubscription, uint64(ps.ID), 0, store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeletePushSubscription(ctx context.Context, id store.PushSubscriptionID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM push_subscription WHERE id = ?`,
			int64(id)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM push_subscription WHERE id = ?`, int64(id))
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
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindPushSubscription, uint64(id), 0, store.ChangeOpDestroyed, now)
	})
}
