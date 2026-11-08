package admin

// cmd_diag_ghostrows_test.go pins the CLI wiring of `herold diag
// imapimport-ghost-rows` (re #558): the default is a dry run that deletes
// nothing, and --dry-run=false removes the ghost row. Dual-backend coverage
// of the logic lives in internal/diag/ghostrows.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

func TestCLI_DiagImapImportGhostRows_DefaultDryRun(t *testing.T) {
	t.Parallel()
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	meta := st.Meta()
	p, err := meta.InsertPrincipal(ctx, store.Principal{Kind: store.PrincipalKindUser, CanonicalEmail: "ghost-558@test.local"})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	acc, err := meta.CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID: p.ID, AccountName: "A", Host: "127.0.0.1", Port: 993,
		TLSMode: store.IMAPImportTLSModeImplicit, Username: "u",
		AuthMethod: store.IMAPImportAuthMethodPassword, CredentialCT: []byte("v1:x"),
		State: store.IMAPImportAccountStateEnabled,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	if err := meta.UpsertIMAPImportFolderCursor(ctx, store.IMAPImportFolderCursor{
		AccountID: acc.ID, UpstreamFolder: "Archive", UIDValidity: 1, UIDNext: 237, HighWaterUID: 236,
	}); err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if err := meta.UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID: acc.ID, UpstreamFolder: "Archive", UpstreamUID: 29138, HeroldMessageID: 1, HeroldMailboxID: 1,
	}); err != nil {
		t.Fatalf("state: %v", err)
	}
	accID := acc.ID
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	run := func(extra ...string) string {
		root := NewRootCmd()
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs(append([]string{"--system-config", systomlPath, "diag", "imapimport-ghost-rows"}, extra...))
		root.SetContext(context.Background())
		if err := root.Execute(); err != nil {
			t.Fatalf("run %v: %v\nstderr=%s", extra, err, stderr.String())
		}
		return stdout.String()
	}
	count := func() int {
		s, err := openStore(ctx, cfg, discardLogger(), clk)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer s.Close()
		rows, err := s.Meta().ListIMAPImportMessageStatesByAccount(ctx, accID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return len(rows)
	}

	out := run()
	if !strings.Contains(out, "dry run: 1 ghost row(s) would be removed") {
		t.Errorf("default run output = %q, want dry-run wording", out)
	}
	if n := count(); n != 1 {
		t.Fatalf("default run changed state: %d rows", n)
	}
	out = run("--dry-run=false")
	if !strings.Contains(out, "removed 1 ghost row(s)") {
		t.Errorf("apply output = %q", out)
	}
	if n := count(); n != 0 {
		t.Fatalf("rows after apply = %d, want 0", n)
	}
}
