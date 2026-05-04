package storesqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// metadata implements store.Metadata against SQLite. All writes go
// through runTx, which takes the writer mutex and opens a BEGIN
// IMMEDIATE transaction so readers never race a half-applied state.
type metadata struct {
	s *Store
}

// mapErr converts a SQLite driver error into the public sentinel
// vocabulary. Unknown errors are returned unchanged (wrapped by callers
// with fmt.Errorf("...: %w", err)).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE,
			sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return store.ErrConflict
		}
	}
	return err
}

// usMicros converts t to Unix microseconds. Zero time -> 0.
func usMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// fromMicros converts a stored Unix-micros value to UTC time. 0 -> zero
// time.
func fromMicros(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

// runTx begins a transaction with the writer lock held, runs fn, and
// commits or rolls back. Retries on SQLITE_BUSY are already handled by
// the 30s busy_timeout PRAGMA.
func (m *metadata) runTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	m.s.writerMu.Lock()
	defer m.s.writerMu.Unlock()
	tx, err := m.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storesqlite: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storesqlite: commit: %w", err)
	}
	return nil
}

// -- principals -------------------------------------------------------

func (m *metadata) InsertPrincipal(ctx context.Context, p store.Principal) (store.Principal, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var telemetry *int64
		if p.ClientlogTelemetryEnabled != nil {
			v := boolToInt(*p.ClientlogTelemetryEnabled)
			telemetry = &v
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO principals (kind, canonical_email, display_name, password_hash,
			  totp_secret, quota_bytes, flags, used_bytes, created_at_us, updated_at_us,
			  clientlog_telemetry_enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			int64(p.Kind), strings.ToLower(p.CanonicalEmail), p.DisplayName, p.PasswordHash,
			p.TOTPSecret, p.QuotaBytes, int64(p.Flags), usMicros(now), usMicros(now),
			nullable(telemetry))
		if err != nil {
			return fmt.Errorf("principal %q: %w", strings.ToLower(p.CanonicalEmail), mapErr(err))
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.Principal{}, err
	}
	p.ID = store.PrincipalID(id)
	p.CreatedAt = now
	p.UpdatedAt = now
	p.SeenAddressesEnabled = true // default: enabled
	p.CanonicalEmail = strings.ToLower(p.CanonicalEmail)
	return p, nil
}

func (m *metadata) GetPrincipalByID(ctx context.Context, id store.PrincipalID) (store.Principal, error) {
	return m.selectPrincipal(ctx, `WHERE id = ?`, int64(id))
}

func (m *metadata) GetPrincipalByEmail(ctx context.Context, email string) (store.Principal, error) {
	return m.selectPrincipal(ctx, `WHERE canonical_email = ?`, strings.ToLower(email))
}

func (m *metadata) selectPrincipal(ctx context.Context, where string, args ...any) (store.Principal, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, seen_addresses_enabled,
		       avatar_blob_hash, avatar_blob_size, xface_enabled,
		       clientlog_telemetry_enabled,
		       created_at_us, updated_at_us
		  FROM principals `+where, args...)
	var p store.Principal
	var kind int64
	var flags int64
	var seenAddrEnabled int64
	var xfaceEnabled int64
	var avatarHash sql.NullString
	var telemetryEnabled sql.NullInt64
	var createdUs, updatedUs int64
	var totp []byte
	var id int64
	err := row.Scan(&id, &kind, &p.CanonicalEmail, &p.DisplayName, &p.PasswordHash,
		&totp, &p.QuotaBytes, &flags, &seenAddrEnabled,
		&avatarHash, &p.AvatarBlobSize, &xfaceEnabled,
		&telemetryEnabled,
		&createdUs, &updatedUs)
	if err != nil {
		return store.Principal{}, mapErr(err)
	}
	p.ID = store.PrincipalID(id)
	p.Kind = store.PrincipalKind(kind)
	p.Flags = store.PrincipalFlags(flags)
	p.SeenAddressesEnabled = seenAddrEnabled != 0
	p.XFaceEnabled = xfaceEnabled != 0
	if avatarHash.Valid {
		p.AvatarBlobHash = avatarHash.String
	}
	if telemetryEnabled.Valid {
		v := telemetryEnabled.Int64 != 0
		p.ClientlogTelemetryEnabled = &v
	}
	p.CreatedAt = fromMicros(createdUs)
	p.UpdatedAt = fromMicros(updatedUs)
	if len(totp) > 0 {
		p.TOTPSecret = totp
	}
	return p, nil
}

func (m *metadata) UpdatePrincipal(ctx context.Context, p store.Principal) error {
	now := m.s.clock.Now().UTC()
	var avatarHash any
	if p.AvatarBlobHash != "" {
		avatarHash = p.AvatarBlobHash
	}
	var telemetry *int64
	if p.ClientlogTelemetryEnabled != nil {
		v := boolToInt(*p.ClientlogTelemetryEnabled)
		telemetry = &v
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE principals
			   SET kind = ?, canonical_email = ?, display_name = ?, password_hash = ?,
			       totp_secret = ?, quota_bytes = ?, flags = ?,
			       seen_addresses_enabled = ?,
			       avatar_blob_hash = ?, avatar_blob_size = ?, xface_enabled = ?,
			       clientlog_telemetry_enabled = ?,
			       updated_at_us = ?
			 WHERE id = ?`,
			int64(p.Kind), strings.ToLower(p.CanonicalEmail), p.DisplayName, p.PasswordHash,
			p.TOTPSecret, p.QuotaBytes, int64(p.Flags), boolToInt(p.SeenAddressesEnabled),
			avatarHash, p.AvatarBlobSize, boolToInt(p.XFaceEnabled),
			nullable(telemetry),
			usMicros(now), int64(p.ID))
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

// -- domains ----------------------------------------------------------

func (m *metadata) InsertDomain(ctx context.Context, d store.Domain) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO domains (name, is_local, created_at_us) VALUES (?, ?, ?)`,
			strings.ToLower(d.Name), boolToInt(d.IsLocal), usMicros(now))
		if err != nil {
			return fmt.Errorf("domain %q: %w", strings.ToLower(d.Name), mapErr(err))
		}
		return nil
	})
}

func (m *metadata) GetDomain(ctx context.Context, name string) (store.Domain, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT name, is_local, created_at_us FROM domains WHERE name = ?`,
		strings.ToLower(name))
	var d store.Domain
	var isLocal int64
	var createdUs int64
	err := row.Scan(&d.Name, &isLocal, &createdUs)
	if err != nil {
		return store.Domain{}, mapErr(err)
	}
	d.IsLocal = isLocal != 0
	d.CreatedAt = fromMicros(createdUs)
	return d, nil
}

