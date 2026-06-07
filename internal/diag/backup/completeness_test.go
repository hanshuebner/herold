package backup_test

// completeness_test.go ensures that every real SQLite table in the schema
// is covered by the backup machinery (manifest.TableNames + rowsForTable +
// EnumerateRows + Insert). A table that exists in the schema but is absent
// from any of these three places would silently drop rows in a real backup,
// producing a runtime "unknown table" error from the backend instead of a
// test failure. This test catches that class of omission at development time.
//
// How it works:
//  1. Open a fresh SQLite store (applies all migrations -> real schema).
//  2. Query sqlite_master for every user table name.
//  3. Assert every real table is in manifest.TableNames.
//  4. Assert every manifest.TableNames entry is a real table (catches stale
//     or mistyped names).
//
// Two allowlists keep this honest:
//   intentionalExclusions — tables that legitimately must never be in a
//     backup (only schema_migrations qualifies today).
//   knownBackupGaps — tables that SHOULD be backed up but currently are not.
//     These are pre-existing omissions on main (they predate the imap-import
//     work and were surfaced by this guard). They are NOT policy; each is a
//     standing data-safety bug to be fixed in the backup-hardening follow-up.
//     The list exists so this guard passes today without pretending the gaps
//     are intentional — fixing a gap means deleting its entry here and wiring
//     the table in (Row + rowsForTable + EnumerateRows/Insert + TableNames).

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/backup"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// intentionalExclusions lists tables that legitimately must never appear in a
// backup. Each entry must have a comment.
var intentionalExclusions = map[string]string{
	// schema_migrations is internal migration bookkeeping; it is not a
	// business-data table and must not be backed up or restored (restore
	// would overwrite the version cursor and break forward-only guarantees).
	"schema_migrations": "internal migration bookkeeping — not business data",
}

// knownBackupGaps lists tables that SHOULD be backed up but currently are not.
// These are pre-existing omissions on main, surfaced by this guard; they are
// standing data-safety bugs, not policy, to be fixed in the backup-hardening
// follow-up. Fixing one means deleting its entry here and wiring the table in.
var knownBackupGaps = map[string]string{
	// identity_submission (migration 0032) carries AEAD-sealed external SMTP
	// submission credentials. Per the decision to back up sealed-credential
	// tables (so same-host kill -9 recovery preserves accounts), this MUST be
	// added to the backup — the new imapimport_account is already handled the
	// same way. Tracked for the backup-hardening follow-up.
	"identity_submission": "sealed outbound SMTP creds not yet wired into backup — data loss on restore",
	// sieve_named_scripts (migration 0042) stores user-named ManageSieve
	// scripts. Losing them on restore loses the user's script library — should
	// be backed up.
	"sieve_named_scripts": "user named Sieve scripts not yet wired into backup",
	// inbound_attpol_domain / inbound_attpol_recipient (migration 0014) hold
	// per-domain / per-recipient inbound attachment policy. These are
	// application state mutated via API, not system.toml, so a restore should
	// preserve them. Not yet wired into backup.
	"inbound_attpol_domain":    "inbound attachment policy not yet wired into backup",
	"inbound_attpol_recipient": "inbound attachment policy not yet wired into backup",
}

func TestBackupCompleteness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Open a fresh SQLite store so all migrations run and produce the real
	// table set.
	dir := t.TempDir()
	st, err := storesqlite.Open(ctx, filepath.Join(dir, "completeness.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	// Acquire the raw DB handle to query sqlite_master.
	ss, ok := st.(*storesqlite.Store)
	if !ok {
		t.Skip("store is not a *storesqlite.Store; skipping completeness check")
	}
	db := storesqlite.DBHandle(ss)

	// Query all real tables (exclude sqlite internal tables, views, etc.).
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master
		  WHERE type = 'table'
		    AND name NOT LIKE 'sqlite_%'
		 ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
	defer rows.Close()

	var realTables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		realTables = append(realTables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master rows: %v", err)
	}

	// Build a set from manifest.TableNames for O(1) lookup.
	manifestSet := make(map[string]struct{}, len(backup.TableNames))
	for _, n := range backup.TableNames {
		manifestSet[n] = struct{}{}
	}

	// Real table -> assert it is in manifest OR in the intentional exclusions.
	var missing []string
	for _, tbl := range realTables {
		if _, excluded := intentionalExclusions[tbl]; excluded {
			continue
		}
		if _, gap := knownBackupGaps[tbl]; gap {
			continue
		}
		if _, inManifest := manifestSet[tbl]; !inManifest {
			missing = append(missing, tbl)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("real tables not in manifest.TableNames:\n  %v\n"+
			"Wire each in: add a *Row in rows.go, a rowsForTable case in backend.go, "+
			"EnumerateRows+Insert cases in adapter_sqlite.go, and the name to TableNames. "+
			"If a table genuinely must never be backed up, add it to intentionalExclusions "+
			"with a comment; if it is a pre-existing un-wired table, add it to knownBackupGaps.",
			missing)
	}

	// manifest.TableNames entry -> assert it is a real table (catches stale/typo names).
	realSet := make(map[string]struct{}, len(realTables))
	for _, n := range realTables {
		realSet[n] = struct{}{}
	}
	var stale []string
	for _, n := range backup.TableNames {
		if _, exists := realSet[n]; !exists {
			stale = append(stale, n)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("manifest.TableNames entries that are NOT real tables (stale or mistyped): %v", stale)
	}
}
