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
const CurrentSchemaVersion = 36

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
	// Backend records the source backend kind: "sqlite", "postgres",
	// or "fakestore" (test harness only).
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
	// Phase 3 Wave 3.8a JMAP PushSubscription (REQ-PROTO-120..122,
	// migration 0017). FK to principals(id); restored after the
	// principals row is in place. No child tables of its own — the
	// outbound push dispatcher is stateless w.r.t. herold's store.
	"push_subscription",
	"oidc_providers",
	"oidc_links",
	"api_keys",
	"aliases",
	"sieve_scripts",
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
	"messages",
	// Phase 3 Wave 3.11 M:N message-mailbox membership (migration 0024).
	// FK to messages(id) and mailboxes(id); restored after both parents.
	"message_mailboxes",
	// Phase 3 Wave 3.9 email reactions (REQ-PROTO-100..103,
	// REQ-FLOW-100..108, migration 0019). FK to messages(id); restored
	// after messages are in place.
	"email_reactions",
	"mailbox_acl",
	"state_changes",
	"audit_log",
	"cursors",
	"queue",
	"dkim_keys",
	"acme_accounts",
	"acme_orders",
	"acme_certs",
	"webhooks",
	"dmarc_reports_raw",
	"dmarc_rows",
	"jmap_states",
	"jmap_email_submissions",
	"jmap_identities",
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
	"blob_refs",
}