func (m *metadata) ListLocalDomains(ctx context.Context) ([]store.Domain, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT name, is_local, created_at_us FROM domains
		 WHERE is_local = 1 ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Domain
	for rows.Next() {
		var d store.Domain
		var isLocal int64
		var createdUs int64
		if err := rows.Scan(&d.Name, &isLocal, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		d.IsLocal = isLocal != 0
		d.CreatedAt = fromMicros(createdUs)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteDomain(ctx context.Context, name string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM domains WHERE name = ?`, strings.ToLower(name))
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

// -- aliases ----------------------------------------------------------

func (m *metadata) InsertAlias(ctx context.Context, a store.Alias) (store.Alias, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	var expiresUs *int64
	if a.ExpiresAt != nil {
		x := usMicros(*a.ExpiresAt)
		expiresUs = &x
	}
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO aliases (local_part, domain, target_principal, expires_at_us, created_at_us)
			VALUES (?, ?, ?, ?, ?)`,
			strings.ToLower(a.LocalPart), strings.ToLower(a.Domain),
			int64(a.TargetPrincipal), nullable(expiresUs), usMicros(now))
		if err != nil {
			return fmt.Errorf("alias %s@%s: %w", strings.ToLower(a.LocalPart), strings.ToLower(a.Domain), mapErr(err))
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.Alias{}, err
	}
	a.ID = store.AliasID(id)
	a.LocalPart = strings.ToLower(a.LocalPart)
	a.Domain = strings.ToLower(a.Domain)
	a.CreatedAt = now
	return a, nil
}

func (m *metadata) ResolveAlias(ctx context.Context, localPart, domain string) (store.PrincipalID, error) {
	lp := strings.ToLower(localPart)
	dom := strings.ToLower(domain)
	now := m.s.clock.Now().UnixMicro()
	var target int64
	err := m.s.db.QueryRowContext(ctx, `
		SELECT target_principal FROM aliases
		 WHERE local_part = ? AND domain = ?
		   AND (expires_at_us IS NULL OR expires_at_us > ?)`,
		lp, dom, now).Scan(&target)
	if err == nil {
		return store.PrincipalID(target), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, mapErr(err)
	}
	// Canonical-address fallback: localPart@domain matches a principal's
	// canonical email.
	var pid int64
	err = m.s.db.QueryRowContext(ctx, `
		SELECT id FROM principals WHERE canonical_email = ?`,
		lp+"@"+dom).Scan(&pid)
	if err != nil {
		return 0, mapErr(err)
	}
	return store.PrincipalID(pid), nil
}

func (m *metadata) ListAliases(ctx context.Context, domain string) ([]store.Alias, error) {
	dom := strings.ToLower(strings.TrimSpace(domain))
	var (
		rows *sql.Rows
		err  error
	)
	if dom == "" {
		rows, err = m.s.db.QueryContext(ctx, `
			SELECT id, local_part, domain, target_principal, expires_at_us, created_at_us
			  FROM aliases ORDER BY domain, local_part`)
	} else {
		rows, err = m.s.db.QueryContext(ctx, `
			SELECT id, local_part, domain, target_principal, expires_at_us, created_at_us
			  FROM aliases WHERE domain = ? ORDER BY local_part`, dom)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.Alias, 0)
	for rows.Next() {
		var a store.Alias
		var id, target int64
		var expires sql.NullInt64
		var createdUs int64
		if err := rows.Scan(&id, &a.LocalPart, &a.Domain, &target, &expires, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		a.ID = store.AliasID(id)
		a.TargetPrincipal = store.PrincipalID(target)
		if expires.Valid && expires.Int64 != 0 {
			t := fromMicros(expires.Int64)
			a.ExpiresAt = &t
		}
		a.CreatedAt = fromMicros(createdUs)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteAlias(ctx context.Context, id store.AliasID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM aliases WHERE id = ?`, int64(id))
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

// -- OIDC -------------------------------------------------------------

func (m *metadata) InsertOIDCProvider(ctx context.Context, p store.OIDCProvider) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO oidc_providers (name, issuer_url, client_id, client_secret_ref,
			  scopes_csv, auto_provision, created_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.Name, p.IssuerURL, p.ClientID, p.ClientSecretRef,
			strings.Join(p.Scopes, ","), boolToInt(p.AutoProvision), usMicros(now))
		if err != nil {
			return fmt.Errorf("OIDC provider %q: %w", p.Name, mapErr(err))
		}
		return nil
	})
}

func (m *metadata) GetOIDCProvider(ctx context.Context, name string) (store.OIDCProvider, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT name, issuer_url, client_id, client_secret_ref, scopes_csv,
		       auto_provision, created_at_us
		  FROM oidc_providers WHERE name = ?`, name)
	var p store.OIDCProvider
	var scopes string
	var auto int64
	var createdUs int64
	err := row.Scan(&p.Name, &p.IssuerURL, &p.ClientID, &p.ClientSecretRef,
		&scopes, &auto, &createdUs)
	if err != nil {
		return store.OIDCProvider{}, mapErr(err)
	}
	if scopes != "" {
		p.Scopes = strings.Split(scopes, ",")
	}
	p.AutoProvision = auto != 0
	p.CreatedAt = fromMicros(createdUs)
	return p, nil
}

func (m *metadata) LinkOIDC(ctx context.Context, link store.OIDCLink) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO oidc_links (principal_id, provider_name, subject,
			  email_at_provider, linked_at_us)
			VALUES (?, ?, ?, ?, ?)`,
			int64(link.PrincipalID), link.ProviderName, link.Subject,
			link.EmailAtProvider, usMicros(now))
		if err != nil {
			return fmt.Errorf("OIDC link principal %d provider %q subject %q: %w", link.PrincipalID, link.ProviderName, link.Subject, mapErr(err))
		}
		return nil
	})
}

func (m *metadata) LookupOIDCLink(ctx context.Context, provider, subject string) (store.OIDCLink, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT principal_id, provider_name, subject, email_at_provider, linked_at_us
		  FROM oidc_links WHERE provider_name = ? AND subject = ?`, provider, subject)
	var l store.OIDCLink
	var pid int64
	var linkedUs int64
	err := row.Scan(&pid, &l.ProviderName, &l.Subject, &l.EmailAtProvider, &linkedUs)
	if err != nil {
		return store.OIDCLink{}, mapErr(err)
	}
	l.PrincipalID = store.PrincipalID(pid)
	l.LinkedAt = fromMicros(linkedUs)
	return l, nil
}

// -- API keys ---------------------------------------------------------

func (m *metadata) InsertAPIKey(ctx context.Context, k store.APIKey) (store.APIKey, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	scope := k.ScopeJSON
	if scope == "" {
		// Defence in depth: a caller that forgets to set ScopeJSON
		// gets the legacy-row backfill value ['admin']; protoadmin's
		// REST handler validates the input before reaching here, so
		// this path only fires on direct test fixtures that haven't
		// been updated.
		scope = `["admin"]`
	}
	addrJSON := encodeStringSliceJSON(k.AllowedFromAddresses)
	domJSON := encodeStringSliceJSON(k.AllowedFromDomains)
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO api_keys (principal_id, hash, name, created_at_us, last_used_at_us,
			                      scope_json, allowed_from_addresses_json, allowed_from_domains_json)
			VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
			int64(k.PrincipalID), k.Hash, k.Name, usMicros(now), scope, addrJSON, domJSON)
		if err != nil {
			return fmt.Errorf("API key %q: %w", k.Name, mapErr(err))
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.APIKey{}, err
	}
	k.ID = store.APIKeyID(id)
	k.CreatedAt = now
	k.ScopeJSON = scope
	return k, nil
}

func (m *metadata) GetAPIKeyByHash(ctx context.Context, hash string) (store.APIKey, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, principal_id, hash, name, created_at_us, last_used_at_us,
		       scope_json, allowed_from_addresses_json, allowed_from_domains_json
		  FROM api_keys WHERE hash = ?`, hash)
	var k store.APIKey
	var id, pid int64
	var createdUs, lastUs int64
	var addrJSON, domJSON string
	err := row.Scan(&id, &pid, &k.Hash, &k.Name, &createdUs, &lastUs,
		&k.ScopeJSON, &addrJSON, &domJSON)
	if err != nil {
		return store.APIKey{}, mapErr(err)
	}
	k.ID = store.APIKeyID(id)
	k.PrincipalID = store.PrincipalID(pid)
	k.CreatedAt = fromMicros(createdUs)
	k.LastUsedAt = fromMicros(lastUs)
	k.AllowedFromAddresses = decodeStringSliceJSON(addrJSON)
	k.AllowedFromDomains = decodeStringSliceJSON(domJSON)
	return k, nil
}

func (m *metadata) TouchAPIKey(ctx context.Context, id store.APIKeyID, at time.Time) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE api_keys SET last_used_at_us = ? WHERE id = ?`,
			usMicros(at), int64(id))
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

func (m *metadata) ListAPIKeysByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.APIKey, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, principal_id, hash, name, created_at_us, last_used_at_us,
		       scope_json, allowed_from_addresses_json, allowed_from_domains_json
		  FROM api_keys WHERE principal_id = ? ORDER BY id`, int64(pid))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.APIKey, 0)
	for rows.Next() {
		var k store.APIKey
		var id, ownerID int64
		var createdUs, lastUs int64
		var addrJSON, domJSON string
		if err := rows.Scan(&id, &ownerID, &k.Hash, &k.Name, &createdUs, &lastUs,
			&k.ScopeJSON, &addrJSON, &domJSON); err != nil {
			return nil, mapErr(err)
		}
		k.ID = store.APIKeyID(id)
		k.PrincipalID = store.PrincipalID(ownerID)
		k.CreatedAt = fromMicros(createdUs)
		k.LastUsedAt = fromMicros(lastUs)
		k.AllowedFromAddresses = decodeStringSliceJSON(addrJSON)
		k.AllowedFromDomains = decodeStringSliceJSON(domJSON)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteAPIKey(ctx context.Context, id store.APIKeyID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM api_keys WHERE id = ?`, int64(id))
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

func (m *metadata) ListOIDCLinksByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.OIDCLink, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT principal_id, provider_name, subject, email_at_provider, linked_at_us
		  FROM oidc_links WHERE principal_id = ? ORDER BY provider_name`, int64(pid))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.OIDCLink, 0)
	for rows.Next() {
		var l store.OIDCLink
		var ownerID int64
		var linkedUs int64
		if err := rows.Scan(&ownerID, &l.ProviderName, &l.Subject, &l.EmailAtProvider, &linkedUs); err != nil {
			return nil, mapErr(err)
		}
		l.PrincipalID = store.PrincipalID(ownerID)
		l.LinkedAt = fromMicros(linkedUs)
		out = append(out, l)
	}
	return out, rows.Err()
}

// -- mailboxes --------------------------------------------------------

func (m *metadata) InsertMailbox(ctx context.Context, mb store.Mailbox) (store.Mailbox, error) {
	now := m.s.clock.Now().UTC()
	if mb.UIDValidity == 0 {
		mb.UIDValidity = newUIDValidity(now, m.s.randReader)
	}
	if mb.Color != nil {
		if !validMailboxColor(*mb.Color) {
			return store.Mailbox{}, fmt.Errorf("color %q: %w", *mb.Color, store.ErrInvalidArgument)
		}
	}
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var color any
		if mb.Color != nil {
			color = *mb.Color
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO mailboxes (principal_id, parent_id, name, attributes, uidvalidity,
			  uidnext, highest_modseq, created_at_us, updated_at_us, color_hex, sort_order)
			VALUES (?, ?, ?, ?, ?, 1, 0, ?, ?, ?, ?)`,
			int64(mb.PrincipalID), int64(mb.ParentID), mb.Name, int64(mb.Attributes),
			int64(mb.UIDValidity), usMicros(now), usMicros(now), color, int64(mb.SortOrder))
		if err != nil {
			return fmt.Errorf("mailbox %q: %w", mb.Name, mapErr(err))
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		_, err = tx.ExecContext(ctx, `
			INSERT INTO state_changes (principal_id, seq, entity_kind, entity_id,
			  parent_entity_id, op, produced_at_us)
			VALUES (?, (SELECT COALESCE(MAX(seq), 0)+1 FROM state_changes WHERE principal_id = ?), ?, ?, 0, ?, ?)`,
			int64(mb.PrincipalID), int64(mb.PrincipalID),
			string(store.EntityKindMailbox), id, int(store.ChangeOpCreated), usMicros(now))
		return mapErr(err)
	})
	if err != nil {
		return store.Mailbox{}, err
	}
	mb.ID = store.MailboxID(id)
	mb.UIDNext = 1
	mb.HighestModSeq = 0
	mb.CreatedAt = now
	mb.UpdatedAt = now
	return mb, nil
}

