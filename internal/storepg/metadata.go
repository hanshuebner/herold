package storepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// metadata implements store.Metadata against Postgres via pgxpool.
type metadata struct {
	s *Store
}

// mapErr converts a pgx / pgconn error into the public sentinel
// vocabulary. 23505 (unique_violation) -> ErrConflict; no-rows ->
// ErrNotFound; 23503 (foreign_key_violation) -> ErrConflict (wrapping).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return store.ErrConflict
		case "23503":
			return fmt.Errorf("foreign key violation: %w", store.ErrConflict)
		}
	}
	return err
}

func usMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

func fromMicros(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

func (m *metadata) runTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	m.s.writerMu.Lock()
	defer m.s.writerMu.Unlock()
	tx, err := m.s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("storepg: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storepg: commit: %w", err)
	}
	return nil
}

// -- principals -------------------------------------------------------

func (m *metadata) InsertPrincipal(ctx context.Context, p store.Principal) (store.Principal, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO principals (kind, canonical_email, display_name, password_hash,
			  totp_secret, quota_bytes, flags, used_bytes, created_at_us, updated_at_us,
			  clientlog_telemetry_enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, $9, $10) RETURNING id`,
			int32(p.Kind), strings.ToLower(p.CanonicalEmail), p.DisplayName, p.PasswordHash,
			p.TOTPSecret, p.QuotaBytes, int64(p.Flags), usMicros(now), usMicros(now),
			p.ClientlogTelemetryEnabled,
		).Scan(&id); err != nil {
			return fmt.Errorf("principal %q: %w", strings.ToLower(p.CanonicalEmail), mapErr(err))
		}
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
	return m.selectPrincipal(ctx, `WHERE id = $1`, int64(id))
}

func (m *metadata) GetPrincipalByEmail(ctx context.Context, email string) (store.Principal, error) {
	return m.selectPrincipal(ctx, `WHERE canonical_email = $1`, strings.ToLower(email))
}

func (m *metadata) selectPrincipal(ctx context.Context, where string, args ...any) (store.Principal, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, seen_addresses_enabled,
		       avatar_blob_hash, avatar_blob_size, xface_enabled,
		       clientlog_telemetry_enabled,
		       created_at_us, updated_at_us
		  FROM principals `+where, args...)
	var p store.Principal
	var kind int32
	var flags int64
	var avatarHash *string
	var createdUs, updatedUs int64
	var totp []byte
	var id int64
	err := row.Scan(&id, &kind, &p.CanonicalEmail, &p.DisplayName, &p.PasswordHash,
		&totp, &p.QuotaBytes, &flags, &p.SeenAddressesEnabled,
		&avatarHash, &p.AvatarBlobSize, &p.XFaceEnabled,
		&p.ClientlogTelemetryEnabled,
		&createdUs, &updatedUs)
	if err != nil {
		return store.Principal{}, mapErr(err)
	}
	p.ID = store.PrincipalID(id)
	p.Kind = store.PrincipalKind(kind)
	p.Flags = store.PrincipalFlags(flags)
	if avatarHash != nil {
		p.AvatarBlobHash = *avatarHash
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE principals
			   SET kind = $1, canonical_email = $2, display_name = $3, password_hash = $4,
			       totp_secret = $5, quota_bytes = $6, flags = $7,
			       seen_addresses_enabled = $8,
			       avatar_blob_hash = $9, avatar_blob_size = $10, xface_enabled = $11,
			       clientlog_telemetry_enabled = $12,
			       updated_at_us = $13
			 WHERE id = $14`,
			int32(p.Kind), strings.ToLower(p.CanonicalEmail), p.DisplayName, p.PasswordHash,
			p.TOTPSecret, p.QuotaBytes, int64(p.Flags), p.SeenAddressesEnabled,
			avatarHash, p.AvatarBlobSize, p.XFaceEnabled,
			p.ClientlogTelemetryEnabled,
			usMicros(now), int64(p.ID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- domains ----------------------------------------------------------

func (m *metadata) InsertDomain(ctx context.Context, d store.Domain) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO domains (name, is_local, created_at_us) VALUES ($1, $2, $3)`,
			strings.ToLower(d.Name), d.IsLocal, usMicros(now))
		if err != nil {
			return fmt.Errorf("domain %q: %w", strings.ToLower(d.Name), mapErr(err))
		}
		return nil
	})
}

func (m *metadata) GetDomain(ctx context.Context, name string) (store.Domain, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT name, is_local, created_at_us FROM domains WHERE name = $1`,
		strings.ToLower(name))
	var d store.Domain
	var createdUs int64
	err := row.Scan(&d.Name, &d.IsLocal, &createdUs)
	if err != nil {
		return store.Domain{}, mapErr(err)
	}
	d.CreatedAt = fromMicros(createdUs)
	return d, nil
}

func (m *metadata) ListLocalDomains(ctx context.Context) ([]store.Domain, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT name, is_local, created_at_us FROM domains
		 WHERE is_local = TRUE ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Domain
	for rows.Next() {
		var d store.Domain
		var createdUs int64
		if err := rows.Scan(&d.Name, &d.IsLocal, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		d.CreatedAt = fromMicros(createdUs)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteDomain(ctx context.Context, name string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM domains WHERE name = $1`,
			strings.ToLower(name))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO aliases (local_part, domain, target_principal, expires_at_us, created_at_us)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			strings.ToLower(a.LocalPart), strings.ToLower(a.Domain),
			int64(a.TargetPrincipal), expiresUs, usMicros(now)).Scan(&id); err != nil {
			return fmt.Errorf("alias %s@%s: %w", strings.ToLower(a.LocalPart), strings.ToLower(a.Domain), mapErr(err))
		}
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
	err := m.s.pool.QueryRow(ctx, `
		SELECT target_principal FROM aliases
		 WHERE local_part = $1 AND domain = $2
		   AND (expires_at_us IS NULL OR expires_at_us > $3)`,
		lp, dom, now).Scan(&target)
	if err == nil {
		return store.PrincipalID(target), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, mapErr(err)
	}
	var pid int64
	err = m.s.pool.QueryRow(ctx, `
		SELECT id FROM principals WHERE canonical_email = $1`, lp+"@"+dom).Scan(&pid)
	if err != nil {
		return 0, mapErr(err)
	}
	return store.PrincipalID(pid), nil
}

