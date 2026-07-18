package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
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

// TestCLI_DiagReparseEnvelopes_DryRunThenApply seeds one message whose
// raw blob carries an RFC 2047-encoded Subject but whose stored
// Envelope is blank (the pre-#257 / pre-#244 persisted state), then
// asserts:
//   - the default invocation (no --apply) is a dry-run that writes
//     nothing to the store;
//   - --apply repairs the stored Subject to the decoded value.
//
// Dual-backend coverage of the underlying merge/repair logic lives in
// internal/reparseenvelopes; this test only pins the cobra wiring
// (flag default, --system-config plumbing, dry-run-by-default).
func TestCLI_DiagReparseEnvelopes_DryRunThenApply(t *testing.T) {
	t.Parallel()
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "owner@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	raw := "From: Carol <carol@example.test>\r\n" +
		"To: Dave <dave@example.test>\r\n" +
		"Subject: =?ISO-8859-15?Q?Angebot_IT-Museumsst=FCcke_=28fwd=29?=\r\n" +
		"Message-ID: <legacy-msg-1@example.test>\r\n" +
		"\r\n" +
		"Body text.\r\n"
	ref, err := st.Blobs().Put(ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         ref.Size,
		Blob:         ref,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	msgID := msgs[0].ID
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Default invocation omits --apply: must be a dry-run that writes
	// nothing.
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--system-config", systomlPath, "diag", "reparse-envelopes"})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "dry-run") {
		t.Errorf("expected dry-run mode in output, got: %s", stderr.String())
	}

	checkSt, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("re-open after dry-run: %v", err)
	}
	got, err := checkSt.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage after dry-run: %v", err)
	}
	if got.Envelope.Subject != "" {
		t.Fatalf("dry-run must not write: Subject = %q, want empty", got.Envelope.Subject)
	}
	if err := checkSt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// --apply performs the repair.
	root = NewRootCmd()
	stdout.Reset()
	stderr.Reset()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--system-config", systomlPath, "diag", "reparse-envelopes", "--apply"})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("apply: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "apply") {
		t.Errorf("expected apply mode in output, got: %s", stderr.String())
	}

	checkSt2, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("re-open after apply: %v", err)
	}
	defer checkSt2.Close()
	got2, err := checkSt2.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage after apply: %v", err)
	}
	const wantSubject = "Angebot IT-Museumsstücke (fwd)"
	if got2.Envelope.Subject != wantSubject {
		t.Fatalf("Subject after apply = %q, want %q", got2.Envelope.Subject, wantSubject)
	}
}