func (m *metadata) GetMailboxByID(ctx context.Context, id store.MailboxID) (store.Mailbox, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, principal_id, parent_id, name, attributes, uidvalidity, uidnext,
		       highest_modseq, created_at_us, updated_at_us, color_hex, sort_order
		  FROM mailboxes WHERE id = ?`, int64(id))
	return scanMailbox(row)
}

type rowLike interface {
	Scan(dest ...any) error
}

func scanMailbox(row rowLike) (store.Mailbox, error) {
	var mb store.Mailbox
	var id, pid, parent int64
	var attrs, uidv, uidn, hm int64
	var createdUs, updatedUs int64
	var color sql.NullString
	var sortOrder int64
	err := row.Scan(&id, &pid, &parent, &mb.Name, &attrs, &uidv, &uidn, &hm, &createdUs, &updatedUs, &color, &sortOrder)
	if err != nil {
		return store.Mailbox{}, mapErr(err)
	}
	mb.ID = store.MailboxID(id)
	mb.PrincipalID = store.PrincipalID(pid)
	mb.ParentID = store.MailboxID(parent)
	mb.Attributes = store.MailboxAttributes(attrs)
	mb.UIDValidity = store.UIDValidity(uidv)
	mb.UIDNext = store.UID(uidn)
	mb.HighestModSeq = store.ModSeq(hm)
	mb.CreatedAt = fromMicros(createdUs)
	mb.UpdatedAt = fromMicros(updatedUs)
	mb.SortOrder = uint32(sortOrder)
	if color.Valid {
		v := color.String
		mb.Color = &v
	}
	return mb, nil
}

func (m *metadata) ListMailboxes(ctx context.Context, principalID store.PrincipalID) ([]store.Mailbox, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, principal_id, parent_id, name, attributes, uidvalidity, uidnext,
		       highest_modseq, created_at_us, updated_at_us, color_hex, sort_order
		  FROM mailboxes WHERE principal_id = ? ORDER BY name`, int64(principalID))
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

func (m *metadata) DeleteMailbox(ctx context.Context, id store.MailboxID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid int64
		err := tx.QueryRowContext(ctx, `SELECT principal_id FROM mailboxes WHERE id = ?`, int64(id)).Scan(&pid)
		if err != nil {
			return mapErr(err)
		}
		// Find messages whose ONLY membership is this mailbox — those
		// messages must have their blob refcounts decremented and their
		// messages rows removed.
		hashRows, err := tx.QueryContext(ctx, `
			SELECT m.blob_hash, m.size
			  FROM messages m
			  JOIN message_mailboxes mm ON mm.message_id = m.id
			 WHERE mm.mailbox_id = ?
			   AND (SELECT COUNT(*) FROM message_mailboxes mm2 WHERE mm2.message_id = m.id) = 1`,
			int64(id))
		if err != nil {
			return mapErr(err)
		}
		type blobInfo struct {
			hash string
			size int64
		}
		var soleBlobs []blobInfo
		for hashRows.Next() {
			var bi blobInfo
			if err := hashRows.Scan(&bi.hash, &bi.size); err != nil {
				hashRows.Close()
				return mapErr(err)
			}
			soleBlobs = append(soleBlobs, bi)
		}
		hashRows.Close()

		// For sole-membership messages: delete messages rows (message_mailboxes
		// rows cascade), decrement refcounts, and adjust principal used_bytes.
		for _, bi := range soleBlobs {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM messages
				 WHERE blob_hash = ?
				   AND id IN (SELECT message_id FROM message_mailboxes WHERE mailbox_id = ?)`,
				bi.hash, int64(id)); err != nil {
				return mapErr(err)
			}
			if err := decRef(ctx, tx, bi.hash, m.s.clock.Now()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE principals SET used_bytes = used_bytes - ?, updated_at_us = ? WHERE id = ?`,
				bi.size, usMicros(m.s.clock.Now()), pid); err != nil {
				return mapErr(err)
			}
		}
		// For multi-membership messages: just remove the membership rows
		// for this mailbox (ON DELETE CASCADE on the FK would handle it, but
		// we want explicit control here for the used_bytes accounting above).
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM message_mailboxes WHERE mailbox_id = ?`, int64(id)); err != nil {
			return mapErr(err)
		}
		// Now delete the mailbox itself.
		res, err := tx.ExecContext(ctx, `DELETE FROM mailboxes WHERE id = ?`, int64(id))
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
			store.EntityKindMailbox, uint64(id), 0, store.ChangeOpDestroyed, m.s.clock.Now())
	})
}

// -- messages ---------------------------------------------------------

// scanMessageRow scans the mailbox-independent message columns.
func scanMessageRow(row rowLike) (store.Message, error) {
	var msg store.Message
	var id, pid int64
	var idUs, rcvUs int64
	var blobSize int64
	var thread int64
	var envDateUs int64
	err := row.Scan(&id, &pid, &msg.Blob.Hash, &blobSize, &idUs, &rcvUs,
		&msg.Size, &thread,
		&msg.Envelope.Subject, &msg.Envelope.From, &msg.Envelope.To,
		&msg.Envelope.Cc, &msg.Envelope.Bcc, &msg.Envelope.ReplyTo,
		&msg.Envelope.MessageID, &msg.Envelope.InReplyTo, &envDateUs)
	if err != nil {
		return store.Message{}, mapErr(err)
	}
	msg.ID = store.MessageID(id)
	msg.PrincipalID = store.PrincipalID(pid)
	msg.InternalDate = fromMicros(idUs)
	msg.ReceivedAt = fromMicros(rcvUs)
	msg.Blob.Size = blobSize
	msg.ThreadID = uint64(thread)
	msg.Envelope.Date = fromMicros(envDateUs)
	return msg, nil
}

// scanMessageMailboxRow scans one message_mailboxes row.
func scanMessageMailboxRow(row rowLike) (store.MessageMailbox, error) {
	var mm store.MessageMailbox
	var mid, mbox, uid, modseq, flags int64
	var keywords string
	var snoozedUs sql.NullInt64
	err := row.Scan(&mid, &mbox, &uid, &modseq, &flags, &keywords, &snoozedUs)
	if err != nil {
		return store.MessageMailbox{}, mapErr(err)
	}
	mm.MessageID = store.MessageID(mid)
	mm.MailboxID = store.MailboxID(mbox)
	mm.UID = store.UID(uid)
	mm.ModSeq = store.ModSeq(modseq)
	mm.Flags = store.MessageFlags(flags)
	if keywords != "" {
		mm.Keywords = strings.Split(keywords, ",")
	}
	if snoozedUs.Valid {
		t := fromMicros(snoozedUs.Int64)
		mm.SnoozedUntil = &t
	}
	return mm, nil
}

// loadMailboxes fetches all message_mailboxes rows for a message and
// populates the convenience fields on msg from the entry matching
// mailboxID (or the first entry when mailboxID == 0).
func (m *metadata) loadMailboxes(ctx context.Context, msg *store.Message, mailboxID store.MailboxID) error {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us
		  FROM message_mailboxes
		 WHERE message_id = ?
		 ORDER BY mailbox_id`,
		int64(msg.ID))
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		mm, err := scanMessageMailboxRow(rows)
		if err != nil {
			return err
		}
		msg.Mailboxes = append(msg.Mailboxes, mm)
	}
	if err := rows.Err(); err != nil {
		return mapErr(err)
	}
	// Populate convenience fields from the target mailbox entry or
	// the first entry (for single-mailbox or unscoped reads).
	for _, mm := range msg.Mailboxes {
		if mailboxID == 0 || mm.MailboxID == mailboxID {
			msg.MailboxID = mm.MailboxID
			msg.UID = mm.UID
			msg.ModSeq = mm.ModSeq
			msg.Flags = mm.Flags
			msg.Keywords = mm.Keywords
			msg.SnoozedUntil = mm.SnoozedUntil
			break
		}
	}
	return nil
}

func (m *metadata) InsertMessage(ctx context.Context, msg store.Message, targets []store.MessageMailbox) (store.UID, store.ModSeq, error) {
	if len(targets) == 0 {
		return 0, 0, fmt.Errorf("storesqlite: InsertMessage: targets must not be empty")
	}
	now := m.s.clock.Now().UTC()
	var firstUID store.UID
	var firstModSeq store.ModSeq
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Principal from caller (required).
		pid := int64(msg.PrincipalID)
		if pid == 0 {
			// Fallback: derive from first target's mailbox.
			if err := tx.QueryRowContext(ctx, `SELECT principal_id FROM mailboxes WHERE id = ?`,
				int64(targets[0].MailboxID)).Scan(&pid); err != nil {
				return mapErr(err)
			}
		}
		// Quota check.
		var quota, used int64
		if err := tx.QueryRowContext(ctx, `SELECT quota_bytes, used_bytes FROM principals WHERE id = ?`, pid).Scan(&quota, &used); err != nil {
			return mapErr(err)
		}
		if quota > 0 && used+msg.Size > quota {
			return store.ErrQuotaExceeded
		}
		// Normalise env_message_id.
		if msg.Envelope.MessageID != "" {
			msg.Envelope.MessageID = mailparse.NormalizeMessageID(msg.Envelope.MessageID)
		}
		// Thread resolution against the principal's existing messages.
		if msg.ThreadID == 0 && msg.Envelope.InReplyTo != "" {
			refs := mailparse.ParseReferences(msg.Envelope.InReplyTo)
			for _, ref := range refs {
				var ancestorID, ancestorThread int64
				lookupErr := tx.QueryRowContext(ctx, `
					SELECT m.id, m.thread_id
					  FROM messages m
					 WHERE m.principal_id = ?
					   AND m.env_message_id = ?
					 LIMIT 1`,
					pid, ref).Scan(&ancestorID, &ancestorThread)
				if lookupErr == sql.ErrNoRows {
					continue
				}
				if lookupErr != nil {
					return fmt.Errorf("storesqlite: thread lookup: %w", lookupErr)
				}
				if ancestorThread != 0 {
					msg.ThreadID = uint64(ancestorThread)
				} else {
					msg.ThreadID = uint64(ancestorID)
				}
				break
			}
		}
		// Insert the mailbox-independent messages row.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO messages (principal_id, blob_hash, blob_size,
			  internal_date_us, received_at_us, size, thread_id,
			  env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
			  env_message_id, env_in_reply_to, env_date_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pid, msg.Blob.Hash, msg.Blob.Size,
			usMicros(msg.InternalDate), usMicros(msg.ReceivedAt), msg.Size, int64(msg.ThreadID),
			msg.Envelope.Subject, msg.Envelope.From, msg.Envelope.To,
			msg.Envelope.Cc, msg.Envelope.Bcc, msg.Envelope.ReplyTo,
			msg.Envelope.MessageID, msg.Envelope.InReplyTo, usMicros(msg.Envelope.Date))
		if err != nil {
			return mapErr(err)
		}
		mid, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		// Insert one message_mailboxes row per target.
		for i, t := range targets {
			// Inherit message-level flags/keywords if the target doesn't
			// specify its own. This lets callers set Flags/Keywords on the
			// Message struct alone (common in tests and SMTP deliver).
			if t.Flags == 0 && len(t.Keywords) == 0 {
				t.Flags = msg.Flags
				t.Keywords = msg.Keywords
			}
			var uidNext, highest int64
			if err := tx.QueryRowContext(ctx, `SELECT uidnext, highest_modseq FROM mailboxes WHERE id = ?`,
				int64(t.MailboxID)).Scan(&uidNext, &highest); err != nil {
				return mapErr(err)
			}
			allocUID := store.UID(uidNext)
			allocModSeq := store.ModSeq(highest + 1)
			var snoozedArg any
			if t.SnoozedUntil != nil {
				snoozedArg = usMicros(*t.SnoozedUntil)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO message_mailboxes
				  (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				mid, int64(t.MailboxID), int64(allocUID), int64(allocModSeq),
				int64(t.Flags), strings.Join(t.Keywords, ","), snoozedArg); err != nil {
				return mapErr(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = ?, updated_at_us = ?
				 WHERE id = ?`, int64(allocModSeq), usMicros(now), int64(t.MailboxID)); err != nil {
				return mapErr(err)
			}
			if err := appendStateChange(ctx, tx, store.PrincipalID(pid),
				store.EntityKindEmail, uint64(mid), uint64(t.MailboxID), store.ChangeOpCreated, now); err != nil {
				return err
			}
			if i == 0 {
				firstUID = allocUID
				firstModSeq = allocModSeq
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE principals SET used_bytes = used_bytes + ?, updated_at_us = ? WHERE id = ?`,
			msg.Size, usMicros(now), pid); err != nil {
			return mapErr(err)
		}
		return incRef(ctx, tx, msg.Blob.Hash, msg.Blob.Size, now)
	})
	if err != nil {
		return 0, 0, err
	}
	return firstUID, firstModSeq, nil
}

