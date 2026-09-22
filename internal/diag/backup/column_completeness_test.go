package backup

// column_completeness_test.go — per-table column coverage guard, generalised
// to every table registered with the backup engine (re #482, generalising
// the single-table guard #478 added for jmap_email_submissions).
//
// TestBackupCompleteness (completeness_test.go) only checks that every real
// TABLE is registered in the backup engine; it says nothing about whether a
// registered table's Row struct covers every COLUMN. That gap let
// jmap_email_submissions.external, held_for_reauth, hold_deadline_us and
// relay_held exist in the schema for two migrations (0069, 0114) while
// JMAPEmailSubmissionRow carried none of them, and let a further nine
// tables (audit_log, imapimport_account, jmap_categorisation_config,
// jmap_identities, jmap_states, mailboxes, messages, principals, sessions)
// accumulate the same class of gap across many more migrations: the table
// passed TestBackupCompleteness the whole time, and a backup/restore
// silently dropped every column absent from its Row struct.
//
// TestBackupColumnCompleteness closes that gap for every table in tableReg,
// using the same reflection metadata (getFieldMeta) the engine itself uses
// to build SELECT/INSERT statements, compared against each table's live
// PRAGMA table_info. Any future migration that adds a column to a backed-up
// table without updating its Row struct now fails this test immediately,
// rather than surfacing as a silent data-loss bug discovered by an
// independent audit.
//
// columnExclusions is the declared allowlist for the small number of
// columns that legitimately never appear in a Row struct. Every entry
// carries a one-line reason; a column absent from a Row struct AND absent
// from this map fails the test. This makes an omission something that must
// be declared, not something that can merely go unnoticed.

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// columnExclusions maps "table.column" to a one-line reason the column is
// deliberately absent from that table's Row struct.
var columnExclusions = map[string]string{
	// jmap_identities' three verification-token columns are short-lived
	// (24h TTL) secrets for an in-flight email-verification round; the
	// suite re-issues them on the next interaction with an unverified
	// row, so restoring a stale hash has no value and would only extend
	// a secret's exposure window past its intended lifetime. Mirrors the
	// existing doc comment on JMAPIdentityRow.VerifiedAtUs.
	"jmap_identities.verification_token_hash":          "24h-TTL secret for an in-flight verification; re-issued on next interaction, never restored",
	"jmap_identities.verification_code_hash":           "24h-TTL secret for an in-flight verification; re-issued on next interaction, never restored",
	"jmap_identities.verification_token_expires_at_us": "expiry of the excluded verification_token_hash; meaningless without it",
}

func TestBackupColumnCompleteness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dir := t.TempDir()
	st, err := storesqlite.Open(ctx, filepath.Join(dir, "colcheck.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()
	ss, ok := st.(*storesqlite.Store)
	if !ok {
		t.Skip("store is not a *storesqlite.Store; skipping column completeness check")
	}
	db := storesqlite.DBHandle(ss)

	var tables []string
	for tbl := range tableReg {
		tables = append(tables, tbl)
	}
	sort.Strings(tables)

	for _, table := range tables {
		table := table
		t.Run(table, func(t *testing.T) {
			// structCols: the set of SQL columns the table's Row struct
			// declares, via the same metadata the engine uses for
			// SELECT/INSERT.
			structCols := make(map[string]bool)
			for _, f := range getFieldMeta(table) {
				structCols[f.colName] = true
			}

			// realCols: the live schema's columns for this table.
			rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
			if err != nil {
				t.Fatalf("PRAGMA table_info(%s): %v", table, err)
			}
			defer rows.Close()
			var realCols []string
			for rows.Next() {
				var cid int
				var name, ctype string
				var notnull, pk int
				var dflt any
				if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
					t.Fatalf("scan table_info row: %v", err)
				}
				realCols = append(realCols, name)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("table_info rows: %v", err)
			}
			if len(realCols) == 0 {
				t.Fatalf("PRAGMA table_info(%s) returned no columns -- table missing?", table)
			}

			var missing []string
			for _, c := range realCols {
				if structCols[c] {
					continue
				}
				if _, excluded := columnExclusions[table+"."+c]; excluded {
					continue
				}
				missing = append(missing, c)
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("Row struct for %s (rows.go) is missing live schema column(s): %v\n"+
					"Add each as a field with a matching json tag, or -- if the column must never be "+
					"backed up -- document why on the struct and add \"%s.<column>\" to columnExclusions "+
					"in this file with a one-line reason.", table, missing, table)
			}
		})
	}

	// Every columnExclusions entry must name a column that actually
	// exists on its table today; a stale entry silently widens the
	// allowlist for nothing.
	for key := range columnExclusions {
		table, col, ok := splitTableColumn(key)
		if !ok {
			t.Errorf("columnExclusions key %q is not in \"table.column\" form", key)
			continue
		}
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		found := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				t.Fatalf("scan table_info row: %v", err)
			}
			if name == col {
				found = true
			}
		}
		rows.Close()
		if !found {
			t.Errorf("columnExclusions entry %q names a column that does not exist in the live schema of %s -- remove the stale entry", key, table)
		}
	}
}

// splitTableColumn splits a "table.column" key into its two parts.
func splitTableColumn(key string) (table, col string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}
