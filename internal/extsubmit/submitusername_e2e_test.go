package extsubmit_test

// submitusername_e2e_test.go is the end-to-end acceptance gate for the
// SubmitUsername fix (re #126): the SMTP AUTH (SASL) username for password
// auth must be taken from IdentitySubmission.SubmitUsername when set, and
// fall back to the identity's email address only when SubmitUsername is empty.
//
// The test drives Submitter.Probe and Submitter.Submit directly against an
// in-process fakesmtp server configured with ExpectUsername so any regression
// (sending the email address where the provider expects a bare username) is
// caught deterministically by a 535 AUTH rejection.
//
// No store is involved — this exercises extsubmit and fakesmtp only, so it
// runs in the normal CI lane without HEROLD_TEST_STORE or HEROLD_PG_DSN.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakesmtp"
)

// e2eDataKey is a 32-byte AEAD key used to seal credentials in this test.
var e2eDataKey = func() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*3 + 7)
	}
	return key
}()

func sealE2ESecret(t *testing.T, plaintext string) []byte {
	t.Helper()
	ct, err := secrets.Seal(e2eDataKey, []byte(plaintext))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return ct
}

// TestSubmitUsername_E2E drives Probe and Submit against a fakesmtp server
// that enforces ExpectUsername = "vorsitz" (distinct from the identity email
// "vorsitz@classic-computing.de"). Proves the AUTH path sends the configured
// username, not the email address, and that an empty SubmitUsername causes
// AUTH failure against such a provider (re #126).
func TestSubmitUsername_E2E(t *testing.T) {
	const (
		identityEmail = "vorsitz@classic-computing.de"
		authUsername  = "vorsitz"        // what the provider expects, NOT the email
		authPassword  = "hunter2-e2e-pw" // arbitrary test password
	)

	// Start a fake SMTP server that only accepts AUTH from authUsername.
	// Any AUTH attempt with the identity email (the pre-fix behaviour) gets
	// 535, so the outcome would be OutcomeAuthFailed, not OutcomeOK.
	fakeServer := fakesmtp.New(t, fakesmtp.Options{
		Security:       fakesmtp.Plain,
		Hostname:       "smtp.fake.test",
		ExpectUsername: authUsername,
	})

	makeSubmitter := func() *extsubmit.Submitter {
		return &extsubmit.Submitter{
			DataKey:  e2eDataKey,
			HostName: "client.test",
		}
		// No dialFn injection needed: fakeServer listens on 127.0.0.1:<port>
		// and the Submitter dials sub.SubmitHost:sub.SubmitPort directly.
	}

	sub := store.IdentitySubmission{
		IdentityID:       "e2e-identity-1",
		SubmitHost:       fakeServer.Host(),
		SubmitPort:       fakeServer.Port(),
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		SubmitUsername:   authUsername,
		PasswordCT:       sealE2ESecret(t, authPassword),
	}

	ctx := context.Background()

	// Positive: Probe with SubmitUsername set must succeed because the fake
	// server accepts "vorsitz" and rejects any other username.
	t.Run("Probe_SubmitUsername_OK", func(t *testing.T) {
		out := makeSubmitter().Probe(ctx, sub, identityEmail)
		if out.State != extsubmit.OutcomeOK {
			t.Fatalf("Probe: state=%q diagnostic=%q; want OutcomeOK", out.State, out.Diagnostic)
		}
	})

	// Positive: Submit with SubmitUsername set must succeed and the fake must
	// record AuthIdentity == "vorsitz". MAIL FROM must stay the identity email.
	t.Run("Submit_SubmitUsername_OK_AndRecordedByFake", func(t *testing.T) {
		body := "From: " + identityEmail + "\r\nTo: recipient@example.com\r\nSubject: e2e\r\n\r\nBody.\r\n"
		env := extsubmit.Envelope{
			MailFrom:      identityEmail,
			RcptTo:        []string{"recipient@example.com"},
			Body:          bytes.NewReader([]byte(body)),
			CorrelationID: "e2e-submit-001",
		}
		before := len(fakeServer.Messages())
		out := makeSubmitter().Submit(ctx, sub, env)
		if out.State != extsubmit.OutcomeOK {
			t.Fatalf("Submit: state=%q diagnostic=%q; want OutcomeOK", out.State, out.Diagnostic)
		}
		msgs := fakeServer.Messages()
		if len(msgs) <= before {
			t.Fatal("fakesmtp recorded no new message after Submit")
		}
		m := msgs[len(msgs)-1]
		if m.AuthIdentity != authUsername {
			t.Errorf("AuthIdentity = %q; want %q (SubmitUsername, not email)", m.AuthIdentity, authUsername)
		}
		// MAIL FROM must remain the identity's email even though AUTH used a
		// different username.
		if m.MailFrom != identityEmail {
			t.Errorf("MAIL FROM = %q; want %q (identity email unchanged)", m.MailFrom, identityEmail)
		}
		if !strings.Contains(string(m.Data), "Body.") {
			t.Errorf("message body missing expected content; got %q", string(m.Data))
		}
	})

	// Negative: SubmitUsername="" falls back to the identity email as the AUTH
	// username. Against a server that expects "vorsitz" this must fail with
	// OutcomeAuthFailed, proving the email fallback is what broke the original
	// bug and that an explicit SubmitUsername is required for such providers.
	t.Run("Probe_EmptySubmitUsername_AuthFails", func(t *testing.T) {
		subNoUser := sub
		subNoUser.SubmitUsername = ""
		out := makeSubmitter().Probe(ctx, subNoUser, identityEmail)
		if out.State != extsubmit.OutcomeAuthFailed {
			t.Fatalf("Probe with empty SubmitUsername against ExpectUsername=%q: state=%q; want OutcomeAuthFailed",
				authUsername, out.State)
		}
	})
}