func (m *metadata) ReplaceMessageBody(
	ctx context.Context,
	id store.MessageID,
	ref store.BlobRef,
	size int64,
	env store.Envelope,
) error {
	now := m.s.clock.Now().UTC()
	if env.MessageID != "" {
		env.MessageID = mailparse.NormalizeMessageID(env.MessageID)
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Load the current message row so we can compute the byte
		// delta and unref the old blob.
		var pid, oldSize int64
		var oldHash string
		err := tx.QueryRowContext(ctx, `
			SELECT principal_id, blob_hash, size FROM messages WHERE id = ?`,
			int64(id)).Scan(&pid, &oldHash, &oldSize)
		if err != nil {
			return mapErr(err)
		}
		// Quota check on growth.
		if delta := size - oldSize; delta > 0 {
			var quota, used int64
			if err := tx.QueryRowContext(ctx,
				`SELECT quota_bytes, used_bytes FROM principals WHERE id = ?`, pid).Scan(&quota, &used); err != nil {
				return mapErr(err)
			}
			if quota > 0 && used+delta > quota {
				return store.ErrQuotaExceeded
			}
		}
		// Update the messages row with the new blob + envelope.
		if _, err := tx.ExecContext(ctx, `
			UPDATE messages
			   SET blob_hash = ?, blob_size = ?, size = ?,
			       env_subject = ?, env_from = ?, env_to = ?, env_cc = ?, env_bcc = ?, env_reply_to = ?,
			       env_message_id = ?, env_in_reply_to = ?, env_date_us = ?
			 WHERE id = ?`,
			ref.Hash, ref.Size, size,
			env.Subject, env.From, env.To, env.Cc, env.Bcc, env.ReplyTo,
			env.MessageID, env.InReplyTo, usMicros(env.Date),
			int64(id),
		); err != nil {
			return mapErr(err)
		}
		// Adjust principal used_bytes.
		if _, err := tx.ExecContext(ctx, `
			UPDATE principals SET used_bytes = used_bytes + ?, updated_at_us = ? WHERE id = ?`,
			size-oldSize, usMicros(now), pid); err != nil {
			return mapErr(err)
		}
		// Refcount: dec old, inc new (skip when identical hash, which
		// happens when the rewrite produces a byte-identical blob).
		if oldHash != ref.Hash {
			if err := decRef(ctx, tx, oldHash, now); err != nil {
				return err
			}
			if err := incRef(ctx, tx, ref.Hash, ref.Size, now); err != nil {
				return err
			}
		}
		// Bump every membership's modseq + mailbox highest_modseq, and
		// emit one EntityKindEmail/ChangeOpUpdated entry per mailbox so
		// that JMAP Email/changes and IMAP CONDSTORE both observe the
		// rewrite.
		rows, err := tx.QueryContext(ctx,
			`SELECT mailbox_id FROM message_mailboxes WHERE message_id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		var mailboxIDs []int64
		for rows.Next() {
			var mb int64
			if err := rows.Scan(&mb); err != nil {
				rows.Close()
				return mapErr(err)
			}
			mailboxIDs = append(mailboxIDs, mb)
		}
		rows.Close()
		for _, mb := range mailboxIDs {
			var highest int64
			if err := tx.QueryRowContext(ctx,
				`SELECT highest_modseq FROM mailboxes WHERE id = ?`, mb).Scan(&highest); err != nil {
				return mapErr(err)
			}
			newModSeq := highest + 1
			if _, err := tx.ExecContext(ctx, `
				UPDATE message_mailboxes SET modseq = ? WHERE message_id = ? AND mailbox_id = ?`,
				newModSeq, int64(id), mb); err != nil {
				return mapErr(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE mailboxes SET highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
				newModSeq, usMicros(now), mb); err != nil {
				return mapErr(err)
			}
			if err := appendStateChange(ctx, tx, store.PrincipalID(pid),
				store.EntityKindEmail, uint64(id), uint64(mb),
				store.ChangeOpUpdated, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *metadata) GetMessage(ctx context.Context, id store.MessageID) (store.Message, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, principal_id, blob_hash, blob_size, internal_date_us,
		       received_at_us, size, thread_id,
		       env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
		       env_message_id, env_in_reply_to, env_date_us
		  FROM messages WHERE id = ?`, int64(id))
	msg, err := scanMessageRow(row)
	if err != nil {
		return store.Message{}, err
	}
	if err := m.loadMailboxes(ctx, &msg, 0); err != nil {
		return store.Message{}, err
	}
	return msg, nil
}

// scanMessage is a backward-compat shim that scans a joined
// (messages JOIN message_mailboxes) row. Used by ListMessages and
// ListDueSnoozedMessages where a specific mailbox context is known.
func scanMessage(row rowLike) (store.Message, error) {
	var msg store.Message
	var id, pid int64
	var idUs, rcvUs int64
	var blobSize int64
	var thread int64
	var envDateUs int64
	// message_mailboxes fields
	var mbox, uid, modseq, flags int64
	var keywords string
	var snoozedUs sql.NullInt64
	err := row.Scan(
		&id, &pid, &msg.Blob.Hash, &blobSize, &idUs, &rcvUs,
		&msg.Size, &thread,
		&msg.Envelope.Subject, &msg.Envelope.From, &msg.Envelope.To,
		&msg.Envelope.Cc, &msg.Envelope.Bcc, &msg.Envelope.ReplyTo,
		&msg.Envelope.MessageID, &msg.Envelope.InReplyTo, &envDateUs,
		// message_mailboxes
		&mbox, &uid, &modseq, &flags, &keywords, &snoozedUs,
	)
	if err != nil {
		return store.Message{}, mapErr(err)
	}
	msg.ID = store.MessageID(id)
	msg.PrincipalID = store.PrincipalID(pid)
	msg.InternalDate = fromMicros(idUs)
	msg.ReceivedAt = fromMicros(rcvUs)
	msg.Blob.Size = blobSize
	msg.ThreadID = uint64(thread)
	msg.Envelope.Date = fromMicros(envDateUs)
	msg.MailboxID = store.MailboxID(mbox)
	msg.UID = store.UID(uid)
	msg.ModSeq = store.ModSeq(modseq)
	msg.Flags = store.MessageFlags(flags)
	if keywords != "" {
		msg.Keywords = strings.Split(keywords, ",")
	}
	if snoozedUs.Valid {
		t := fromMicros(snoozedUs.Int64)
		msg.SnoozedUntil = &t
	}
	mm := store.MessageMailbox{
		MessageID:    msg.ID,
		MailboxID:    msg.MailboxID,
		UID:          msg.UID,
		ModSeq:       msg.ModSeq,
		Flags:        msg.Flags,
		Keywords:     msg.Keywords,
		SnoozedUntil: msg.SnoozedUntil,
	}
	msg.Mailboxes = []store.MessageMailbox{mm}
	return msg, nil
}

func (m *metadata) UpdateMessageThreadID(ctx context.Context, msgID store.MessageID, threadID uint64) error {
	_, err := m.s.db.ExecContext(ctx,
		`UPDATE messages SET thread_id = ? WHERE id = ?`,
		int64(threadID), int64(msgID))
	return mapErr(err)
}

func (m *metadata) UpdateMessageFlags(
	ctx context.Context,
	id store.MessageID,
	mailboxID store.MailboxID,
	flagAdd, flagClear store.MessageFlags,
	keywordAdd, keywordClear []string,
	unchangedSince store.ModSeq,
) (store.ModSeq, error) {
	now := m.s.clock.Now().UTC()
	var modseq store.ModSeq
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var curFlags int64
		var curKeywords string
		var curModSeq int64
		err := tx.QueryRowContext(ctx, `
			SELECT flags, keywords_csv, modseq
			  FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
			int64(id), int64(mailboxID)).Scan(&curFlags, &curKeywords, &curModSeq)
		if err != nil {
			return mapErr(err)
		}
		if unchangedSince != 0 && store.ModSeq(curModSeq) > unchangedSince {
			return fmt.Errorf("%w: UNCHANGEDSINCE %d < current %d", store.ErrConflict, unchangedSince, curModSeq)
		}
		newFlags := (store.MessageFlags(curFlags) | flagAdd) &^ flagClear
		kwSet := map[string]struct{}{}
		if curKeywords != "" {
			for _, k := range strings.Split(curKeywords, ",") {
				kwSet[k] = struct{}{}
			}
		}
		for _, k := range keywordAdd {
			kwSet[strings.ToLower(k)] = struct{}{}
		}
		for _, k := range keywordClear {
			delete(kwSet, strings.ToLower(k))
		}
		kws := make([]string, 0, len(kwSet))
		for k := range kwSet {
			kws = append(kws, k)
		}
		sortStrings(kws)

		// Advance mailbox modseq.
		var pid, highest int64
		if err := tx.QueryRowContext(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = ?`,
			int64(mailboxID)).Scan(&pid, &highest); err != nil {
			return mapErr(err)
		}
		modseq = store.ModSeq(highest + 1)

		if _, err := tx.ExecContext(ctx, `
			UPDATE message_mailboxes SET flags = ?, keywords_csv = ?, modseq = ?
			 WHERE message_id = ? AND mailbox_id = ?`,
			int64(newFlags), strings.Join(kws, ","), int64(modseq),
			int64(id), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailboxes SET highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
			int64(modseq), usMicros(now), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(id), uint64(mailboxID), store.ChangeOpUpdated, now)
	})
	if err != nil {
		return 0, err
	}
	return modseq, nil
}

