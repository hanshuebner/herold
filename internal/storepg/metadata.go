package storepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
			return fmt.Errorf("%w: %s", store.ErrConflict, pgErr.Message)
		case "23503":
			return fmt.Errorf("%w: foreign key violation: %s", store.ErrConflict, pgErr.Message)
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
		return tx.QueryRow(ctx, `
			INSERT INTO principals (kind, canonical_email, display_name, password_hash,
			  totp_secret, quota_bytes, flags, used_bytes, created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, $9) RETURNING id`,
			int32(p.Kind), strings.ToLower(p.CanonicalEmail), p.DisplayName, p.PasswordHash,
			p.TOTPSecret, p.QuotaBytes, int64(p.Flags), usMicros(now), usMicros(now),
		).Scan(&id)
	})
	if err != nil {
		return store.Principal{}, mapErr(err)
	}
	p.ID = store.PrincipalID(id)
	p.CreatedAt = now
	p.UpdatedAt = now
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
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals `+where, args...)
	var p store.Principal
	var kind int32
	var flags int64
	var createdUs, updatedUs int64
	var totp []byte
	var id int64
	err := row.Scan(&id, &kind, &p.CanonicalEmail, &p.DisplayName, &p.PasswordHash,
		&totp, &p.QuotaBytes, &flags, &createdUs, &updatedUs)
	if err != nil {
		return store.Principal{}, mapErr(err)
	}
	p.ID = store.PrincipalID(id)
	p.Kind = store.PrincipalKind(kind)
	p.Flags = store.PrincipalFlags(flags)
	p.CreatedAt = fromMicros(createdUs)
	p.UpdatedAt = fromMicros(updatedUs)
	if len(totp) > 0 {
		p.TOTPSecret = totp
	}
	return p, nil
}

func (m *metadata) UpdatePrincipal(ctx context.Context, p store.Principal) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE principals
			   SET kind = $1, canonical_email = $2, display_name = $3, password_hash = $4,
			       totp_secret = $5, quota_bytes = $6, flags = $7, updated_at_us = $8
			 WHERE id = $9`,
			int32(p.Kind), strings.ToLower(p.CanonicalEmail), p.DisplayName, p.PasswordHash,
			p.TOTPSecret, p.QuotaBytes, int64(p.Flags), usMicros(now), int64(p.ID))
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
		return mapErr(err)
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
		return tx.QueryRow(ctx, `
			INSERT INTO aliases (local_part, domain, target_principal, expires_at_us, created_at_us)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			strings.ToLower(a.LocalPart), strings.ToLower(a.Domain),
			int64(a.TargetPrincipal), expiresUs, usMicros(now)).Scan(&id)
	})
	if err != nil {
		return store.Alias{}, mapErr(err)
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
		return mapErr(err)
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
		return mapErr(err)
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO api_keys (principal_id, hash, name, created_at_us, last_used_at_us)
			VALUES ($1, $2, $3, $4, 0) RETURNING id`,
			int64(k.PrincipalID), k.Hash, k.Name, usMicros(now)).Scan(&id)
	})
	if err != nil {
		return store.APIKey{}, mapErr(err)
	}
	k.ID = store.APIKeyID(id)
	k.CreatedAt = now
	return k, nil
}

