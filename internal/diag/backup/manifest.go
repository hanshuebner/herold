package backup

import "time"

// CurrentBackupVersion is the on-disk format version this build
// produces. Restore tooling refuses bundles with a higher value so a
// future incompatible bump is caught at the earliest point.
const CurrentBackupVersion = 1

// CurrentSchemaVersion is the maximum migration number both backends
// know about today. Bumped whenever a new migration ships in
// internal/storesqlite/migrations or internal/storepg/migrations.
const CurrentSchemaVersion = 10

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
	"oidc_providers",
	"oidc_links",
	"api_keys",
	"aliases",
	"sieve_scripts",
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
	"blob_refs",
}
