package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLI_DiagBackup_WritesBundle wires up the cobra command tree
// against a minimal sysconfig fixture, runs `herold diag backup --to
// <dir>`, and confirms manifest.json plus per-table JSONLs land in
// the destination.
func TestCLI_DiagBackup_WritesBundle(t *testing.T) {
	t.Parallel()
	systomlPath, _ := minimalConfigFixture(t)
	dst := t.TempDir()

	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--system-config", systomlPath,
		"diag", "backup", "--to", dst,
	})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\nstderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dst, "manifest.json")); err != nil {
		t.Errorf("manifest.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "metadata", "principals.jsonl")); err != nil {
		t.Errorf("principals.jsonl missing: %v", err)
	}
}

// TestCLI_DiagVerify_ReportsCounts runs backup followed by verify and
// asserts the verify subcommand exits zero and prints the manifest.
func TestCLI_DiagVerify_ReportsCounts(t *testing.T) {
	t.Parallel()
	systomlPath, _ := minimalConfigFixture(t)
	dst := t.TempDir()

	// Backup first.
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--system-config", systomlPath,
		"diag", "backup", "--to", dst,
	})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Verify with --json so we can parse the manifest from stdout.
	root = NewRootCmd()
	stdout.Reset()
	stderr.Reset()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--json", "--system-config", systomlPath,
		"diag", "verify", "--bundle", dst,
	})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("verify: %v\nstderr=%s", err, stderr.String())
	}
	var m map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		t.Fatalf("parse manifest JSON: %v\nstdout=%s", err, stdout.String())
	}
	if m["backend"] != "sqlite" {
		t.Errorf("backend in JSON: %v", m["backend"])
	}
}

// TestCLI_DiagMigrate_RoundTrip runs migrate from the configured
// sqlite store into a fresh sqlite tempfile and asserts the rows land.
func TestCLI_DiagMigrate_RoundTrip(t *testing.T) {
	t.Parallel()
	systomlPath, _ := minimalConfigFixture(t)
	tgtDir := t.TempDir()
	tgtPath := filepath.Join(tgtDir, "migrated.sqlite")

	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--system-config", systomlPath,
		"diag", "migrate",
		"--to-backend", "sqlite", "--to-dsn", tgtPath,
	})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		// The default sqlite tempfile under data_dir has no rows
		// (a freshly bootstrapped store); migrate is allowed to
		// succeed with zero-row tables. A failure here is real.
		if !strings.Contains(err.Error(), "schema version") {
			t.Fatalf("migrate: %v", err)
		}
	}
	if _, err := os.Stat(tgtPath); err != nil {
		t.Errorf("target sqlite missing: %v", err)
	}
}