func (m *metadata) ListAliases(ctx context.Context, domain string) ([]store.Alias, error) {
	dom := strings.ToLower(strings.TrimSpace(domain))
	var (
		rows pgx.Rows
		err  error
	)
	if dom == "" {
		rows, err = m.s.pool.Query(ctx, `
			SELECT id, local_part, domain, target_principal, expires_at_us, created_at_us
			  FROM aliases ORDER BY domain, local_part`)
	} else {
		rows, err = m.s.pool.Query(ctx, `
			SELECT id, local_part, domain, target_principal, expires_at_us, created_at_us
			  FROM aliases WHERE domain = $1 ORDER BY local_part`, dom)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.Alias, 0)
	for rows.Next() {
		var a store.Alias
		var id, target int64
		var expires *int64
		var createdUs int64
		if err := rows.Scan(&id, &a.LocalPart, &a.Domain, &target, &expires, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		a.ID = store.AliasID(id)
		a.TargetPrincipal = store.PrincipalID(target)
		if expires != nil && *expires != 0 {
			t := fromMicros(*expires)
			a.ExpiresAt = &t
		}
		a.CreatedAt = fromMicros(createdUs)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteAlias(ctx context.Context, id store.AliasID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM aliases WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- OIDC -------------------------------------------------------------

func (m *metadata) InsertOIDCProvider(ctx context.Context, p store.OIDCProvider) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO oidc_providers (name, issuer_url, client_id, client_secret_ref,
			  scopes_csv, auto_provision, created_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			p.Name, p.IssuerURL, p.ClientID, p.ClientSecretRef,
			strings.Join(p.Scopes, ","), p.AutoProvision, usMicros(now))
		if err != nil {
			return fmt.Errorf("OIDC provider %q: %w", p.Name, mapErr(err))
		}
		return nil
	})
}

func (m *metadata) GetOIDCProvider(ctx context.Context, name string) (store.OIDCProvider, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT name, issuer_url, client_id, client_secret_ref, scopes_csv,
		       auto_provision, created_at_us
		  FROM oidc_providers WHERE name = $1`, name)
	var p store.OIDCProvider
	var scopes string
	var createdUs int64
	err := row.Scan(&p.Name, &p.IssuerURL, &p.ClientID, &p.ClientSecretRef,
		&scopes, &p.AutoProvision, &createdUs)
	if err != nil {
		return store.OIDCProvider{}, mapErr(err)
	}
	if scopes != "" {
		p.Scopes = strings.Split(scopes, ",")
	}
	p.CreatedAt = fromMicros(createdUs)
	return p, nil
}

func (m *metadata) LinkOIDC(ctx context.Context, link store.OIDCLink) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO oidc_links (principal_id, provider_name, subject,
			  email_at_provider, linked_at_us)
			VALUES ($1, $2, $3, $4, $5)`,
			int64(link.PrincipalID), link.ProviderName, link.Subject,
			link.EmailAtProvider, usMicros(now))
		if err != nil {
			return fmt.Errorf("OIDC link principal %d provider %q subject %q: %w", link.PrincipalID, link.ProviderName, link.Subject, mapErr(err))
		}
		return nil
	})
}

func (m *metadata) LookupOIDCLink(ctx context.Context, provider, subject string) (store.OIDCLink, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT principal_id, provider_name, subject, email_at_provider, linked_at_us
		  FROM oidc_links WHERE provider_name = $1 AND subject = $2`, provider, subject)
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
		scope = `["admin"]`
	}
	addrJSON := encodeStringSliceJSON(k.AllowedFromAddresses)
	domJSON := encodeStringSliceJSON(k.AllowedFromDomains)
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO api_keys (principal_id, hash, name, created_at_us, last_used_at_us,
			                      scope_json, allowed_from_addresses_json, allowed_from_domains_json)
			VALUES ($1, $2, $3, $4, 0, $5, $6, $7) RETURNING id`,
			int64(k.PrincipalID), k.Hash, k.Name, usMicros(now), scope, addrJSON, domJSON).Scan(&id); err != nil {
			return fmt.Errorf("API key %q: %w", k.Name, mapErr(err))
		}
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
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, principal_id, hash, name, created_at_us, last_used_at_us,
		       scope_json, allowed_from_addresses_json, allowed_from_domains_json
		  FROM api_keys WHERE hash = $1`, hash)
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE api_keys SET last_used_at_us = $1 WHERE id = $2`,
			usMicros(at), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListAPIKeysByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.APIKey, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, principal_id, hash, name, created_at_us, last_used_at_us,
		       scope_json, allowed_from_addresses_json, allowed_from_domains_json
		  FROM api_keys WHERE principal_id = $1 ORDER BY id`, int64(pid))
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListOIDCLinksByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.OIDCLink, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT principal_id, provider_name, subject, email_at_provider, linked_at_us
		  FROM oidc_links WHERE principal_id = $1 ORDER BY provider_name`, int64(pid))
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var color any
		if mb.Color != nil {
			color = *mb.Color
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO mailboxes (principal_id, parent_id, name, attributes, uidvalidity,
			  uidnext, highest_modseq, created_at_us, updated_at_us, color_hex, sort_order)
			VALUES ($1, $2, $3, $4, $5, 1, 0, $6, $7, $8, $9) RETURNING id`,
			int64(mb.PrincipalID), int64(mb.ParentID), mb.Name, int64(mb.Attributes),
			int64(mb.UIDValidity), usMicros(now), usMicros(now), color, int64(mb.SortOrder)).Scan(&id); err != nil {
			return fmt.Errorf("mailbox %q: %w", mb.Name, mapErr(err))
		}
		return appendStateChange(ctx, tx, mb.PrincipalID,
			store.EntityKindMailbox, uint64(id), 0, store.ChangeOpCreated, now)
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
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, principal_id, parent_id, name, attributes, uidvalidity, uidnext,
		       highest_modseq, created_at_us, updated_at_us, color_hex, sort_order
		  FROM mailboxes WHERE id = $1`, int64(id))
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
	var color *string
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
	if color != nil {
		v := *color
		mb.Color = &v
	}
	return mb, nil
}

