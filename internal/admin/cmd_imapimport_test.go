package admin

// cmd_imapimport_test.go — CLI tests for `herold imapimport status`.
//
// REQ-IMAP-IMP-62.

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
)

// TestCLIIMAPImportStatus_Empty verifies that `herold imapimport status`
// returns a 200 OK with an empty items list when no workers are running.
func TestCLIIMAPImportStatus_Empty(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("imapimport", "status", "--json")
	if err != nil {
		t.Fatalf("imapimport status: %v", err)
	}
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("expected items field; got %s", out)
	}
}

// TestCLIIMAPImportStatus_WithData verifies that the status command renders
// worker data returned by the provider. We rebuild the admin server with a
// fake status provider seeded with one worker status.
func TestCLIIMAPImportStatus_WithData(t *testing.T) {
	fakeSnap := []protoadmin.IMAPImportWorkerStatus{
		{
			AccountID:           "acct-1",
			PrincipalID:         "42",
			AccountName:         "my-gmail",
			Host:                "imap.gmail.com",
			Phase:               "syncing",
			CurrentFolder:       "INBOX",
			ConnMode:            "dual",
			Connected:           true,
			ConsecutiveFailures: 0,
			MessagesFetched:     137,
			FlagsPropagated:     5,
		},
	}
	env := newCLITestEnv(t, func(opts *protoadmin.Options) {
		opts.IMAPImportStatus = &staticIMAPStatusProvider{snap: fakeSnap}
	})

	// JSON output must contain the account id.
	out, _, err := env.run("imapimport", "status", "--json")
	if err != nil {
		t.Fatalf("imapimport status --json: %v", err)
	}
	if !strings.Contains(out, "acct-1") {
		t.Fatalf("expected acct-1 in JSON output; got %s", out)
	}
	if !strings.Contains(out, "imap.gmail.com") {
		t.Fatalf("expected imap.gmail.com in JSON output; got %s", out)
	}
}

// TestCLIIMAPImportOwnAddresses_SetAndList is the #396 (third round)
// regression test for the CLI half of required outcome 1: `own-addresses
// set` replaces the configured list via the admin REST PATCH endpoint,
// and `own-addresses list` reads it back, scoped to one account id.
func TestCLIIMAPImportOwnAddresses_SetAndList(t *testing.T) {
	env := newCLITestEnv(t, nil)
	ctx := context.Background()

	p, err := env.store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "cli-own-addr@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	acc, err := env.store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "CLI Test Account",
		Host:         "imap.example.test",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "cli-own-addr@example.test",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: []byte("v1:test"),
		State:        store.IMAPImportAccountStateEnabled,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	if _, _, err := env.run("imapimport", "own-addresses", "set", "cli-own-addr@example.test", acc.ID,
		"info@classic-computing.de", "vorstand@classic-computing.de"); err != nil {
		t.Fatalf("own-addresses set: %v", err)
	}

	out, _, err := env.run("imapimport", "own-addresses", "list", "cli-own-addr@example.test", acc.ID, "--json")
	if err != nil {
		t.Fatalf("own-addresses list: %v", err)
	}
	if !strings.Contains(out, "info@classic-computing.de") || !strings.Contains(out, "vorstand@classic-computing.de") {
		t.Fatalf("expected both configured addresses in JSON output; got %s", out)
	}

	row, err := env.store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	want := []string{"info@classic-computing.de", "vorstand@classic-computing.de"}
	if !equalStringSlicesAdmin(row.OwnAddresses, want) {
		t.Fatalf("stored OwnAddresses = %v, want %v", row.OwnAddresses, want)
	}
}

// TestCLIIMAPImportRestoreArchive_RefusalExitsNonZeroForceOverrides is the
// CLI-level regression test for the restore-archive eligibility guard (re
// #376, second round follow-up): a message in INBOX matching neither #376
// shape makes the command exit non-zero and leaves the message untouched;
// --force overrides the refusal and moves it.
func TestCLIIMAPImportRestoreArchive_RefusalExitsNonZeroForceOverrides(t *testing.T) {
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "restore-cli@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inboxMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	blob, err := st.Blobs().Put(ctx, strings.NewReader("body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "restore-archive-cli", MessageID: "cli-refused@test"},
	}, []store.MessageMailbox{{MailboxID: inboxMB.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg, err := st.Meta().GetMessageByMessageIDHeader(ctx, p.ID, "cli-refused@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	msgIDStr := strconv.FormatUint(uint64(msg.ID), 10)

	runRestoreArchive := func(extraArgs ...string) (stdout, stderr string, err error) {
		root := NewRootCmd()
		var out, errBuf bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errBuf)
		args := append([]string{
			"--system-config", systomlPath,
			"imapimport", "restore-archive", "restore-cli@example.test",
			"--message", msgIDStr,
		}, extraArgs...)
		root.SetArgs(args)
		root.SetContext(context.Background())
		err = root.Execute()
		return out.String(), errBuf.String(), err
	}

	// Without --force: the command exits non-zero and leaves the message
	// in INBOX.
	stdout, _, err := runRestoreArchive()
	if err == nil {
		t.Fatal("restore-archive on an ineligible message should exit non-zero without --force")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("error = %q; want it to mention the refusal", err.Error())
	}
	if !strings.Contains(stdout, "refused") {
		t.Errorf("stdout = %q; want the per-message refusal reported", stdout)
	}

	st2, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore (recheck): %v", err)
	}
	afterRefuse, err := st2.Meta().GetMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage (after refused run): %v", err)
	}
	if len(afterRefuse.Mailboxes) != 1 || afterRefuse.Mailboxes[0].MailboxID != inboxMB.ID {
		t.Fatalf("refused message was modified: mailboxes = %+v", afterRefuse.Mailboxes)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// With --force: the command succeeds and moves the message.
	_, _, err = runRestoreArchive("--force")
	if err != nil {
		t.Fatalf("restore-archive --force: %v", err)
	}

	st3, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore (final): %v", err)
	}
	defer st3.Close()
	afterForce, err := st3.Meta().GetMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage (after forced run): %v", err)
	}
	for _, mm := range afterForce.Mailboxes {
		if mm.MailboxID == inboxMB.ID {
			t.Fatalf("forced message still has an INBOX membership: %+v", afterForce.Mailboxes)
		}
	}
}

// equalStringSlicesAdmin is protoadmin's equalStringSlices, duplicated
// here since it lives in a different package's test file.
func equalStringSlicesAdmin(a, b []string) bool {
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

// staticIMAPStatusProvider is a minimal protoadmin.IMAPImportStatusProvider
// that returns a fixed snapshot. Used only in this test file.
type staticIMAPStatusProvider struct {
	snap []protoadmin.IMAPImportWorkerStatus
}

func (s *staticIMAPStatusProvider) Snapshot() []protoadmin.IMAPImportWorkerStatus {
	return s.snap
}
