package backup

import "time"

// CurrentBackupVersion is the on-disk format version this build
// produces. Restore tooling refuses bundles with a higher value so a
// future incompatible bump is caught at the earliest point.
const CurrentBackupVersion = 1

// CurrentSchemaVersion is the maximum migration number both backends
// know about today. Bumped whenever a new migration ships in
// internal/storesqlite/migrations or internal/storepg/migrations.
//
// 14 — 0014_inbound_attachment_policy.sql (Phase 3 Wave 3.5c Track B,
//
//	REQ-FLOW-ATTPOL-01).  Track B owns its rows; this constant only
//	tracks the migration ceiling.
//
// 15 — 0015_webhook_extracted.sql (Phase 3 Wave 3.5c Track C,
//
//	REQ-HOOK-02 + REQ-HOOK-EXTRACTED-01..03).  Adds target_kind,
//	body_mode, extracted_text_max_bytes, text_required to webhooks.
//
// 16 — 0016_apikey_scope.sql (Phase 3 Wave 3.6, REQ-AUTH-SCOPE-04).
//
//	Adds api_keys.scope_json (closed-enum scope set; immutable).
//	Backfills existing rows to '["admin"]' so legacy keys retain
//	their pre-3.6 capability while operators rotate to least-priv
//	scopes.
//
// 17 — 0017_push_subscription.sql (Phase 3 Wave 3.8a,
//
//	REQ-PROTO-120..122). Adds push_subscription table + the
//	jmap_states.push_subscription_state column so the JMAP
//	PushSubscription datatype has a /changes-able state. Outbound
//	push delivery (REQ-PROTO-123..126) and the notificationRules
//	engine (REQ-PROTO-127) ride this row in 3.8b/3.8c without
//	further migration.
//
// 18 — 0018_ses_seen_messages.sql (Phase 3 Wave 3.2,
//
//	REQ-HOOK-SES-01..07). Adds ses_seen_messages table for SES
//	inbound MessageId replay deduplication (24-hour TTL).
//
// 19 — 0019_email_reactions.sql (Phase 3 Wave 3.9,
//
//	REQ-PROTO-100..103, REQ-FLOW-100..108). Adds email_reactions
//	table with composite PK (email_id, emoji, principal_id).
//
// 20 — 0020_coach.sql (Phase 3 Wave 3.10,
//
//	REQ-PROTO-110..112). Adds coach_events and coach_dismiss tables
//	for ShortcutCoachStat JMAP datatype; adds
//	jmap_states.shortcut_coach_state column.
//
// 21 — 0021_apikey_from_constraints.sql (REQ-SEND-12 / REQ-FLOW-41,
//
//	REQ-SEND-30). Column-only migration: adds
//	api_keys.allowed_from_addresses_json and
//	api_keys.allowed_from_domains_json (no new tables).
//
// 22 — 0022_messages_env_message_id_index.sql. Index-only migration:
//
//	adds idx_messages_env_message_id on messages(env_message_id) to
//	speed up the thread-resolution lookup in InsertMessage.
//	No new tables.
//
// 23 — 0023_mailbox_sort_order.sql. Column-only migration: adds
//
//	mailboxes.sort_order (INTEGER/BIGINT NOT NULL DEFAULT 0) for the
//	JMAP Mailbox.sortOrder property (RFC 8621 §2.1). No new tables.
//
// 24 — 0024_message_mailboxes.sql. M:N membership: removes per-mailbox
//
//	columns from messages (mailbox_id, uid, modseq, flags, keywords_csv,
//	snoozed_until_us) and introduces the message_mailboxes join table
//	with (message_id, mailbox_id, uid, modseq, flags, keywords_csv,
//	snoozed_until_us). Adds messages.principal_id (denorm for query
//	speed). Forward-only; no downgrade path.
//
// 25 — 0025_category_settings_state.sql. Column-only migration: adds
//
//	jmap_states.category_settings_state (REQ-CAT-50) so the
//	CategorySettings JMAP datatype has a /changes-able state counter.
//	No new tables.
//
// 26 — 0026_managed_rules.sql. ManagedRule structured filter abstraction
//
//	(Wave 3.15, REQ-FLT-01..31). Adds the managed_rules table and
//	jmap_states.managed_rule_state counter. Adds
//	sieve_scripts.user_script column so the user-written Sieve half
//	survives recompilations of the managed-rule preamble.
//
// 27 — 0027_llm_classifications.sql. Per-message LLM classification
//
//	records (REQ-FILT-66 / REQ-FILT-216 / G14). Adds the
//	llm_classifications table and jmap_categorisation_config.guardrail
//	column. Forward-only.
//
// 28 — 0028_derived_categories.sql. Derived category list per account
//
//	(REQ-FILT-217). Adds jmap_categorisation_config.derived_categories_json
//	column. Column-only migration.
//
// 29 — 0029_derived_categories_epoch.sql. Epoch guard for derived
//
//	categories optimistic locking (REQ-FILT-217). Adds
//	jmap_categorisation_config.derived_categories_epoch column.
//	Column-only migration.
//
// 30 — 0030_seen_addresses.sql. Per-principal seen-addresses history
//
//	(REQ-MAIL-11e..m). Adds the seen_addresses table and
//	jmap_states.seen_address_state column. Forward-only.
//
// 31 — 0031_seen_addresses_enabled.sql. Per-principal
//
//	seen-addresses-enabled flag (REQ-SET-15). Adds
//	principals.seen_addresses_enabled column (default true).
//	Column-only migration.
//
// 32 — 0032_identity_submission.sql. Per-Identity external SMTP
//
//	submission config (REQ-AUTH-EXT-SUBMIT-01..10). Adds the
//	identity_submission table with FK to jmap_identities(id) ON
//	DELETE CASCADE; carries AEAD-sealed credential blobs and the
//	background refresh-due index.
//
// 33 — 0033_email_submission_external.sql. Adds the external flag to
//
//	jmap_email_submissions (REQ-AUTH-EXT-SUBMIT-05) so /get can
//	reconstruct deliveryStatus for externally-routed submissions
//	without consulting the queue. Column-only migration.
//
// 34 — 0034_chat_dm_pairs.sql. Server-side DM deduplication (re #47).
//
//	Adds chat_dm_pairs (pid_lo, pid_hi, conversation_id) with
//	PRIMARY KEY (pid_lo, pid_hi) so concurrent Conversation/set
//	calls for the same DM pair collide at the constraint level rather
//	than producing duplicate rows. FK to chat_conversations(id) ON
//	DELETE CASCADE.
//
// 35 — 0035_identity_avatar.sql. Per-Identity avatar + outbound X-Face/Face
//
//	headers (REQ-SET-03b). Adds avatar_blob_hash TEXT, avatar_blob_size
//	INTEGER/BIGINT, xface_enabled BOOLEAN/INTEGER to jmap_identities.
//	Refcounting managed by the application layer.
//
// 36 — 0036_principal_avatar.sql. Promotes the default-identity avatar
//
//	to the principal row (REQ-SET-03b, REQ-MAIL-44). Adds avatar_blob_hash
//	TEXT, avatar_blob_size INTEGER/BIGINT, xface_enabled BOOLEAN/INTEGER
//	to principals so chat Principal/get and mail-thread cross-user avatar
//	lookups can read the picture without leaking the per-Identity overlay.
//
// 37 — 0037_clientlog.sql. Ring-buffer table for client-side log events
//
//	(REQ-OPS-206, REQ-OPS-206a, REQ-OPS-219). Adds the clientlog table
//	with columns for slice, timestamps, app, kind, level, identity, route,
//	build, ua, msg, stack, and full payload JSON.  Four indexes cover the
//	canonical pagination, request-id correlation, session-ts ordering, and
//	per-user server-ts ordering access patterns.  Excluded from
//	herold diag backup by default (--include-clientlog opt-in).
//
// 38 — 0038_clientlog_telemetry.sql. Per-user client-log telemetry opt-out
//
//	(REQ-OPS-208, REQ-CLOG-06). Adds
//	principals.clientlog_telemetry_enabled (nullable boolean) so each
//	user can override the system default for behavioural telemetry.
//	NULL means "use system default". Column-only migration.
//
// 39 — 0039_sessions.sql. Server-side session rows (REQ-OPS-208,
//
//	REQ-CLOG-06). Adds the sessions table keyed on session_id (the
//	CSRF token from the signed cookie) with principal_id FK, expiry,
//	clientlog_telemetry_enabled (non-NULL effective value computed at
//	session creation / refresh), and clientlog_livetail_until_us
//	(NULL when live-tail is inactive, REQ-OPS-211). Enables
//	TelemetryGate.IsEnabled(sessionID) without a principal lookup on
//	the hot path.
//
// 40 — 0040_email_pretrash_mailboxes.sql. Pre-trash mailbox snapshot
//
//	for label-preserving Restore (re #29). Adds the
//	email_pretrash_mailboxes table (email_id, mailbox_id, PRIMARY KEY)
//	with ON DELETE CASCADE from messages(id). The server snapshots
//	non-Trash memberships when a message gains a Trash membership and
//	replays them on Restore, then clears the snapshot.
//
// 41 — 0041_messages_env_references.sql. Column-only migration: adds
//
//	messages.env_references TEXT NOT NULL DEFAULT '' to store the raw
//	References header value. InsertMessage now uses both InReplyTo and
//	References for thread resolution so RFC 8621 sec 8.1 threading
//	(union of all ancestor message-ids) works correctly. Fixes
//	Thread/get returning fragmented threads and the downstream failures
//	in someInThreadHaveKeyword, noneInThreadHaveKeyword, and
//	collapseThreads. No new tables.
//
// 42 — 0042_sieve_named_scripts.sql. ManageSieve named-script storage
//
//	(RFC 5804 §2.4..§2.10). Adds sieve_named_scripts
//	(principal_id, name, script, is_active, updated_at_us) with
//	composite PK (principal_id, name) and a partial unique index
//	on (principal_id) WHERE is_active to enforce the at-most-one
//	active script invariant. Backfills a "default" row from the
//	legacy sieve_scripts table; the legacy single-slot table remains
//	the runtime delivery path's read source, kept in sync via
//	SetActiveSieveScript.
//
// 43 — 0043_messages_internalize_pending.sql. On-demand external
//
//	image internalization for imported mail
//	(17-external-images.md REQ-EXTIMG-90..99). Adds
//	messages.internalize_pending: 0 = not pending (default,
//	covers every existing row), 1 = pending (rewrite at first
//	JMAP Email/get, then clear).
//
// 44 — 0044_state_change_cause.sql. Cause classification for the
//
//	state-change feed (REQ-EXTIMG-BG-INTERNAL-01..03,
//	docs/design/server/architecture/05-sync-and-state.md "Cause
//	classification"). Adds state_changes.cause TEXT NOT NULL
//	DEFAULT 'user' plus the partial index
//	idx_state_changes_principal_kind_seq_user. Existing rows
//	back-fill to 'user'; v1 'background' producer is the extimg
//	internalize-worker.
//
// 45 — 0045_jmap_states_internalize_status.sql. Pending-count push
//
//	channel for the extimg internalize-worker
//	(REQ-EXTIMG-BG-INTERNAL-20..23). Adds
//	jmap_states.internalize_status_state, bumped once per
//	processed worker batch via IncrementJMAPState; surfaced over
//	EventSource as the InternalizeStatus type.
//
// 46 — 0046_internalize_pending_received_at_index.sql. Partial covering
//
//	index supporting newest-first iteration of pending rows by
//	received_at_us, tie-broken on id (REQ-EXTIMG-BG-INTERNAL-80..82).
//	Index-only migration: no schema column changes. Supports the new
//	ListMessagesWithInternalizePendingByReceivedAt store method that
//	the extimg internalize-worker calls in place of the legacy
//	id-ordered ListMessagesWithInternalizePending.
//
// Migration 0047 (REQ-PERF-INDEX-09) adds a non-partial covering index
//
//	idx_messages_principal_received_at on
//	messages(principal_id, received_at_us DESC, id DESC). Eliminates
//	the temp B-tree sort that Email/query date-range filters
//	(before / after / newer_than / older_than) and the inbox no-filter
//	list page were producing on the maintainer's 278k-message corpus.
//	Index-only migration: no schema column changes.
//
// 48 — 0048_identity_verification.sql (REQ-IDENT-01..91). Adds the
//
//	verification trio plus a verified-state index to jmap_identities:
//	verified_at_us (NULL when unverified), verification_token_hash
//	(sha256 of raw token), verification_code_hash (sha256 of raw 6-digit
//	code), verification_token_expires_at_us. Index
//	idx_jmap_identities_verified_created supports the GC pass.
//	Forward-only column adds; existing rows backfill to NULL/NULL/
//	NULL/NULL (i.e. pre-feature unverified) — first JMAP read of these
//	rows will surface verifiedAt = null and the suite is expected to
//	resend on first interaction.
//
// 49 — 0049_message_mailboxes_received_to.sql (REQ-FLOW-33..35). Adds
//
//	message_mailboxes.received_to TEXT NOT NULL DEFAULT '' so the
//	per-recipient fan-out row remembers the envelope RCPT TO that
//	produced it. Empty string is the pre-feature / unknown sentinel
//	the render path treats as "do not inject the X-Herold-Recipient
//	header" (REQ-FLOW-34). Caller-side wiring of the actual envelope
//	address lands in task #17.
//
// 50 — 0050_tagged_address_filters.sql (REQ-TAG-10..11, REQ-TAG-30..32).
//
//	Adds the tagged_address_filters table (id, principal_id,
//	base_identity_id, suffix, action, label_name, created_at_us,
//	updated_at_us) with UNIQUE(principal_id, base_identity_id,
//	suffix) and a (principal_id, base_identity_id) fan-out index.
//	FK to principals(id) and jmap_identities(id) both ON DELETE
//	CASCADE so identity destruction takes the filter rows with it.
//	The 100-filter per-principal cap lives in the store helpers,
//	not the schema.
//
// 51 — 0051_tagged_address_dismissals.sql (REQ-TAG-10, REQ-TAG-60..62).
//
//	Adds the tagged_address_dismissals table (principal_id,
//	base_identity_id, suffix, dismissed_at_us) with composite
//	PRIMARY KEY (principal_id, base_identity_id, suffix). Same FK
//	cascade semantics as 0050. The 500-dismissal cap lives in the
//	store helpers.
//
// 52 — 0052_identity_verify_resend.sql (REQ-IDENT-36).
//
//	Adds three resend rate-limit bookkeeping columns to
//	jmap_identities: verify_last_issued_at_us (cooldown anchor),
//	verify_window_started_at_us / verify_window_count (24h daily-cap
//	window). All three are populated atomically by the dispatcher
//	on every token issuance. The columns survive a successful
//	verify so the daily cap cannot be evaded by burning the first
//	verification and re-creating.
//
// 53 — 0053_identity_is_default.sql (REQ-IDENT-70).
//
//	Adds is_default to jmap_identities, backing the herold JMAP
//	Identity.isDefault extension property. Exactly one identity per
//	principal is the default; the store's SetDefaultJMAPIdentity
//	enforces the single-default invariant in one transaction. The
//	synthesised "default" identity has no row and is the default
//	whenever no persisted row owned by the principal is flagged.
//
// 54 — 0054_sessions_last_seen_at.sql (REQ-AUTH-72, issue #12).
//
//	Adds last_seen_at_us to sessions, backing the admin listener's
//	idle-timeout enforcement. The resolver touches last_seen_at on
//	every accepted request and rejects the session when
//	(now - last_seen_at) exceeds SessionConfig.IdleTTL. Existing
//	rows are backfilled from created_at_us so the column appearing
//	does not retroactively expire any live session.
//
// 55 — 0055_file_shares.sql (REQ-SHARE-01..23, REQ-SHARE-10).
//
//	Adds the file_shares table (id TEXT pk, principal_id FK ON DELETE
//	CASCADE, blob_hash, blob_size, filename, content_type,
//	created_at_us, expires_at_us, max_downloads NULL, download_count,
//	password_hash NULL, state CHECK IN ('pending','active','revoked'),
//	last_downloaded_at_us NULL, revoked_at_us NULL). Two indexes:
//	idx_file_shares_principal_created (principal_id, created_at_us DESC)
//	for the management list and idx_file_shares_state_expires
//	(state, expires_at_us) for the sweeper. Cap and quota enforcement
//	live in the store helpers (REQ-SHARE-12, REQ-SHARE-50).
//
// 56 — 0056_jmap_states_file_share.sql (REQ-SHARE-40..44).
//
//	Adds file_share_state to jmap_states, the per-principal JMAP state
//	counter bumped on every FileShare/set create/update/destroy so
//	clients track drift via FileShare/changes. No new table; defaults
//	to 0 so existing rows remain valid.
//
// 57 — 0057_file_shares_source.sql (REQ-SHARE-04).
//
//	Adds three nullable message back-reference columns to file_shares:
//	source_message_id TEXT NULL (JMAP Email id / store message id of
//	the originating message; soft reference, no FK so the snapshot
//	survives message deletion), source_subject TEXT NULL (subject line
//	snapshot), source_recipients TEXT NULL (JSON array of To+Cc+Bcc
//	recipient email addresses). All three are populated atomically on
//	the pending -> active transition by ConfirmFileShare when the
//	caller supplies a non-zero FileShareSource. NULL while pending or
//	when the caller supplied no context. Recipient-facing surfaces
//	MUST NOT expose these columns.
//
// 58 — 0058_imap_import.sql (issue #25, REQ-IMAP-IMP-02, -15..19,
//
//	-34, -42, -70, -74). Adds four tables for the upstream IMAP
//	live-mirror worker:
//	  imapimport_account       — per-principal upstream account config
//	                             with AEAD-sealed credential_ct BLOB,
//	                             nullable backfill_floor_date and
//	                             last_success_at timestamps, boolean
//	                             delete_propagates, and state lifecycle.
//	  imapimport_folder_map    — (account_id, upstream_folder) →
//	                             herold_mailbox_name translation table;
//	                             ON DELETE CASCADE from the account.
//	  imapimport_folder_cursor — per-folder IMAP sync cursors (UIDVALIDITY,
//	                             UIDNEXT, low/high-water UIDs, CONDSTORE
//	                             HIGHESTMODSEQ); ON DELETE CASCADE.
//	  imapimport_message_state — upstream-UID → herold message mapping
//	                             for flag/delete write-back with a
//	                             last_synced_flags INTEGER bitfield;
//	                             ON DELETE CASCADE.
//
// 59 — 0059_messages_body_meta.sql. Adds three columns to messages for
//
//	precomputed body metadata (JMAP Email/get preview + hasAttachment
//	fast path):
//	  preview            — TEXT NOT NULL DEFAULT '' — first 256 chars of
//	                       plain-text body; empty until computed.
//	  has_attachment     — BOOLEAN/INTEGER NOT NULL DEFAULT FALSE/0 —
//	                       true when the message has a non-inline
//	                       attachment part.
//	  body_meta_computed — BOOLEAN/INTEGER NOT NULL DEFAULT FALSE/0 —
//	                       0 = not yet computed; 1 = authoritative.
//	Partial index idx_messages_body_meta_pending on id WHERE
//	body_meta_computed = 0 (SQLite) / NOT body_meta_computed (Postgres)
//	makes the background sweep worker's paged scan cheap.
//
// 60 — 0060_apikey_oneshot.sql. Adds one_shot column to api_keys
//
//	(INTEGER NOT NULL DEFAULT 0 / BOOLEAN NOT NULL DEFAULT FALSE).
//	Keys with one_shot set are automatically deleted after the first
//	successful POST /api/v1/principals/{pid}/totp/confirm call so the
//	bootstrap / recovery API key cannot be reused beyond its single
//	intended purpose (REQ-AUTH-44, re #21, re #24).
//
// 61 — 0061_blob_part_index.sql (re #46). Adds the blob_part_index table:
//
//	a per-blob cache of the serialised MIME part index produced by the
//	bodymeta/partindex worker. Keyed on blob_hash (TEXT PRIMARY KEY) so
//	the index dedups across messages sharing a blob and never goes stale
//	(blobs are immutable). The payload (parts_json TEXT) is opaque to
//	the store; the caller owns the schema. index_version (INTEGER) lets
//	a worker with a higher minVersion treat old rows as absent.
//	The JMAP part-download path reads this cache to serve byte-range
//	requests without re-parsing the full message blob. Excluded from
//	backup by design: the index is a pure cache of immutable blob
//	content and can always be regenerated by the worker.
//
// 67 — 0067_session_elevations.sql (REQ-AUTH-74, issue #79). Adds the
//
//	session_elevations table: one row per active TOTP step-up elevation
//	keyed on session_id (FK → sessions ON DELETE CASCADE). Columns:
//	session_id, principal_id, elevated_at_us, expires_at_us.  Admin
//	endpoints require an active, unexpired elevation row in addition to
//	the principal's admin flag.  The row is created by POST
//	/api/v1/auth/step-up and expires after the operator-configured
//	elevation_ttl (default 15 minutes).
//
// 68 — 0068_sessions_tombstone.sql (REQ-AUTH-77, issue #80). Adds
//
//	revoked_at_us to the sessions table (NULL for active sessions,
//	non-NULL for tombstoned/revoked sessions).  Revocation via DELETE
//	/api/v1/auth/sessions/{id} sets this column rather than deleting
//	the row, so the next request on the revoked cookie receives
//	{"type": "session_revoked"} (REQ-AUTH-76, REQ-AS-13).  The row
//	expires naturally via EvictExpiredSessions once the short tombstone
//	TTL elapses.
//
// 69 — 0069_ext_submission_held_reauth.sql (re #70,
//
//	REQ-AUTH-EXT-SUBMIT-05). Adds held_for_reauth and hold_deadline_us
//	to jmap_email_submissions so external submissions that fail with
//	OutcomeAuthFailed can be parked and retried automatically when the
//	identity's OAuth token is recovered. held_for_reauth INTEGER NOT NULL
//	DEFAULT 0 (SQLite) / BOOLEAN NOT NULL DEFAULT FALSE (Postgres);
//	hold_deadline_us INTEGER NULL (SQLite) / BIGINT NULL (Postgres) stores
//	the epoch-microsecond expiry. Partial index on (identity_id,
//	hold_deadline_us) WHERE held_for_reauth speeds the Retryer's list
//	scan. Column-only migration.
//
// 70 — 0070_rethread_same_msgid.sql (re #88, REQ-STORE-40). Repairs
//
//	fragmented threads caused by self-sent messages: each self-sent
//	message is stored twice (a Sent copy and a delivered copy) sharing
//	the same Message-ID header. The old thread assignment treated each
//	copy as an independent message, giving them different thread_id
//	values. The migration sets every row in a same-MessageID group to
//	the effective thread of the group's first (lowest id) row. Data-only
//	migration; no schema change.
//
// 71 — 0071_identity_submission_username.sql (re #126). Adds
//
//	submit_username TEXT NOT NULL DEFAULT '' to identity_submission so
//	the SMTP AUTH (SASL) username for password auth can differ from the
//	identity's email address. Empty string means "use the identity email
//	at auth time". Not used for oauth2 (XOAUTH2 user stays the email).
//	Column-only migration.
//
// 72 — 0072_principal_managed_domains.sql (REQ-ADM-307, re #145).
//
//	Introduces the delegated-operator authorization model. Creates the
//	principal_managed_domains association table (principal_id -> domain)
//	for domain-scoped operators. Adds PrincipalFlagSuperAdmin = 32 (bit 5)
//	and auto-promotes every existing PrincipalFlagAdmin principal to
//	super-admin so no operator changes behaviour on upgrade.
//
// 73 — 0073_system_events.sql (REQ-ADM-304, re #142).
//
//	Adds the system_events bounded ring-buffer table for system-initiated
//	operational telemetry (SMTP acceptance, recipient resolution, SES
//	inbound receipt, webhook dispatch outcomes). Separate from the audit
//	log which is the security/compliance record for actor-initiated
//	actions. The domain column enables REQ-ADM-307 scope filtering.
//	Excluded from herold diag backup by default.
//
// 74 — 0074_imapimport_debug_log.sql (re #138).
//
//	Adds imapimport_account.debug_log: a runtime-toggled boolean that
//	raises per-worker verbosity (connection lifecycle, IDLE arm/wake,
//	sync round counts, NOOP poll ticks) and routes events into the
//	system_events ring-buffer tagged with the account ID as actor_id.
//
// 75 — 0075_audit_log_domain.sql (REQ-ADM-307, re #145).
//
//	Adds audit_log.domain TEXT NOT NULL DEFAULT '': the mail domain the
//	audit entry relates to, used by AuditLogFilter.Domains for
//	REQ-ADM-307 operator-scope filtering. Existing rows default to ''
//	(global entries); domain operators see nothing from pre-migration
//	history (correct fail-closed behavior). Index on (domain, id DESC)
//	WHERE domain != '' supports the IN-list scope filter.
//
// 76 — 0076_email_bulk_jobs.sql (issue #149/#161, REQ-PROTO-40..48
//
//	vendor extension https://netzhansa.com/jmap/email-bulk-mutation).
//	Adds the email_bulk_jobs table backing `Email/setByQuery` +
//	`EmailBulkJob/get`: one row per whole-mailbox background bulk
//	mutation, holding the resolved target-id list, a resumable
//	processed cursor, and a failures_json array so a partial per-batch
//	failure never desyncs failedIds/errors. Adds
//	jmap_states.email_bulk_job_state, the per-principal push counter.
//	Excluded from herold diag backup: ephemeral operational state.
//
// 77 — 0077_messages_failed_images.sql (issue #162). Adds
//
//	messages.failed_image_count INTEGER NOT NULL DEFAULT 0 and
//	messages.failed_image_state TEXT NOT NULL DEFAULT ''. Column-only
//	migration on an existing table, mirroring migration 43's
//	internalize_pending column (no new adapter/backup row type
//	needed). failed_image_count is the JMAP Email.failedImageCount
//	badge property; failed_image_state is an opaque
//	extimg.EncodeRetainedState blob (failed URLs + a splice-back HTML
//	template + the DKIM verdict) that only the JMAP Email/retryImages
//	handler decodes. Both reset to 0/'' whenever ReplaceMessageBody
//	swaps the blob, since a new body invalidates any previously
//	retained failed-fetch state.
//
// 78 — 0078_push_subscription_fcm.sql (re #200). Adds
//
//	push_subscription.transport TEXT NOT NULL DEFAULT 'webpush' and
//	push_subscription.fcm_token TEXT NOT NULL DEFAULT ''. Column-only
//	migration on an existing table; no new adapter/backup row type
//	needed (push_subscription's backup encoding already round-trips
//	unknown-to-it columns via the existing row scan).
//
// 79 — 0079_grants.sql (epic #182, REQ-AC-01..05).
//
//	Adds the grants table: the unified resource-grant authorization
//	substrate. One row binds a subject (principal today; the
//	subject_kind column admits 'group' later) to a per-kind access
//	level on a typed resource (server/domain/list/mailbox), with a
//	provenance ('local' | 'idp:<provider>') keeping operator-assigned
//	and IdP-derived grants independent. Back-fills server:superadmin
//	from PrincipalFlagSuperAdmin and domain:operator from
//	principal_managed_domains so the table is a faithful projection of
//	existing authority on upgrade.
//
// 80 — 0080_alias_external_target.sql (issue #181). aliases.target_principal
//
//	becomes nullable and a new nullable target_address column carries an
//	external forwarding addr-spec; exactly one of the two is set (CHECK
//	constraint + store.Metadata.InsertAlias validation). No new backup
//	row type: AliasRow.TargetPrincipal becomes *int64 and gains
//	TargetAddress *string, both nil for pre-migration rows.
//
// 81 — 0081_srs_secrets.sql (issue #204, SRS return-path rewriting for
//
//	mail forwarded via #181 external-target aliases or #63 Sieve
//	redirect). Adds the srs_secrets table: keyed-MAC secrets
//	internal/srs signs and verifies SRS0/SRS1 return-path addresses
//	with. Rotation is additive (insert a new row; the highest id
//	signs, every row still verifies) so an in-flight SRS address
//	survives a rotation. New SRSSecretRow backup row type.
//
// 82 — 0082_oauth2_native_grant.sql (issue #199, REQ-AND-AUTH-01/02).
//
//	OAuth2 authorization-code + PKCE grant for native clients. Adds
//	api_keys.expires_at_us (nullable; NULL for every pre-migration row
//	and for the existing operator-issued / device-token keys, which
//	never expire on their own -- only the short-lived OAuth2 access
//	token minted at POST /oauth2/token populates it). Adds two new
//	tables: oauth_auth_codes (single-use authorization codes, 60s TTL)
//	and oauth_refresh_tokens (rotation-chain rows with reuse detection,
//	FK to api_keys(id) ON DELETE SET NULL for the paired access token).
//	Both new tables are excluded from backup (ephemeral, like
//	sessions/session_elevations).
//
// 83 — 0083_oidc_claim_mapping.sql (epic #188, REQ-AC-60..70). External
//
//	IdP claim-to-grant mapping, layered on the #182 grants substrate.
//	Adds oidc_providers.authz_trusted (claim-mapping is inert for a
//	provider until a server:superadmin sets this true, REQ-AC-66).
//	Adds two new tables: oidc_claim_allowlist (per-provider allowlist
//	of claim names a rule may consult, REQ-AC-67) and
//	oidc_claim_mapping_rules (one row per "claim has this value ->
//	grant this (resource, level)" rule, REQ-AC-60). Both are
//	authorization data -- backed up like grants, unlike the ephemeral
//	oauth_* tables of migration 82.
//
// 84 — 0084_mailbox_acl_grants.sql (epic #210, REQ-AC-50..53). Retires the
//
//	mailbox_acl table onto the #182 grants substrate: every row becomes a
//	`mailbox`-kind grant whose Level carries the full RFC 4314 letter-set
//	(not the coarse tier), provenance "acl-migration". grants.subject_kind
//	gains a third value, "anyone" (subject_id unused), for the RFC 4314
//	"anyone" pseudo-identifier mailbox_acl encoded as a NULL principal_id.
//	No new backed-up table: mailbox ACL rows are grants rows, already
//	covered by the "grants" entry above; mailbox_acl is dropped and
//	removed from TableNames.
//
// 85 — 0085_mailing_lists.sql (epic #183, REQ-MLIST-01..12). Storage
//
//	foundation for hosted mailing lists, Stage 1: fan-out with List-*
//	headers, admin-maintained roster. Adds mailing_list (a Group
//	principal's list configuration: posting_address, denormalised
//	domain, owner_id, subject_tag, arc_seal, posting/subscribe policy,
//	bounce_policy_json, archive_mailbox_id reserved for Stage 4,
//	max_message_size_bytes) and mailing_list_member (one roster row per
//	principal or external-address member, with state/delivery_mode
//	enums and Stage 2 bounce-scoring columns included now). Both new
//	tables restored after principals and mailboxes.
//
// 86 — 0086_mailing_list_unsubscribe.sql (epic #184, REQ-MLIST-56..59).
//
//	Adds mailing_list.unsubscribe_enabled (default true), gating the
//	List-Unsubscribe / RFC 8058 one-click header pair and the
//	token-authorised subscriber self-service management page. No new
//	table; MailingListRow gains the one field.
//
// 87 — 0087_queue_header_overlay.sql (issue #184, REQ-MLIST-11 regression).
//
//	Adds queue.header_overlay: the small per-recipient header block
//	(e.g. List-Unsubscribe/List-Unsubscribe-Post) Queue.deliver prepends
//	to the shared body blob at wire-delivery time, so per-recipient
//	header variation no longer requires a distinct persisted blob per
//	fan-out copy. No new table; QueueRow gains the one field.
//
// 88 — 0088_mailing_list_member_awaiting_approval.sql (issue #185,
//
//	REQ-MLIST-60..63, Stage 3 self-subscription). Widens
//	mailing_list_member.state to accept 'awaiting-approval': a
//	request-approval list's confirmed-but-not-yet-owner-approved
//	subscriber. No new table or column; MailingListMemberRow.State
//	simply accepts one more string value.
//
// 89 — 0089_mailing_list_archive_retention.sql (epic #187,
//
//	REQ-MLIST-70..74, Stage 4 archive mailbox). Adds
//	mailing_list.archive_retention_days / archive_retention_max_messages
//	(both default 0, meaning unbounded), the per-list age/count retention
//	bound internal/archiveretention sweeps against. No new table;
//	MailingListRow gains the two fields. archive_mailbox_id itself was
//	already part of the schema from migration 0085 (reserved for this
//	stage) and is now populated by the admin REST archive-enable path.
//
// 90 — 0090_message_delivery_disposition.sql (issue #143). Adds
//
//	messages.delivery_disposition ('', the NOT NULL DEFAULT, meaning
//	not recorded; 'delivered_inbox' / 'delivered_junk' otherwise),
//	written once by internal/protosmtp/deliver.go's deliverOne at
//	INSERT time and never recomputed from current mailbox membership.
//	Fixes message research's retrospective trace silently rewriting
//	itself when a message is later moved out of Junk. No new table;
//	MessageRow gains the one field.
//
// 91 — 0091_session_elevation_absolute_cap.sql (REQ-AUTH-74,
//
//	REQ-AUTH-ELEV-CONFIG, issue #225). Renames session_elevations.
//	expires_at_us to idle_deadline_us and adds absolute_deadline_us
//	(backfilled from idle_deadline_us for existing rows). Splits the
//	single fixed elevation deadline into a sliding idle deadline
//	(extended by ExtendElevation on every request that passes the
//	active-elevation check) and a fixed absolute cap that is never
//	extended, fixing elevation expiring on a fixed wall-clock deadline
//	regardless of operator activity. No new table; SessionElevationRow
//	gains one field and renames another. Excluded from backup for the
//	same reason as before (elevation rows expire naturally).
//
// 92 — 0092_oauth_clients.sql (issue #199, REQ-AND-AUTH-01/02). DB-backed
//
//	OAuth2 client registry for the native-client authorization-code +
//	PKCE grant, replacing the compiled-in map in
//	internal/directory/oauth2client.go. Adds oauth_clients (client_id
//	primary key, name, redirect_uris_json, scopes_json, public,
//	client_secret_hash, created_at_us). No FK to oauth_auth_codes /
//	oauth_refresh_tokens -- those already carry client_id as a
//	free-text column from migration 0082, so deleting a client refuses
//	new authorize/token requests without retroactively invalidating
//	tokens already issued. Backed up like oidc_providers (durable
//	configuration, not an ephemeral credential).
//
// 93 — 0093_oidc_autoprovision_domain.sql (issue #230, REQ-AUTH-56).
//
//	Adds oidc_providers.auto_provision_domain: the local domain a
//	principal auto-provisioned on first OIDC sign-in is created under.
//	auto_provision (the per-provider opt-in) already existed from an
//	earlier migration; this column is what internal/directoryoidc's
//	CompleteSignIn consults to derive the new principal's canonical
//	email (local-part from the verified `email` claim, domain from this
//	column). No new table; OIDCProviderRow gains the one field.
//
// 94 — 0094_mailing_list_held_post.sql (issue #189, REQ-MLIST-80,
//
//	moderation v2 milestone). Adds mailing_list_held_post: a post a
//	list's posting policy held (the `moderated` policy holds every
//	post; `members-only`/`announce-only` reject a non-conforming post
//	outright, no row) rather than fanning out or rejecting, pending an
//	owner/moderator approve/reject/discard decision. The row's
//	blob_hash/blob_size reference the held message's raw bytes in the
//	normal content-addressed blob store, kept alive by a caller-managed
//	blob_refs reference for as long as status stays 'pending'.
//
// 95 — 0095_mailing_list_held_post_approving.sql (issue #189
//
//	verification fix, REQ-MLIST-80). Widens
//	mailing_list_held_post.status to accept 'approving': the transient
//	claimed state ApproveHeldPost's two-phase claim/fanOut/finalize
//	protocol puts a row in BEFORE running fan-out, so at most one
//	concurrent caller can ever fan a held post out (a direct
//	pending->approved CAS-after-the-fact allowed two concurrent
//	approvers to both pass the pre-fanout read and both mail every
//	member). No new table; MailingListHeldPostRow.Status simply accepts
//	one more string value.
//
// 96 — 0096_queue_message_id.sql (issue #235, split from #143's
//
//	2026-07-12 alias-forward triage comment). Adds queue.message_id: the
//	RFC 5322 Message-ID header captured from the submitted body at
//	Queue.Submit time, normalised the same way InsertMessage already
//	normalises messages.env_message_id. Lets message research join a
//	relay/forward queue row back to the received message it originated
//	from by exact Message-ID match -- an alias-forward relay row's
//	mail_from/rcpt_to are SRS-rewritten and no longer contain the
//	original sender/recipient address as a substring, so the existing
//	address-contains search cannot find it. No new table; QueueRow
//	gains the one field.
//
// 97 — 0097_sub_principals.sql (issue #227, REQ-SUBACCT-01..11 --
//
//	store half). Adds principals.parent_principal_id: a nullable
//	self-referencing FK (ON DELETE CASCADE) that turns a principals row
//	into a sub-principal (store.PrincipalKindSubAccount) owned by an
//	individual principal. A sub-principal carries its own Mailbox tree /
//	Identity set / Sieve scripts / state strings by construction (same
//	table, same per-principal machinery); it is never authenticatable
//	(directory.Authenticate rejects it, the single seam behind session-
//	cookie, device-token, OAuth2 authorization-code, IMAP, SMTP-
//	submission, and ManageSieve login) and is excluded from the admin
//	principal-list surfaces. Its mail counts against the parent's quota:
//	InsertMessage / ReplaceMessageBody / the delete and expunge paths
//	resolve the quota-owning principal before touching used_bytes. No
//	new table; PrincipalRow gains the one nullable field.
//
// 98 — 0098_mailing_list_held_post_archived_at.sql (issue #189
//
//	verification fix, second hardening pass, REQ-MLIST-70/80). Adds
//	mailing_list_held_post.archived_at_us: a per-held-post exactly-once
//	latch (NULL until filed, set once by whichever ApproveHeldPost
//	attempt's ClaimMailingListHeldPostArchive call wins the CAS) so a
//	crash-resumed or stale-lease-reclaimed approval never files a
//	second archive copy of the same held post. No new table;
//	MailingListHeldPostRow gains the one field.
//
// 99 — 0099_drop_principal_managed_domains.sql (issue #237, REQ-ADM-307,
//
//	REQ-AC-30/31). Retires the principal_managed_domains association
//	table that predated the #182 grants substrate: a defensive
//	back-fill (NOT EXISTS against the grants natural key) covers any row
//	assigned between migration 0079's own back-fill and this migration,
//	then the table is dropped. grants is now the sole
//	source of domain:operator authority; internal/authz's OperatorDomains
//	and Resolve no longer consult a second table. PrincipalManagedDomainRow
//	and its TableNames/tableReg entries are removed from herold diag
//	backup along with the table.
const CurrentSchemaVersion = 99