func (m *metadata) ListMailboxes(ctx context.Context, principalID store.PrincipalID) ([]store.Mailbox, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, principal_id, parent_id, name, attributes, uidvalidity, uidnext,
		       highest_modseq, created_at_us, updated_at_us, color_hex, sort_order
		  FROM mailboxes WHERE principal_id = $1 ORDER BY name`, int64(principalID))
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
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		err := tx.QueryRow(ctx, `SELECT principal_id FROM mailboxes WHERE id = $1`, int64(id)).Scan(&pid)
		if err != nil {
			return mapErr(err)
		}
		// Gather messages that would have no remaining membership after
		// we remove all message_mailboxes rows for this mailbox.
		orphanRows, err := tx.Query(ctx, `
			SELECT m.id, m.blob_hash, m.size
			  FROM messages m
			  JOIN message_mailboxes mm ON mm.message_id = m.id AND mm.mailbox_id = $1
			 WHERE (SELECT COUNT(*) FROM message_mailboxes mm2
			         WHERE mm2.message_id = m.id) = 1`,
			int64(id))
		if err != nil {
			return mapErr(err)
		}
		type orphan struct {
			id   int64
			hash string
			size int64
		}
		var orphans []orphan
		for orphanRows.Next() {
			var o orphan
			if err := orphanRows.Scan(&o.id, &o.hash, &o.size); err != nil {
				orphanRows.Close()
				return mapErr(err)
			}
			orphans = append(orphans, o)
		}
		orphanRows.Close()
		// Delete orphaned messages rows (cascade removes message_mailboxes).
		for _, o := range orphans {
			if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE id = $1`, o.id); err != nil {
				return mapErr(err)
			}
			if err := decRef(ctx, tx, o.hash, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE principals SET used_bytes = used_bytes - $1, updated_at_us = $2 WHERE id = $3`,
				o.size, usMicros(now), pid); err != nil {
				return mapErr(err)
			}
		}
		// Remove remaining message_mailboxes rows (multi-mailbox messages
		// still live in other mailboxes — only the membership is removed).
		if _, err := tx.Exec(ctx,
			`DELETE FROM message_mailboxes WHERE mailbox_id = $1`, int64(id)); err != nil {
			return mapErr(err)
		}
		res, err := tx.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindMailbox, uint64(id), 0, store.ChangeOpDestroyed, now)
	})
}

// -- messages ---------------------------------------------------------

func (m *metadata) InsertMessage(ctx context.Context, msg store.Message, targets []store.MessageMailbox) (store.UID, store.ModSeq, error) {
	if len(targets) == 0 {
		return 0, 0, fmt.Errorf("storepg: InsertMessage: targets must not be empty")
	}
	now := m.s.clock.Now().UTC()
	var firstUID store.UID
	var firstModSeq store.ModSeq
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		// Resolve principal: caller should set msg.PrincipalID; fall back
		// to the first target's mailbox owner for backward compat.
		pid := int64(msg.PrincipalID)
		if pid == 0 {
			if err := tx.QueryRow(ctx, `SELECT principal_id FROM mailboxes WHERE id = $1`,
				int64(targets[0].MailboxID)).Scan(&pid); err != nil {
				return mapErr(err)
			}
		}
		var quota, used int64
		if err := tx.QueryRow(ctx, `
			SELECT quota_bytes, used_bytes FROM principals WHERE id = $1`, pid).Scan(&quota, &used); err != nil {
			return mapErr(err)
		}
		if quota > 0 && used+msg.Size > quota {
			return store.ErrQuotaExceeded
		}
		// Normalise env_message_id.
		if msg.Envelope.MessageID != "" {
			msg.Envelope.MessageID = mailparse.NormalizeMessageID(msg.Envelope.MessageID)
		}
		// Thread resolution using principal_id directly (post-migration 0024).
		if msg.ThreadID == 0 && msg.Envelope.InReplyTo != "" {
			refs := mailparse.ParseReferences(msg.Envelope.InReplyTo)
			for _, ref := range refs {
				var ancestorID, ancestorThread int64
				lookupErr := tx.QueryRow(ctx, `
					SELECT m.id, m.thread_id
					  FROM messages m
					 WHERE m.principal_id = $1
					   AND m.env_message_id = $2
					 LIMIT 1`,
					pid, ref).Scan(&ancestorID, &ancestorThread)
				if errors.Is(lookupErr, pgx.ErrNoRows) {
					continue
				}
				if lookupErr != nil {
					return fmt.Errorf("storepg: thread lookup: %w", lookupErr)
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
		var mid int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO messages (principal_id,
			  internal_date_us, received_at_us, size, blob_hash, blob_size, thread_id,
			  env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
			  env_message_id, env_in_reply_to, env_date_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			RETURNING id`,
			pid,
			usMicros(msg.InternalDate), usMicros(msg.ReceivedAt), msg.Size,
			msg.Blob.Hash, msg.Blob.Size, int64(msg.ThreadID),
			msg.Envelope.Subject, msg.Envelope.From, msg.Envelope.To,
			msg.Envelope.Cc, msg.Envelope.Bcc, msg.Envelope.ReplyTo,
			msg.Envelope.MessageID, msg.Envelope.InReplyTo, usMicros(msg.Envelope.Date),
		).Scan(&mid); err != nil {
			return mapErr(err)
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
			if err := tx.QueryRow(ctx,
				`SELECT uidnext, highest_modseq FROM mailboxes WHERE id = $1`,
				int64(t.MailboxID)).Scan(&uidNext, &highest); err != nil {
				return mapErr(err)
			}
			allocUID := store.UID(uidNext)
			allocModSeq := store.ModSeq(highest + 1)
			var snoozedArg any
			if t.SnoozedUntil != nil {
				snoozedArg = usMicros(*t.SnoozedUntil)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO message_mailboxes
				  (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				mid, int64(t.MailboxID), int64(allocUID), int64(allocModSeq),
				int64(t.Flags), strings.Join(t.Keywords, ","), snoozedArg); err != nil {
				return mapErr(err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = $1, updated_at_us = $2
				 WHERE id = $3`, int64(allocModSeq), usMicros(now), int64(t.MailboxID)); err != nil {
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
		if _, err := tx.Exec(ctx, `
			UPDATE principals SET used_bytes = used_bytes + $1, updated_at_us = $2 WHERE id = $3`,
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid, oldSize int64
		var oldHash string
		err := tx.QueryRow(ctx, `
			SELECT principal_id, blob_hash, size FROM messages WHERE id = $1`,
			int64(id)).Scan(&pid, &oldHash, &oldSize)
		if err != nil {
			return mapErr(err)
		}
		if delta := size - oldSize; delta > 0 {
			var quota, used int64
			if err := tx.QueryRow(ctx,
				`SELECT quota_bytes, used_bytes FROM principals WHERE id = $1`, pid).Scan(&quota, &used); err != nil {
				return mapErr(err)
			}
			if quota > 0 && used+delta > quota {
				return store.ErrQuotaExceeded
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE messages
			   SET blob_hash = $1, blob_size = $2, size = $3,
			       env_subject = $4, env_from = $5, env_to = $6, env_cc = $7, env_bcc = $8, env_reply_to = $9,
			       env_message_id = $10, env_in_reply_to = $11, env_date_us = $12
			 WHERE id = $13`,
			ref.Hash, ref.Size, size,
			env.Subject, env.From, env.To, env.Cc, env.Bcc, env.ReplyTo,
			env.MessageID, env.InReplyTo, usMicros(env.Date),
			int64(id),
		); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE principals SET used_bytes = used_bytes + $1, updated_at_us = $2 WHERE id = $3`,
			size-oldSize, usMicros(now), pid); err != nil {
			return mapErr(err)
		}
		if oldHash != ref.Hash {
			if err := decRef(ctx, tx, oldHash, now); err != nil {
				return err
			}
			if err := incRef(ctx, tx, ref.Hash, ref.Size, now); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx,
			`SELECT mailbox_id FROM message_mailboxes WHERE message_id = $1`, int64(id))
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
			if err := tx.QueryRow(ctx,
				`SELECT highest_modseq FROM mailboxes WHERE id = $1`, mb).Scan(&highest); err != nil {
				return mapErr(err)
			}
			newModSeq := highest + 1
			if _, err := tx.Exec(ctx, `
				UPDATE message_mailboxes SET modseq = $1 WHERE message_id = $2 AND mailbox_id = $3`,
				newModSeq, int64(id), mb); err != nil {
				return mapErr(err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE mailboxes SET highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
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
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, principal_id, internal_date_us, received_at_us, size,
		       blob_hash, blob_size, thread_id,
		       env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
		       env_message_id, env_in_reply_to, env_date_us
		  FROM messages WHERE id = $1`, int64(id))
	msg, err := scanMessageRow(row)
	if err != nil {
		return store.Message{}, err
	}
	if err := m.loadMailboxes(ctx, &msg, 0); err != nil {
		return store.Message{}, err
	}
	return msg, nil
}

// scanMessageRow scans the mailbox-independent columns from the messages table.
func scanMessageRow(row rowLike) (store.Message, error) {
	var msg store.Message
	var id, pid int64
	var idUs, rcvUs int64
	var blobSize int64
	var thread int64
	var envDateUs int64
	err := row.Scan(&id, &pid, &idUs, &rcvUs,
		&msg.Size, &msg.Blob.Hash, &blobSize, &thread,
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

// loadMailboxes queries message_mailboxes for the given message and
// populates msg.Mailboxes plus the convenience fields. If mailboxID is
// non-zero the convenience fields are populated from the matching entry;
// otherwise the first entry is used.
func (m *metadata) loadMailboxes(ctx context.Context, msg *store.Message, mailboxID store.MailboxID) error {
	rows, err := m.s.pool.Query(ctx, `
		SELECT message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us
		  FROM message_mailboxes
		 WHERE message_id = $1
		 ORDER BY mailbox_id`, int64(msg.ID))
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var mid, mbox, uid, modseq, flags int64
		var keywords string
		var snoozedUs *int64
		if err := rows.Scan(&mid, &mbox, &uid, &modseq, &flags, &keywords, &snoozedUs); err != nil {
			return mapErr(err)
		}
		mm := store.MessageMailbox{
			MessageID: store.MessageID(mid),
			MailboxID: store.MailboxID(mbox),
			UID:       store.UID(uid),
			ModSeq:    store.ModSeq(modseq),
			Flags:     store.MessageFlags(flags),
		}
		if keywords != "" {
			mm.Keywords = strings.Split(keywords, ",")
		}
		if snoozedUs != nil {
			t := fromMicros(*snoozedUs)
			mm.SnoozedUntil = &t
		}
		msg.Mailboxes = append(msg.Mailboxes, mm)
	}
	if err := rows.Err(); err != nil {
		return mapErr(err)
	}
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

// scanMessage scans a joined (messages JOIN message_mailboxes) row used
// by ListMessages and ListDueSnoozedMessages.
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
	var snoozedUs *int64
	err := row.Scan(
		&id, &pid, &idUs, &rcvUs,
		&msg.Size, &msg.Blob.Hash, &blobSize, &thread,
		&msg.Envelope.Subject, &msg.Envelope.From, &msg.Envelope.To,
		&msg.Envelope.Cc, &msg.Envelope.Bcc, &msg.Envelope.ReplyTo,
		&msg.Envelope.MessageID, &msg.Envelope.InReplyTo, &envDateUs,
		// message_mailboxes columns
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
	if snoozedUs != nil {
		t := fromMicros(*snoozedUs)
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var curFlags int64
		var curKeywords string
		var curModSeq int64
		err := tx.QueryRow(ctx, `
			SELECT flags, keywords_csv, modseq
			  FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
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
				if k != "" {
					kwSet[k] = struct{}{}
				}
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

		var pid, highest int64
		err = tx.QueryRow(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = $1`,
			int64(mailboxID)).Scan(&pid, &highest)
		if err != nil {
			return mapErr(err)
		}
		modseq = store.ModSeq(highest + 1)

		if _, err := tx.Exec(ctx, `
			UPDATE message_mailboxes SET flags = $1, keywords_csv = $2, modseq = $3
			 WHERE message_id = $4 AND mailbox_id = $5`,
			int64(newFlags), strings.Join(kws, ","), int64(modseq),
			int64(id), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid, highest int64
		err := tx.QueryRow(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = $1`,
			int64(mailboxID)).Scan(&pid, &highest)
		if err != nil {
			return mapErr(err)
		}
		var removed int
		for _, id := range ids {
			// Check membership exists.
			var memberCount int64
			if err := tx.QueryRow(ctx,
				`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
				int64(id), int64(mailboxID)).Scan(&memberCount); err != nil {
				return mapErr(err)
			}
			if memberCount == 0 {
				continue // silently skip
			}
			// Delete this membership.
			if _, err := tx.Exec(ctx,
				`DELETE FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
				int64(id), int64(mailboxID)); err != nil {
				return mapErr(err)
			}
			// Check remaining memberships.
			var remaining int64
			if err := tx.QueryRow(ctx,
				`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = $1`, int64(id)).Scan(&remaining); err != nil {
				return mapErr(err)
			}
			if remaining == 0 {
				// Last membership: delete messages row + dec refcount.
				var size int64
				var hash string
				if err := tx.QueryRow(ctx,
					`SELECT size, blob_hash FROM messages WHERE id = $1`, int64(id)).Scan(&size, &hash); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return mapErr(err)
				}
				if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE id = $1`, int64(id)); err != nil {
					return mapErr(err)
				}
				if hash != "" {
					if err := decRef(ctx, tx, hash, now); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx,
						`UPDATE principals SET used_bytes = used_bytes - $1, updated_at_us = $2 WHERE id = $3`,
						size, usMicros(now), pid); err != nil {
						return mapErr(err)
					}
				}
			}
			highest++
			if _, err := tx.Exec(ctx,
				`UPDATE mailboxes SET highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
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

func (m *metadata) UpdateMessageThreadID(ctx context.Context, msgID store.MessageID, threadID uint64) error {
	_, err := m.s.pool.Exec(ctx,
		`UPDATE messages SET thread_id = $1 WHERE id = $2`,
		int64(threadID), int64(msgID))
	return mapErr(err)
}

// AddMessageToMailbox adds an existing message to mailboxID.
func (m *metadata) AddMessageToMailbox(ctx context.Context, msgID store.MessageID, mailboxID store.MailboxID) (store.UID, store.ModSeq, error) {
	now := m.s.clock.Now().UTC()
	var allocUID store.UID
	var allocModSeq store.ModSeq
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM messages WHERE id = $1`, int64(msgID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		var existing int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
			int64(msgID), int64(mailboxID)).Scan(&existing); err != nil {
			return mapErr(err)
		}
		if existing > 0 {
			return fmt.Errorf("message already in mailbox: %w", store.ErrConflict)
		}
		var uidNext, highest, attrs int64
		if err := tx.QueryRow(ctx,
			`SELECT uidnext, highest_modseq, attributes FROM mailboxes WHERE id = $1`, int64(mailboxID)).Scan(&uidNext, &highest, &attrs); err != nil {
			return mapErr(err)
		}
		// Snapshot pre-trash mailboxes if the target is Trash.
		if store.MailboxAttributes(attrs)&store.MailboxAttrTrash != 0 {
			if err := pgSnapshotPretrashMailboxes(ctx, tx, int64(msgID), int64(mailboxID)); err != nil {
				return err
			}
		}
		allocUID = store.UID(uidNext)
		allocModSeq = store.ModSeq(highest + 1)
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_mailboxes (message_id, mailbox_id, uid, modseq, flags, keywords_csv)
			VALUES ($1, $2, $3, $4, 0, '')`,
			int64(msgID), int64(mailboxID), int64(allocUID), int64(allocModSeq)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
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

// RemoveMessageFromMailbox removes the (msgID, mailboxID) membership row.
func (m *metadata) RemoveMessageFromMailbox(ctx context.Context, msgID store.MessageID, mailboxID store.MailboxID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var exists int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
			int64(msgID), int64(mailboxID)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		if exists == 0 {
			return store.ErrNotFound
		}
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM messages WHERE id = $1`, int64(msgID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		// Check if we're removing from Trash — if so, replay pre-trash mailboxes.
		var attrs int64
		if err := tx.QueryRow(ctx, `SELECT attributes FROM mailboxes WHERE id = $1`,
			int64(mailboxID)).Scan(&attrs); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return mapErr(err)
		}
		isTrash := store.MailboxAttributes(attrs)&store.MailboxAttrTrash != 0
		if _, err := tx.Exec(ctx,
			`DELETE FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
			int64(msgID), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		if isTrash {
			if err := pgReplayPretrashMailboxes(ctx, tx, int64(msgID), now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET highest_modseq = highest_modseq + 1, updated_at_us = $1 WHERE id = $2`,
			usMicros(now), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		var remaining int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = $1`, int64(msgID)).Scan(&remaining); err != nil {
			return mapErr(err)
		}
		if remaining == 0 {
			var size int64
			var hash string
			if err := tx.QueryRow(ctx,
				`SELECT size, blob_hash FROM messages WHERE id = $1`, int64(msgID)).Scan(&size, &hash); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return mapErr(err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE id = $1`, int64(msgID)); err != nil {
				return mapErr(err)
			}
			if hash != "" {
				if err := decRef(ctx, tx, hash, now); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx,
					`UPDATE principals SET used_bytes = used_bytes - $1, updated_at_us = $2 WHERE id = $3`,
					size, usMicros(now), pid); err != nil {
					return mapErr(err)
				}
			}
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(msgID), uint64(mailboxID), store.ChangeOpDestroyed, now)
	})
}

