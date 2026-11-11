package email

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// The schema migrations dominate the cost of opening a fresh SQLite
// store (SQL parsing inside the pure-Go engine, several seconds per open
// under the race detector). The package builds one fully migrated
// database file once per test process and hands every test a private copy
// of it, so each test still gets its own empty on-disk store at the
// production schema while paying the migration cost a single time.
var (
	sqliteTemplateOnce sync.Once
	sqliteTemplateDir  string
	sqliteTemplatePath string
	sqliteTemplateErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sqliteTemplateDir != "" {
		_ = os.RemoveAll(sqliteTemplateDir)
	}
	os.Exit(code)
}

func buildSQLiteTemplate() {
	dir, err := os.MkdirTemp("", "herold-email-template-")
	if err != nil {
		sqliteTemplateErr = err
		return
	}
	sqliteTemplateDir = dir
	path := filepath.Join(dir, "template.db")
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), path, nil, clk)
	if err != nil {
		sqliteTemplateErr = err
		return
	}
	// Close checkpoints the WAL into the main file so the copy made by
	// OpenSQLiteStore is self-contained.
	if err := st.Close(); err != nil {
		sqliteTemplateErr = err
		return
	}
	sqliteTemplatePath = path
}

// OpenSQLiteStore opens a fresh on-disk SQLite store under t.TempDir(),
// seeded from the migrated template, and closes it at test cleanup.
func OpenSQLiteStore(t *testing.T, clk clock.Clock) store.Store {
	t.Helper()
	sqliteTemplateOnce.Do(buildSQLiteTemplate)
	if sqliteTemplateErr != nil {
		t.Fatalf("build sqlite template: %v", sqliteTemplateErr)
	}
	data, err := os.ReadFile(sqliteTemplatePath)
	if err != nil {
		t.Fatalf("read sqlite template: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(dbPath, data, 0o600); err != nil {
		t.Fatalf("write sqlite copy: %v", err)
	}
	st, err := storesqlite.Open(context.Background(), dbPath, nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