func (m *metadata) GetAPIKeyByHash(ctx context.Context, hash string) (store.APIKey, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, principal_id, hash, name, created_at_us, last_used_at_us
		  FROM api_keys WHERE hash = $1`, hash)
	var k store.APIKey
	var id, pid int64
	var createdUs, lastUs int64
	err := row.Scan(&id, &pid, &k.Hash, &k.Name, &createdUs, &lastUs)
	if err != nil {
		return store.APIKey{}, mapErr(err)
	}
	k.ID = store.APIKeyID(id)
	k.PrincipalID = store.PrincipalID(pid)
	k.CreatedAt = fromMicros(createdUs)
	k.LastUsedAt = fromMicros(lastUs)
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
		SELECT id, principal_id, hash, name, created_at_us, last_used_at_us
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
		if err := rows.Scan(&id, &ownerID, &k.Hash, &k.Name, &createdUs, &lastUs); err != nil {
			return nil, mapErr(err)
		}
		k.ID = store.APIKeyID(id)
		k.PrincipalID = store.PrincipalID(ownerID)
		k.CreatedAt = fromMicros(createdUs)
		k.LastUsedAt = fromMicros(lastUs)
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
			  uidnext, highest_modseq, created_at_us, updated_at_us, color_hex)
			VALUES ($1, $2, $3, $4, $5, 1, 0, $6, $7, $8) RETURNING id`,
			int64(mb.PrincipalID), int64(mb.ParentID), mb.Name, int64(mb.Attributes),
			int64(mb.UIDValidity), usMicros(now), usMicros(now), color).Scan(&id); err != nil {
			return mapErr(err)
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
		       highest_modseq, created_at_us, updated_at_us, color_hex
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
	err := row.Scan(&id, &pid, &parent, &mb.Name, &attrs, &uidv, &uidn, &hm, &createdUs, &updatedUs, &color)
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
	if color != nil {
		v := *color
		mb.Color = &v
	}
	return mb, nil
}

func (m *metadata) ListMailboxes(ctx context.Context, principalID store.PrincipalID) ([]store.Mailbox, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, principal_id, parent_id, name, attributes, uidvalidity, uidnext,
		       highest_modseq, created_at_us, updated_at_us, color_hex
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		err := tx.QueryRow(ctx, `SELECT principal_id FROM mailboxes WHERE id = $1`, int64(id)).Scan(&pid)
		if err != nil {
			return mapErr(err)
		}
		hashRows, err := tx.Query(ctx, `SELECT blob_hash FROM messages WHERE mailbox_id = $1`, int64(id))
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
			if err := decRef(ctx, tx, h, m.s.clock.Now()); err != nil {
				return err
			}
		}
		res, err := tx.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindMailbox, uint64(id), 0, store.ChangeOpDestroyed, m.s.clock.Now())
	})
}

// -- messages ---------------------------------------------------------