func (m *metadata) MoveMessage(ctx context.Context, msgID store.MessageID, fromMailboxID, targetMailboxID store.MailboxID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		// Verify source membership exists.
		var srcFlags int64
		var srcKeywords string
		var snoozedUs *int64
		err := tx.QueryRow(ctx, `
			SELECT flags, keywords_csv, snoozed_until_us
			  FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
			int64(msgID), int64(fromMailboxID)).Scan(&srcFlags, &srcKeywords, &snoozedUs)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return mapErr(err)
		}

		var pid, uidNext, tgtHighest, tgtAttrs int64
		err = tx.QueryRow(ctx, `SELECT principal_id, uidnext, highest_modseq, attributes FROM mailboxes WHERE id = $1`,
			int64(targetMailboxID)).Scan(&pid, &uidNext, &tgtHighest, &tgtAttrs)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return mapErr(err)
		}

		newUID := uidNext
		newModseq := tgtHighest + 1

		// Snapshot pre-trash mailboxes if the target is Trash.
		isToTrash := store.MailboxAttributes(tgtAttrs)&store.MailboxAttrTrash != 0
		if isToTrash {
			if err := pgSnapshotPretrashMailboxes(ctx, tx, int64(msgID), int64(targetMailboxID)); err != nil {
				return err
			}
		}

		var snoozedArg any
		if snoozedUs != nil {
			snoozedArg = *snoozedUs
		}
		// Insert new membership in target.
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_mailboxes
			  (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			int64(msgID), int64(targetMailboxID), newUID, newModseq,
			srcFlags, srcKeywords, snoozedArg); err != nil {
			return mapErr(err)
		}
		// Delete source membership.
		if _, err := tx.Exec(ctx,
			`DELETE FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
			int64(msgID), int64(fromMailboxID)); err != nil {
			return mapErr(err)
		}

		// Replay pre-trash mailboxes if the source is Trash (i.e. restoring).
		var srcAttrs int64
		if err := tx.QueryRow(ctx, `SELECT attributes FROM mailboxes WHERE id = $1`,
			int64(fromMailboxID)).Scan(&srcAttrs); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return mapErr(err)
		}
		isFromTrash := store.MailboxAttributes(srcAttrs)&store.MailboxAttrTrash != 0
		if isFromTrash {
			if err := pgReplayPretrashMailboxes(ctx, tx, int64(msgID), now); err != nil {
				return err
			}
		}

		// Advance target mailbox uidnext + highest_modseq.
		if _, err := tx.Exec(ctx,
			`UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
			newModseq, usMicros(now), int64(targetMailboxID)); err != nil {
			return mapErr(err)
		}
		// Advance source mailbox highest_modseq.
		if _, err := tx.Exec(ctx,
			`UPDATE mailboxes SET highest_modseq = highest_modseq + 1, updated_at_us = $1 WHERE id = $2`,
			usMicros(now), int64(fromMailboxID)); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(msgID), uint64(targetMailboxID), store.ChangeOpUpdated, now)
	})
}

// pgSnapshotPretrashMailboxes records the current non-Trash mailbox memberships
// of msgID in email_pretrash_mailboxes, replacing any prior snapshot.
// trashMailboxID is the Trash mailbox being added; it is excluded from the
// snapshot so replay does not re-add Trash.
func pgSnapshotPretrashMailboxes(ctx context.Context, tx pgx.Tx, msgID, trashMailboxID int64) error {
	// Clear any prior snapshot.
	if _, err := tx.Exec(ctx,
		`DELETE FROM email_pretrash_mailboxes WHERE email_id = $1`, msgID); err != nil {
		return err
	}
	// Drain the SELECT into a slice before issuing INSERTs: pgx pins the
	// connection while a Rows cursor is open, and tx.Exec on the same tx
	// would fail with "conn busy" until the cursor is closed.
	rows, err := tx.Query(ctx, `
		SELECT mm.mailbox_id
		  FROM message_mailboxes mm
		  JOIN mailboxes mb ON mb.id = mm.mailbox_id
		 WHERE mm.message_id = $1
		   AND (mb.attributes & $2) = 0
		   AND mm.mailbox_id != $3`,
		msgID, int64(store.MailboxAttrTrash), trashMailboxID)
	if err != nil {
		return err
	}
	var mailboxIDs []int64
	for rows.Next() {
		var mbID int64
		if err := rows.Scan(&mbID); err != nil {
			rows.Close()
			return err
		}
		mailboxIDs = append(mailboxIDs, mbID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, mbID := range mailboxIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO email_pretrash_mailboxes (email_id, mailbox_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			msgID, mbID); err != nil {
			return err
		}
	}
	return nil
}

// pgReplayPretrashMailboxes restores the pre-trash mailbox memberships for msgID
// that were recorded by pgSnapshotPretrashMailboxes. Each mailbox is re-added if
// the mailbox still exists and the message is not already a member. The snapshot
// is deleted after replay.
func pgReplayPretrashMailboxes(ctx context.Context, tx pgx.Tx, msgID int64, now time.Time) error {
	rows, err := tx.Query(ctx,
		`SELECT mailbox_id FROM email_pretrash_mailboxes WHERE email_id = $1`, msgID)
	if err != nil {
		return err
	}
	var mailboxIDs []int64
	for rows.Next() {
		var mbID int64
		if err := rows.Scan(&mbID); err != nil {
			rows.Close()
			return err
		}
		mailboxIDs = append(mailboxIDs, mbID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, mbID := range mailboxIDs {
		var uidNext, highest int64
		err := tx.QueryRow(ctx,
			`SELECT uidnext, highest_modseq FROM mailboxes WHERE id = $1`, mbID).
			Scan(&uidNext, &highest)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // mailbox was deleted; skip
		}
		if err != nil {
			return err
		}
		var cnt int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
			msgID, mbID).Scan(&cnt); err != nil {
			return err
		}
		if cnt > 0 {
			continue // already a member; skip
		}
		newUID := uidNext
		newModSeq := highest + 1
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_mailboxes (message_id, mailbox_id, uid, modseq, flags, keywords_csv)
			VALUES ($1, $2, $3, $4, 0, '')`,
			msgID, mbID, newUID, newModSeq); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
			newModSeq, usMicros(now), mbID); err != nil {
			return err
		}
	}
	// Clear the snapshot after replay.
	if _, err := tx.Exec(ctx,
		`DELETE FROM email_pretrash_mailboxes WHERE email_id = $1`, msgID); err != nil {
		return err
	}
	return nil
}

