package backup

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"testing"
)

// TestCurrentSchemaVersionMatchesMaxMigration enforces the invariant
// that CurrentSchemaVersion equals the highest migration number
// shipped under both internal/storesqlite/migrations and
// internal/storepg/migrations, and that the two backends have
// numerically-identical migration sets with no gaps.
//
// This test is the canonical pre-commit / CI gate against the failure
// mode that produced commit 3a32efe (waves 3.9, 3.10, REQ-SEND-12 each
// landed a migration without bumping CurrentSchemaVersion or extending
// the diag adapter). Any future migration that forgets to update
// manifest.go will trip this test in CI and block merge.
func TestCurrentSchemaVersionMatchesMaxMigration(t *testing.T) {
	repoRoot := repoRootFromTestFile(t)

	sqliteMigrations := readMigrationNumbers(t,
		filepath.Join(repoRoot, "internal", "storesqlite", "migrations"))
	pgMigrations := readMigrationNumbers(t,
		filepath.Join(repoRoot, "internal", "storepg", "migrations"))

	if len(sqliteMigrations) == 0 {
		t.Fatalf("no SQLite migrations found")
	}
	if len(pgMigrations) == 0 {
		t.Fatalf("no Postgres migrations found")
	}

	// Per-backend invariants: contiguous from 1, no gaps, no
	// duplicates.
	assertContiguous(t, "sqlite", sqliteMigrations)
	assertContiguous(t, "postgres", pgMigrations)

	// Cross-backend invariant: identical sets. A migration that
	// exists in only one backend is a parity bug.
	if !equalIntSlices(sqliteMigrations, pgMigrations) {
		t.Fatalf("migration set mismatch:\n  sqlite:   %v\n  postgres: %v\n"+
			"every migration MUST ship to both backends in the same commit",
			sqliteMigrations, pgMigrations)
	}

	max := sqliteMigrations[len(sqliteMigrations)-1]
	if CurrentSchemaVersion != max {
		t.Fatalf("CurrentSchemaVersion = %d but the highest migration is %d.\n"+
			"When you add migration NNNN_*.sql you MUST also:\n"+
			"  (1) bump CurrentSchemaVersion in internal/diag/backup/manifest.go\n"+
			"  (2) add a comment block describing the migration\n"+
			"  (3) extend TableNames if the migration adds tables\n"+
			"  (4) extend rows.go, backend.go, adapter_sqlite.go,\n"+
			"      adapter_fakestore.go, and testharness/fakestore/diag.go\n"+
			"      with the corresponding row type + dispatch cases\n"+
			"See commit 3a32efe for a worked example.",
			CurrentSchemaVersion, max)
	}
}

// migrationFilenameRE matches NNNN_anything.sql where NNNN is a
// zero-padded decimal number. The leading number is the migration's
// ordinal; everything after the underscore is human-readable.
var migrationFilenameRE = regexp.MustCompile(`^(\d{4})_[A-Za-z0-9_]+\.sql$`)

// readMigrationNumbers reads dir, parses the leading NNNN out of every
// file matching the migration naming convention, and returns a sorted
// ascending slice of those numbers.
func readMigrationNumbers(t *testing.T, dir string) []int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %q: %v", dir, err)
	}
	var nums []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilenameRE.FindStringSubmatch(e.Name())
		if m == nil {
			t.Fatalf("migration file %q in %q does not match NNNN_*.sql convention",
				e.Name(), dir)
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("parse migration number %q: %v", m[1], err)
		}
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}

func assertContiguous(t *testing.T, label string, nums []int) {
	t.Helper()
	if nums[0] != 1 {
		t.Fatalf("%s migrations do not start at 0001 (got first = %d)", label, nums[0])
	}
	for i := 1; i < len(nums); i++ {
		if nums[i] == nums[i-1] {
			t.Fatalf("%s migrations have a duplicate number: %04d appears twice",
				label, nums[i])
		}
		if nums[i] != nums[i-1]+1 {
			t.Fatalf("%s migrations have a gap between %04d and %04d",
				label, nums[i-1], nums[i])
		}
	}
}

func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// repoRootFromTestFile walks up from the running test source file
// (this file) to the directory containing go.mod so the test works
// regardless of how `go test` was invoked.
func repoRootFromTestFile(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller(0) returned !ok")
	}
	dir := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("walked to filesystem root from %q without finding go.mod", here)
		}
		dir = parent
	}
}