func (m *metadata) ExpungeMessages(ctx context.Context, mailboxID store.MailboxID, ids []store.MessageID) error {
	if len(ids) == 0 {
		return nil
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid, highest int64
		if err := tx.QueryRowContext(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = ?`,
			int64(mailboxID)).Scan(&pid, &highest); err != nil {
			return mapErr(err)
		}
		var removed int
		for _, id := range ids {
			// Check the membership exists.
			var memberCount int64
			err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
				int64(id), int64(mailboxID)).Scan(&memberCount)
			if err != nil {
				return mapErr(err)
			}
			if memberCount == 0 {
				continue // silently skip
			}
			// Delete this membership.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
				int64(id), int64(mailboxID)); err != nil {
				return mapErr(err)
			}
			// Check remaining memberships.
			var remaining int64
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = ?`, int64(id)).Scan(&remaining); err != nil {
				return mapErr(err)
			}
			if remaining == 0 {
				// Last membership gone: clean up messages row + blob refcount.
				var size int64
				var hash string
				if err := tx.QueryRowContext(ctx,
					`SELECT size, blob_hash FROM messages WHERE id = ?`, int64(id)).Scan(&size, &hash); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return mapErr(err)
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, int64(id)); err != nil {
					return mapErr(err)
				}
				if hash != "" {
					if err := decRef(ctx, tx, hash, now); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx,
						`UPDATE principals SET used_bytes = used_bytes - ?, updated_at_us = ? WHERE id = ?`,
						size, usMicros(now), pid); err != nil {
						return mapErr(err)
					}
				}
			}
			highest++
			if _, err := tx.ExecContext(ctx,
				`UPDATE mailboxes SET highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
				highest, usMicros(now), int64(mailboxID)); err != nil {
				return mapErr(err)
			}
			if err := appendStateChange(ctx, tx, store.PrincipalID(pid),
				store.EntityKindEmail, uint64(id), uint64(mailboxID), store.ChangeOpDestroyed, now); err != nil {
				return err
			}
			removed++
		}
		if removed == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) MoveMessage(ctx context.Context, msgID store.MessageID, fromMailboxID, targetMailboxID store.MailboxID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Verify source membership exists.
		var srcUID, srcModSeq, srcFlags int64
		var srcKeywords string
		var snoozedUs sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT uid, modseq, flags, keywords_csv, snoozed_until_us
			  FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
			int64(msgID), int64(fromMailboxID)).Scan(&srcUID, &srcModSeq, &srcFlags, &srcKeywords, &snoozedUs)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return mapErr(err)
		}

		// Get target mailbox info.
		var pid, tgtUIDNext, tgtHighest int64
		if err := tx.QueryRowContext(ctx, `SELECT principal_id, uidnext, highest_modseq FROM mailboxes WHERE id = ?`,
			int64(targetMailboxID)).Scan(&pid, &tgtUIDNext, &tgtHighest); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		newUID := tgtUIDNext
		newModSeq := tgtHighest + 1

		var snoozedArg any
		if snoozedUs.Valid {
			snoozedArg = snoozedUs.Int64
		}
		// Insert new membership in target.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_mailboxes
			  (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			int64(msgID), int64(targetMailboxID), newUID, newModSeq,
			srcFlags, srcKeywords, snoozedArg); err != nil {
			return mapErr(err)
		}
		// Delete source membership.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
			int64(msgID), int64(fromMailboxID)); err != nil {
			return mapErr(err)
		}
		// Advance target mailbox uidnext + highest_modseq.
		if _, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
			newModSeq, usMicros(now), int64(targetMailboxID)); err != nil {
			return mapErr(err)
		}
		// Advance source mailbox highest_modseq.
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailboxes SET highest_modseq = highest_modseq + 1, updated_at_us = ? WHERE id = ?`,
			usMicros(now), int64(fromMailboxID)); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(msgID), uint64(targetMailboxID), store.ChangeOpUpdated, now)
	})
}

// AddMessageToMailbox adds an existing message to mailboxID.
func (m *metadata) AddMessageToMailbox(ctx context.Context, msgID store.MessageID, mailboxID store.MailboxID) (store.UID, store.ModSeq, error) {
	now := m.s.clock.Now().UTC()
	var allocUID store.UID
	var allocModSeq store.ModSeq
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Verify message exists and get pid for state change.
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM messages WHERE id = ?`, int64(msgID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		// Check for duplicate membership.
		var existing int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
			int64(msgID), int64(mailboxID)).Scan(&existing); err != nil {
			return mapErr(err)
		}
		if existing > 0 {
			return fmt.Errorf("message already in mailbox: %w", store.ErrConflict)
		}
		var uidNext, highest int64
		if err := tx.QueryRowContext(ctx,
			`SELECT uidnext, highest_modseq FROM mailboxes WHERE id = ?`, int64(mailboxID)).Scan(&uidNext, &highest); err != nil {
			return mapErr(err)
		}
		allocUID = store.UID(uidNext)
		allocModSeq = store.ModSeq(highest + 1)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_mailboxes
			  (message_id, mailbox_id, uid, modseq, flags, keywords_csv)
			VALUES (?, ?, ?, ?, 0, '')`,
			int64(msgID), int64(mailboxID), int64(allocUID), int64(allocModSeq)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
			int64(allocModSeq), usMicros(now), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(msgID), uint64(mailboxID), store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, 0, err
	}
	return allocUID, allocModSeq, nil
}

// RemoveMessageFromMailbox removes the (msgID, mailboxID) membership.
// If no memberships remain, the messages row and blob refcount are
// cleaned up.
func (m *metadata) RemoveMessageFromMailbox(ctx context.Context, msgID store.MessageID, mailboxID store.MailboxID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Verify membership exists.
		var exists int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
			int64(msgID), int64(mailboxID)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		if exists == 0 {
			return store.ErrNotFound
		}
		// Get pid for state change.
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM messages WHERE id = ?`, int64(msgID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
			int64(msgID), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		// Bump mailbox modseq.
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailboxes SET highest_modseq = highest_modseq + 1, updated_at_us = ? WHERE id = ?`,
			usMicros(now), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		// Check remaining memberships.
		var remaining int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = ?`, int64(msgID)).Scan(&remaining); err != nil {
			return mapErr(err)
		}
		if remaining == 0 {
			var size int64
			var hash string
			if err := tx.QueryRowContext(ctx,
				`SELECT size, blob_hash FROM messages WHERE id = ?`, int64(msgID)).Scan(&size, &hash); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return mapErr(err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, int64(msgID)); err != nil {
				return mapErr(err)
			}
			if hash != "" {
				if err := decRef(ctx, tx, hash, now); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx,
					`UPDATE principals SET used_bytes = used_bytes - ?, updated_at_us = ? WHERE id = ?`,
					size, usMicros(now), pid); err != nil {
					return mapErr(err)
				}
			}
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(msgID), uint64(mailboxID), store.ChangeOpDestroyed, now)
	})
}

func (m *metadata) UpdateMailboxModseqAndAppendChange(
	ctx context.Context,
	mailboxID store.MailboxID,
	change store.StateChange,
) (store.ModSeq, store.ChangeSeq, error) {
	now := m.s.clock.Now().UTC()
	var newModseq store.ModSeq
	var newSeq store.ChangeSeq
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var pid, highest int64
		err := tx.QueryRowContext(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = ?`,
			int64(mailboxID)).Scan(&pid, &highest)
		if err != nil {
			return mapErr(err)
		}
		newModseq = store.ModSeq(highest + 1)
		if _, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
			int64(newModseq), usMicros(now), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		// The caller's StateChange.PrincipalID is ignored — the per-mailbox
		// owner authoritatively scopes the seq. Caller-supplied EntityID and
		// ParentEntityID are honoured verbatim; ParentEntityID falls back to
		// the mailbox itself when the caller leaves it zero (the common case
		// where the change is a child entity of mailboxID).
		parentEntityID := change.ParentEntityID
		if parentEntityID == 0 {
			parentEntityID = uint64(mailboxID)
		}
		seq, err := appendStateChangeSeq(ctx, tx, store.PrincipalID(pid), change.Kind,
			change.EntityID, parentEntityID, change.Op, now)
		if err != nil {
			return err
		}
		newSeq = seq
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return newModseq, newSeq, nil
}

// -- change feed ------------------------------------------------------

