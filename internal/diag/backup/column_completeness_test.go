package backup

// column_completeness_test.go — per-table column coverage guard, scoped to
// jmap_email_submissions (re #478).
//
// TestBackupCompleteness (completeness_test.go) only checks that every real
// TABLE is registered in the backup engine; it says nothing about whether a
// registered table's Row struct covers every COLUMN. That gap let
// jmap_email_submissions.external, held_for_reauth, hold_deadline_us and
// relay_held exist in the schema for two migrations (0069, 0114) while
// JMAPEmailSubmissionRow carried none of them: the table passed
// TestBackupCompleteness the whole time, and a backup/restore silently
// dropped all four columns.
//
// This test closes that gap for jmap_email_submissions specifically, using
// the same reflection metadata (getFieldMeta) the engine itself uses to
// build SELECT/INSERT statements, compared against the live schema's
// PRAGMA table_info. Any future migration that adds a column to
// jmap_email_submissions without updating JMAPEmailSubmissionRow now fails
// this test immediately, rather than surfacing as a silent data-loss bug
// discovered by an independent audit.
//
// This check is NOT generalised to every table in tableReg here. A spike
// (run once, not committed) comparing every registered table's struct
// columns against its live schema found nine further tables with
// pre-existing, undocumented column gaps: audit_log, imapimport_account,
// jmap_categorisation_config, jmap_identities (beyond the three
// intentionally-excluded verification-token columns), jmap_states,
// mailboxes, messages, principals, sessions. Auditing each -- deciding
// which gaps are intentional (like jmap_identities' short-lived
// verification token hashes) versus real bugs, and fixing the real ones --
// is a substantial, unrelated body of work that does not belong in a
// focused fix for #478. A repo-wide version of this guard is a follow-up;
// wiring it here today would either mass-allowlist gaps nobody has audited
// (defeating the guard's purpose) or fail CI on unrelated tables this
// ticket does not touch.

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func TestBackupColumnCompleteness_EmailSubmissions(t *testing.T) {
	const table = "jmap_email_submissions"
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

	// structCols: the set of SQL columns JMAPEmailSubmissionRow declares,
	// via the same metadata the engine uses for SELECT/INSERT.
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
		if !structCols[c] {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("JMAPEmailSubmissionRow (rows.go) is missing live schema column(s) of %s: %v\n"+
			"Add each as a field with a matching json tag, or -- if the column must never be "+
			"backed up -- document why directly on the struct and add it to an explicit "+
			"exclusion list here.", table, missing)
	}
}