func (m *metadata) UpdateMailboxModseqAndAppendChange(
	ctx context.Context,
	mailboxID store.MailboxID,
	change store.StateChange,
) (store.ModSeq, store.ChangeSeq, error) {
	now := m.s.clock.Now().UTC()
	var newModseq store.ModSeq
	var newSeq store.ChangeSeq
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var pid, highest int64
		err := tx.QueryRow(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = $1`,
			int64(mailboxID)).Scan(&pid, &highest)
		if err != nil {
			return mapErr(err)
		}
		newModseq = store.ModSeq(highest + 1)
		if _, err := tx.Exec(ctx,
			`UPDATE mailboxes SET highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
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
	rows, err := m.s.pool.Query(ctx, `
		SELECT seq, principal_id, entity_kind, entity_id, parent_entity_id, op, produced_at_us
		  FROM state_changes
		 WHERE principal_id = $1 AND seq > $2
		 ORDER BY seq ASC LIMIT $3`, int64(principalID), int64(fromSeq), max)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.StateChange
	for rows.Next() {
		var seq, pid int64
		var kind string
		var op int16
		var eid, peid, prodUs int64
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
	err := m.s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM state_changes WHERE principal_id = $1 AND entity_kind = $2`,
		int64(principalID), string(kind)).Scan(&seq)
	if err != nil {
		return 0, mapErr(err)
	}
	return store.ChangeSeq(seq), nil
}

// -- helpers ----------------------------------------------------------

func appendStateChange(
	ctx context.Context, tx pgx.Tx, principalID store.PrincipalID,
	kind store.EntityKind, entityID uint64, parentEntityID uint64,
	op store.ChangeOp, now time.Time,
) error {
	_, err := appendStateChangeSeq(ctx, tx, principalID, kind, entityID, parentEntityID, op, now)
	return err
}

func appendStateChangeSeq(
	ctx context.Context, tx pgx.Tx, principalID store.PrincipalID,
	kind store.EntityKind, entityID uint64, parentEntityID uint64,
	op store.ChangeOp, now time.Time,
) (store.ChangeSeq, error) {
	var next int64
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0)+1 FROM state_changes WHERE principal_id = $1`,
		int64(principalID)).Scan(&next)
	if err != nil {
		return 0, mapErr(err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO state_changes (principal_id, seq, entity_kind, entity_id,
		  parent_entity_id, op, produced_at_us) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		int64(principalID), next, string(kind), int64(entityID),
		int64(parentEntityID), int16(op), usMicros(now))
	if err != nil {
		return 0, mapErr(err)
	}
	return store.ChangeSeq(next), nil
}

func incRef(ctx context.Context, tx pgx.Tx, hash string, size int64, now time.Time) error {
	res, err := tx.Exec(ctx,
		`UPDATE blob_refs SET ref_count = ref_count + 1, last_change_us = $1 WHERE hash = $2`,
		usMicros(now), hash)
	if err != nil {
		return mapErr(err)
	}
	if res.RowsAffected() > 0 {
		return nil
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO blob_refs (hash, size, ref_count, last_change_us) VALUES ($1, $2, 1, $3)`,
		hash, size, usMicros(now))
	return mapErr(err)
}

// decRef decrements blob_refs.ref_count for hash, refusing to drive
// it below zero. Wave 2.9.9 audit (Track A): the WHERE ref_count > 0
// guard mirrors the storesqlite SQL-level guard. Postgres' bigint
// would otherwise underflow into
// negative territory on a duplicate hard-delete or a retention pass
// racing a concurrent unref, which would in turn confuse the orphan-
// blob sweeper into garbage-collecting a still-referenced blob. The
// rows-affected==0 path is graceful (already at zero or unknown
// hash); logged WARN so operators can spot pathological double-unref
// patterns without erroring a legitimate batch hard-delete.
func decRef(ctx context.Context, tx pgx.Tx, hash string, now time.Time) error {
	res, err := tx.Exec(ctx,
		`UPDATE blob_refs SET ref_count = ref_count - 1, last_change_us = $1 WHERE hash = $2 AND ref_count > 0`,
		usMicros(now), hash)
	if err != nil {
		return mapErr(err)
	}
	if res.RowsAffected() == 0 {
		slog.Warn("storepg: decRef no-op (already zero or unknown hash)", "hash", hash)
	}
	return nil
}

func (m *metadata) GetBlobRef(ctx context.Context, hash string) (int64, int64, error) {
	var size, refs int64
	err := m.s.pool.QueryRow(ctx,
		`SELECT size, ref_count FROM blob_refs WHERE hash = $1`, hash).Scan(&size, &refs)
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
	tx, err := m.s.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := incRef(ctx, tx, hash, size, m.s.clock.Now()); err != nil {
		return err
	}
	return mapErr(tx.Commit(ctx))
}

// DecRefBlob decrements the blob_refs row for hash, refusing to go below
// zero. No-op when hash is empty (REQ-SET-03b).
func (m *metadata) DecRefBlob(ctx context.Context, hash string) error {
	if hash == "" {
		return nil
	}
	tx, err := m.s.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := decRef(ctx, tx, hash, m.s.clock.Now()); err != nil {
		return err
	}
	return mapErr(tx.Commit(ctx))
}

// newUIDValidity returns a 32-bit UIDVALIDITY seeded from the given
// time plus a one-byte salt drawn from rs. Production passes
// crypto/rand.Reader (set on Store at Open time); tests inject a
// deterministic reader for byte-exact reproducibility. UIDVALIDITY
// is opaque to clients but externally visible, so the better posture
// (unguessable salt) costs nothing and matches the SQLite backend.
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
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals WHERE id > $1 ORDER BY id ASC LIMIT $2`,
		int64(after), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Principal
	for rows.Next() {
		var p store.Principal
		var kind int32
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
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals
		 WHERE lower(display_name) LIKE $1 OR lower(canonical_email) LIKE $2
		 LIMIT $3`,
		"%"+lower+"%",
		lower+"%@%",
		limit*2, // over-fetch; Go-side sort trims to limit
	)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Principal
	for rows.Next() {
		var p store.Principal
		var kind int32
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
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals
		 WHERE (lower(display_name) LIKE $1 OR lower(canonical_email) LIKE $2)
		   AND lower(canonical_email) LIKE $3
		 LIMIT $4`,
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
		var kind int32
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var exists int64
		err := tx.QueryRow(ctx,
			`SELECT 1 FROM principals WHERE id = $1`, int64(pid)).Scan(&exists)
		if err != nil {
			return mapErr(err)
		}
		hashRows, err := tx.Query(ctx, `
			SELECT blob_hash FROM messages WHERE principal_id = $1`,
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
		if _, err := tx.Exec(ctx,
			`DELETE FROM state_changes WHERE principal_id = $1`, int64(pid)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM audit_log WHERE principal_id = $1`, int64(pid)); err != nil {
			return mapErr(err)
		}
		// Phase 2 queue rows: drop and decrement body-blob refcounts.
		// queue.principal_id is ON DELETE SET NULL so we explicitly
		// remove rows where this principal was the submitter; rows
		// arising from a Sieve redirect under a different principal
		// stay alive.
		queueRows, err := tx.Query(ctx,
			`SELECT body_blob_hash FROM queue WHERE principal_id = $1`, int64(pid))
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
		if _, err := tx.Exec(ctx,
			`DELETE FROM queue WHERE principal_id = $1`, int64(pid)); err != nil {
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
		res, err := tx.Exec(ctx,
			`DELETE FROM principals WHERE id = $1`, int64(pid))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- ListOIDCProviders / DeleteOIDCProvider / UnlinkOIDC --------------

func (m *metadata) ListOIDCProviders(ctx context.Context) ([]store.OIDCProvider, error) {
	rows, err := m.s.pool.Query(ctx, `
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
		var createdUs int64
		if err := rows.Scan(&p.Name, &p.IssuerURL, &p.ClientID, &p.ClientSecretRef,
			&scopes, &p.AutoProvision, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		if scopes != "" {
			p.Scopes = strings.Split(scopes, ",")
		}
		p.CreatedAt = fromMicros(createdUs)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (m *metadata) DeleteOIDCProvider(ctx context.Context, id store.OIDCProviderID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM oidc_providers WHERE name = $1`, string(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) UnlinkOIDC(ctx context.Context, pid store.PrincipalID, providerID store.OIDCProviderID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM oidc_links WHERE principal_id = $1 AND provider_name = $2`,
			int64(pid), string(providerID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- cursors ----------------------------------------------------------

func (m *metadata) GetFTSCursor(ctx context.Context, key string) (uint64, error) {
	var seq int64
	err := m.s.pool.QueryRow(ctx,
		`SELECT seq FROM cursors WHERE key = $1`, key).Scan(&seq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, mapErr(err)
	}
	return uint64(seq), nil
}

func (m *metadata) SetFTSCursor(ctx context.Context, key string, seq uint64) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO cursors (key, seq) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET seq = EXCLUDED.seq`,
			key, int64(seq))
		return mapErr(err)
	})
}

// -- audit log --------------------------------------------------------

// encodeAuditMetadata mirrors the SQLite backend: deterministic
// sorted-key JSON. Same function name, same output → the migration
// tool copies rows verbatim.
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
		return nil, fmt.Errorf("storepg: decode audit metadata: %w", err)
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_log (at_us, actor_kind, actor_id, action, subject,
			  remote_addr, outcome, message, metadata_json, principal_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			usMicros(at), int32(entry.ActorKind), entry.ActorID, entry.Action, entry.Subject,
			entry.RemoteAddr, int32(entry.Outcome), entry.Message, metaJSON,
			auditPrincipalID(entry))
		return mapErr(err)
	})
}

// -- GetMailboxByName / ListMessages / SetMailboxSubscribed / RenameMailbox --

func (m *metadata) GetMailboxByName(ctx context.Context, pid store.PrincipalID, name string) (store.Mailbox, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, principal_id, parent_id, name, attributes, uidvalidity, uidnext,
		       highest_modseq, created_at_us, updated_at_us, color_hex, sort_order
		  FROM mailboxes WHERE principal_id = $1 AND name = $2`,
		int64(pid), name)
	return scanMailbox(row)
}

func (m *metadata) ListMessages(ctx context.Context, mailboxID store.MailboxID, filter store.MessageFilter) ([]store.Message, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	// ReceivedBefore adds an optional time-range predicate. The column
	// internal_date_us is used because it is always set. When nil no
	// constraint is applied — callers that do not set ReceivedBefore
	// get the previous behaviour.
	var rows pgx.Rows
	var err error
	if filter.ReceivedBefore != nil {
		receivedBeforeUs := usMicros(*filter.ReceivedBefore)
		rows, err = m.s.pool.Query(ctx, `
			SELECT m.id, m.principal_id, m.internal_date_us, m.received_at_us,
			       m.size, m.blob_hash, m.blob_size, m.thread_id,
			       m.env_subject, m.env_from, m.env_to, m.env_cc, m.env_bcc, m.env_reply_to,
			       m.env_message_id, m.env_in_reply_to, m.env_date_us,
			       mm.mailbox_id, mm.uid, mm.modseq, mm.flags, mm.keywords_csv, mm.snoozed_until_us
			  FROM messages m
			  JOIN message_mailboxes mm ON mm.message_id = m.id AND mm.mailbox_id = $1
			 WHERE mm.uid > $2 AND m.internal_date_us < $4
			 ORDER BY mm.uid ASC LIMIT $3`,
			int64(mailboxID), int64(filter.AfterUID), limit, receivedBeforeUs)
	} else {
		rows, err = m.s.pool.Query(ctx, `
			SELECT m.id, m.principal_id, m.internal_date_us, m.received_at_us,
			       m.size, m.blob_hash, m.blob_size, m.thread_id,
			       m.env_subject, m.env_from, m.env_to, m.env_cc, m.env_bcc, m.env_reply_to,
			       m.env_message_id, m.env_in_reply_to, m.env_date_us,
			       mm.mailbox_id, mm.uid, mm.modseq, mm.flags, mm.keywords_csv, mm.snoozed_until_us
			  FROM messages m
			  JOIN message_mailboxes mm ON mm.message_id = m.id AND mm.mailbox_id = $1
			 WHERE mm.uid > $2
			 ORDER BY mm.uid ASC LIMIT $3`,
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var attrs int64
		if err := tx.QueryRow(ctx,
			`SELECT attributes FROM mailboxes WHERE id = $1`, int64(mailboxID)).Scan(&attrs); err != nil {
			return mapErr(err)
		}
		bit := int64(store.MailboxAttrSubscribed)
		if subscribed {
			attrs |= bit
		} else {
			attrs &^= bit
		}
		res, err := tx.Exec(ctx,
			`UPDATE mailboxes SET attributes = $1, updated_at_us = $2 WHERE id = $3`,
			attrs, usMicros(now), int64(mailboxID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) RenameMailbox(ctx context.Context, mailboxID store.MailboxID, newName string) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM mailboxes WHERE id = $1`, int64(mailboxID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		var other int64
		err := tx.QueryRow(ctx,
			`SELECT id FROM mailboxes WHERE principal_id = $1 AND name = $2 AND id != $3`,
			pid, newName, int64(mailboxID)).Scan(&other)
		if err == nil {
			return fmt.Errorf("mailbox %q: %w", newName, store.ErrConflict)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return mapErr(err)
		}
		res, err := tx.Exec(ctx,
			`UPDATE mailboxes SET name = $1, updated_at_us = $2 WHERE id = $3`,
			newName, usMicros(now), int64(mailboxID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM mailboxes WHERE id = $1`, int64(mailboxID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		res, err := tx.Exec(ctx,
			`UPDATE mailboxes SET parent_id = $1, updated_at_us = $2 WHERE id = $3`,
			int64(newParentID), usMicros(now), int64(mailboxID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx,
			`SELECT principal_id FROM mailboxes WHERE id = $1`, int64(mailboxID)).Scan(&pid); err != nil {
			return mapErr(err)
		}
		res, err := tx.Exec(ctx,
			`UPDATE mailboxes SET sort_order = $1, updated_at_us = $2 WHERE id = $3`,
			int64(sortOrder), usMicros(now), int64(mailboxID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindMailbox, uint64(mailboxID), 0, store.ChangeOpUpdated, now)
	})
}

// SetMailboxColor implements REQ-PROTO-56 / REQ-STORE-34. The colour is
// validated against the "#RRGGBB" hex literal grammar before any SQL is
// emitted; nil clears the column. Postgres also enforces the format via
// CHECK constraint, but the Go check produces a clean ErrInvalidArgument.
func (m *metadata) SetMailboxColor(ctx context.Context, mailboxID store.MailboxID, color *string) error {
	if color != nil && !validMailboxColor(*color) {
		return fmt.Errorf("color %q: %w", *color, store.ErrInvalidArgument)
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var v any
		if color != nil {
			v = *color
		}
		res, err := tx.Exec(ctx,
			`UPDATE mailboxes SET color_hex = $1, updated_at_us = $2 WHERE id = $3`,
			v, usMicros(now), int64(mailboxID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// validMailboxColor reports whether s matches the JMAP Mailbox.color
// grammar "#RRGGBB" (six hex digits, leading '#').
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
	err := m.s.pool.QueryRow(ctx,
		`SELECT script FROM sieve_scripts WHERE principal_id = $1`, int64(pid)).Scan(&script)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", mapErr(err)
	}
	return script, nil
}

func (m *metadata) SetSieveScript(ctx context.Context, pid store.PrincipalID, text string) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		if text == "" {
			_, err := tx.Exec(ctx,
				`DELETE FROM sieve_scripts WHERE principal_id = $1`, int64(pid))
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO sieve_scripts (principal_id, script, updated_at_us)
			VALUES ($1, $2, $3)
			ON CONFLICT (principal_id) DO UPDATE SET script = EXCLUDED.script, updated_at_us = EXCLUDED.updated_at_us`,
			int64(pid), text, usMicros(now))
		return mapErr(err)
	})
}

func (m *metadata) GetUserSieveScript(ctx context.Context, pid store.PrincipalID) (string, error) {
	var script string
	err := m.s.pool.QueryRow(ctx,
		`SELECT COALESCE(user_script, '') FROM sieve_scripts WHERE principal_id = $1`, int64(pid)).Scan(&script)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", mapErr(err)
	}
	return script, nil
}

func (m *metadata) SetUserSieveScript(ctx context.Context, pid store.PrincipalID, text string) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO sieve_scripts (principal_id, script, user_script, updated_at_us)
			VALUES ($1, '', $2, $3)
			ON CONFLICT (principal_id) DO UPDATE
			  SET user_script = EXCLUDED.user_script,
			      updated_at_us = EXCLUDED.updated_at_us`,
			int64(pid), text, usMicros(now))
		return mapErr(err)
	})
}

// -- JMAP snooze (REQ-PROTO-49) --------------------------------------

func (m *metadata) ListDueSnoozedMessages(ctx context.Context, now time.Time, limit int) ([]store.Message, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	// Join messages with message_mailboxes to get per-mailbox snooze state.
	// The invariant: snoozed_until_us non-null iff '$snoozed' in keywords_csv.
	rows, err := m.s.pool.Query(ctx, `
		SELECT m.id, m.principal_id, m.internal_date_us, m.received_at_us,
		       m.size, m.blob_hash, m.blob_size, m.thread_id,
		       m.env_subject, m.env_from, m.env_to, m.env_cc, m.env_bcc, m.env_reply_to,
		       m.env_message_id, m.env_in_reply_to, m.env_date_us,
		       mm.mailbox_id, mm.uid, mm.modseq, mm.flags, mm.keywords_csv, mm.snoozed_until_us
		  FROM messages m
		  JOIN message_mailboxes mm ON mm.message_id = m.id
		 WHERE mm.snoozed_until_us IS NOT NULL
		   AND mm.snoozed_until_us <= $1
		   AND (',' || mm.keywords_csv || ',') LIKE '%,$snoozed,%'
		 ORDER BY mm.snoozed_until_us ASC LIMIT $2`,
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var curKeywords string
		err := tx.QueryRow(ctx, `
			SELECT keywords_csv
			  FROM message_mailboxes WHERE message_id = $1 AND mailbox_id = $2`,
			int64(msgID), int64(mailboxID)).Scan(&curKeywords)
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
		err = tx.QueryRow(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = $1`,
			int64(mailboxID)).Scan(&pid, &highest)
		if err != nil {
			return mapErr(err)
		}
		modseq = store.ModSeq(highest + 1)

		var snoozedArg any
		if when != nil {
			snoozedArg = usMicros(when.UTC())
		}
		if _, err := tx.Exec(ctx, `
			UPDATE message_mailboxes
			   SET keywords_csv = $1, modseq = $2, snoozed_until_us = $3
			 WHERE message_id = $4 AND mailbox_id = $5`,
			strings.Join(kws, ","), int64(modseq), snoozedArg, int64(msgID), int64(mailboxID)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
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
	pos := 1
	add := func(cond string, v any) {
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", pos)))
		args = append(args, v)
		pos++
	}
	if filter.AfterID != 0 {
		add("id > ?", int64(filter.AfterID))
	}
	if filter.PrincipalID != 0 {
		add("principal_id = ?", int64(filter.PrincipalID))
	}
	if filter.Action != "" {
		add("action = ?", filter.Action)
	}
	if !filter.Since.IsZero() {
		add("at_us >= ?", usMicros(filter.Since))
	}
	if !filter.Until.IsZero() {
		add("at_us < ?", usMicros(filter.Until))
	}
	q := `SELECT id, at_us, actor_kind, actor_id, action, subject,
	             remote_addr, outcome, message, metadata_json
	        FROM audit_log`
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
	var out []store.AuditLogEntry
	for rows.Next() {
		var e store.AuditLogEntry
		var id, atUs int64
		var actorKind, outcome int32
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
