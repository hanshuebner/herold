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
const CurrentSchemaVersion = 24

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
	"chat_conversations",
	"chat_memberships",
	"chat_messages",
	"chat_blocks",
	// Phase 3 Wave 3.2 SES inbound replay deduplication
	// (REQ-HOOK-SES-01..07, migration 0018). No FK dependencies.
	"ses_seen_messages",
	"blob_refs",
}