func (m *metadata) ReadChangeFeed(
	ctx context.Context,
	principalID store.PrincipalID,
	fromSeq store.ChangeSeq,
	max int,
) ([]store.StateChange, error) {
	if max <= 0 {
		max = 1000
	}
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT seq, principal_id, entity_kind, entity_id, parent_entity_id, op, produced_at_us
		  FROM state_changes
		 WHERE principal_id = ? AND seq > ?
		 ORDER BY seq ASC LIMIT ?`, int64(principalID), int64(fromSeq), max)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.StateChange
	for rows.Next() {
		var seq, pid, eid, peid, prodUs int64
		var kind string
		var op int
		if err := rows.Scan(&seq, &pid, &kind, &eid, &peid, &op, &prodUs); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.StateChange{
			Seq:            store.ChangeSeq(seq),
			PrincipalID:    store.PrincipalID(pid),
			Kind:           store.EntityKind(kind),
			EntityID:       uint64(eid),
			ParentEntityID: uint64(peid),
			Op:             store.ChangeOp(op),
			ProducedAt:     fromMicros(prodUs),
		})
	}
	return out, rows.Err()
}

func (m *metadata) GetMaxChangeSeqForKind(
	ctx context.Context,
	principalID store.PrincipalID,
	kind store.EntityKind,
) (store.ChangeSeq, error) {
	var seq int64
	err := m.s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM state_changes WHERE principal_id = ? AND entity_kind = ?`,
		int64(principalID), string(kind)).Scan(&seq)
	if err != nil {
		return 0, mapErr(err)
	}
	return store.ChangeSeq(seq), nil
}

// -- helpers ----------------------------------------------------------

func appendStateChange(
	ctx context.Context, tx *sql.Tx, principalID store.PrincipalID,
	kind store.EntityKind, entityID uint64, parentEntityID uint64,
	op store.ChangeOp, now time.Time,
) error {
	_, err := appendStateChangeSeq(ctx, tx, principalID, kind, entityID, parentEntityID, op, now)
	return err
}

func appendStateChangeSeq(
	ctx context.Context, tx *sql.Tx, principalID store.PrincipalID,
	kind store.EntityKind, entityID uint64, parentEntityID uint64,
	op store.ChangeOp, now time.Time,
) (store.ChangeSeq, error) {
	var next int64
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0)+1 FROM state_changes WHERE principal_id = ?`,
		int64(principalID)).Scan(&next)
	if err != nil {
		return 0, mapErr(err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO state_changes (principal_id, seq, entity_kind, entity_id,
		  parent_entity_id, op, produced_at_us) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		int64(principalID), next, string(kind), int64(entityID),
		int64(parentEntityID), int(op), usMicros(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return store.ChangeSeq(next), nil
}

func incRef(ctx context.Context, tx *sql.Tx, hash string, size int64, now time.Time) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE blob_refs SET ref_count = ref_count + 1, last_change_us = ? WHERE hash = ?`,
		usMicros(now), hash)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storesqlite: rows affected: %w", err)
	}
	if n > 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO blob_refs (hash, size, ref_count, last_change_us) VALUES (?, ?, 1, ?)`,
		hash, size, usMicros(now))
	return mapErr(err)
}

// decRef decrements blob_refs.ref_count for hash, refusing to drive
// it below zero. Wave 2.9.9 audit (Track A): the WHERE ref_count > 0
// guard is the SQL-level enforcement of the same invariant the
// fakestore clamps in memory; without it a duplicate hard-delete or a
// retention pass that races a concurrent unref could underflow the
// column on SQLite (signed INTEGER, so wrap-around to a negative
// value) and confuse the orphan-blob sweeper into garbage-collecting
// a still-referenced blob. The rows-affected==0 path is graceful: the
// hash either was never registered, or was already at zero, both of
// which the caller treats as a no-op. Logged at WARN so an operator
// can spot a runaway double-unref without it surfacing as a hard
// error during a hard-delete batch.
func decRef(ctx context.Context, tx *sql.Tx, hash string, now time.Time) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE blob_refs SET ref_count = ref_count - 1, last_change_us = ? WHERE hash = ? AND ref_count > 0`,
		usMicros(now), hash)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storesqlite: decRef rows affected: %w", err)
	}
	if n == 0 {
		slog.Warn("storesqlite: decRef no-op (already zero or unknown hash)", "hash", hash)
	}
	return nil
}

func (m *metadata) GetBlobRef(ctx context.Context, hash string) (int64, int64, error) {
	var size, refs int64
	err := m.s.db.QueryRowContext(ctx,
		`SELECT size, ref_count FROM blob_refs WHERE hash = ?`, hash).Scan(&size, &refs)
	if err != nil {
		return 0, 0, mapErr(err)
	}
	return size, refs, nil
}

// IncRefBlob increments (or inserts with count=1) the blob_refs row for
// hash. size is used only when inserting a new row (REQ-SET-03b).
func (m *metadata) IncRefBlob(ctx context.Context, hash string, size int64) error {
	if hash == "" {
		return nil
	}
	tx, err := m.s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := incRef(ctx, tx, hash, size, m.s.clock.Now()); err != nil {
		return err
	}
	return mapErr(tx.Commit())
}