// Manifest is the metadata block written to <bundle>/manifest.json. It
// summarises the backup so operators (and the verify subcommand) can
// cross-check the JSONL files and blob tree without re-reading the
// whole bundle.
type Manifest struct {
	// SchemaVersion is the source store's max applied migration
	// version at backup time.
	SchemaVersion int `json:"schema_version"`
	// BackupVersion is the on-disk format version of this bundle.
	BackupVersion int `json:"backup_version"`
	// CreatedAt is the wall-clock instant the bundle write started
	// (from the injected Clock).
	CreatedAt time.Time `json:"created_at"`
	// Backend records the source backend kind: "sqlite" or "postgres".
	Backend string `json:"backend"`
	// Tables maps table name to row count written to that table's
	// JSONL file. The verify tool re-reads each JSONL to confirm.
	Tables map[string]int64 `json:"tables"`
	// Blobs aggregates the blob tree's count and byte total.
	Blobs BlobSummary `json:"blobs"`
	// HostHerold is the producing herold version + git SHA (left
	// empty for now; wired in once observe.Version lands).
	HostHerold string `json:"host_herold,omitempty"`
	// TotalBytes is the sum of every file under the bundle root,
	// computed after the bundle has been written.
	TotalBytes int64 `json:"total_bytes"`
}

