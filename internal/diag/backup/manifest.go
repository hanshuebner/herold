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
const CurrentSchemaVersion = 17

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
	"mailboxes",
	"messages",
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
	"blob_refs",
}
