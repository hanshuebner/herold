package backup

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func init() {
	RegisterAdapter(func(s store.Store) (Backend, bool) {
		ss, ok := s.(*storesqlite.Store)
		if !ok {
			return nil, false
		}
		return &sqliteBackend{
			db:    storesqlite.DBHandle(ss),
			wmu:   storesqlite.WriterMu(ss),
			blobs: ss.Blobs(),
		}, true
	})
}

// sqliteBackend implements Backend against a SQLite-backed store.
// Streams use the underlying *sql.DB directly so backup work does not
// queue behind metadata writers; restore takes the writer mutex so
// bulk inserts respect the single-writer discipline.
type sqliteBackend struct {
	db    *sql.DB
	wmu   *sync.Mutex
	blobs store.Blobs
}

func (b *sqliteBackend) Kind() string       { return "sqlite" }
func (b *sqliteBackend) Blobs() store.Blobs { return b.blobs }

func (b *sqliteBackend) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := b.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("sqlite: schema version: %w", err)
	}
	return v, nil
}

func (b *sqliteBackend) IsEmpty(ctx context.Context) (bool, error) {
	for _, t := range TableNames {
		var n int
		if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return false, fmt.Errorf("sqlite: count %s: %w", t, err)
		}
		if n != 0 {
			return false, nil
		}
	}
	return true, nil
}