// BlobSummary is the count + bytes total for the blobs/ tree in a
// bundle.
type BlobSummary struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// TableNames is the canonical ordered list of table jsonl files the
// bundle contains. Order is FK-respecting for restore (parents before
// children); backup writes in the same order so a streaming reader
// can verify FK integrity in one pass without buffering.
var TableNames = []string{
	"domains",
	"principals",
	// Unified resource-grant authorization substrate (epic #182,
	// REQ-AC-01..05, migration 0079). subject_id is polymorphic (no FK);
	// granted_by FKs principals(id) ON DELETE SET NULL, so grants is
	// restored after principals. Authorization data — backed up (unlike
	// the ephemeral session/elevation tables).
	"grants",
	// Phase 3 Wave 3.8a JMAP PushSubscription (REQ-PROTO-120..122,
	// migration 0017). FK to principals(id); restored after the
	// principals row is in place. No child tables of its own — the
	// outbound push dispatcher is stateless w.r.t. herold's store.
	"push_subscription",
	"oidc_providers",
	// External IdP claim-to-grant mapping (epic #188, REQ-AC-60..70,
	// migration 0083). Both FK oidc_providers(name) ON DELETE CASCADE, so
	// restored after oidc_providers. created_by (mapping rules only) FKs
	// principals(id) ON DELETE SET NULL, already in place.
	"oidc_claim_allowlist",
	"oidc_claim_mapping_rules",
	"oidc_links",
	// OAuth2 native-client registry (issue #199, migration 0092). No FK
	// to oauth_auth_codes / oauth_refresh_tokens (those carry client_id
	// as free text, from migration 0082); order here is by convention,
	// not a restore-ordering requirement.
	"oauth_clients",
	"api_keys",
	"aliases",
	"sieve_scripts",
	// ManageSieve named-script storage (RFC 5804, migration 0042). FK to
	// principals(id) ON DELETE CASCADE; restored after sieve_scripts so the
	// legacy single-slot and the named-script set are both present before any
	// child table that might reference them.
	"sieve_named_scripts",
	// Phase 3 Wave 3.15 ManagedRule structured filter abstraction
	// (REQ-FLT-01..31, migration 0026). FK to principals(id); restored
	// after principals are in place.
	"managed_rules",
	// Phase 2 LLM categorisation (REQ-FILT-200..221, migration 0009).
	// Per-principal singleton row; principals already populated above.
	"jmap_categorisation_config",
	// Phase 2 Wave 2.9.6 chat per-account defaults (REQ-CHAT-20/92,
	// migration 0013). FK to principals(id); inserted before the chat
	// conversation tables which themselves reference principals.
	"chat_account_settings",
	// Phase 3 Wave 3.10 ShortcutCoachStat (REQ-PROTO-110..112,
	// migration 0020). FK to principals(id); coach_dismiss also FKs
	// principals. Restored after principals are in place.
	"coach_events",
	"coach_dismiss",
	"mailboxes",
	// Hosted mailing lists, Stage 1 storage foundation (epic #183,
	// REQ-MLIST-01..12, migration 0085). mailing_list FKs principals(id)
	// twice (principal_id, owner_id) and mailboxes(id) (archive_mailbox_id,
	// nullable, Stage 4); restored after both parents. mailing_list_member
	// FKs mailing_list(id), restored immediately after it.
	"mailing_list",
	"mailing_list_member",
	// Hosted mailing lists, moderation (v2 milestone, issue #189,
	// REQ-MLIST-80, migration 0094). FK to mailing_list(id) ON DELETE
	// CASCADE and principals(id) ON DELETE SET NULL (decided_by);
	// restored after both. Its blob (blob_hash/blob_size) is backed up
	// like any other blob-referencing row: blob_refs and the blob tree
	// itself are handled generically, not by this table's own entry.
	"mailing_list_held_post",
	"messages",
	// Phase 3 Wave 3.11 M:N message-mailbox membership (migration 0024).
	// FK to messages(id) and mailboxes(id); restored after both parents.
	"message_mailboxes",
	// Pre-trash mailbox snapshot for label-preserving Restore (re #29,
	// migration 0040). FK to messages(id) ON DELETE CASCADE; restored
	// after message_mailboxes so FK to messages is satisfied.
	"email_pretrash_mailboxes",
	// Phase 3 Wave 3.9 email reactions (REQ-PROTO-100..103,
	// REQ-FLOW-100..108, migration 0019). FK to messages(id); restored
	// after messages are in place.
	"email_reactions",
	"state_changes",
	"audit_log",
	"cursors",
	// Inbound attachment policy tables (migration 0014, REQ-FLOW-ATTPOL-01).
	// No FK to other user tables (plain TEXT PKs); restored before queue so
	// inbound delivery policies are in place before any queued retries.
	"inbound_attpol_domain",
	"inbound_attpol_recipient",
	"queue",
	"dkim_keys",
	// SRS (Sender Rewriting Scheme) signing secrets (issue #204, migration
	// 0081). No FK; self-contained rows, restored in any order relative to
	// their neighbours.
	"srs_secrets",
	"acme_accounts",
	"acme_orders",
	"acme_certs",
	"webhooks",
	"dmarc_reports_raw",
	"dmarc_rows",
	"jmap_states",
	"jmap_email_submissions",
	"jmap_identities",
	// Per-Identity external SMTP submission config (migration 0032,
	// REQ-AUTH-EXT-SUBMIT-01..10). FK to jmap_identities(id) ON DELETE
	// CASCADE; restored after jmap_identities.
	"identity_submission",
	"tlsrpt_failures",
	// Phase 2 Wave 2.6 JMAP for Contacts (REQ-PROTO-55). address_books
	// must precede contacts so the FK-respecting restore order holds.
	"address_books",
	"contacts",
	// Phase 2 Wave 2.7 JMAP for Calendars (REQ-PROTO-54). calendars
	// must precede calendar_events so the FK-respecting restore order
	// holds.
	"calendars",
	"calendar_events",
	// Phase 2 Wave 2.8 chat (REQ-CHAT-*). chat_conversations precedes
	// chat_memberships and chat_messages (both FK back to it);
	// chat_blocks references principals only.
	// chat_dm_pairs FKs to chat_conversations(id) so it is restored
	// last among the chat tables (migration 0034, re #47).
	"chat_conversations",
	"chat_memberships",
	"chat_messages",
	"chat_blocks",
	"chat_dm_pairs",
	// Phase 3 Wave 3.2 SES inbound replay deduplication
	// (REQ-HOOK-SES-01..07, migration 0018). No FK dependencies.
	"ses_seen_messages",
	// LLM per-message classification records (REQ-FILT-66 / REQ-FILT-216,
	// migration 0027). FK to messages(id) and principals(id); restored
	// after both parents.
	"llm_classifications",
	// Per-principal seen-addresses history (REQ-MAIL-11e..m, migration 0030).
	// FK to principals(id); restored after principals.
	"seen_addresses",
	// Tagged-address (sub-addressing) filters and dismissals
	// (REQ-TAG-10..11, migrations 0050 + 0051). FKs to principals(id)
	// AND jmap_identities(id) — both parents are restored above.
	"tagged_address_filters",
	"tagged_address_dismissals",
	// Attachment shares (REQ-SHARE-01..23, migration 0055).
	// FK to principals(id) ON DELETE CASCADE; restored after principals.
	// blob_refs must be restored after file_shares so the refcount rows
	// exist when the backup verifier replays them.
	"file_shares",
	"blob_refs",
	// IMAP import (issue #25, REQ-IMAP-IMP-02, -15..19, -34, -42, -70,
	// -74, migration 0057). imapimport_account FKs to principals(id) ON
	// DELETE CASCADE; the three child tables all FK to
	// imapimport_account(id) ON DELETE CASCADE. Restore order:
	// account first, then children. Reverse-order DELETE clears children
	// before the account row.
	"imapimport_account",
	"imapimport_folder_map",
	"imapimport_folder_cursor",
	"imapimport_message_state",
	// Server-side session rows (REQ-OPS-208, REQ-CLOG-06, migration 0039).
	// FK to principals(id) ON DELETE CASCADE; restored after principals.
	// Excluded from backup by default: sessions expire naturally and
	// restoring stale session rows would confuse TelemetryGate.
	"sessions",
	// TOTP step-up elevation records (REQ-AUTH-74, migration 0067).
	// FK → sessions(session_id) ON DELETE CASCADE. Excluded from backup:
	// elevation rows expire after elevation_ttl (default 15 minutes) and
	// restoring stale records into a fresh system would have no effect
	// (the parent session row would also be absent after a restore).
	"session_elevations",
	// Client-log ring buffer (REQ-OPS-206, migration 0037).  No FK
	// constraints; excluded from backup by default (--include-clientlog
	// opts in, REQ-OPS-206a).  Listed last so restore tooling can skip
	// it cleanly without disturbing FK-ordered table groups.
	"clientlog",
	// System-events ring buffer (REQ-ADM-304, migration 0073, re #142).
	// No FK constraints; excluded from backup by default
	// (--include-system-events opts in).  Operational telemetry; not part
	// of the security/compliance backup set.
	"system_events",
	// Whole-mailbox async bulk-mutation jobs (issue #149/#161, migration
	// 0076). FK to principals(id) ON DELETE CASCADE. Excluded from backup
	// permanently (like sessions/session_elevations): ephemeral operational
	// state that a restore cannot meaningfully resume mid-flight.
	"email_bulk_jobs",
	// OAuth2 native-client grant (issue #199, migration 0082). FK to
	// principals(id) ON DELETE CASCADE and api_keys(id) ON DELETE SET
	// NULL; restored after both parents. Excluded from backup (like
	// sessions/session_elevations): authorization codes and refresh-token
	// rotation state are ephemeral and a restored row would not resume a
	// meaningful in-flight grant.
	"oauth_auth_codes",
	"oauth_refresh_tokens",
}
