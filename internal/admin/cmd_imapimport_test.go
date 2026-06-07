package admin

// cmd_imapimport_test.go — CLI tests for `herold imapimport status`.
//
// REQ-IMAP-IMP-62.

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protoadmin"
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

// staticIMAPStatusProvider is a minimal protoadmin.IMAPImportStatusProvider
// that returns a fixed snapshot. Used only in this test file.
type staticIMAPStatusProvider struct {
	snap []protoadmin.IMAPImportWorkerStatus
}

func (s *staticIMAPStatusProvider) Snapshot() []protoadmin.IMAPImportWorkerStatus {
	return s.snap
}