// DecRefBlob decrements the blob_refs row for hash, refusing to go below
// zero. No-op when hash is empty (REQ-SET-03b).
func (m *metadata) DecRefBlob(ctx context.Context, hash string) error {
	if hash == "" {
		return nil
	}
	tx, err := m.s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := decRef(ctx, tx, hash, m.s.clock.Now()); err != nil {
		return err
	}
	return mapErr(tx.Commit())
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func nullable(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// newUIDValidity returns a 32-bit UIDVALIDITY seeded from the given
// time. IMAP requires UIDVALIDITY to be strictly increasing across
// mailbox re-creations; using the clock's seconds-since-epoch satisfies
// that for any non-pathological clock, and adds a small random low bias
// so same-second creations still differ.
//
// rs is the entropy source for the low-byte salt. Production passes
// crypto/rand.Reader (set on the Store at Open time); tests pass a
// deterministic reader. UIDVALIDITY is opaque to clients but
// externally visible, so we prefer a non-guessable salt over math/rand
// — even though the security risk is low, the better posture costs
// nothing. On a Read failure we fall back to a zero salt so the
// timestamp alone remains usable.
func newUIDValidity(t time.Time, rs io.Reader) store.UIDValidity {
	sec := t.Unix()
	if sec <= 0 {
		sec = 1
	}
	var b [1]byte
	if rs != nil {
		_, _ = io.ReadFull(rs, b[:])
	}
	return store.UIDValidity(uint32(sec)) + store.UIDValidity(uint32(b[0]))
}

func sortStrings(s []string) {
	// Minimal insertion sort (keywords lists are short).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// -- ListPrincipals ---------------------------------------------------

func (m *metadata) ListPrincipals(ctx context.Context, after store.PrincipalID, limit int) ([]store.Principal, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals WHERE id > ? ORDER BY id ASC LIMIT ?`,
		int64(after), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Principal
	for rows.Next() {
		var p store.Principal
		var kind int64
		var flags int64
		var createdUs, updatedUs int64
		var totp []byte
		var id int64
		if err := rows.Scan(&id, &kind, &p.CanonicalEmail, &p.DisplayName, &p.PasswordHash,
			&totp, &p.QuotaBytes, &flags, &createdUs, &updatedUs); err != nil {
			return nil, mapErr(err)
		}
		p.ID = store.PrincipalID(id)
		p.Kind = store.PrincipalKind(kind)
		p.Flags = store.PrincipalFlags(flags)
		p.CreatedAt = fromMicros(createdUs)
		p.UpdatedAt = fromMicros(updatedUs)
		if len(totp) > 0 {
			p.TOTPSecret = totp
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// -- SearchPrincipalsByText --------------------------------------------

func (m *metadata) SearchPrincipalsByText(ctx context.Context, prefix string, limit int) ([]store.Principal, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	lower := strings.ToLower(prefix)
	// Two groups: display-name matches (anywhere) and email-local-part
	// matches (prefix). We fetch both groups in one query using a
	// computed priority column, then sort in Go to guarantee determinism.
	// The LIKE patterns use '%' to match anywhere in display_name and
	// prefix-match the local-part of canonical_email.
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals
		 WHERE lower(display_name) LIKE ? OR lower(canonical_email) LIKE ?
		 LIMIT ?`,
		"%"+lower+"%",
		lower+"%@%",
		limit*2, // over-fetch to ensure we can sort and trim to limit
	)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Principal
	for rows.Next() {
		var p store.Principal
		var kind int64
		var flags int64
		var createdUs, updatedUs int64
		var totp []byte
		var id int64
		if err := rows.Scan(&id, &kind, &p.CanonicalEmail, &p.DisplayName, &p.PasswordHash,
			&totp, &p.QuotaBytes, &flags, &createdUs, &updatedUs); err != nil {
			return nil, mapErr(err)
		}
		p.ID = store.PrincipalID(id)
		p.Kind = store.PrincipalKind(kind)
		p.Flags = store.PrincipalFlags(flags)
		p.CreatedAt = fromMicros(createdUs)
		p.UpdatedAt = fromMicros(updatedUs)
		if len(totp) > 0 {
			p.TOTPSecret = totp
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return store.SortPrincipalSearchResults(out, lower, limit), nil
}

// -- SearchPrincipalsByTextInDomain ------------------------------------

func (m *metadata) SearchPrincipalsByTextInDomain(ctx context.Context, prefix, domain string, limit int) ([]store.Principal, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return m.SearchPrincipalsByText(ctx, prefix, limit)
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	lower := strings.ToLower(prefix)
	lowerDomain := strings.ToLower(domain)
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals
		 WHERE (lower(display_name) LIKE ? OR lower(canonical_email) LIKE ?)
		   AND lower(canonical_email) LIKE ?
		 LIMIT ?`,
		"%"+lower+"%",
		lower+"%@%",
		"%@"+lowerDomain,
		limit*2, // over-fetch; Go-side sort trims to limit
	)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Principal
	for rows.Next() {
		var p store.Principal
		var kind int64
		var flags int64
		var createdUs, updatedUs int64
		var totp []byte
		var id int64
		if err := rows.Scan(&id, &kind, &p.CanonicalEmail, &p.DisplayName, &p.PasswordHash,
			&totp, &p.QuotaBytes, &flags, &createdUs, &updatedUs); err != nil {
			return nil, mapErr(err)
		}
		p.ID = store.PrincipalID(id)
		p.Kind = store.PrincipalKind(kind)
		p.Flags = store.PrincipalFlags(flags)
		p.CreatedAt = fromMicros(createdUs)
		p.UpdatedAt = fromMicros(updatedUs)
		if len(totp) > 0 {
			p.TOTPSecret = totp
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return store.SortPrincipalSearchResults(out, lower, limit), nil
}

// -- DeletePrincipal --------------------------------------------------

func (m *metadata) DeletePrincipal(ctx context.Context, pid store.PrincipalID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Confirm existence up-front so the caller can distinguish
		// "already gone" from "nothing to do".
		var exists int64
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM principals WHERE id = ?`, int64(pid)).Scan(&exists)
		if err != nil {
			return mapErr(err)
		}
		// Decrement refcounts for every blob still owned by this principal.
		// Blobs are refcounted per messages row (not per membership), so we
		// look at messages.principal_id directly.
		hashRows, err := tx.QueryContext(ctx, `
			SELECT blob_hash FROM messages WHERE principal_id = ?`,
			int64(pid))
		if err != nil {
			return mapErr(err)
		}
		var hashes []string
		for hashRows.Next() {
			var h string
			if err := hashRows.Scan(&h); err != nil {
				hashRows.Close()
				return mapErr(err)
			}
			hashes = append(hashes, h)
		}
		hashRows.Close()
		for _, h := range hashes {
			if err := decRef(ctx, tx, h, now); err != nil {
				return err
			}
		}
		// Per-principal tables that lack ON DELETE CASCADE to principals:
		// state_changes and audit_log carry a principal_id column but are
		// not FK-bound (so the cascade does not reach them). Wipe them
		// explicitly within the same tx.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM state_changes WHERE principal_id = ?`, int64(pid)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM audit_log WHERE principal_id = ?`, int64(pid)); err != nil {
			return mapErr(err)
		}
		// Phase 2 queue rows belonging to this principal: drop them and
		// decrement their body-blob refcounts so the GC reclaims bytes.
		// queue.principal_id has ON DELETE SET NULL so a forwarded row
		// (originally tied to this principal) survives, but we do want
		// to drop rows whose submitter is being removed. The migration
		// makes the FK SET NULL deliberately so Sieve-redirected rows
		// without a clear submitter context are not accidentally
		// orphaned by ON DELETE CASCADE on principal removal.
		queueRows, err := tx.QueryContext(ctx,
			`SELECT body_blob_hash FROM queue WHERE principal_id = ?`, int64(pid))
		if err != nil {
			return mapErr(err)
		}
		var queueHashes []string
		for queueRows.Next() {
			var h string
			if err := queueRows.Scan(&h); err != nil {
				queueRows.Close()
				return mapErr(err)
			}
			queueHashes = append(queueHashes, h)
		}
		queueRows.Close()
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM queue WHERE principal_id = ?`, int64(pid)); err != nil {
			return mapErr(err)
		}
		for _, h := range queueHashes {
			if h == "" {
				continue
			}
			if err := decRef(ctx, tx, h, now); err != nil {
				return err
			}
		}
		// principals cascades to aliases, oidc_links, api_keys, mailboxes,
		// mailbox_acl, jmap_states; mailboxes cascades to messages and
		// further mailbox_acl rows. Blob refs stay (GC sweeps later).
		res, err := tx.ExecContext(ctx,
			`DELETE FROM principals WHERE id = ?`, int64(pid))
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

// -- ListOIDCProviders / DeleteOIDCProvider / UnlinkOIDC --------------

func (m *metadata) ListOIDCProviders(ctx context.Context) ([]store.OIDCProvider, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT name, issuer_url, client_id, client_secret_ref, scopes_csv,
		       auto_provision, created_at_us
		  FROM oidc_providers ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.OIDCProvider
	for rows.Next() {
		var p store.OIDCProvider
		var scopes string
		var auto int64
		var createdUs int64
		if err := rows.Scan(&p.Name, &p.IssuerURL, &p.ClientID, &p.ClientSecretRef,
			&scopes, &auto, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		if scopes != "" {
			p.Scopes = strings.Split(scopes, ",")
		}
		p.AutoProvision = auto != 0
		p.CreatedAt = fromMicros(createdUs)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteOIDCProvider(ctx context.Context, id store.OIDCProviderID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// oidc_links FK to oidc_providers cascades on delete — the two
		// rows land together.
		res, err := tx.ExecContext(ctx,
			`DELETE FROM oidc_providers WHERE name = ?`, string(id))
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

func (m *metadata) UnlinkOIDC(ctx context.Context, pid store.PrincipalID, providerID store.OIDCProviderID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM oidc_links WHERE principal_id = ? AND provider_name = ?`,
			int64(pid), string(providerID))
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

// -- cursors ----------------------------------------------------------

func (m *metadata) GetFTSCursor(ctx context.Context, key string) (uint64, error) {
	var seq int64
	err := m.s.db.QueryRowContext(ctx,
		`SELECT seq FROM cursors WHERE key = ?`, key).Scan(&seq)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, mapErr(err)
	}
	return uint64(seq), nil
}

func (m *metadata) SetFTSCursor(ctx context.Context, key string, seq uint64) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cursors (key, seq) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET seq = excluded.seq`,
			key, int64(seq))
		return mapErr(err)
	})
}

// -- audit log --------------------------------------------------------

// encodeAuditMetadata marshals the Metadata map to canonical JSON
// (keys sorted) so backup diffs are stable. Empty or nil input yields
// an empty string so the column stays compact for the common case.
func encodeAuditMetadata(meta map[string]string) (string, error) {
	if len(meta) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return "", err
		}
		vb, err := json.Marshal(meta[k])
		if err != nil {
			return "", err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.String(), nil
}

func decodeAuditMetadata(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("storesqlite: decode audit metadata: %w", err)
	}
	return out, nil
}

// encodeStringSliceJSON serialises a (possibly nil) slice to a compact
// JSON array string for storage. nil and empty both produce "[]".
func encodeStringSliceJSON(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// decodeStringSliceJSON deserialises a JSON array from the database.
// Empty / invalid strings return nil (no constraint).
func decodeStringSliceJSON(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// auditPrincipalID extracts the principal-id from a subject of the
// form "principal:<id>" for indexing; 0 if the subject refers to
// something else. Actions "upon" a principal and actions "by" a
// principal (ActorKind == ActorPrincipal) are both indexed to the same
// column so ListAuditLog(PrincipalID=...) returns either class.
func auditPrincipalID(e store.AuditLogEntry) int64 {
	if strings.HasPrefix(e.Subject, "principal:") {
		if id, err := strconv.ParseUint(strings.TrimPrefix(e.Subject, "principal:"), 10, 64); err == nil {
			return int64(id)
		}
	}
	if e.ActorKind == store.ActorPrincipal {
		if id, err := strconv.ParseUint(e.ActorID, 10, 64); err == nil {
			return int64(id)
		}
	}
	return 0
}

func (m *metadata) AppendAuditLog(ctx context.Context, entry store.AuditLogEntry) error {
	metaJSON, err := encodeAuditMetadata(entry.Metadata)
	if err != nil {
		return err
	}
	at := entry.At
	if at.IsZero() {
		at = m.s.clock.Now().UTC()
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO audit_log (at_us, actor_kind, actor_id, action, subject,
			  remote_addr, outcome, message, metadata_json, principal_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			usMicros(at), int64(entry.ActorKind), entry.ActorID, entry.Action, entry.Subject,
			entry.RemoteAddr, int64(entry.Outcome), entry.Message, metaJSON,
			auditPrincipalID(entry))
		return mapErr(err)
	})
}

// -- GetMailboxByName / ListMessages / SetMailboxSubscribed / RenameMailbox --

func (m *metadata) GetMailboxByName(ctx context.Context, pid store.PrincipalID, name string) (store.Mailbox, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT id, principal_id, parent_id, name, attributes, uidvalidity, uidnext,
		       highest_modseq, created_at_us, updated_at_us, color_hex, sort_order
		  FROM mailboxes WHERE principal_id = ? AND name = ?`,
		int64(pid), name)
	return scanMailbox(row)
}

func (m *metadata) ListMessages(ctx context.Context, mailboxID store.MailboxID, filter store.MessageFilter) ([]store.Message, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	// ReceivedBefore adds an optional time-range predicate. The column
	// internal_date_us is used (matches InternalDate on the Message type)
	// because it is always set and indexed alongside the mailbox_id foreign
	// key. 0 means "no constraint" — callers that do not set ReceivedBefore
	// get the previous behaviour.
	var receivedBeforeUs int64
	if filter.ReceivedBefore != nil {
		receivedBeforeUs = usMicros(*filter.ReceivedBefore)
	}
	var rows *sql.Rows
	var err error
	if receivedBeforeUs > 0 {
		rows, err = m.s.db.QueryContext(ctx, `
			SELECT m.id, m.principal_id, m.blob_hash, m.blob_size, m.internal_date_us,
			       m.received_at_us, m.size, m.thread_id,
			       m.env_subject, m.env_from, m.env_to, m.env_cc, m.env_bcc, m.env_reply_to,
			       m.env_message_id, m.env_in_reply_to, m.env_date_us,
			       mm.mailbox_id, mm.uid, mm.modseq, mm.flags, mm.keywords_csv, mm.snoozed_until_us
			  FROM messages m
			  JOIN message_mailboxes mm ON mm.message_id = m.id
			 WHERE mm.mailbox_id = ? AND mm.uid > ? AND m.internal_date_us < ?
			 ORDER BY mm.uid ASC LIMIT ?`,
			int64(mailboxID), int64(filter.AfterUID), receivedBeforeUs, limit)
	} else {
		rows, err = m.s.db.QueryContext(ctx, `
			SELECT m.id, m.principal_id, m.blob_hash, m.blob_size, m.internal_date_us,
			       m.received_at_us, m.size, m.thread_id,
			       m.env_subject, m.env_from, m.env_to, m.env_cc, m.env_bcc, m.env_reply_to,
			       m.env_message_id, m.env_in_reply_to, m.env_date_us,
			       mm.mailbox_id, mm.uid, mm.modseq, mm.flags, mm.keywords_csv, mm.snoozed_until_us
			  FROM messages m
			  JOIN message_mailboxes mm ON mm.message_id = m.id
			 WHERE mm.mailbox_id = ? AND mm.uid > ?
			 ORDER BY mm.uid ASC LIMIT ?`,
			int64(mailboxID), int64(filter.AfterUID), limit)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (m *metadata) SetMailboxSubscribed(ctx context.Context, mailboxID store.MailboxID, subscribed bool) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var attrs int64
		if err := tx.QueryRowContext(ctx,
			`SELECT attributes FROM mailboxes WHERE id = ?`, int64(mailboxID)).Scan(&attrs); err != nil {
			return mapErr(err)
		}
		bit := int64(store.MailboxAttrSubscribed)
		if subscribed {
			attrs |= bit
		} else {
			attrs &^= bit
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET attributes = ?, updated_at_us = ? WHERE id = ?`,
			attrs, usMicros(now), int64(mailboxID))
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

func (m *metadata) RenameMailbox(ctx context.Context, mailboxID store.MailboxID, newName string) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Fetch principal_id to scope the conflict check.
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM mailboxes WHERE id = ?`, int64(mailboxID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		// Collision check within the owning principal.
		var other int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM mailboxes WHERE principal_id = ? AND name = ? AND id != ?`,
			pid, newName, int64(mailboxID)).Scan(&other)
		if err == nil {
			return fmt.Errorf("mailbox %q: %w", newName, store.ErrConflict)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET name = ?, updated_at_us = ? WHERE id = ?`,
			newName, usMicros(now), int64(mailboxID))
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
			store.EntityKindMailbox, uint64(mailboxID), 0, store.ChangeOpUpdated, now)
	})
}

