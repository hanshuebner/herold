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
			        totp_secret, quota_bytes, flags, seen_addresses_enabled,
			        avatar_blob_hash, avatar_blob_size, xface_enabled,
			        used_bytes, created_at_us, updated_at_us FROM principals ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r PrincipalRow
				var seenAddrEnabled, xfaceEnabled int64
				var avatarHash sql.NullString
				if err := rs.Scan(&r.ID, &r.Kind, &r.CanonicalEmail, &r.DisplayName, &r.PasswordHash,
					&r.TOTPSecret, &r.QuotaBytes, &r.Flags, &seenAddrEnabled,
					&avatarHash, &r.AvatarBlobSize, &xfaceEnabled,
					&r.UsedBytes, &r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				r.SeenAddressesEnabled = seenAddrEnabled != 0
				r.XFaceEnabled = xfaceEnabled != 0
				if avatarHash.Valid {
					r.AvatarBlobHash = avatarHash.String
				}
				return &r, nil
			}, fn)
	case "push_subscription":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, device_client_id, url, p256dh, auth,
			        expires_at_us, types_csv, verification_code, verified,
			        vapid_key_at_registration, notification_rules_json,
			        quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
			        created_at_us, updated_at_us
			   FROM push_subscription ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r PushSubscriptionRow
				var (
					expiresUs            sql.NullInt64
					quietStart, quietEnd sql.NullInt64
					verified             int64
					rules                []byte
				)
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.DeviceClientID, &r.URL,
					&r.P256DH, &r.Auth, &expiresUs, &r.TypesCSV,
					&r.VerificationCode, &verified,
					&r.VAPIDKeyAtRegistration, &rules,
					&quietStart, &quietEnd, &r.QuietHoursTZ,
					&r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				if expiresUs.Valid {
					v := expiresUs.Int64
					r.ExpiresAtUs = &v
				}
				if quietStart.Valid {
					v := quietStart.Int64
					r.QuietHoursStartLocal = &v
				}
				if quietEnd.Valid {
					v := quietEnd.Int64
					r.QuietHoursEndLocal = &v
				}
				r.Verified = verified != 0
				if len(rules) > 0 {
					r.NotificationRulesJSON = rules
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
			`SELECT id, principal_id, hash, name, created_at_us, last_used_at_us, scope_json,
			        allowed_from_addresses_json, allowed_from_domains_json
			   FROM api_keys ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r APIKeyRow
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Hash, &r.Name,
					&r.CreatedAtUs, &r.LastUsedAtUs, &r.ScopeJSON,
					&r.AllowedFromAddressesJSON, &r.AllowedFromDomainsJSON); err != nil {
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
	case "jmap_categorisation_config":
		return enumerate(ctx, s.tx,
			`SELECT principal_id, prompt, category_set_json, endpoint_url, model,
			        api_key_env, timeout_sec, enabled, updated_at_us
			   FROM jmap_categorisation_config ORDER BY principal_id`,
			func(rs *sql.Rows) (any, error) {
				var r CategorisationConfigRow
				var endpoint, model, apiKey sql.NullString
				var enabled int64
				if err := rs.Scan(&r.PrincipalID, &r.Prompt, &r.CategorySetJSON,
					&endpoint, &model, &apiKey, &r.TimeoutSec, &enabled, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				if endpoint.Valid {
					v := endpoint.String
					r.EndpointURL = &v
				}
				if model.Valid {
					v := model.String
					r.Model = &v
				}
				if apiKey.Valid {
					v := apiKey.String
					r.APIKeyEnv = &v
				}
				r.Enabled = enabled != 0
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
			`SELECT id, principal_id, internal_date_us, received_at_us, size,
			        blob_hash, blob_size, thread_id, env_subject, env_from,
			        env_to, env_cc, env_bcc, env_reply_to, env_message_id,
			        env_in_reply_to, env_date_us
			   FROM messages ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r MessageRow
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.InternalDateUs, &r.ReceivedAtUs,
					&r.Size, &r.BlobHash, &r.BlobSize, &r.ThreadID,
					&r.EnvSubject, &r.EnvFrom, &r.EnvTo, &r.EnvCc, &r.EnvBcc,
					&r.EnvReplyTo, &r.EnvMessageID, &r.EnvInReplyTo, &r.EnvDateUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "message_mailboxes":
		return enumerate(ctx, s.tx,
			`SELECT message_id, mailbox_id, uid, modseq, flags, keywords_csv,
			        snoozed_until_us
			   FROM message_mailboxes ORDER BY message_id, mailbox_id`,
			func(rs *sql.Rows) (any, error) {
				var r MessageMailboxRow
				var snooze sql.NullInt64
				if err := rs.Scan(&r.MessageID, &r.MailboxID, &r.UID, &r.ModSeq,
					&r.Flags, &r.KeywordsCSV, &snooze); err != nil {
					return nil, err
				}
				if snooze.Valid {
					v := snooze.Int64
					r.SnoozedUntilUs = &v
				}
				return &r, nil
			}, fn)
	case "managed_rules":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, name, enabled, sort_order,
			        conditions_json, actions_json, created_at_us, updated_at_us
			   FROM managed_rules ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ManagedRuleRow
				var enabled int64
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Name, &enabled,
					&r.SortOrder, &r.ConditionsJSON, &r.ActionsJSON,
					&r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				r.Enabled = enabled != 0
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
			        retry_policy_json, active, created_at_us, updated_at_us,
			        target_kind, body_mode, extracted_text_max_bytes, text_required
			   FROM webhooks ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r WebhookRow
				var active, textReq int64
				if err := rs.Scan(&r.ID, &r.OwnerKind, &r.OwnerID, &r.TargetURL,
					&r.HMACSecret, &r.DeliveryMode, &r.RetryPolicyJSON,
					&active, &r.CreatedAtUs, &r.UpdatedAtUs,
					&r.TargetKind, &r.BodyMode, &r.ExtractedTextMaxBytes,
					&textReq); err != nil {
					return nil, err
				}
				r.Active = active != 0
				r.TextRequired = textReq != 0
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
			        updated_at_us, shortcut_coach_state, category_settings_state,
			        managed_rule_state, seen_address_state
			   FROM jmap_states ORDER BY principal_id`,
			func(rs *sql.Rows) (any, error) {
				var r JMAPStateRow
				if err := rs.Scan(&r.PrincipalID, &r.MailboxState, &r.EmailState,
					&r.ThreadState, &r.IdentityState, &r.EmailSubmissionState,
					&r.VacationResponseState, &r.UpdatedAtUs, &r.ShortcutCoachState,
					&r.CategorySettingsState, &r.ManagedRuleState, &r.SeenAddressState); err != nil {
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
			        created_at_us, updated_at_us,
			        avatar_blob_hash, avatar_blob_size, xface_enabled
			   FROM jmap_identities ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r JMAPIdentityRow
				var md, xf int64
				var avatarHash sql.NullString
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Name, &r.Email,
					&r.ReplyToJSON, &r.BccJSON, &r.TextSignature, &r.HTMLSignature,
					&md, &r.CreatedAtUs, &r.UpdatedAtUs,
					&avatarHash, &r.AvatarBlobSize, &xf); err != nil {
					return nil, err
				}
				r.MayDelete = md != 0
				r.XFaceEnabled = xf != 0
				if avatarHash.Valid {
					r.AvatarBlobHash = avatarHash.String
				}
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
	case "address_books":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, name, description, color_hex, sort_order,
			        is_subscribed, is_default, rights_mask,
			        created_at_us, updated_at_us, modseq
			   FROM address_books ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r AddressBookRow
				var color sql.NullString
				var subscribed, isDefault int64
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Name, &r.Description,
					&color, &r.SortOrder, &subscribed, &isDefault, &r.RightsMask,
					&r.CreatedAtUs, &r.UpdatedAtUs, &r.ModSeq); err != nil {
					return nil, err
				}
				if color.Valid {
					v := color.String
					r.ColorHex = &v
				}
				r.IsSubscribed = subscribed != 0
				r.IsDefault = isDefault != 0
				return &r, nil
			}, fn)
	case "contacts":
		return enumerate(ctx, s.tx,
			`SELECT id, address_book_id, principal_id, uid, jscontact_json,
			        display_name, given_name, surname, org_name, primary_email,
			        search_blob, created_at_us, updated_at_us, modseq
			   FROM contacts ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ContactRow
				if err := rs.Scan(&r.ID, &r.AddressBookID, &r.PrincipalID, &r.UID,
					&r.JSContactJSON, &r.DisplayName, &r.GivenName, &r.Surname,
					&r.OrgName, &r.PrimaryEmail, &r.SearchBlob,
					&r.CreatedAtUs, &r.UpdatedAtUs, &r.ModSeq); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "calendars":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, name, description, color_hex, sort_order,
			        is_subscribed, is_default, is_visible, time_zone_id, rights_mask,
			        created_at_us, updated_at_us, modseq
			   FROM calendars ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r CalendarRow
				var color sql.NullString
				var subscribed, isDefault, isVisible int64
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Name, &r.Description,
					&color, &r.SortOrder, &subscribed, &isDefault, &isVisible,
					&r.TimeZoneID, &r.RightsMask,
					&r.CreatedAtUs, &r.UpdatedAtUs, &r.ModSeq); err != nil {
					return nil, err
				}
				if color.Valid {
					v := color.String
					r.ColorHex = &v
				}
				r.IsSubscribed = subscribed != 0
				r.IsDefault = isDefault != 0
				r.IsVisible = isVisible != 0
				return &r, nil
			}, fn)
	case "calendar_events":
		return enumerate(ctx, s.tx,
			`SELECT id, calendar_id, principal_id, uid, jscalendar_json,
			        start_us, end_us, is_recurring, rrule_json,
			        summary, organizer_email, status,
			        created_at_us, updated_at_us, modseq
			   FROM calendar_events ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r CalendarEventRow
				var isRecurring int64
				var organizer sql.NullString
				if err := rs.Scan(&r.ID, &r.CalendarID, &r.PrincipalID, &r.UID,
					&r.JSCalendarJSON, &r.StartUs, &r.EndUs, &isRecurring, &r.RRuleJSON,
					&r.Summary, &organizer, &r.Status,
					&r.CreatedAtUs, &r.UpdatedAtUs, &r.ModSeq); err != nil {
					return nil, err
				}
				r.IsRecurring = isRecurring != 0
				if organizer.Valid {
					v := organizer.String
					r.OrganizerEmail = &v
				}
				return &r, nil
			}, fn)
	case "chat_account_settings":
		return enumerate(ctx, s.tx,
			`SELECT principal_id, default_retention_seconds,
			        default_edit_window_seconds, created_at_us, updated_at_us
			   FROM chat_account_settings ORDER BY principal_id`,
			func(rs *sql.Rows) (any, error) {
				var r ChatAccountSettingsRow
				if err := rs.Scan(&r.PrincipalID, &r.DefaultRetentionSeconds,
					&r.DefaultEditWindowSeconds, &r.CreatedAtUs, &r.UpdatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "chat_conversations":
		return enumerate(ctx, s.tx,
			`SELECT id, kind, name, topic, created_by_principal_id,
			        created_at_us, updated_at_us, last_message_at_us,
			        message_count, is_archived, modseq,
			        read_receipts_enabled, retention_seconds, edit_window_seconds
			   FROM chat_conversations ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ChatConversationRow
				var name, topic sql.NullString
				var lastMsg sql.NullInt64
				var archived, readReceipts int64
				var retention, editWindow sql.NullInt64
				if err := rs.Scan(&r.ID, &r.Kind, &name, &topic, &r.CreatedByPrincipalID,
					&r.CreatedAtUs, &r.UpdatedAtUs, &lastMsg,
					&r.MessageCount, &archived, &r.ModSeq,
					&readReceipts, &retention, &editWindow); err != nil {
					return nil, err
				}
				if name.Valid {
					v := name.String
					r.Name = &v
				}
				if topic.Valid {
					v := topic.String
					r.Topic = &v
				}
				if lastMsg.Valid {
					v := lastMsg.Int64
					r.LastMessageAtUs = &v
				}
				r.IsArchived = archived != 0
				r.ReadReceiptsEnabled = readReceipts != 0
				if retention.Valid {
					v := retention.Int64
					r.RetentionSeconds = &v
				}
				if editWindow.Valid {
					v := editWindow.Int64
					r.EditWindowSeconds = &v
				}
				return &r, nil
			}, fn)
	case "chat_memberships":
		return enumerate(ctx, s.tx,
			`SELECT id, conversation_id, principal_id, role, joined_at_us,
			        last_read_message_id, is_muted, mute_until_us,
			        notifications_setting, modseq
			   FROM chat_memberships ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ChatMembershipRow
				var lastRead, muteUntil sql.NullInt64
				var muted int64
				if err := rs.Scan(&r.ID, &r.ConversationID, &r.PrincipalID, &r.Role,
					&r.JoinedAtUs, &lastRead, &muted, &muteUntil,
					&r.NotificationsSetting, &r.ModSeq); err != nil {
					return nil, err
				}
				if lastRead.Valid {
					v := lastRead.Int64
					r.LastReadMessageID = &v
				}
				if muteUntil.Valid {
					v := muteUntil.Int64
					r.MuteUntilUs = &v
				}
				r.IsMuted = muted != 0
				return &r, nil
			}, fn)
	case "chat_messages":
		return enumerate(ctx, s.tx,
			`SELECT id, conversation_id, sender_principal_id, is_system,
			        body_text, body_html, body_format, reply_to_message_id,
			        reactions_json, attachments_json, metadata_json,
			        edited_at_us, deleted_at_us, created_at_us, modseq
			   FROM chat_messages ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ChatMessageRow
				var sender sql.NullInt64
				var isSystem int64
				var bodyText, bodyHTML sql.NullString
				var replyTo sql.NullInt64
				var editedUs, deletedUs sql.NullInt64
				if err := rs.Scan(&r.ID, &r.ConversationID, &sender, &isSystem,
					&bodyText, &bodyHTML, &r.BodyFormat, &replyTo,
					&r.ReactionsJSON, &r.AttachmentsJSON, &r.MetadataJSON,
					&editedUs, &deletedUs, &r.CreatedAtUs, &r.ModSeq); err != nil {
					return nil, err
				}
				r.IsSystem = isSystem != 0
				if sender.Valid {
					v := sender.Int64
					r.SenderPrincipalID = &v
				}
				if bodyText.Valid {
					v := bodyText.String
					r.BodyText = &v
				}
				if bodyHTML.Valid {
					v := bodyHTML.String
					r.BodyHTML = &v
				}
				if replyTo.Valid {
					v := replyTo.Int64
					r.ReplyToMessageID = &v
				}
				if editedUs.Valid {
					v := editedUs.Int64
					r.EditedAtUs = &v
				}
				if deletedUs.Valid {
					v := deletedUs.Int64
					r.DeletedAtUs = &v
				}
				return &r, nil
			}, fn)
	case "chat_blocks":
		return enumerate(ctx, s.tx,
			`SELECT blocker_principal_id, blocked_principal_id, created_at_us, reason
			   FROM chat_blocks ORDER BY blocker_principal_id, blocked_principal_id`,
			func(rs *sql.Rows) (any, error) {
				var r ChatBlockRow
				var reason sql.NullString
				if err := rs.Scan(&r.BlockerPrincipalID, &r.BlockedPrincipalID,
					&r.CreatedAtUs, &reason); err != nil {
					return nil, err
				}
				if reason.Valid {
					v := reason.String
					r.Reason = &v
				}
				return &r, nil
			}, fn)
	case "chat_dm_pairs":
		return enumerate(ctx, s.tx,
			`SELECT pid_lo, pid_hi, conversation_id
			   FROM chat_dm_pairs ORDER BY pid_lo, pid_hi`,
			func(rs *sql.Rows) (any, error) {
				var r ChatDMPairRow
				if err := rs.Scan(&r.PidLo, &r.PidHi, &r.ConversationID); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "email_reactions":
		return enumerate(ctx, s.tx,
			`SELECT email_id, emoji, principal_id, created_at_us
			   FROM email_reactions ORDER BY email_id, emoji, principal_id`,
			func(rs *sql.Rows) (any, error) {
				var r EmailReactionRow
				if err := rs.Scan(&r.EmailID, &r.Emoji, &r.PrincipalID, &r.CreatedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "coach_events":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, action, input_method, event_count, occurred_at, recorded_at
			   FROM coach_events ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r CoachEventRow
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Action, &r.InputMethod,
					&r.EventCount, &r.OccurredAt, &r.RecordedAt); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "coach_dismiss":
		return enumerate(ctx, s.tx,
			`SELECT principal_id, action, dismiss_count, dismiss_until, updated_at
			   FROM coach_dismiss ORDER BY principal_id, action`,
			func(rs *sql.Rows) (any, error) {
				var r CoachDismissRow
				var dismissUntil sql.NullInt64
				if err := rs.Scan(&r.PrincipalID, &r.Action, &r.DismissCount,
					&dismissUntil, &r.UpdatedAt); err != nil {
					return nil, err
				}
				if dismissUntil.Valid {
					v := dismissUntil.Int64
					r.DismissUntil = &v
				}
				return &r, nil
			}, fn)
	case "ses_seen_messages":
		return enumerate(ctx, s.tx,
			`SELECT message_id, seen_at_us FROM ses_seen_messages ORDER BY message_id`,
			func(rs *sql.Rows) (any, error) {
				var r SESSeenMessageRow
				if err := rs.Scan(&r.MessageID, &r.SeenAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "llm_classifications":
		return enumerate(ctx, s.tx,
			`SELECT message_id, principal_id,
			        spam_verdict, spam_confidence, spam_reason, spam_prompt_applied,
			        spam_model, spam_classified_at_us,
			        category_assigned, category_prompt_applied, category_model,
			        category_classified_at_us
			   FROM llm_classifications ORDER BY message_id`,
			func(rs *sql.Rows) (any, error) {
				var r LLMClassificationRow
				if err := rs.Scan(&r.MessageID, &r.PrincipalID,
					&r.SpamVerdict, &r.SpamConfidence, &r.SpamReason, &r.SpamPromptApplied,
					&r.SpamModel, &r.SpamClassifiedAtUs,
					&r.CategoryAssigned, &r.CategoryPromptApplied, &r.CategoryModel,
					&r.CategoryClassifiedAtUs); err != nil {
					return nil, err
				}
				return &r, nil
			}, fn)
	case "seen_addresses":
		return enumerate(ctx, s.tx,
			`SELECT id, principal_id, email, display_name,
			        first_seen_at_us, last_used_at_us, send_count, received_count
			   FROM seen_addresses ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r SeenAddressRow
				if err := rs.Scan(&r.ID, &r.PrincipalID, &r.Email, &r.DisplayName,
					&r.FirstSeenAtUs, &r.LastUsedAtUs, &r.SendCount, &r.ReceivedCount); err != nil {
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
	case "sessions":
		// Sessions are excluded from backup by default (stale sessions confuse
		// TelemetryGate), so this branch is only reached if a future caller
		// explicitly enumerates the table.  It is provided for completeness
		// and to satisfy the "unknown table" guard.
		return enumerate(ctx, s.tx,
			`SELECT session_id, principal_id, created_at_us, expires_at_us,
			        clientlog_telemetry_enabled, clientlog_livetail_until_us
			   FROM sessions ORDER BY session_id`,
			func(rs *sql.Rows) (any, error) {
				var r SessionRow
				var telemetryInt int64
				var livetailUs sql.NullInt64
				if err := rs.Scan(&r.SessionID, &r.PrincipalID, &r.CreatedAtUs, &r.ExpiresAtUs,
					&telemetryInt, &livetailUs); err != nil {
					return nil, err
				}
				r.ClientlogTelemetryEnabled = telemetryInt != 0
				if livetailUs.Valid {
					v := livetailUs.Int64
					r.ClientlogLivetailUntilUs = &v
				}
				return &r, nil
			}, fn)
	case "clientlog":
		return enumerate(ctx, s.tx,
			`SELECT id, slice, server_ts, client_ts, clock_skew_ms,
			        app, kind, level, user_id, session_id, page_id,
			        request_id, route, build_sha, ua, msg, stack, payload_json
			   FROM clientlog ORDER BY id`,
			func(rs *sql.Rows) (any, error) {
				var r ClientLogRow
				var userID, sessionID, requestID, route, stack sql.NullString
				if err := rs.Scan(&r.ID, &r.Slice, &r.ServerTS, &r.ClientTS, &r.ClockSkewMS,
					&r.App, &r.Kind, &r.Level, &userID, &sessionID, &r.PageID,
					&requestID, &route, &r.BuildSHA, &r.UA, &r.Msg, &stack, &r.PayloadJSON); err != nil {
					return nil, err
				}
				if userID.Valid {
					v := userID.String
					r.UserID = &v
				}
				if sessionID.Valid {
					v := sessionID.String
					r.SessionID = &v
				}
				if requestID.Valid {
					v := requestID.String
					r.RequestID = &v
				}
				if route.Valid {
					v := route.String
					r.Route = &v
				}
				if stack.Valid {
					v := stack.String
					r.Stack = &v
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
		var avatarHash any
		if r.AvatarBlobHash != "" {
			avatarHash = r.AvatarBlobHash
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO principals (id, kind, canonical_email, display_name, password_hash,
			   totp_secret, quota_bytes, flags, seen_addresses_enabled,
			   avatar_blob_hash, avatar_blob_size, xface_enabled,
			   used_bytes, created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Kind, r.CanonicalEmail, r.DisplayName, r.PasswordHash,
			r.TOTPSecret, r.QuotaBytes, r.Flags, boolToInt(r.SeenAddressesEnabled),
			avatarHash, r.AvatarBlobSize, boolToInt(r.XFaceEnabled),
			r.UsedBytes, r.CreatedAtUs, r.UpdatedAtUs)
		return err
	case "push_subscription":
		r := row.(*PushSubscriptionRow)
		var expires, qhStart, qhEnd, rules any
		if r.ExpiresAtUs != nil {
			expires = *r.ExpiresAtUs
		}
		if r.QuietHoursStartLocal != nil {
			qhStart = *r.QuietHoursStartLocal
		}
		if r.QuietHoursEndLocal != nil {
			qhEnd = *r.QuietHoursEndLocal
		}
		if len(r.NotificationRulesJSON) > 0 {
			rules = r.NotificationRulesJSON
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO push_subscription (id, principal_id, device_client_id, url, p256dh, auth,
			   expires_at_us, types_csv, verification_code, verified,
			   vapid_key_at_registration, notification_rules_json,
			   quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
			   created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.DeviceClientID, r.URL, r.P256DH, r.Auth,
			expires, r.TypesCSV, r.VerificationCode, boolToInt(r.Verified),
			r.VAPIDKeyAtRegistration, rules,
			qhStart, qhEnd, r.QuietHoursTZ,
			r.CreatedAtUs, r.UpdatedAtUs)
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
		scope := r.ScopeJSON
		if scope == "" {
			// Wave-3.6 backward-compat: a backup taken before the
			// migration carries no scope_json; restore inserts the
			// admin sentinel so the legacy capability is preserved.
			scope = `["admin"]`
		}
		addrJSON := r.AllowedFromAddressesJSON
		if addrJSON == "" {
			addrJSON = "[]"
		}
		domJSON := r.AllowedFromDomainsJSON
		if domJSON == "" {
			domJSON = "[]"
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO api_keys (id, principal_id, hash, name, created_at_us, last_used_at_us,
			   scope_json, allowed_from_addresses_json, allowed_from_domains_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Hash, r.Name, r.CreatedAtUs, r.LastUsedAtUs,
			scope, addrJSON, domJSON)
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
	case "jmap_categorisation_config":
		r := row.(*CategorisationConfigRow)
		var endpoint, model, apiKey any
		if r.EndpointURL != nil {
			endpoint = *r.EndpointURL
		}
		if r.Model != nil {
			model = *r.Model
		}
		if r.APIKeyEnv != nil {
			apiKey = *r.APIKeyEnv
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO jmap_categorisation_config (principal_id, prompt, category_set_json,
			   endpoint_url, model, api_key_env, timeout_sec, enabled, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.PrincipalID, r.Prompt, r.CategorySetJSON,
			endpoint, model, apiKey, r.TimeoutSec, boolToInt(r.Enabled), r.UpdatedAtUs)
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
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO messages (id, principal_id, internal_date_us, received_at_us,
			   size, blob_hash, blob_size, thread_id, env_subject, env_from, env_to,
			   env_cc, env_bcc, env_reply_to, env_message_id, env_in_reply_to, env_date_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.InternalDateUs, r.ReceivedAtUs,
			r.Size, r.BlobHash, r.BlobSize, r.ThreadID,
			r.EnvSubject, r.EnvFrom, r.EnvTo, r.EnvCc, r.EnvBcc, r.EnvReplyTo,
			r.EnvMessageID, r.EnvInReplyTo, r.EnvDateUs)
		return err
	case "message_mailboxes":
		r := row.(*MessageMailboxRow)
		var snooze any
		if r.SnoozedUntilUs != nil {
			snooze = *r.SnoozedUntilUs
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO message_mailboxes (message_id, mailbox_id, uid, modseq,
			   flags, keywords_csv, snoozed_until_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.MessageID, r.MailboxID, r.UID, r.ModSeq,
			r.Flags, r.KeywordsCSV, snooze)
		return err
	case "managed_rules":
		r := row.(*ManagedRuleRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO managed_rules (id, principal_id, name, enabled, sort_order,
			   conditions_json, actions_json, created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Name, boolToInt(r.Enabled), r.SortOrder,
			r.ConditionsJSON, r.ActionsJSON, r.CreatedAtUs, r.UpdatedAtUs)
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
			   delivery_mode, retry_policy_json, active, created_at_us, updated_at_us,
			   target_kind, body_mode, extracted_text_max_bytes, text_required)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.OwnerKind, r.OwnerID, r.TargetURL, r.HMACSecret,
			r.DeliveryMode, r.RetryPolicyJSON, boolToInt(r.Active),
			r.CreatedAtUs, r.UpdatedAtUs,
			r.TargetKind, r.BodyMode, r.ExtractedTextMaxBytes,
			boolToInt(r.TextRequired))
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
			   identity_state, email_submission_state, vacation_response_state, updated_at_us,
			   shortcut_coach_state, category_settings_state, managed_rule_state,
			   seen_address_state)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.PrincipalID, r.MailboxState, r.EmailState, r.ThreadState,
			r.IdentityState, r.EmailSubmissionState, r.VacationResponseState, r.UpdatedAtUs,
			r.ShortcutCoachState, r.CategorySettingsState, r.ManagedRuleState,
			r.SeenAddressState)
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
		var avatarHash any
		if r.AvatarBlobHash != "" {
			avatarHash = r.AvatarBlobHash
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO jmap_identities (id, principal_id, name, email, reply_to_json,
			   bcc_json, text_signature, html_signature, may_delete, created_at_us, updated_at_us,
			   avatar_blob_hash, avatar_blob_size, xface_enabled)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Name, r.Email, r.ReplyToJSON, r.BccJSON,
			r.TextSignature, r.HTMLSignature, boolToInt(r.MayDelete),
			r.CreatedAtUs, r.UpdatedAtUs,
			avatarHash, r.AvatarBlobSize, boolToInt(r.XFaceEnabled))
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
	case "address_books":
		r := row.(*AddressBookRow)
		var color any
		if r.ColorHex != nil {
			color = *r.ColorHex
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO address_books (id, principal_id, name, description, color_hex,
			   sort_order, is_subscribed, is_default, rights_mask,
			   created_at_us, updated_at_us, modseq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Name, r.Description, color,
			r.SortOrder, boolToInt(r.IsSubscribed), boolToInt(r.IsDefault),
			r.RightsMask, r.CreatedAtUs, r.UpdatedAtUs, r.ModSeq)
		return err
	case "contacts":
		r := row.(*ContactRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO contacts (id, address_book_id, principal_id, uid, jscontact_json,
			   display_name, given_name, surname, org_name, primary_email, search_blob,
			   created_at_us, updated_at_us, modseq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.AddressBookID, r.PrincipalID, r.UID, r.JSContactJSON,
			r.DisplayName, r.GivenName, r.Surname, r.OrgName, r.PrimaryEmail, r.SearchBlob,
			r.CreatedAtUs, r.UpdatedAtUs, r.ModSeq)
		return err
	case "calendars":
		r := row.(*CalendarRow)
		var color any
		if r.ColorHex != nil {
			color = *r.ColorHex
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO calendars (id, principal_id, name, description, color_hex,
			   sort_order, is_subscribed, is_default, is_visible, time_zone_id,
			   rights_mask, created_at_us, updated_at_us, modseq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Name, r.Description, color,
			r.SortOrder, boolToInt(r.IsSubscribed), boolToInt(r.IsDefault),
			boolToInt(r.IsVisible), r.TimeZoneID, r.RightsMask,
			r.CreatedAtUs, r.UpdatedAtUs, r.ModSeq)
		return err
	case "calendar_events":
		r := row.(*CalendarEventRow)
		var organizer any
		if r.OrganizerEmail != nil {
			organizer = *r.OrganizerEmail
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO calendar_events (id, calendar_id, principal_id, uid, jscalendar_json,
			   start_us, end_us, is_recurring, rrule_json,
			   summary, organizer_email, status,
			   created_at_us, updated_at_us, modseq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.CalendarID, r.PrincipalID, r.UID, r.JSCalendarJSON,
			r.StartUs, r.EndUs, boolToInt(r.IsRecurring), r.RRuleJSON,
			r.Summary, organizer, r.Status,
			r.CreatedAtUs, r.UpdatedAtUs, r.ModSeq)
		return err
	case "chat_account_settings":
		r := row.(*ChatAccountSettingsRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO chat_account_settings (principal_id,
			   default_retention_seconds, default_edit_window_seconds,
			   created_at_us, updated_at_us)
			 VALUES (?, ?, ?, ?, ?)`,
			r.PrincipalID, r.DefaultRetentionSeconds, r.DefaultEditWindowSeconds,
			r.CreatedAtUs, r.UpdatedAtUs)
		return err
	case "chat_conversations":
		r := row.(*ChatConversationRow)
		var name, topic, lastMsg any
		if r.Name != nil {
			name = *r.Name
		}
		if r.Topic != nil {
			topic = *r.Topic
		}
		if r.LastMessageAtUs != nil {
			lastMsg = *r.LastMessageAtUs
		}
		var retention, editWindow any
		if r.RetentionSeconds != nil {
			retention = *r.RetentionSeconds
		}
		if r.EditWindowSeconds != nil {
			editWindow = *r.EditWindowSeconds
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO chat_conversations (id, kind, name, topic,
			   created_by_principal_id, created_at_us, updated_at_us,
			   last_message_at_us, message_count, is_archived, modseq,
			   read_receipts_enabled, retention_seconds, edit_window_seconds)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Kind, name, topic, r.CreatedByPrincipalID,
			r.CreatedAtUs, r.UpdatedAtUs, lastMsg,
			r.MessageCount, boolToInt(r.IsArchived), r.ModSeq,
			boolToInt(r.ReadReceiptsEnabled), retention, editWindow)
		return err
	case "chat_memberships":
		r := row.(*ChatMembershipRow)
		var lastRead, muteUntil any
		if r.LastReadMessageID != nil {
			lastRead = *r.LastReadMessageID
		}
		if r.MuteUntilUs != nil {
			muteUntil = *r.MuteUntilUs
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO chat_memberships (id, conversation_id, principal_id, role,
			   joined_at_us, last_read_message_id, is_muted, mute_until_us,
			   notifications_setting, modseq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.ConversationID, r.PrincipalID, r.Role,
			r.JoinedAtUs, lastRead, boolToInt(r.IsMuted), muteUntil,
			r.NotificationsSetting, r.ModSeq)
		return err
	case "chat_messages":
		r := row.(*ChatMessageRow)
		var sender, replyTo, editedUs, deletedUs any
		var bodyText, bodyHTML any
		if r.SenderPrincipalID != nil {
			sender = *r.SenderPrincipalID
		}
		if r.ReplyToMessageID != nil {
			replyTo = *r.ReplyToMessageID
		}
		if r.EditedAtUs != nil {
			editedUs = *r.EditedAtUs
		}
		if r.DeletedAtUs != nil {
			deletedUs = *r.DeletedAtUs
		}
		if r.BodyText != nil {
			bodyText = *r.BodyText
		}
		if r.BodyHTML != nil {
			bodyHTML = *r.BodyHTML
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO chat_messages (id, conversation_id, sender_principal_id,
			   is_system, body_text, body_html, body_format, reply_to_message_id,
			   reactions_json, attachments_json, metadata_json,
			   edited_at_us, deleted_at_us, created_at_us, modseq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.ConversationID, sender, boolToInt(r.IsSystem),
			bodyText, bodyHTML, r.BodyFormat, replyTo,
			nullableBytes(r.ReactionsJSON), nullableBytes(r.AttachmentsJSON),
			nullableBytes(r.MetadataJSON), editedUs, deletedUs,
			r.CreatedAtUs, r.ModSeq)
		return err
	case "chat_blocks":
		r := row.(*ChatBlockRow)
		var reason any
		if r.Reason != nil {
			reason = *r.Reason
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO chat_blocks (blocker_principal_id, blocked_principal_id,
			   created_at_us, reason)
			 VALUES (?, ?, ?, ?)`,
			r.BlockerPrincipalID, r.BlockedPrincipalID, r.CreatedAtUs, reason)
		return err
	case "chat_dm_pairs":
		r := row.(*ChatDMPairRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO chat_dm_pairs (pid_lo, pid_hi, conversation_id)
			 VALUES (?, ?, ?)`,
			r.PidLo, r.PidHi, r.ConversationID)
		return err
	case "email_reactions":
		r := row.(*EmailReactionRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO email_reactions (email_id, emoji, principal_id, created_at_us)
			 VALUES (?, ?, ?, ?)`,
			r.EmailID, r.Emoji, r.PrincipalID, r.CreatedAtUs)
		return err
	case "coach_events":
		r := row.(*CoachEventRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO coach_events (id, principal_id, action, input_method,
			   event_count, occurred_at, recorded_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Action, r.InputMethod,
			r.EventCount, r.OccurredAt, r.RecordedAt)
		return err
	case "coach_dismiss":
		r := row.(*CoachDismissRow)
		var dismissUntil any
		if r.DismissUntil != nil {
			dismissUntil = *r.DismissUntil
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO coach_dismiss (principal_id, action, dismiss_count, dismiss_until, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			r.PrincipalID, r.Action, r.DismissCount, dismissUntil, r.UpdatedAt)
		return err
	case "ses_seen_messages":
		r := row.(*SESSeenMessageRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO ses_seen_messages (message_id, seen_at_us)
			 VALUES (?, ?)
			 ON CONFLICT(message_id) DO NOTHING`,
			r.MessageID, r.SeenAtUs)
		return err
	case "llm_classifications":
		r := row.(*LLMClassificationRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO llm_classifications (message_id, principal_id,
			   spam_verdict, spam_confidence, spam_reason, spam_prompt_applied,
			   spam_model, spam_classified_at_us,
			   category_assigned, category_prompt_applied, category_model,
			   category_classified_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.MessageID, r.PrincipalID,
			r.SpamVerdict, r.SpamConfidence, r.SpamReason, r.SpamPromptApplied,
			r.SpamModel, r.SpamClassifiedAtUs,
			r.CategoryAssigned, r.CategoryPromptApplied, r.CategoryModel,
			r.CategoryClassifiedAtUs)
		return err
	case "seen_addresses":
		r := row.(*SeenAddressRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO seen_addresses (id, principal_id, email, display_name,
			   first_seen_at_us, last_used_at_us, send_count, received_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.PrincipalID, r.Email, r.DisplayName,
			r.FirstSeenAtUs, r.LastUsedAtUs, r.SendCount, r.ReceivedCount)
		return err
	case "blob_refs":
		r := row.(*BlobRefRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO blob_refs (hash, size, ref_count, last_change_us)
			 VALUES (?, ?, ?, ?)`,
			r.Hash, r.Size, r.RefCount, r.LastChangeUs)
		return err
	case "sessions":
		r := row.(*SessionRow)
		var telemetryInt int64
		if r.ClientlogTelemetryEnabled {
			telemetryInt = 1
		}
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO sessions (session_id, principal_id, created_at_us, expires_at_us,
			   clientlog_telemetry_enabled, clientlog_livetail_until_us)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			r.SessionID, r.PrincipalID, r.CreatedAtUs, r.ExpiresAtUs,
			telemetryInt, r.ClientlogLivetailUntilUs)
		return err
	case "clientlog":
		r := row.(*ClientLogRow)
		_, err := s.tx.ExecContext(ctx,
			`INSERT INTO clientlog (id, slice, server_ts, client_ts, clock_skew_ms,
			   app, kind, level, user_id, session_id, page_id, request_id, route,
			   build_sha, ua, msg, stack, payload_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Slice, r.ServerTS, r.ClientTS, r.ClockSkewMS,
			r.App, r.Kind, r.Level, r.UserID, r.SessionID, r.PageID,
			r.RequestID, r.Route, r.BuildSHA, r.UA, r.Msg, r.Stack, r.PayloadJSON)
		return err
	}
	return fmt.Errorf("sqlite sink: unknown table %q", table)
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
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