func (b *sqliteBackend) TruncateAll(ctx context.Context) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin truncate: %w", err)
	}
	defer tx.Rollback()
	// SQLite has no TRUNCATE; DELETE works the same with cascade
	// disabled (we issue them in reverse FK order). Reset
	// AUTOINCREMENT counters via sqlite_sequence so subsequent
	// inserts start at 1.
	for i := len(TableNames) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+TableNames[i]); err != nil {
			return fmt.Errorf("sqlite: delete %s: %w", TableNames[i], err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence`); err != nil {
		// sqlite_sequence may not exist if no AUTOINCREMENT table was
		// ever populated; ignore.
		_ = err
	}
	return tx.Commit()
}

func (b *sqliteBackend) Snapshot(ctx context.Context) (Source, error) {
	// SQLite WAL gives readers a stable snapshot via BEGIN, but we
	// use BEGIN IMMEDIATE for consistency with the writer side and so
	// the snapshot tx fences out further writes from this connection.
	// A read-only DEFERRED transaction is sufficient for cross-table
	// consistency on WAL.
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin snapshot: %w", err)
	}
	return &sqliteSource{tx: tx}, nil
}

func (b *sqliteBackend) Restore(ctx context.Context) (Sink, error) {
	b.wmu.Lock()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		b.wmu.Unlock()
		return nil, fmt.Errorf("sqlite: begin restore: %w", err)
	}
	return &sqliteSink{tx: tx, wmu: b.wmu}, nil
}

// sqliteSource is the Source for sqliteBackend snapshots. It scans
// rows column-by-column into the typed Row structs from rows.go.
type sqliteSource struct {
	tx     *sql.Tx
	closed bool
}

func (s *sqliteSource) CountRows(ctx context.Context, table string) (int64, error) {
	var n int64
	if err := s.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite count %s: %w", table, err)
	}
	return n, nil
}

func (s *sqliteSource) EnumerateRows(ctx context.Context, table string, fn func(row any) error) error {
	switch table {
	case "domains":
		return enumerate(ctx, s.tx, `SELECT name, is_local, created_at_us FROM domains ORDER BY name`,
			func(rs *sql.Rows) (any, error) {
				var r DomainRow
				var b int64
				if err := rs.Scan(&r.Name, &b, &r.CreatedAtUs); err != nil {
					return nil, err
				}
				r.IsLocal = b != 0
				return &r, nil
			}, fn)
	case "principals":
		return enumerate(ctx, s.tx,
			`SELECT id, kind, canonical_email, display_name, password_hash,
			        totp_secret, quota_bytes, flags, used_bytes,
			        created_at_us, updated_at_us FROM principals ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r PrincipalRow
				if err := rs.Scan(&r.ID, &r.Kind, &r.CanonicalEmail, &r.DisplayName, &r.PasswordHash,
					&r.TOTPSecret, &r.QuotaBytes, &r.Flags, &r.UsedBytes,
					&r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "oidc_providers":
		return enumerate(ctx, s.tx,
			`SELECT name, issuer_url, client_id, client_secret_ref, scopes_csv,
			        auto_provision, created_at_us FROM oidc_providers ORDER BY name`,
			func(rs *sql.Rows) (any, error) {
				var r OIDCProviderRow
				var ap int64
				if err := rs.Scan(&r.Name, &r.IssuerURL, &r.ClientID, &r.ClientSecretRef,
					&r.ScopesCSV, &ap, &r.CreatedAtUs); err != nil {
					return nil, err
				}
				r.AutoProvision = ap != 0
				return &r, nil
			}, fn)
	case "oidc_links":
		return enumerate(ctx, s.tx,
			`SELECT principal_id, provider_name, subject, email_at_provider, linked_at_us
			   FROM oidc_links ORDER BY provider_name, subject`,
			func(rs *sql.Rows) (any, error) {
				var r OIDCLinkRow
				if err := rs.Scan(&r.PrincipalID, &r.ProviderName, &r.Subject,
					&r.EmailAtProvider, &r.LinkedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "api_keys":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, hash, name, created_at_us, last_used_at_us
			   FROM api_keys ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r APIKeyRow
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Hash, &r.Name,
					&r.CreatedAtUs, &r.LastUsedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "aliases":
		return enumerate(ctx, s.tx,
			`SELECT id, local_part, domain, target_principal, expires_at_us, created_at_us
			   FROM aliases ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r AliasRow
				var exp sql.NullInt64
				if err := rs.Scan(&r.ID, &r.LocalPart, &r.Domain, &r.TargetPrincipal,
					&exp, &r.CreatedAtUs); err != nil {
					return nil, err
				}
				if exp.Valid {
					v := exp.Int64
					r.ExpiresAtUs = &v
				}
				return &r, nil
			}, fn)
	case "sieve_scripts":
		return enumerate(ctx, s.tx,
			`SELECT principal_id, script, updated_at_us FROM sieve_scripts ORDER BY principal_id`,
			func(rs *sql.Rows) (any, error) {
				var r SieveScriptRow
				if err := rs.Scan(&r.PrincipalID, &r.Script, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "mailboxes":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, parent_id, name, attributes, uidvalidity,
			        uidnext, highest_modseq, created_at_us, updated_at_us
			   FROM mailboxes ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r MailboxRow
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.ParentID, &r.Name,
					&r.Attributes, &r.UIDValidity, &r.UIDNext, &r.HighestModSeq,
					&r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "messages":
		return enumerate(ctx, s.tx,
			`SELECT id, mailbox_id, uid, modseq, flags, keywords_csv,
			        internal_date_us, received_at_us, size, blob_hash, blob_size,
			        thread_id, env_subject, env_from, env_to, env_cc, env_bcc,
			        env_reply_to, env_message_id, env_in_reply_to, env_date_us,
			        snoozed_until_us
			   FROM messages ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r MessageRow
				var snooze sql.NullInt64
				if err := rs.Scan(&r.ID, &r.MailboxID, &r.UID, &r.ModSeq, &r.Flags,
					&r.KeywordsCSV, &r.InternalDateUs, &r.ReceivedAtUs, &r.Size,
					&r.BlobHash, &r.BlobSize, &r.ThreadID, &r.EnvSubject, &r.EnvFrom,
					&r.EnvTo, &r.EnvCc, &r.EnvBcc, &r.EnvReplyTo, &r.EnvMessageID,
					&r.EnvInReplyTo, &r.EnvDateUs, &snooze); err != nil {
					return nil, err
				}
				if snooze.Valid {
					v := snooze.Int64
					r.SnoozedUntilUs = &v
				}
				return &r, nil
			}, fn)
	case "mailbox_acl":
		return enumerate(ctx, s.tx,
			`SELECT id, mailbox_id, principal_id, rights_mask, granted_by, created_at_us
			   FROM mailbox_acl ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r MailboxACLRow
				var pid sql.NullInt64
				if err := rs.Scan(&r.ID, &r.MailboxID, &pid, &r.RightsMask,
					&r.GrantedBy, &r.CreatedAtUs); err != nil {
					return nil, err
				}
				if pid.Valid {
					v := pid.Int64
					r.PrincipalID = &v
				}
				return &r, nil
			}, fn)
	case "state_changes":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, seq, entity_kind, entity_id,
			        parent_entity_id, op, produced_at_us
			   FROM state_changes ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r StateChangeRow
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Seq, &r.EntityKind,
					&r.EntityID, &r.ParentEntityID, &r.Op, &r.ProducedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "audit_log":
		return enumerate(ctx, s.tx,
			`SELECT id, at_us, actor_kind, actor_id, action, subject,
			        remote_addr, outcome, message, metadata_json, principal_id
			   FROM audit_log ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r AuditLogRow
				if err := rs.Scan(&r.ID, &r.AtUs, &r.ActorKind, &r.ActorID, &r.Action,
					&r.Subject, &r.RemoteAddr, &r.Outcome, &r.Message,
					&r.MetadataJSON, &r.PrincipalID); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "cursors":
		return enumerate(ctx, s.tx, `SELECT key, seq FROM cursors ORDER BY key`,
			func(rs *sql.Rows) (any, error) {
				var r CursorRow
				if err := rs.Scan(&r.Key, &r.Seq); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "queue":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, mail_from, rcpt_to, envelope_id,
			        body_blob_hash, headers_blob_hash, state, attempts,
			        last_attempt_at_us, next_attempt_at_us, last_error,
			        dsn_notify_flags, dsn_ret, dsn_envid, dsn_orcpt,
			        idempotency_key, created_at_us FROM queue ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r QueueRow
				var pid sql.NullInt64
				var idem sql.NullString
				if err := rs.Scan(&r.ID, &pid, &r.MailFrom, &r.RcptTo, &r.EnvelopeID,
					&r.BodyBlobHash, &r.HeadersBlobHash, &r.State, &r.Attempts,
					&r.LastAttemptAtUs, &r.NextAttemptAtUs, &r.LastError,
					&r.DSNNotifyFlags, &r.DSNRet, &r.DSNEnvID, &r.DSNOrcpt,
					&idem, &r.CreatedAtUs); err != nil {
					return nil, err
				}
				if pid.Valid {
					v := pid.Int64
					r.PrincipalID = &v
				}
				if idem.Valid {
					v := idem.String
					r.IdempotencyKey = &v
				}
				return &r, nil
			}, fn)
	case "dkim_keys":
		return enumerate(ctx, s.tx,
			`SELECT id, domain, selector, algorithm, private_key_pem, public_key_b64,
			        status, created_at_us, rotated_at_us FROM dkim_keys ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r DKIMKeyRow
				if err := rs.Scan(&r.ID, &r.Domain, &r.Selector, &r.Algorithm,
					&r.PrivateKeyPEM, &r.PublicKeyB64, &r.Status,
					&r.CreatedAtUs, &r.RotatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "acme_accounts":
		return enumerate(ctx, s.tx,
			`SELECT id, directory_url, contact_email, account_key_pem, kid, created_at_us
			   FROM acme_accounts ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ACMEAccountRow
				if err := rs.Scan(&r.ID, &r.DirectoryURL, &r.ContactEmail,
					&r.AccountKeyPEM, &r.KID, &r.CreatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "acme_orders":
		return enumerate(ctx, s.tx,
			`SELECT id, account_id, hostnames_json, status, order_url, finalize_url,
			        certificate_url, challenge_type, updated_at_us, error
			   FROM acme_orders ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ACMEOrderRow
				if err := rs.Scan(&r.ID, &r.AccountID, &r.HostnamesJSON, &r.Status,
					&r.OrderURL, &r.FinalizeURL, &r.CertificateURL,
					&r.ChallengeType, &r.UpdatedAtUs, &r.Error); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "acme_certs":
		return enumerate(ctx, s.tx,
			`SELECT hostname, chain_pem, private_key_pem, not_before_us, not_after_us,
			        issuer, order_id FROM acme_certs ORDER BY hostname`,
			func(rs *sql.Rows) (any, error) {
				var r ACMECertRow
				var oid sql.NullInt64
				if err := rs.Scan(&r.Hostname, &r.ChainPEM, &r.PrivateKeyPEM,
					&r.NotBeforeUs, &r.NotAfterUs, &r.Issuer, &oid); err != nil {
					return nil, err
				}
				if oid.Valid {
					v := oid.Int64
					r.OrderID = &v
				}
				return &r, nil
			}, fn)
	case "webhooks":
		return enumerate(ctx, s.tx,
			`SELECT id, owner_kind, owner_id, target_url, hmac_secret, delivery_mode,
			        retry_policy_json, active, created_at_us, updated_at_us
			   FROM webhooks ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r WebhookRow
				var active int64
				if err := rs.Scan(&r.ID, &r.OwnerKind, &r.OwnerID, &r.TargetURL,
					&r.HMACSecret, &r.DeliveryMode, &r.RetryPolicyJSON,
					&active, &r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				r.Active = active != 0
				return &r, nil
			}, fn)
	case "dmarc_reports_raw":
		return enumerate(ctx, s.tx,
			`SELECT id, received_at_us, reporter_email, reporter_org, report_id,
			        domain, date_begin_us, date_end_us, xml_blob_hash, parsed_ok,
			        parse_error FROM dmarc_reports_raw ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r DMARCReportRow
				var ok int64
				if err := rs.Scan(&r.ID, &r.ReceivedAtUs, &r.ReporterEmail,
					&r.ReporterOrg, &r.ReportID, &r.Domain, &r.DateBeginUs,
					&r.DateEndUs, &r.XMLBlobHash, &ok, &r.ParseError); err != nil {
					return nil, err
				}
				r.ParsedOK = ok != 0
				return &r, nil
			}, fn)
	case "dmarc_rows":
		return enumerate(ctx, s.tx,
			`SELECT id, report_id, source_ip, count, disposition, spf_aligned,
			        dkim_aligned, spf_result, dkim_result, header_from,
			        envelope_from, envelope_to FROM dmarc_rows ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r DMARCRowRow
				var spfA, dkimA int64
				if err := rs.Scan(&r.ID, &r.ReportID, &r.SourceIP, &r.Count,
					&r.Disposition, &spfA, &dkimA, &r.SPFResult, &r.DKIMResult,
					&r.HeaderFrom, &r.EnvelopeFrom, &r.EnvelopeTo); err != nil {
					return nil, err
				}
				r.SPFAligned = spfA != 0
				r.DKIMAligned = dkimA != 0
				return &r, nil
			}, fn)
	case "jmap_states":
		return enumerate(ctx, s.tx,
			`SELECT principal_id, mailbox_state, email_state, thread_state,
			        identity_state, email_submission_state, vacation_response_state,
			        updated_at_us FROM jmap_states ORDER BY principal_id`,
			func(rs *sql.Rows) (any, error) {
				var r JMAPStateRow
				if err := rs.Scan(&r.PrincipalID, &r.MailboxState, &r.EmailState,
					&r.ThreadState, &r.IdentityState, &r.EmailSubmissionState,
					&r.VacationResponseState, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "jmap_email_submissions":
		return enumerate(ctx, s.tx,
			`SELECT id, envelope_id, principal_id, identity_id, email_id,
			        thread_id, send_at_us, created_at_us, undo_status, properties
			   FROM jmap_email_submissions ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r JMAPEmailSubmissionRow
				if err := rs.Scan(&r.ID, &r.EnvelopeID, &r.PrincipalID, &r.IdentityID,
					&r.EmailID, &r.ThreadID, &r.SendAtUs, &r.CreatedAtUs,
					&r.UndoStatus, &r.Properties); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "jmap_identities":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, name, email, reply_to_json, bcc_json,
			        text_signature, html_signature, may_delete,
			        created_at_us, updated_at_us FROM jmap_identities ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r JMAPIdentityRow
				var md int64
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Name, &r.Email,
					&r.ReplyToJSON, &r.BccJSON, &r.TextSignature, &r.HTMLSignature,
					&md, &r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				r.MayDelete = md != 0
				return &r, nil
			}, fn)
	case "tlsrpt_failures":
		return enumerate(ctx, s.tx,
			`SELECT id, recorded_at_us, policy_domain, receiving_mta_hostname,
			        failure_type, failure_code, failure_detail_json
			   FROM tlsrpt_failures ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r TLSRPTFailureRow
				if err := rs.Scan(&r.ID, &r.RecordedAtUs, &r.PolicyDomain,
					&r.ReceivingMTAHostname, &r.FailureType, &r.FailureCode,
					&r.FailureDetailJSON); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "blob_refs":
		return enumerate(ctx, s.tx,
			`SELECT hash, size, ref_count, last_change_us FROM blob_refs ORDER BY hash`,
			func(rs *sql.Rows) (any, error) {
				var r BlobRefRow
				if err := rs.Scan(&r.Hash, &r.Size, &r.RefCount, &r.LastChangeUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	}
	return fmt.Errorf("sqlite: unknown table %q", table)
}

func (s *sqliteSource) EnumerateBlobHashes(ctx context.Context, fn func(hash string, size int64) error) error {
	// Collect the hashes referenced by message blobs and the queue
	// body / headers blobs. blob_refs may carry rows the messages /
	// queue tables no longer reference (refcount 0 awaiting GC); we
	// include them so a backup taken during the GC grace window is
	// faithful to disk.
	rs, err := s.tx.QueryContext(ctx, `SELECT hash, size FROM blob_refs ORDER BY hash`)
	if err != nil {
		return fmt.Errorf("sqlite: enumerate blobs: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var h string
		var size int64
		if err := rs.Scan(&h, &size); err != nil {
			return err
		}
		if err := fn(h, size); err != nil {
			return err
		}
	}
	return rs.Err()
}

func (s *sqliteSource) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.tx.Rollback()
}

// sqliteSink implements Sink against a sqlite writer transaction.
type sqliteSink struct {
	tx     *sql.Tx
	wmu    *sync.Mutex
	closed bool
}

func (s *sqliteSink) Insert(ctx context.Context, table string, row any) error {
	switch table {
	case "domains":
		r := row.(*DomainRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO domains (name, is_local, created_at_us) VALUES (?, ?, ?)`,
			r.Name, boolToInt(r.IsLocal), r.CreatedAtUs)
		return err
	case "principals":
		r := row.(*PrincipalRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO principals (id, kind, canonical_email, display_name, password_hash,
			   totp_secret, quota_bytes, flags, used_bytes, created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Kind, r.CanonicalEmail, r.DisplayName, r.PasswordHash,
			r.TOTPSecret, r.QuotaBytes, r.Flags, r.UsedBytes, r.CreatedAtUs, r.UpdatedAtUs)
		return err
	case "oidc_providers":
		r := row.(*OIDCProviderRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO oidc_providers (name, issuer_url, client_id, client_secret_ref,
			   scopes_csv, auto_provision, created_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.Name, r.IssuerURL, r.ClientID, r.ClientSecretRef, r.ScopesCSV,
			boolToInt(r.AutoProvision), r.CreatedAtUs)
		return err
	case "oidc_links":
		r := row.(*OIDCLinkRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO oidc_links (principal_id, provider_name, subject, email_at_provider, linked_at_us)
			 VALUES (?, ?, ?, ?, ?)`,
			r.PrincipalID, r.ProviderName, r.Subject, r.EmailAtProvider, r.LinkedAtUs)
		return err
	case "api_keys":
		r := row.(*APIKeyRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO api_keys (id, principal_id, hash, name, created_at_us, last_used_at_us)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Hash, r.Name, r.CreatedAtUs, r.LastUsedAtUs)
		return err
	case "aliases":
		r := row.(*AliasRow)
		var exp any
		if r.ExpiresAtUs != nil {
			exp = *r.ExpiresAtUs
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO aliases (id, local_part, domain, target_principal, expires_at_us, created_at_us)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			r.ID, r.LocalPart, r.Domain, r.TargetPrincipal, exp, r.CreatedAtUs)
		return err
	case "sieve_scripts":
		r := row.(*SieveScriptRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO sieve_scripts (principal_id, script, updated_at_us) VALUES (?, ?, ?)`,
			r.PrincipalID, r.Script, r.UpdatedAtUs)
		return err
	case "mailboxes":
		r := row.(*MailboxRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO mailboxes (id, principal_id, parent_id, name, attributes,
			   uidvalidity, uidnext, highest_modseq, created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.ParentID, r.Name, r.Attributes,
			r.UIDValidity, r.UIDNext, r.HighestModSeq, r.CreatedAtUs, r.UpdatedAtUs)
		return err
	case "messages":
		r := row.(*MessageRow)
		var snooze any
		if r.SnoozedUntilUs != nil {
			snooze = *r.SnoozedUntilUs
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO messages (id, mailbox_id, uid, modseq, flags, keywords_csv,
			   internal_date_us, received_at_us, size, blob_hash, blob_size, thread_id,
			   env_subject, env_from, env_to, env_cc, env_bcc, env_reply_to,
			   env_message_id, env_in_reply_to, env_date_us, snoozed_until_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.MailboxID, r.UID, r.ModSeq, r.Flags, r.KeywordsCSV,
			r.InternalDateUs, r.ReceivedAtUs, r.Size, r.BlobHash, r.BlobSize, r.ThreadID,
			r.EnvSubject, r.EnvFrom, r.EnvTo, r.EnvCc, r.EnvBcc, r.EnvReplyTo,
			r.EnvMessageID, r.EnvInReplyTo, r.EnvDateUs, snooze)
		return err
	case "mailbox_acl":
		r := row.(*MailboxACLRow)
		var pid any
		if r.PrincipalID != nil {
			pid = *r.PrincipalID
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO mailbox_acl (id, mailbox_id, principal_id, rights_mask, granted_by, created_at_us)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			r.ID, r.MailboxID, pid, r.RightsMask, r.GrantedBy, r.CreatedAtUs)
		return err
	case "state_changes":
		r := row.(*StateChangeRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO state_changes (id, principal_id, seq, entity_kind, entity_id,
			   parent_entity_id, op, produced_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Seq, r.EntityKind, r.EntityID,
			r.ParentEntityID, r.Op, r.ProducedAtUs)
		return err
	case "audit_log":
		r := row.(*AuditLogRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO audit_log (id, at_us, actor_kind, actor_id, action, subject,
			   remote_addr, outcome, message, metadata_json, principal_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.AtUs, r.ActorKind, r.ActorID, r.Action, r.Subject,
			r.RemoteAddr, r.Outcome, r.Message, r.MetadataJSON, r.PrincipalID)
		return err
	case "cursors":
		r := row.(*CursorRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO cursors (key, seq) VALUES (?, ?)`, r.Key, r.Seq)
		return err
	case "queue":
		r := row.(*QueueRow)
		var pid, idem any
		if r.PrincipalID != nil {
			pid = *r.PrincipalID
		}
		if r.IdempotencyKey != nil {
			idem = *r.IdempotencyKey
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO queue (id, principal_id, mail_from, rcpt_to, envelope_id,
			   body_blob_hash, headers_blob_hash, state, attempts,
			   last_attempt_at_us, next_attempt_at_us, last_error,
			   dsn_notify_flags, dsn_ret, dsn_envid, dsn_orcpt,
			   idempotency_key, created_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, pid, r.MailFrom, r.RcptTo, r.EnvelopeID,
			r.BodyBlobHash, r.HeadersBlobHash, r.State, r.Attempts,
			r.LastAttemptAtUs, r.NextAttemptAtUs, r.LastError,
			r.DSNNotifyFlags, r.DSNRet, r.DSNEnvID, r.DSNOrcpt,
			idem, r.CreatedAtUs)
		return err
	case "dkim_keys":
		r := row.(*DKIMKeyRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO dkim_keys (id, domain, selector, algorithm, private_key_pem,
			   public_key_b64, status, created_at_us, rotated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Domain, r.Selector, r.Algorithm, r.PrivateKeyPEM,
			r.PublicKeyB64, r.Status, r.CreatedAtUs, r.RotatedAtUs)
		return err
	case "acme_accounts":
		r := row.(*ACMEAccountRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO acme_accounts (id, directory_url, contact_email, account_key_pem, kid, created_at_us)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			r.ID, r.DirectoryURL, r.ContactEmail, r.AccountKeyPEM, r.KID, r.CreatedAtUs)
		return err
	case "acme_orders":
		r := row.(*ACMEOrderRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO acme_orders (id, account_id, hostnames_json, status, order_url,
			   finalize_url, certificate_url, challenge_type, updated_at_us, error)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.AccountID, r.HostnamesJSON, r.Status, r.OrderURL,
			r.FinalizeURL, r.CertificateURL, r.ChallengeType, r.UpdatedAtUs, r.Error)
		return err
	case "acme_certs":
		r := row.(*ACMECertRow)
		var oid any
		if r.OrderID != nil {
			oid = *r.OrderID
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO acme_certs (hostname, chain_pem, private_key_pem,
			   not_before_us, not_after_us, issuer, order_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.Hostname, r.ChainPEM, r.PrivateKeyPEM, r.NotBeforeUs,
			r.NotAfterUs, r.Issuer, oid)
		return err
	case "webhooks":
		r := row.(*WebhookRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO webhooks (id, owner_kind, owner_id, target_url, hmac_secret,
			   delivery_mode, retry_policy_json, active, created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.OwnerKind, r.OwnerID, r.TargetURL, r.HMACSecret,
			r.DeliveryMode, r.RetryPolicyJSON, boolToInt(r.Active),
			r.CreatedAtUs, r.UpdatedAtUs)
		return err
	case "dmarc_reports_raw":
		r := row.(*DMARCReportRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO dmarc_reports_raw (id, received_at_us, reporter_email, reporter_org,
			   report_id, domain, date_begin_us, date_end_us, xml_blob_hash, parsed_ok, parse_error)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.ReceivedAtUs, r.ReporterEmail, r.ReporterOrg, r.ReportID,
			r.Domain, r.DateBeginUs, r.DateEndUs, r.XMLBlobHash,
			boolToInt(r.ParsedOK), r.ParseError)
		return err
	case "dmarc_rows":
		r := row.(*DMARCRowRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO dmarc_rows (id, report_id, source_ip, count, disposition,
			   spf_aligned, dkim_aligned, spf_result, dkim_result, header_from,
			   envelope_from, envelope_to)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.ReportID, r.SourceIP, r.Count, r.Disposition,
			boolToInt(r.SPFAligned), boolToInt(r.DKIMAligned),
			r.SPFResult, r.DKIMResult, r.HeaderFrom, r.EnvelopeFrom, r.EnvelopeTo)
		return err
	case "jmap_states":
		r := row.(*JMAPStateRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO jmap_states (principal_id, mailbox_state, email_state, thread_state,
			   identity_state, email_submission_state, vacation_response_state, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.PrincipalID, r.MailboxState, r.EmailState, r.ThreadState,
			r.IdentityState, r.EmailSubmissionState, r.VacationResponseState, r.UpdatedAtUs)
		return err
	case "jmap_email_submissions":
		r := row.(*JMAPEmailSubmissionRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO jmap_email_submissions (id, envelope_id, principal_id, identity_id,
			   email_id, thread_id, send_at_us, created_at_us, undo_status, properties)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.EnvelopeID, r.PrincipalID, r.IdentityID, r.EmailID,
			r.ThreadID, r.SendAtUs, r.CreatedAtUs, r.UndoStatus, r.Properties)
		return err
	case "jmap_identities":
		r := row.(*JMAPIdentityRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO jmap_identities (id, principal_id, name, email, reply_to_json,
			   bcc_json, text_signature, html_signature, may_delete, created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Name, r.Email, r.ReplyToJSON, r.BccJSON,
			r.TextSignature, r.HTMLSignature, boolToInt(r.MayDelete),
			r.CreatedAtUs, r.UpdatedAtUs)
		return err
	case "tlsrpt_failures":
		r := row.(*TLSRPTFailureRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO tlsrpt_failures (id, recorded_at_us, policy_domain,
			   receiving_mta_hostname, failure_type, failure_code, failure_detail_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.RecordedAtUs, r.PolicyDomain, r.ReceivingMTAHostname,
			r.FailureType, r.FailureCode, r.FailureDetailJSON)
		return err
	case "blob_refs":
		r := row.(*BlobRefRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO blob_refs (hash, size, ref_count, last_change_us)
			 VALUES (?, ?, ?, ?)`,
			r.Hash, r.Size, r.RefCount, r.LastChangeUs)
		return err
	}
	return fmt.Errorf("sqlite sink: unknown table %q", table)
}

func (s *sqliteSink) Commit(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	defer s.wmu.Unlock()
	return s.tx.Commit()
}

func (s *sqliteSink) Rollback(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	defer s.wmu.Unlock()
	return s.tx.Rollback()
}

// enumerate runs query and calls fn once per row, mapping each row to
// its typed Row struct via mk.
func enumerate(ctx context.Context, tx *sql.Tx, query string,
	mk func(rs *sql.Rows) (any, error), fn func(row any) error) error {
	rs, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		row, err := mk(rs)
		if err != nil {
			return err
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return rs.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
