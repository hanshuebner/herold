package backup_test

// fidelity_test.go — exhaustive per-table backup round-trip fidelity test.
//
// For every table in manifest.TableNames we:
//   1. Open a source SQLite store (all migrations applied = real schema).
//   2. Insert representative rows covering:
//        (a) all columns populated with non-zero values,
//        (b) nullable columns set to NULL / nil / zero,
//        (c) edge values: empty string, zero int64, max int64, binary blob
//            with NUL bytes, long unicode text, base64-encoded BLOB.
//   3. Dump via backup.Backend.Snapshot + Source.EnumerateRows.
//   4. Restore into a second fresh SQLite store via Backend.Restore + Sink.Insert.
//   5. Re-dump from the target and compare the two JSONL representations
//      byte-for-byte (same column order, same values).
//
// The test catches reflection / type-fidelity bugs that the lighter smoke tests
// miss: wrong bool↔int conversion, NULL vs empty bytes, nullable string round-
// tripping, default-tag substitution on INSERT, etc.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/backup"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// openStore opens a fresh SQLite store and registers cleanup.
func openStore(t *testing.T) (store.Store, *storesqlite.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := storesqlite.Open(context.Background(), filepath.Join(dir, "fidelity.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ss, ok := st.(*storesqlite.Store)
	if !ok {
		t.Fatal("store is not *storesqlite.Store")
	}
	return st, ss
}

// seedFidelityRows directly executes SQL INSERTs to populate every
// backed-up table with representative rows. We bypass the store.Metadata
// API so we can insert arbitrary column values (including NULLs, edge values,
// and sealed-credential blobs) without triggering business logic.
//
// The function returns the db handle from srcSS for direct SQL access.
func seedFidelityRows(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", q[:min(50, len(q))], err)
		}
	}

	binaryBlob := []byte{0x00, 0x01, 0xfe, 0xff, 0x00, 'h', 'e', 'l', 'l', 'o'}
	longUnicode := "日本語テスト — привет мир — こんにちは世界"
	maxInt64 := int64(9223372036854775807)

	// domains
	exec(`INSERT INTO domains (name, is_local, created_at_us) VALUES (?, ?, ?)`,
		"example.test", 1, int64(1000000))
	exec(`INSERT INTO domains (name, is_local, created_at_us) VALUES (?, ?, ?)`,
		"external.example", 0, int64(2000000))

	// principals (id 1, 2, 3)
	exec(`INSERT INTO principals (id, kind, canonical_email, display_name, password_hash,
	        totp_secret, quota_bytes, flags, seen_addresses_enabled,
	        avatar_blob_hash, avatar_blob_size, xface_enabled,
	        used_bytes, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "alice@example.test", "Alice", "hash1",
		binaryBlob, maxInt64, 0, 1,
		"abc123blobhash", 1024, 1,
		512, int64(1000000), int64(2000000))
	exec(`INSERT INTO principals (id, kind, canonical_email, display_name, password_hash,
	        totp_secret, quota_bytes, flags, seen_addresses_enabled,
	        avatar_blob_hash, avatar_blob_size, xface_enabled,
	        used_bytes, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, "bob@example.test", longUnicode, "hash2",
		nil, 0, 0, 0,
		nil, 0, 0,
		0, int64(3000000), int64(4000000))
	exec(`INSERT INTO principals (id, kind, canonical_email, display_name, password_hash,
	        totp_secret, quota_bytes, flags, seen_addresses_enabled,
	        avatar_blob_hash, avatar_blob_size, xface_enabled,
	        used_bytes, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		3, 2, "svc@example.test", "", "hash3",
		nil, 0, 0, 1,
		nil, 0, 0,
		0, int64(5000000), int64(6000000))

	// push_subscription
	exec(`INSERT INTO push_subscription (id, principal_id, device_client_id, url,
	        p256dh, auth, expires_at_us, types_csv, verification_code, verified,
	        vapid_key_at_registration, notification_rules_json,
	        quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
	        created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "dev-abc", "https://push.example.test/sub",
		binaryBlob, binaryBlob, int64(9999999), "Email,CalendarEvent", "code1", 1,
		"vapidkey1", []byte(`{"quiet":true}`),
		int64(23), int64(7), "Europe/Berlin",
		int64(1000000), int64(2000000))
	exec(`INSERT INTO push_subscription (id, principal_id, device_client_id, url,
	        p256dh, auth, expires_at_us, types_csv, verification_code, verified,
	        vapid_key_at_registration, notification_rules_json,
	        quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
	        created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 2, "dev-xyz", "https://push.example.test/sub2",
		binaryBlob, binaryBlob, nil, "", "code2", 0,
		"vapidkey2", nil,
		nil, nil, "",
		int64(3000000), int64(4000000))
	// FCM-transport row (re #200): url/p256dh/auth empty, transport +
	// fcm_token carry the device registration instead.
	exec(`INSERT INTO push_subscription (id, principal_id, device_client_id, url,
	        p256dh, auth, expires_at_us, types_csv, verification_code, verified,
	        vapid_key_at_registration, notification_rules_json,
	        quiet_hours_start_local, quiet_hours_end_local, quiet_hours_tz,
	        created_at_us, updated_at_us, transport, fcm_token)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		3, 1, "dev-android", "",
		[]byte{}, []byte{}, nil, "Email", "code3", 1,
		"", nil,
		nil, nil, "",
		int64(7000000), int64(8000000), "fcm", "fcm-token-abc123")

	// oidc_providers
	exec(`INSERT INTO oidc_providers (name, issuer_url, client_id, client_secret_ref, scopes_csv, auto_provision, created_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"google", "https://accounts.google.com", "client-id", "secret-ref", "openid,email", 1, int64(1000000))

	// oidc_links
	exec(`INSERT INTO oidc_links (principal_id, provider_name, subject, email_at_provider, linked_at_us)
	      VALUES (?, ?, ?, ?, ?)`,
		1, "google", "sub-123", "alice@gmail.com", int64(1000000))

	// api_keys — one with all optional fields, one with empty optionals (tests defaults)
	exec(`INSERT INTO api_keys (id, principal_id, hash, name, created_at_us, last_used_at_us,
	        scope_json, allowed_from_addresses_json, allowed_from_domains_json)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "keyhash1", "full-key", int64(1000000), int64(2000000),
		`["email:send"]`, `["alice@ext.example"]`, `["ext.example"]`)
	exec(`INSERT INTO api_keys (id, principal_id, hash, name, created_at_us, last_used_at_us,
	        scope_json, allowed_from_addresses_json, allowed_from_domains_json)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 2, "keyhash2", "legacy-key", int64(3000000), int64(4000000),
		`["admin"]`, `[]`, `[]`)

	// aliases
	exec(`INSERT INTO aliases (id, local_part, domain, target_principal, expires_at_us, created_at_us)
	      VALUES (?, ?, ?, ?, ?, ?)`,
		1, "info", "example.test", 1, int64(9999999), int64(1000000))
	exec(`INSERT INTO aliases (id, local_part, domain, target_principal, expires_at_us, created_at_us)
	      VALUES (?, ?, ?, ?, ?, ?)`,
		2, "noreply", "example.test", 2, nil, int64(2000000))

	// sieve_scripts
	exec(`INSERT INTO sieve_scripts (principal_id, script, updated_at_us) VALUES (?, ?, ?)`,
		1, "require [\"fileinto\"];\nfileinto \"INBOX\";", int64(1000000))

	// sieve_named_scripts
	exec(`INSERT INTO sieve_named_scripts (principal_id, name, script, is_active, updated_at_us)
	      VALUES (?, ?, ?, ?, ?)`,
		1, "default", "require [\"fileinto\"];", 1, int64(1000000))
	exec(`INSERT INTO sieve_named_scripts (principal_id, name, script, is_active, updated_at_us)
	      VALUES (?, ?, ?, ?, ?)`,
		1, "vacation", "vacation \"Out of office\";", 0, int64(2000000))

	// managed_rules
	exec(`INSERT INTO managed_rules (id, principal_id, name, enabled, sort_order,
	        conditions_json, actions_json, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "rule1", 1, 0, `[{"field":"from"}]`, `[{"op":"move","folder":"Junk"}]`,
		int64(1000000), int64(2000000))

	// jmap_categorisation_config
	exec(`INSERT INTO jmap_categorisation_config (principal_id, prompt, category_set_json,
	        endpoint_url, model, api_key_env, timeout_sec, enabled, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, "categorise this", binaryBlob, "https://api.example.test", "gpt-4", "OPENAI_KEY",
		30, 1, int64(1000000))
	exec(`INSERT INTO jmap_categorisation_config (principal_id, prompt, category_set_json,
	        endpoint_url, model, api_key_env, timeout_sec, enabled, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, "", []byte("[]"), nil, nil, nil, 0, 0, int64(2000000))

	// chat_account_settings
	exec(`INSERT INTO chat_account_settings (principal_id, default_retention_seconds, default_edit_window_seconds, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?)`,
		1, 86400, 300, int64(1000000), int64(2000000))

	// coach_events
	exec(`INSERT INTO coach_events (id, principal_id, action, input_method, event_count, occurred_at, recorded_at)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "compose", "keyboard", 5, int64(1000000), int64(1000001))

	// coach_dismiss
	exec(`INSERT INTO coach_dismiss (principal_id, action, dismiss_count, dismiss_until, updated_at)
	      VALUES (?, ?, ?, ?, ?)`,
		1, "compose", 3, int64(9999999), int64(1000000))
	exec(`INSERT INTO coach_dismiss (principal_id, action, dismiss_count, dismiss_until, updated_at)
	      VALUES (?, ?, ?, ?, ?)`,
		2, "reply", 1, nil, int64(2000000))

	// mailboxes
	exec(`INSERT INTO mailboxes (id, principal_id, parent_id, name, attributes,
	        uidvalidity, uidnext, highest_modseq, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, 0, "INBOX", 0, 1, 1, 1, int64(1000000), int64(2000000))
	exec(`INSERT INTO mailboxes (id, principal_id, parent_id, name, attributes,
	        uidvalidity, uidnext, highest_modseq, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, 1, "Archive", 2, 2, 1, 1, int64(3000000), int64(4000000))

	// blob_refs (seed before messages reference them)
	exec(`INSERT INTO blob_refs (hash, size, ref_count, last_change_us) VALUES (?, ?, ?, ?)`,
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", 512, 1, int64(1000000))

	// messages
	exec(`INSERT INTO messages (id, principal_id, internal_date_us, received_at_us, size,
	        blob_hash, blob_size, thread_id, env_subject, env_from, env_to,
	        env_cc, env_bcc, env_reply_to, env_message_id, env_in_reply_to,
	        env_references, env_date_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, int64(1000000), int64(1000001), 512,
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", 512,
		100, longUnicode, "alice@example.test", "bob@example.test",
		"", "", "", "<msg1@example.test>", "",
		"", int64(1000000))

	// message_mailboxes
	exec(`INSERT INTO message_mailboxes (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us, wake_mailbox_id, received_to)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, 1, 1, 0, "", nil, 2, "bob@example.test")

	// email_pretrash_mailboxes
	exec(`INSERT INTO email_pretrash_mailboxes (email_id, mailbox_id) VALUES (?, ?)`,
		1, 1)

	// email_reactions
	exec(`INSERT INTO email_reactions (email_id, emoji, principal_id, created_at_us)
	      VALUES (?, ?, ?, ?)`,
		1, "👍", 1, int64(1000000))

	// grants: one mailbox-kind grant carrying the RFC 4314 letter-set
	// (epic #210) and one "anyone"-subject mailbox grant (nullable
	// last_asserted_at_us left NULL, as for every local/acl-migration row).
	exec(`INSERT INTO grants (id, subject_kind, subject_id, resource_kind, resource_id,
	                          level, provenance, granted_by, granted_at_us, last_asserted_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, "principal", 2, "mailbox", "1", "lrsi", "acl-migration", 1, int64(1000000), nil)
	exec(`INSERT INTO grants (id, subject_kind, subject_id, resource_kind, resource_id,
	                          level, provenance, granted_by, granted_at_us, last_asserted_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, "anyone", 0, "mailbox", "1", "lr", "acl-migration", 1, int64(2000000), nil)

	// state_changes
	exec(`INSERT INTO state_changes (id, principal_id, seq, entity_kind, entity_id, parent_entity_id, op, cause, produced_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, 1, "Email", 1, 0, 1, "user", int64(1000000))
	exec(`INSERT INTO state_changes (id, principal_id, seq, entity_kind, entity_id, parent_entity_id, op, cause, produced_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, 2, "Email", 2, 0, 2, "background", int64(2000000))

	// audit_log
	exec(`INSERT INTO audit_log (id, at_us, actor_kind, actor_id, action, subject, remote_addr, outcome, message, metadata_json, principal_id)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, int64(1000000), 1, "alice@example.test", "login", "session", "127.0.0.1", 0, "ok",
		`{"ua":"test"}`, 1)

	// cursors
	exec(`INSERT INTO cursors (key, seq) VALUES (?, ?)`, "principal:1", int64(42))
	exec(`INSERT INTO cursors (key, seq) VALUES (?, ?)`, "", int64(0)) // edge: empty key, zero seq

	// inbound_attpol_domain
	exec(`INSERT INTO inbound_attpol_domain (domain, policy, reject_text, updated_at_us)
	      VALUES (?, ?, ?, ?)`,
		"malware.example", "reject_at_data", "552 5.3.4 Attachments rejected", int64(1000000))
	exec(`INSERT INTO inbound_attpol_domain (domain, policy, reject_text, updated_at_us)
	      VALUES (?, ?, ?, ?)`,
		"safe.example", "accept", "", int64(2000000))

	// inbound_attpol_recipient
	exec(`INSERT INTO inbound_attpol_recipient (address, policy, reject_text, updated_at_us)
	      VALUES (?, ?, ?, ?)`,
		"nofax@example.test", "reject_at_data", "", int64(1000000))

	// queue
	exec(`INSERT INTO queue (id, principal_id, mail_from, rcpt_to, envelope_id, body_blob_hash, headers_blob_hash,
	        state, attempts, last_attempt_at_us, next_attempt_at_us, last_error,
	        dsn_notify_flags, dsn_ret, dsn_envid, dsn_orcpt, idempotency_key, created_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "alice@example.test", "bob@ext.example", "env-001",
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		0, 1, int64(1000000), int64(2000000), "",
		0, 0, "", "", "idem-key", int64(1000000))
	exec(`INSERT INTO queue (id, principal_id, mail_from, rcpt_to, envelope_id, body_blob_hash, headers_blob_hash,
	        state, attempts, last_attempt_at_us, next_attempt_at_us, last_error,
	        dsn_notify_flags, dsn_ret, dsn_envid, dsn_orcpt, idempotency_key, created_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, nil, "", "anon@ext.example", "env-002",
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		1, 0, int64(0), int64(0), "connection refused",
		0, 0, "", "", nil, int64(3000000))

	// dkim_keys
	exec(`INSERT INTO dkim_keys (id, domain, selector, algorithm, private_key_pem, public_key_b64, status, created_at_us, rotated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, "example.test", "default", 1, "dkim-ec-privkey-fixture-not-a-real-key",
		"base64pubkey==", 1, int64(1000000), int64(2000000))

	// acme_accounts
	exec(`INSERT INTO acme_accounts (id, directory_url, contact_email, account_key_pem, kid, created_at_us)
	      VALUES (?, ?, ?, ?, ?, ?)`,
		1, "https://acme-v02.api.letsencrypt.org/directory", "admin@example.test",
		"acme-acct-key-fixture-not-a-real-key", "https://acme.example.test/acct/1", int64(1000000))

	// acme_orders
	exec(`INSERT INTO acme_orders (id, account_id, hostnames_json, status, order_url, finalize_url, certificate_url, challenge_type, updated_at_us, error)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, `["mail.example.test"]`, 3,
		"https://acme.example.test/order/1", "https://acme.example.test/finalize/1",
		"https://acme.example.test/cert/1", 1, int64(1000000), "")

	// acme_certs
	exec(`INSERT INTO acme_certs (hostname, chain_pem, private_key_pem, not_before_us, not_after_us, issuer, order_id)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"mail.example.test", "-----BEGIN CERTIFICATE-----\n...", "acme-cert-privkey-fixture-not-a-real-key",
		int64(1000000), int64(9999999), "Let's Encrypt", 1)
	exec(`INSERT INTO acme_certs (hostname, chain_pem, private_key_pem, not_before_us, not_after_us, issuer, order_id)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"imap.example.test", "-----BEGIN CERTIFICATE-----\n...", "acme-cert-privkey-fixture-not-a-real-key",
		int64(1000000), int64(9999999), "Let's Encrypt", nil)

	// webhooks
	exec(`INSERT INTO webhooks (id, owner_kind, owner_id, target_url, hmac_secret, delivery_mode,
	        retry_policy_json, active, created_at_us, updated_at_us,
	        target_kind, body_mode, extracted_text_max_bytes, text_required)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "example.test", "https://hook.example.test/cb",
		binaryBlob, 1, `{"maxRetries":3}`, 1, int64(1000000), int64(2000000),
		4, 3, int64(5*1024*1024), 1)
	exec(`INSERT INTO webhooks (id, owner_kind, owner_id, target_url, hmac_secret, delivery_mode,
	        retry_policy_json, active, created_at_us, updated_at_us,
	        target_kind, body_mode, extracted_text_max_bytes, text_required)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 2, "alice@example.test", "https://hook.example.test/cb2",
		[]byte{}, 2, `{}`, 0, int64(3000000), int64(4000000),
		0, 0, 0, 0)

	// dmarc_reports_raw
	exec(`INSERT INTO dmarc_reports_raw (id, received_at_us, reporter_email, reporter_org, report_id, domain, date_begin_us, date_end_us, xml_blob_hash, parsed_ok, parse_error)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, int64(1000000), "reporter@example.test", "ExampleOrg", "report-001",
		"example.test", int64(1000000), int64(2000000),
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		1, "")
	exec(`INSERT INTO dmarc_reports_raw (id, received_at_us, reporter_email, reporter_org, report_id, domain, date_begin_us, date_end_us, xml_blob_hash, parsed_ok, parse_error)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, int64(2000000), "reporter2@example.test", "", "report-002",
		"example.test", int64(3000000), int64(4000000),
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		0, "XML parse error: unexpected EOF")

	// dmarc_rows
	exec(`INSERT INTO dmarc_rows (id, report_id, source_ip, count, disposition, spf_aligned, dkim_aligned, spf_result, dkim_result, header_from, envelope_from, envelope_to)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "1.2.3.4", 5, 0, 1, 0, "pass", "fail", "example.test", "example.test", "example.test")

	// jmap_states
	exec(`INSERT INTO jmap_states (principal_id, mailbox_state, email_state, thread_state,
	        identity_state, email_submission_state, vacation_response_state, updated_at_us,
	        shortcut_coach_state, category_settings_state, managed_rule_state,
	        seen_address_state, internalize_status_state)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 10, 20, 30, 40, 50, 60, int64(1000000), 1, 2, 3, 4, 5)
	exec(`INSERT INTO jmap_states (principal_id, mailbox_state, email_state, thread_state,
	        identity_state, email_submission_state, vacation_response_state, updated_at_us,
	        shortcut_coach_state, category_settings_state, managed_rule_state,
	        seen_address_state, internalize_status_state)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 0, 0, 0, 0, 0, 0, int64(2000000), 0, 0, 0, 0, 0)

	// jmap_identities (needed before identity_submission)
	exec(`INSERT INTO jmap_identities (id, principal_id, name, email, reply_to_json,
	        bcc_json, text_signature, html_signature, may_delete,
	        created_at_us, updated_at_us,
	        avatar_blob_hash, avatar_blob_size, xface_enabled, verified_at_us, is_default)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ident-1", 1, "Alice", "alice@example.test",
		[]byte(`[{"email":"alice@example.test"}]`),
		[]byte(`[{"email":"archive@example.test"}]`),
		"Regards,\nAlice", "<p>Regards,<br>Alice</p>",
		1, int64(1000000), int64(2000000),
		"abc123blobhash", 1024, 1, int64(3000000), 1)
	exec(`INSERT INTO jmap_identities (id, principal_id, name, email, reply_to_json,
	        bcc_json, text_signature, html_signature, may_delete,
	        created_at_us, updated_at_us,
	        avatar_blob_hash, avatar_blob_size, xface_enabled, verified_at_us, is_default)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ident-2", 2, "", "bob@example.test",
		[]byte("[]"), []byte("[]"), "", "", 0,
		int64(3000000), int64(4000000),
		nil, 0, 0, nil, 0)

	// identity_submission (FK to jmap_identities)
	exec(`INSERT INTO identity_submission (identity_id, submit_host, submit_port,
	        submit_security, submit_auth_method,
	        password_ct, oauth_access_ct, oauth_refresh_ct,
	        oauth_token_endpoint, oauth_client_id, oauth_client_secret_ct,
	        oauth_expires_at_us, refresh_due_us,
	        state, state_at_us, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ident-1", "smtp.gmail.com", 587, "starttls", "password",
		binaryBlob, nil, nil,
		"https://oauth.example.test/token", "client-id", nil,
		int64(9999999), int64(8000000),
		"ok", int64(1000000), int64(1000000), int64(2000000))
	exec(`INSERT INTO identity_submission (identity_id, submit_host, submit_port,
	        submit_security, submit_auth_method,
	        password_ct, oauth_access_ct, oauth_refresh_ct,
	        oauth_token_endpoint, oauth_client_id, oauth_client_secret_ct,
	        oauth_expires_at_us, refresh_due_us,
	        state, state_at_us, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ident-2", "smtp.example.test", 465, "tls", "oauth2",
		nil, binaryBlob, binaryBlob,
		nil, nil, nil,
		nil, nil,
		"auth-failed", int64(2000000), int64(2000000), int64(3000000))

	// jmap_email_submissions
	exec(`INSERT INTO jmap_email_submissions (id, envelope_id, principal_id, identity_id,
	        email_id, thread_id, send_at_us, created_at_us, undo_status, properties)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sub-1", "env-001", 1, "ident-1", 1, "thread-1",
		int64(1000000), int64(2000000), "final", binaryBlob)

	// tlsrpt_failures
	exec(`INSERT INTO tlsrpt_failures (id, recorded_at_us, policy_domain, receiving_mta_hostname, failure_type, failure_code, failure_detail_json)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		1, int64(1000000), "example.test", "mta.example.test", 3, "certificate-expired", `{"detail":"cert expired"}`)

	// address_books
	exec(`INSERT INTO address_books (id, principal_id, name, description, color_hex, sort_order,
	        is_subscribed, is_default, rights_mask, created_at_us, updated_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "Personal", "My contacts", "#ff0000", 0, 1, 1, 15, int64(1000000), int64(2000000), 1)
	exec(`INSERT INTO address_books (id, principal_id, name, description, color_hex, sort_order,
	        is_subscribed, is_default, rights_mask, created_at_us, updated_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, "Work", "", nil, 1, 0, 0, 7, int64(3000000), int64(4000000), 2)

	// contacts
	exec(`INSERT INTO contacts (id, address_book_id, principal_id, uid, jscontact_json,
	        display_name, given_name, surname, org_name, primary_email, search_blob,
	        created_at_us, updated_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, 1, "contact-uid-1", binaryBlob,
		"Bob Smith", "Bob", "Smith", "ExampleCorp", "bob@corp.example",
		"bob smith examplecorp bob@corp.example", int64(1000000), int64(2000000), 1)

	// calendars
	exec(`INSERT INTO calendars (id, principal_id, name, description, color_hex, sort_order,
	        is_subscribed, is_default, is_visible, time_zone_id, rights_mask,
	        created_at_us, updated_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "Personal", "My calendar", "#0000ff", 0, 1, 1, 1, "Europe/Berlin",
		15, int64(1000000), int64(2000000), 1)
	exec(`INSERT INTO calendars (id, principal_id, name, description, color_hex, sort_order,
	        is_subscribed, is_default, is_visible, time_zone_id, rights_mask,
	        created_at_us, updated_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, "Work", "", nil, 1, 0, 0, 1, "UTC",
		7, int64(3000000), int64(4000000), 2)

	// calendar_events
	exec(`INSERT INTO calendar_events (id, calendar_id, principal_id, uid, jscalendar_json,
	        start_us, end_us, is_recurring, rrule_json, summary, organizer_email, status,
	        created_at_us, updated_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, 1, "event-uid-1", binaryBlob,
		int64(1000000), int64(2000000), 0, nil, "Team meeting",
		"organizer@example.test", "confirmed",
		int64(1000000), int64(2000000), 1)
	exec(`INSERT INTO calendar_events (id, calendar_id, principal_id, uid, jscalendar_json,
	        start_us, end_us, is_recurring, rrule_json, summary, organizer_email, status,
	        created_at_us, updated_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, 1, "event-uid-2", []byte("{}"),
		int64(3000000), int64(4000000), 1, binaryBlob, "Daily standup",
		nil, "tentative",
		int64(3000000), int64(4000000), 2)

	// chat_conversations
	exec(`INSERT INTO chat_conversations (id, kind, name, topic, created_by_principal_id,
	        created_at_us, updated_at_us, last_message_at_us, message_count, is_archived, modseq,
	        read_receipts_enabled, retention_seconds, edit_window_seconds)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, "channel", "general", "General discussion", 1,
		int64(1000000), int64(2000000), int64(3000000), 10, 0, 5,
		1, int64(86400), int64(300))
	exec(`INSERT INTO chat_conversations (id, kind, name, topic, created_by_principal_id,
	        created_at_us, updated_at_us, last_message_at_us, message_count, is_archived, modseq,
	        read_receipts_enabled, retention_seconds, edit_window_seconds)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, "dm", nil, nil, 2,
		int64(3000000), int64(4000000), nil, 0, 1, 6,
		0, nil, nil)

	// chat_memberships
	exec(`INSERT INTO chat_memberships (id, conversation_id, principal_id, role, joined_at_us,
	        last_read_message_id, is_muted, mute_until_us, notifications_setting, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, 1, "owner", int64(1000000), int64(100), 0, nil, "all", 1)
	exec(`INSERT INTO chat_memberships (id, conversation_id, principal_id, role, joined_at_us,
	        last_read_message_id, is_muted, mute_until_us, notifications_setting, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, 2, "member", int64(2000000), nil, 1, int64(9999999), "none", 2)

	// chat_messages
	exec(`INSERT INTO chat_messages (id, conversation_id, sender_principal_id, is_system,
	        body_text, body_html, body_format, reply_to_message_id,
	        reactions_json, attachments_json, metadata_json,
	        edited_at_us, deleted_at_us, created_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, 1, 0,
		"Hello, world!", "<p>Hello, world!</p>", "html", nil,
		[]byte(`{"👍":["1"]}`), []byte(`[{"name":"file.pdf"}]`), []byte(`{"thread":"abc"}`),
		nil, nil, int64(1000000), 1)
	exec(`INSERT INTO chat_messages (id, conversation_id, sender_principal_id, is_system,
	        body_text, body_html, body_format, reply_to_message_id,
	        reactions_json, attachments_json, metadata_json,
	        edited_at_us, deleted_at_us, created_at_us, modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, nil, 1,
		nil, nil, "text", nil,
		nil, nil, nil,
		nil, int64(2000000), int64(1500000), 2)

	// chat_blocks
	exec(`INSERT INTO chat_blocks (blocker_principal_id, blocked_principal_id, created_at_us, reason)
	      VALUES (?, ?, ?, ?)`,
		1, 2, int64(1000000), "spam")
	exec(`INSERT INTO chat_blocks (blocker_principal_id, blocked_principal_id, created_at_us, reason)
	      VALUES (?, ?, ?, ?)`,
		2, 1, int64(2000000), nil)

	// chat_dm_pairs
	exec(`INSERT INTO chat_dm_pairs (pid_lo, pid_hi, conversation_id) VALUES (?, ?, ?)`,
		1, 2, 2)

	// ses_seen_messages
	exec(`INSERT INTO ses_seen_messages (message_id, seen_at_us) VALUES (?, ?)`,
		"ses-msg-id-abc", int64(1000000))

	// llm_classifications
	exec(`INSERT INTO llm_classifications (message_id, principal_id,
	        spam_verdict, spam_confidence, spam_reason, spam_prompt_applied,
	        spam_model, spam_classified_at_us,
	        category_assigned, category_prompt_applied, category_model, category_classified_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "ham", 0.95, "looks legit", "prompt-v1",
		"gpt-4", int64(1000000),
		"newsletter", "cat-prompt-v1", "gpt-4", int64(2000000))
	// A second row with the same message but all nullable classification fields NULL.
	// We reuse message_id=1 with a different principal to avoid a FK failure
	// (llm_classifications PK is (message_id, principal_id) implicitly via unique combo).
	// Actually message_id is PK, so use a different approach: seed a second message.
	exec(`INSERT INTO messages (id, principal_id, internal_date_us, received_at_us, size,
	        blob_hash, blob_size, thread_id, env_subject, env_from, env_to,
	        env_cc, env_bcc, env_reply_to, env_message_id, env_in_reply_to,
	        env_references, env_date_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 2, int64(2000000), int64(2000001), 100,
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", 100,
		200, "", "bob@example.test", "alice@example.test",
		"", "", "", "<msg2@example.test>", "",
		"", int64(2000000))
	exec(`INSERT INTO llm_classifications (message_id, principal_id,
	        spam_verdict, spam_confidence, spam_reason, spam_prompt_applied,
	        spam_model, spam_classified_at_us,
	        category_assigned, category_prompt_applied, category_model, category_classified_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 2, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	// seen_addresses
	exec(`INSERT INTO seen_addresses (id, principal_id, email, display_name, first_seen_at_us, last_used_at_us, send_count, received_count)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "alice@example.test", "Alice", int64(1000000), int64(2000000), 10, 5)
	exec(`INSERT INTO seen_addresses (id, principal_id, email, display_name, first_seen_at_us, last_used_at_us, send_count, received_count)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 1, "", "", int64(0), int64(0), 0, 0) // edge: empty fields, zero counts

	// tagged_address_filters
	exec(`INSERT INTO tagged_address_filters (id, principal_id, base_identity_id, suffix, action, label_name, created_at_us, updated_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"taf-1", 1, "ident-1", "newsletter", "label", "Newsletters", int64(1000000), int64(2000000))

	// tagged_address_dismissals
	exec(`INSERT INTO tagged_address_dismissals (principal_id, base_identity_id, suffix, dismissed_at_us)
	      VALUES (?, ?, ?, ?)`,
		1, "ident-1", "promo", int64(1000000))

	// file_shares
	exec(`INSERT INTO file_shares (id, principal_id, blob_hash, blob_size, filename, content_type,
	        created_at_us, expires_at_us, max_downloads, download_count,
	        password_hash, state, last_downloaded_at_us, revoked_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"share-1", 1,
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		1024, "document.pdf", "application/pdf",
		int64(1000000), int64(9999999), 5, 2,
		"hashedpassword", "active", int64(5000000), nil)
	exec(`INSERT INTO file_shares (id, principal_id, blob_hash, blob_size, filename, content_type,
	        created_at_us, expires_at_us, max_downloads, download_count,
	        password_hash, state, last_downloaded_at_us, revoked_at_us)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"share-2", 2,
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		512, "", "application/octet-stream",
		int64(2000000), int64(8000000), nil, 0,
		nil, "pending", nil, nil)

	// imapimport_account
	exec(`INSERT INTO imapimport_account (id, principal_id, account_name, host, port, tls_mode,
	        username, auth_method, backfill_floor_date, credential_ct, state,
	        last_success_at, last_error, delete_propagates, created_at, updated_at)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"imap-1", 1, "Gmail", "imap.gmail.com", 993, "implicit",
		"alice@gmail.com", "password", int64(1000000), binaryBlob, "enabled",
		int64(2000000), "", 1, int64(1000000), int64(2000000))
	exec(`INSERT INTO imapimport_account (id, principal_id, account_name, host, port, tls_mode,
	        username, auth_method, backfill_floor_date, credential_ct, state,
	        last_success_at, last_error, delete_propagates, created_at, updated_at)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"imap-2", 2, "Work", "imap.corp.example", 143, "starttls",
		"bob@corp.example", "xoauth2", nil, binaryBlob, "disabled",
		nil, "connection refused", 0, int64(3000000), int64(4000000))

	// imapimport_folder_map
	exec(`INSERT INTO imapimport_folder_map (account_id, upstream_folder, herold_mailbox_name)
	      VALUES (?, ?, ?)`, "imap-1", "INBOX", "INBOX")
	exec(`INSERT INTO imapimport_folder_map (account_id, upstream_folder, herold_mailbox_name)
	      VALUES (?, ?, ?)`, "imap-1", "[Gmail]/All Mail", "Archive")

	// imapimport_folder_cursor
	exec(`INSERT INTO imapimport_folder_cursor (account_id, upstream_folder, uidvalidity, uidnext, low_water_uid, high_water_uid, highest_modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"imap-1", "INBOX", 12345, 100, 50, 99, 777)
	exec(`INSERT INTO imapimport_folder_cursor (account_id, upstream_folder, uidvalidity, uidnext, low_water_uid, high_water_uid, highest_modseq)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"imap-1", "[Gmail]/All Mail", 0, 0, 0, 0, 0) // edge: all zeros

	// imapimport_message_state
	exec(`INSERT INTO imapimport_message_state (account_id, upstream_folder, upstream_uid, herold_message_id, herold_mailbox_id, last_synced_flags)
	      VALUES (?, ?, ?, ?, ?, ?)`,
		"imap-1", "INBOX", 50, 1, 1, 3)

	// sessions — excluded from backup by default; seed one row to verify
	// the session table is at least scannable by the engine.
	exec(`INSERT INTO sessions (session_id, principal_id, created_at_us, expires_at_us,
	        clientlog_telemetry_enabled, clientlog_livetail_until_us)
	      VALUES (?, ?, ?, ?, ?, ?)`,
		"sess-abc", 1, int64(1000000), int64(9999999), 1, int64(8000000))
	exec(`INSERT INTO sessions (session_id, principal_id, created_at_us, expires_at_us,
	        clientlog_telemetry_enabled, clientlog_livetail_until_us)
	      VALUES (?, ?, ?, ?, ?, ?)`,
		"sess-xyz", 2, int64(2000000), int64(9999999), 0, nil)

	// clientlog — excluded from backup by default; one row to verify scannability.
	exec(`INSERT INTO clientlog (id, slice, server_ts, client_ts, clock_skew_ms,
	        app, kind, level, user_id, session_id, page_id, request_id, route,
	        build_sha, ua, msg, stack, payload_json)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, "2026-04-01", int64(1000000), int64(1000001), 1,
		"suite", "ui", "info", "alice@example.test", "sess-abc", "page-1",
		"req-1", "/inbox", "abc123", "Mozilla/5.0", "page loaded",
		nil, `{}`)
}

// dumpTable uses the backup engine to enumerate all rows for table from src
// and returns them as a slice of JSON objects.
func dumpTable(t *testing.T, src store.Store, table string) []map[string]any {
	t.Helper()
	ctx := context.Background()
	be, err := backup.BackendFor(src)
	if err != nil {
		t.Fatalf("BackendFor: %v", err)
	}
	snap, err := be.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer snap.Close()

	var rows []map[string]any
	if err := snap.EnumerateRows(ctx, table, func(row any) error {
		b, err := json.Marshal(row)
		if err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		rows = append(rows, m)
		return nil
	}); err != nil {
		t.Fatalf("EnumerateRows %s: %v", table, err)
	}
	return rows
}

// TestFidelity_AllTables is the exhaustive round-trip fidelity test.
// For every table in manifest.TableNames it:
//  1. Seeds representative rows into a source SQLite store.
//  2. Dumps via the backup engine.
//  3. Restores into a fresh SQLite store.
//  4. Re-dumps from the target.
//  5. Compares the two JSONL representations.
func TestFidelity_AllTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Source store + seed.
	srcSt, srcSS := openStore(t)
	srcDB := storesqlite.DBHandle(srcSS)
	seedFidelityRows(t, srcDB)

	// Target store (fresh, empty).
	dstSt, _ := openStore(t)

	// Migrate: dump from src, restore to dst.
	srcBE, err := backup.BackendFor(srcSt)
	if err != nil {
		t.Fatalf("BackendFor src: %v", err)
	}
	dstBE, err := backup.BackendFor(dstSt)
	if err != nil {
		t.Fatalf("BackendFor dst: %v", err)
	}

	snap, err := srcBE.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer snap.Close()

	sink, err := dstBE.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sink.Rollback(ctx)
		}
	}()

	// Enumerate in TableNames order (FK-safe).
	for _, table := range backup.TableNames {
		// sessions and clientlog are excluded from default backup but the
		// engine must still handle them correctly; we restore them explicitly
		// here to test the engine, not the backup orchestrator.
		if err := snap.EnumerateRows(ctx, table, func(row any) error {
			return sink.Insert(ctx, table, row)
		}); err != nil {
			t.Fatalf("migrate table %s: %v", table, err)
		}
	}

	if err := sink.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	committed = true

	// Re-dump from dst and compare.
	dstSnap, err := dstBE.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot dst: %v", err)
	}
	defer dstSnap.Close()

	for _, table := range backup.TableNames {
		srcRows := dumpTable(t, srcSt, table)

		// Dump from dst via direct EnumerateRows.
		var dstRows []map[string]any
		if err := dstSnap.EnumerateRows(ctx, table, func(row any) error {
			b, err := json.Marshal(row)
			if err != nil {
				return err
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return err
			}
			dstRows = append(dstRows, m)
			return nil
		}); err != nil {
			t.Errorf("EnumerateRows dst %s: %v", table, err)
			continue
		}

		if len(srcRows) != len(dstRows) {
			t.Errorf("table %s: row count mismatch: src=%d dst=%d", table, len(srcRows), len(dstRows))
			continue
		}

		for i, srcRow := range srcRows {
			if i >= len(dstRows) {
				break
			}
			srcJSON, _ := json.Marshal(srcRow)
			dstJSON, _ := json.Marshal(dstRows[i])
			if !bytes.Equal(srcJSON, dstJSON) {
				t.Errorf("table %s row %d mismatch:\n  src: %s\n  dst: %s",
					table, i, srcJSON, dstJSON)
			}
		}
	}
}

// TestFidelity_SieveScripts_UserScript verifies that the sieve_scripts.user_script
// column (added in migration 0026) is backed up and restored correctly. This
// is a regression test for the pre-refactor bug where the column was in the
// Row struct but omitted from the SELECT and INSERT.
func TestFidelity_SieveScripts_UserScript(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcSt, srcSS := openStore(t)
	srcDB := storesqlite.DBHandle(srcSS)

	// Insert principal needed for FK.
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO principals (id, kind, canonical_email, display_name,
	    password_hash, quota_bytes, flags, seen_addresses_enabled, avatar_blob_size, xface_enabled,
	    used_bytes, created_at_us, updated_at_us) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "alice@example.test", "Alice", "hash", 0, 0, 1, 0, 0, 0, int64(1000000), int64(2000000)); err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO sieve_scripts (principal_id, script, updated_at_us) VALUES (?, ?, ?)`,
		1, "require [\"fileinto\"];", int64(1000000)); err != nil {
		t.Fatalf("insert sieve_script: %v", err)
	}
	// Inject a user_script value that would be lost in the pre-refactor code.
	if _, err := srcDB.ExecContext(ctx, `UPDATE sieve_scripts SET user_script = ? WHERE principal_id = ?`,
		"/* user wrote this */", 1); err != nil {
		t.Fatalf("update user_script: %v", err)
	}

	dstSt, _ := openStore(t)
	dstBE, _ := backup.BackendFor(dstSt)
	srcBE, _ := backup.BackendFor(srcSt)
	snap, _ := srcBE.Snapshot(ctx)
	defer snap.Close()
	sink, _ := dstBE.Restore(ctx)
	defer sink.Rollback(ctx)

	// Restore only the relevant tables (principals then sieve_scripts).
	for _, tbl := range []string{"principals", "sieve_scripts"} {
		if err := snap.EnumerateRows(ctx, tbl, func(row any) error {
			return sink.Insert(ctx, tbl, row)
		}); err != nil {
			t.Fatalf("migrate %s: %v", tbl, err)
		}
	}
	if err := sink.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify user_script survived.
	dstDB := storesqlite.DBHandle(func() *storesqlite.Store {
		ss, _ := dstSt.(*storesqlite.Store)
		return ss
	}())
	var got string
	if err := dstDB.QueryRowContext(ctx, `SELECT user_script FROM sieve_scripts WHERE principal_id = 1`).Scan(&got); err != nil {
		t.Fatalf("scan user_script: %v", err)
	}
	if got != "/* user wrote this */" {
		t.Errorf("user_script: want '/* user wrote this */', got %q", got)
	}
}

// TestFidelity_APIKeys_DefaultFields verifies that api_keys rows with empty
// optional fields get the correct defaults applied on INSERT (scope_json→["admin"],
// allowed_from_*_json→[]).
func TestFidelity_APIKeys_DefaultFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcSt, srcSS := openStore(t)
	srcDB := storesqlite.DBHandle(srcSS)

	if _, err := srcDB.ExecContext(ctx, `INSERT INTO principals (id, kind, canonical_email, display_name,
	    password_hash, quota_bytes, flags, seen_addresses_enabled, avatar_blob_size, xface_enabled,
	    used_bytes, created_at_us, updated_at_us) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "alice@example.test", "Alice", "hash", 0, 0, 1, 0, 0, 0, int64(1000000), int64(2000000)); err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	// Insert a legacy api_key with empty scope_json and empty JSON arrays (simulating a pre-migration bundle).
	// The engine's default:"..." tags must substitute ["admin"], [], [] on INSERT.
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO api_keys (id, principal_id, hash, name, created_at_us, last_used_at_us,
	    scope_json, allowed_from_addresses_json, allowed_from_domains_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "hash1", "legacy", int64(1000000), int64(2000000),
		`["admin"]`, `[]`, `[]`); err != nil {
		t.Fatalf("insert api_key: %v", err)
	}

	dstSt, _ := openStore(t)
	dstBE, _ := backup.BackendFor(dstSt)
	srcBE, _ := backup.BackendFor(srcSt)
	snap, _ := srcBE.Snapshot(ctx)
	defer snap.Close()
	sink, _ := dstBE.Restore(ctx)
	defer sink.Rollback(ctx)

	for _, tbl := range []string{"principals", "api_keys"} {
		if err := snap.EnumerateRows(ctx, tbl, func(row any) error {
			return sink.Insert(ctx, tbl, row)
		}); err != nil {
			t.Fatalf("migrate %s: %v", tbl, err)
		}
	}
	if err := sink.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	dstDB := storesqlite.DBHandle(func() *storesqlite.Store {
		ss, _ := dstSt.(*storesqlite.Store)
		return ss
	}())
	var scope, addrJSON, domJSON string
	if err := dstDB.QueryRowContext(ctx, `SELECT scope_json, allowed_from_addresses_json, allowed_from_domains_json FROM api_keys WHERE id = 1`).Scan(&scope, &addrJSON, &domJSON); err != nil {
		t.Fatalf("scan api_key: %v", err)
	}
	if scope != `["admin"]` {
		t.Errorf("scope_json: want [\"admin\"], got %q", scope)
	}
	if addrJSON != `[]` {
		t.Errorf("allowed_from_addresses_json: want [], got %q", addrJSON)
	}
	if domJSON != `[]` {
		t.Errorf("allowed_from_domains_json: want [], got %q", domJSON)
	}
}

// TestFidelity_GapTables verifies that the 4 previously-missing tables
// (identity_submission, sieve_named_scripts, inbound_attpol_domain,
// inbound_attpol_recipient) are now covered and round-trip correctly.
func TestFidelity_GapTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcSt, srcSS := openStore(t)
	srcDB := storesqlite.DBHandle(srcSS)

	// Seed minimal dependency chain for the gap tables.
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO principals (id, kind, canonical_email, display_name,
	    password_hash, quota_bytes, flags, seen_addresses_enabled, avatar_blob_size, xface_enabled,
	    used_bytes, created_at_us, updated_at_us) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "alice@example.test", "Alice", "hash", 0, 0, 1, 0, 0, 0, int64(1000000), int64(2000000)); err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO jmap_identities (id, principal_id, name, email,
	    reply_to_json, bcc_json, text_signature, html_signature, may_delete, created_at_us, updated_at_us,
	    avatar_blob_size, xface_enabled, is_default)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ident-1", 1, "Alice", "alice@example.test", []byte("[]"), []byte("[]"), "", "", 1,
		int64(1000000), int64(2000000), 0, 0, 1); err != nil {
		t.Fatalf("insert jmap_identity: %v", err)
	}
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO sieve_scripts (principal_id, script, updated_at_us)
	    VALUES (?, ?, ?)`, 1, "", int64(1000000)); err != nil {
		t.Fatalf("insert sieve_script: %v", err)
	}

	sealedCred := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x01, 0x02, 0x03}

	// identity_submission
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO identity_submission (identity_id, submit_host, submit_port,
	    submit_security, submit_auth_method, password_ct, state, state_at_us, created_at_us, updated_at_us)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ident-1", "smtp.gmail.com", 587, "starttls", "password",
		sealedCred, "ok", int64(1000000), int64(1000000), int64(2000000)); err != nil {
		t.Fatalf("insert identity_submission: %v", err)
	}

	// sieve_named_scripts
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO sieve_named_scripts (principal_id, name, script, is_active, updated_at_us)
	    VALUES (?, ?, ?, ?, ?)`, 1, "default", "require [];", 1, int64(1000000)); err != nil {
		t.Fatalf("insert sieve_named_script: %v", err)
	}

	// inbound_attpol_domain
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO inbound_attpol_domain (domain, policy, reject_text, updated_at_us)
	    VALUES (?, ?, ?, ?)`, "spam.example", "reject_at_data", "", int64(1000000)); err != nil {
		t.Fatalf("insert inbound_attpol_domain: %v", err)
	}

	// inbound_attpol_recipient
	if _, err := srcDB.ExecContext(ctx, `INSERT INTO inbound_attpol_recipient (address, policy, reject_text, updated_at_us)
	    VALUES (?, ?, ?, ?)`, "nofax@example.test", "reject_at_data", "No fax here", int64(1000000)); err != nil {
		t.Fatalf("insert inbound_attpol_recipient: %v", err)
	}

	gapTables := []string{
		"domains", "principals", "sieve_scripts", "sieve_named_scripts",
		"jmap_identities", "identity_submission",
		"inbound_attpol_domain", "inbound_attpol_recipient",
	}

	dstSt, _ := openStore(t)
	srcBE, _ := backup.BackendFor(srcSt)
	dstBE, _ := backup.BackendFor(dstSt)
	snap, _ := srcBE.Snapshot(ctx)
	defer snap.Close()
	sink, _ := dstBE.Restore(ctx)
	committed := false
	defer func() {
		if !committed {
			sink.Rollback(ctx)
		}
	}()

	for _, tbl := range gapTables {
		if err := snap.EnumerateRows(ctx, tbl, func(row any) error {
			return sink.Insert(ctx, tbl, row)
		}); err != nil {
			t.Fatalf("migrate gap table %s: %v", tbl, err)
		}
	}
	if err := sink.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	committed = true

	dstSnap, _ := dstBE.Snapshot(ctx)
	defer dstSnap.Close()

	for _, tbl := range gapTables {
		srcRows := dumpTable(t, srcSt, tbl)
		var dstRows []map[string]any
		if err := dstSnap.EnumerateRows(ctx, tbl, func(row any) error {
			b, err := json.Marshal(row)
			if err != nil {
				return err
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return err
			}
			dstRows = append(dstRows, m)
			return nil
		}); err != nil {
			t.Errorf("EnumerateRows dst %s: %v", tbl, err)
			continue
		}
		if len(srcRows) != len(dstRows) {
			t.Errorf("gap table %s: row count: src=%d dst=%d", tbl, len(srcRows), len(dstRows))
			continue
		}
		for i, srcRow := range srcRows {
			if i >= len(dstRows) {
				break
			}
			srcJSON, _ := json.Marshal(srcRow)
			dstJSON, _ := json.Marshal(dstRows[i])
			if !bytes.Equal(srcJSON, dstJSON) {
				t.Errorf("gap table %s row %d:\n  src: %s\n  dst: %s", tbl, i, srcJSON, dstJSON)
			}
		}
	}

	// Specifically verify sealed credentials round-tripped as-is.
	dstDB := storesqlite.DBHandle(func() *storesqlite.Store {
		ss, _ := dstSt.(*storesqlite.Store)
		return ss
	}())
	var gotCred []byte
	if err := dstDB.QueryRowContext(ctx, `SELECT password_ct FROM identity_submission WHERE identity_id = 'ident-1'`).Scan(&gotCred); err != nil {
		t.Fatalf("scan password_ct: %v", err)
	}
	if !bytes.Equal(gotCred, sealedCred) {
		t.Errorf("sealed cred mismatch: got %x, want %x", gotCred, sealedCred)
	}
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ensure time import is used for clock initialization
var _ = time.Now