// MoveMailbox implements store.Metadata.MoveMailbox: updates the
// parent_id column and appends a change-feed entry.
func (m *metadata) MoveMailbox(ctx context.Context, mailboxID store.MailboxID, newParentID store.MailboxID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM mailboxes WHERE id = ?`, int64(mailboxID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET parent_id = ?, updated_at_us = ? WHERE id = ?`,
			int64(newParentID), usMicros(now), int64(mailboxID))
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
			store.EntityKindMailbox, uint64(mailboxID), 0, store.ChangeOpUpdated, now)
	})
}

// SetMailboxSortOrder implements store.Metadata.SetMailboxSortOrder:
// updates sort_order and appends a change-feed entry.
func (m *metadata) SetMailboxSortOrder(ctx context.Context, mailboxID store.MailboxID, sortOrder uint32) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid int64
		if err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM mailboxes WHERE id = ?`, int64(mailboxID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET sort_order = ?, updated_at_us = ? WHERE id = ?`,
			int64(sortOrder), usMicros(now), int64(mailboxID))
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
			store.EntityKindMailbox, uint64(mailboxID), 0, store.ChangeOpUpdated, now)
	})
}

// SetMailboxColor implements REQ-PROTO-56 / REQ-STORE-34. The colour is
// validated against the "#RRGGBB" hex literal grammar before any SQL is
// emitted; nil clears the column so the JMAP "color is unset" semantic
// (clients render their own default) round-trips.
func (m *metadata) SetMailboxColor(ctx context.Context, mailboxID store.MailboxID, color *string) error {
	if color != nil && !validMailboxColor(*color) {
		return fmt.Errorf("color %q: %w", *color, store.ErrInvalidArgument)
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var v any
		if color != nil {
			v = *color
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET color_hex = ?, updated_at_us = ? WHERE id = ?`,
			v, usMicros(now), int64(mailboxID))
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

// validMailboxColor reports whether s matches the JMAP Mailbox.color
// grammar "#RRGGBB" (six hex digits, leading '#'). Lowercase + uppercase
// hex are both accepted; the value is stored verbatim so clients see
// what they wrote.
func validMailboxColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for i := 1; i < 7; i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// -- Sieve scripts ----------------------------------------------------

func (m *metadata) GetSieveScript(ctx context.Context, pid store.PrincipalID) (string, error) {
	var script string
	err := m.s.db.QueryRowContext(ctx,
		`SELECT script FROM sieve_scripts WHERE principal_id = ?`, int64(pid)).Scan(&script)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", mapErr(err)
	}
	return script, nil
}

func (m *metadata) SetSieveScript(ctx context.Context, pid store.PrincipalID, text string) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		if text == "" {
			_, err := tx.ExecContext(ctx,
				`DELETE FROM sieve_scripts WHERE principal_id = ?`, int64(pid))
			return mapErr(err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sieve_scripts (principal_id, script, updated_at_us)
			VALUES (?, ?, ?)
			ON CONFLICT(principal_id) DO UPDATE SET script = excluded.script, updated_at_us = excluded.updated_at_us`,
			int64(pid), text, usMicros(now))
		return mapErr(err)
	})
}

func (m *metadata) GetUserSieveScript(ctx context.Context, pid store.PrincipalID) (string, error) {
	var script string
	err := m.s.db.QueryRowContext(ctx,
		`SELECT COALESCE(user_script, '') FROM sieve_scripts WHERE principal_id = ?`, int64(pid)).Scan(&script)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", mapErr(err)
	}
	return script, nil
}

func (m *metadata) SetUserSieveScript(ctx context.Context, pid store.PrincipalID, text string) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Upsert the row. On insert we set user_script; on conflict we only
		// update user_script (the effective script column is managed
		// separately by recompileAndPersist via SetSieveScript).
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sieve_scripts (principal_id, script, user_script, updated_at_us)
			VALUES (?, '', ?, ?)
			ON CONFLICT(principal_id) DO UPDATE
			  SET user_script = excluded.user_script,
			      updated_at_us = excluded.updated_at_us`,
			int64(pid), text, usMicros(now))
		return mapErr(err)
	})
}

// -- JMAP snooze (REQ-PROTO-49) --------------------------------------

func (m *metadata) ListDueSnoozedMessages(ctx context.Context, now time.Time, limit int) ([]store.Message, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	// Join message_mailboxes to find snoozed rows. The $snoozed keyword
	// check enforces the atomicity invariant.
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT m.id, m.principal_id, m.blob_hash, m.blob_size, m.internal_date_us,
		       m.received_at_us, m.size, m.thread_id,
		       m.env_subject, m.env_from, m.env_to, m.env_cc, m.env_bcc, m.env_reply_to,
		       m.env_message_id, m.env_in_reply_to, m.env_date_us,
		       mm.mailbox_id, mm.uid, mm.modseq, mm.flags, mm.keywords_csv, mm.snoozed_until_us
		  FROM messages m
		  JOIN message_mailboxes mm ON mm.message_id = m.id
		 WHERE mm.snoozed_until_us IS NOT NULL
		   AND mm.snoozed_until_us <= ?
		   AND (',' || mm.keywords_csv || ',') LIKE '%,$snoozed,%'
		 ORDER BY mm.snoozed_until_us ASC LIMIT ?`,
		usMicros(now.UTC()), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (m *metadata) SetSnooze(ctx context.Context, msgID store.MessageID, mailboxID store.MailboxID, when *time.Time) (store.ModSeq, error) {
	now := m.s.clock.Now().UTC()
	var modseq store.ModSeq
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var curFlags int64
		var curKeywords string
		err := tx.QueryRowContext(ctx, `
			SELECT flags, keywords_csv
			  FROM message_mailboxes WHERE message_id = ? AND mailbox_id = ?`,
			int64(msgID), int64(mailboxID)).Scan(&curFlags, &curKeywords)
		if err != nil {
			return mapErr(err)
		}
		kwSet := map[string]struct{}{}
		if curKeywords != "" {
			for _, k := range strings.Split(curKeywords, ",") {
				if k != "" {
					kwSet[k] = struct{}{}
				}
			}
		}
		if when != nil {
			kwSet["$snoozed"] = struct{}{}
		} else {
			delete(kwSet, "$snoozed")
		}
		kws := make([]string, 0, len(kwSet))
		for k := range kwSet {
			kws = append(kws, k)
		}
		sortStrings(kws)

		var pid, highest int64
		if err := tx.QueryRowContext(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = ?`,
			int64(mailboxID)).Scan(&pid, &highest); err != nil {
			return mapErr(err)
		}
		modseq = store.ModSeq(highest + 1)

		var snoozedArg any
		if when != nil {
			snoozedArg = usMicros(when.UTC())
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE message_mailboxes
			   SET keywords_csv = ?, modseq = ?, snoozed_until_us = ?
			 WHERE message_id = ? AND mailbox_id = ?`,
			strings.Join(kws, ","), int64(modseq), snoozedArg,
			int64(msgID), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailboxes SET highest_modseq = ?, updated_at_us = ? WHERE id = ?`,
			int64(modseq), usMicros(now), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(msgID), uint64(mailboxID), store.ChangeOpUpdated, now)
	})
	if err != nil {
		return 0, err
	}
	return modseq, nil
}

func (m *metadata) ListAuditLog(ctx context.Context, filter store.AuditLogFilter) ([]store.AuditLogEntry, error) {
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
	if filter.PrincipalID != 0 {
		where = append(where, "principal_id = ?")
		args = append(args, int64(filter.PrincipalID))
	}
	if filter.Action != "" {
		where = append(where, "action = ?")
		args = append(args, filter.Action)
	}
	if !filter.Since.IsZero() {
		where = append(where, "at_us >= ?")
		args = append(args, usMicros(filter.Since))
	}
	if !filter.Until.IsZero() {
		where = append(where, "at_us < ?")
		args = append(args, usMicros(filter.Until))
	}
	q := `SELECT id, at_us, actor_kind, actor_id, action, subject,
	             remote_addr, outcome, message, metadata_json
	        FROM audit_log`
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
	var out []store.AuditLogEntry
	for rows.Next() {
		var e store.AuditLogEntry
		var id, atUs int64
		var actorKind, outcome int64
		var metaJSON string
		if err := rows.Scan(&id, &atUs, &actorKind, &e.ActorID, &e.Action, &e.Subject,
			&e.RemoteAddr, &outcome, &e.Message, &metaJSON); err != nil {
			return nil, mapErr(err)
		}
		e.ID = store.AuditLogID(id)
		e.At = fromMicros(atUs)
		e.ActorKind = store.ActorKind(actorKind)
		e.Outcome = store.AuditOutcome(outcome)
		md, err := decodeAuditMetadata(metaJSON)
		if err != nil {
			return nil, err
		}
		e.Metadata = md
		out = append(out, e)
	}
	return out, rows.Err()
}