func (m *metadata) InsertMessage(ctx context.Context, msg store.Message) (store.UID, store.ModSeq, error) {
	now := m.s.clock.Now().UTC()
	var uid store.UID
	var modseq store.ModSeq
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		var uidNext, highestModSeq int64
		if err := tx.QueryRow(ctx, `
			SELECT principal_id, uidnext, highest_modseq FROM mailboxes WHERE id = $1`,
			int64(msg.MailboxID)).Scan(&pid, &uidNext, &highestModSeq); err != nil {
			return mapErr(err)
		}
		var quota, used int64
		if err := tx.QueryRow(ctx, `
			SELECT quota_bytes, used_bytes FROM principals WHERE id = $1`, pid).Scan(&quota, &used); err != nil {
			return mapErr(err)
		}
		if quota > 0 && used+msg.Size > quota {
			return store.ErrQuotaExceeded
		}
		uid = store.UID(uidNext)
		modseq = store.ModSeq(highestModSeq + 1)
		var mid int64
		var snoozedArg any
		if msg.SnoozedUntil != nil {
			snoozedArg = usMicros(*msg.SnoozedUntil)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO messages (mailbox_id, uid, modseq, flags, keywords_csv,
			  internal_date_us, received_at_us, size, blob_hash, blob_size, thread_id,
			  env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
			  env_message_id, env_in_reply_to, env_date_us, snoozed_until_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
			RETURNING id`,
			int64(msg.MailboxID), int64(uid), int64(modseq), int64(msg.Flags),
			strings.Join(msg.Keywords, ","),
			usMicros(msg.InternalDate), usMicros(msg.ReceivedAt), msg.Size,
			msg.Blob.Hash, msg.Blob.Size, int64(msg.ThreadID),
			msg.Envelope.Subject, msg.Envelope.From, msg.Envelope.To,
			msg.Envelope.Cc, msg.Envelope.Bcc, msg.Envelope.ReplyTo,
			msg.Envelope.MessageID, msg.Envelope.InReplyTo, usMicros(msg.Envelope.Date),
			snoozedArg).Scan(&mid); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET uidnext = uidnext + 1, highest_modseq = $1, updated_at_us = $2
			 WHERE id = $3`, int64(modseq), usMicros(now), int64(msg.MailboxID)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE principals SET used_bytes = used_bytes + $1, updated_at_us = $2 WHERE id = $3`,
			msg.Size, usMicros(now), pid); err != nil {
			return mapErr(err)
		}
		if err := incRef(ctx, tx, msg.Blob.Hash, msg.Blob.Size, now); err != nil {
			return err
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(mid), uint64(msg.MailboxID), store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, 0, err
	}
	return uid, modseq, nil
}

func (m *metadata) GetMessage(ctx context.Context, id store.MessageID) (store.Message, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, mailbox_id, uid, modseq, flags, keywords_csv, internal_date_us,
		       received_at_us, size, blob_hash, blob_size, thread_id,
		       env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
		       env_message_id, env_in_reply_to, env_date_us, snoozed_until_us
		  FROM messages WHERE id = $1`, int64(id))
	return scanMessage(row)
}

func scanMessage(row rowLike) (store.Message, error) {
	var msg store.Message
	var id, mbox, uid, modseq, flags int64
	var keywords string
	var idUs, rcvUs int64
	var blobSize int64
	var thread int64
	var envDateUs int64
	var snoozedUs *int64
	err := row.Scan(&id, &mbox, &uid, &modseq, &flags, &keywords, &idUs, &rcvUs,
		&msg.Size, &msg.Blob.Hash, &blobSize, &thread,
		&msg.Envelope.Subject, &msg.Envelope.From, &msg.Envelope.To,
		&msg.Envelope.Cc, &msg.Envelope.Bcc, &msg.Envelope.ReplyTo,
		&msg.Envelope.MessageID, &msg.Envelope.InReplyTo, &envDateUs, &snoozedUs)
	if err != nil {
		return store.Message{}, mapErr(err)
	}
	msg.ID = store.MessageID(id)
	msg.MailboxID = store.MailboxID(mbox)
	msg.UID = store.UID(uid)
	msg.ModSeq = store.ModSeq(modseq)
	msg.Flags = store.MessageFlags(flags)
	if keywords != "" {
		msg.Keywords = strings.Split(keywords, ",")
	}
	msg.InternalDate = fromMicros(idUs)
	msg.ReceivedAt = fromMicros(rcvUs)
	msg.Blob.Size = blobSize
	msg.ThreadID = uint64(thread)
	msg.Envelope.Date = fromMicros(envDateUs)
	if snoozedUs != nil {
		t := fromMicros(*snoozedUs)
		msg.SnoozedUntil = &t
	}
	return msg, nil
}

func (m *metadata) UpdateMessageFlags(
	ctx context.Context,
	id store.MessageID,
	flagAdd, flagClear store.MessageFlags,
	keywordAdd, keywordClear []string,
	unchangedSince store.ModSeq,
) (store.ModSeq, error) {
	now := m.s.clock.Now().UTC()
	var modseq store.ModSeq
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var mbox int64
		var curFlags int64
		var curKeywords string
		var curModSeq int64
		err := tx.QueryRow(ctx, `
			SELECT mailbox_id, flags, keywords_csv, modseq
			  FROM messages WHERE id = $1`, int64(id)).Scan(&mbox, &curFlags, &curKeywords, &curModSeq)
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

		var pid, highest int64
		err = tx.QueryRow(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = $1`, mbox).Scan(&pid, &highest)
		if err != nil {
			return mapErr(err)
		}
		modseq = store.ModSeq(highest + 1)

		if _, err := tx.Exec(ctx, `
			UPDATE messages SET flags = $1, keywords_csv = $2, modseq = $3 WHERE id = $4`,
			int64(newFlags), strings.Join(kws, ","), int64(modseq), int64(id)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
			int64(modseq), usMicros(now), mbox); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(id), uint64(mbox), store.ChangeOpUpdated, now)
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
			// UID is not needed here — it is no longer carried on the
			// change row (consumers join the messages table when they
			// need it).
			var size int64
			var hash string
			err := tx.QueryRow(ctx, `
				SELECT size, blob_hash FROM messages WHERE id = $1 AND mailbox_id = $2`,
				int64(id), int64(mailboxID)).Scan(&size, &hash)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return mapErr(err)
			}
			res, err := tx.Exec(ctx, `DELETE FROM messages WHERE id = $1`, int64(id))
			if err != nil {
				return mapErr(err)
			}
			if res.RowsAffected() == 0 {
				continue
			}
			if err := decRef(ctx, tx, hash, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE principals SET used_bytes = used_bytes - $1, updated_at_us = $2 WHERE id = $3`,
				size, usMicros(now), pid); err != nil {
				return mapErr(err)
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

func decRef(ctx context.Context, tx pgx.Tx, hash string, now time.Time) error {
	_, err := tx.Exec(ctx,
		`UPDATE blob_refs SET ref_count = ref_count - 1, last_change_us = $1 WHERE hash = $2`,
		usMicros(now), hash)
	return mapErr(err)
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
			SELECT blob_hash FROM messages
			 WHERE mailbox_id IN (SELECT id FROM mailboxes WHERE principal_id = $1)`,
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
		       highest_modseq, created_at_us, updated_at_us, color_hex
		  FROM mailboxes WHERE principal_id = $1 AND name = $2`,
		int64(pid), name)
	return scanMailbox(row)
}

func (m *metadata) ListMessages(ctx context.Context, mailboxID store.MailboxID, filter store.MessageFilter) ([]store.Message, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, mailbox_id, uid, modseq, flags, keywords_csv, internal_date_us,
		       received_at_us, size, blob_hash, blob_size, thread_id,
		       env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
		       env_message_id, env_in_reply_to, env_date_us, snoozed_until_us
		  FROM messages
		 WHERE mailbox_id = $1 AND uid > $2
		 ORDER BY uid ASC LIMIT $3`,
		int64(mailboxID), int64(filter.AfterUID), limit)
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
		return nil
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

// -- JMAP snooze (REQ-PROTO-49) --------------------------------------

func (m *metadata) ListDueSnoozedMessages(ctx context.Context, now time.Time, limit int) ([]store.Message, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	// The partial index idx_messages_snoozed_until covers rows with
	// snoozed_until_us NOT NULL; the predicate then filters to due
	// rows. We additionally require ',$snoozed,' to appear inside the
	// padded keywords_csv to enforce the atomicity invariant — a row
	// whose snoozed_until_us was set without the keyword via direct
	// UpdateMessageFlags is a programmer error and is excluded.
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, mailbox_id, uid, modseq, flags, keywords_csv, internal_date_us,
		       received_at_us, size, blob_hash, blob_size, thread_id,
		       env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
		       env_message_id, env_in_reply_to, env_date_us, snoozed_until_us
		  FROM messages
		 WHERE snoozed_until_us IS NOT NULL
		   AND snoozed_until_us <= $1
		   AND (',' || keywords_csv || ',') LIKE '%,$snoozed,%'
		 ORDER BY snoozed_until_us ASC LIMIT $2`,
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

func (m *metadata) SetSnooze(ctx context.Context, msgID store.MessageID, when *time.Time) (store.ModSeq, error) {
	now := m.s.clock.Now().UTC()
	var modseq store.ModSeq
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var mbox int64
		var curFlags int64
		var curKeywords string
		err := tx.QueryRow(ctx, `
			SELECT mailbox_id, flags, keywords_csv
			  FROM messages WHERE id = $1`, int64(msgID)).Scan(&mbox, &curFlags, &curKeywords)
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
		err = tx.QueryRow(ctx, `SELECT principal_id, highest_modseq FROM mailboxes WHERE id = $1`, mbox).Scan(&pid, &highest)
		if err != nil {
			return mapErr(err)
		}
		modseq = store.ModSeq(highest + 1)

		var snoozedArg any
		if when != nil {
			snoozedArg = usMicros(when.UTC())
		}
		if _, err := tx.Exec(ctx, `
			UPDATE messages
			   SET keywords_csv = $1, modseq = $2, snoozed_until_us = $3
			 WHERE id = $4`,
			strings.Join(kws, ","), int64(modseq), snoozedArg, int64(msgID)); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET highest_modseq = $1, updated_at_us = $2 WHERE id = $3`,
			int64(modseq), usMicros(now), mbox); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindEmail, uint64(msgID), uint64(mbox), store.ChangeOpUpdated, now)
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
