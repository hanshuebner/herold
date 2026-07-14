package directory_test

// subaccount_auth_test.go proves REQ-SUBACCT-02 at the seam every
// password-based auth path shares: Directory.Authenticate. Session-
// cookie login (protologin), IMAP/SMTP-submission/ManageSieve SASL
// PLAIN and LOGIN (internal/sasl, constructed with a *Directory
// Authenticator), device tokens (IssueDeviceToken), and the OAuth2
// authorization-code grant (IssueAuthorizationCode) all call
// Authenticate (the last two via the shared authenticateWithOptionalTOTP
// core) before minting anything -- so a rejection here is a rejection
// on every one of those paths.
//
// Each test forces a valid credential onto a sub-principal row via a
// direct store write (something no application code path does --
// InsertSubPrincipal refuses a credential outright) specifically so the
// assertion proves the Kind check is what blocks authentication, not
// merely the absence of a password.

import (
	"context"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// forceCredentialOntoSubPrincipal creates a sub-principal under parentID
// and force-writes a working password hash onto its row by borrowing the
// hash from a disposable ordinary principal created with the same
// password. Returns the sub-principal's email and the matching
// plaintext password.
func forceCredentialOntoSubPrincipal(t *testing.T, ctx context.Context, dir *directory.Directory, fs store.Store, parentID store.PrincipalID, subEmail, password string) string {
	t.Helper()
	sub, err := fs.Meta().InsertSubPrincipal(ctx, parentID, store.Principal{
		CanonicalEmail: subEmail,
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	if sub.PasswordHash != "" {
		t.Fatalf("freshly inserted sub-principal already carries a password hash")
	}

	// Borrow a valid Argon2id hash for `password` from a disposable
	// ordinary principal (the directory package's hashPassword helper is
	// unexported; this test lives in package directory_test).
	donor, err := dir.CreatePrincipal(ctx, "donor-"+subEmail, password)
	if err != nil {
		t.Fatalf("CreatePrincipal(donor): %v", err)
	}
	donorRow, err := fs.Meta().GetPrincipalByID(ctx, donor)
	if err != nil {
		t.Fatalf("GetPrincipalByID(donor): %v", err)
	}

	sub.PasswordHash = donorRow.PasswordHash
	if err := fs.Meta().UpdatePrincipal(ctx, sub); err != nil {
		t.Fatalf("UpdatePrincipal(force credential onto sub-principal): %v", err)
	}

	// Confirm the forced write landed and the row is still a
	// sub-principal (UpdatePrincipal must not have silently reclassified
	// it) before handing back to the caller.
	forced, err := fs.Meta().GetPrincipalByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(sub after force-write): %v", err)
	}
	if forced.PasswordHash == "" {
		t.Fatalf("forced credential did not persist")
	}
	if forced.Kind != store.PrincipalKindSubAccount {
		t.Fatalf("forced-credential row Kind = %v, want PrincipalKindSubAccount", forced.Kind)
	}
	return subEmail
}

// TestSubPrincipal_Authenticate_Rejected is the core seam test:
// Directory.Authenticate refuses a sub-principal even when it carries a
// verifiably-correct password hash.
func TestSubPrincipal_Authenticate_Rejected(t *testing.T) {
	ctx := context.Background()
	dir, fs, _ := newDir(t)
	parent, err := dir.CreatePrincipal(ctx, "subauth-parent@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("CreatePrincipal(parent): %v", err)
	}
	email := forceCredentialOntoSubPrincipal(t, ctx, dir, fs, parent, "subauth-sub@example.test", "sub-account-password-1")

	if _, err := dir.Authenticate(ctx, email, "sub-account-password-1"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("Authenticate(sub-principal, correct forced password) = %v, want ErrUnauthorized", err)
	}
}

// TestSubPrincipal_IssueDeviceToken_Rejected proves the device-token
// grant (native/mobile client bootstrap, issue #199) refuses a
// sub-principal.
func TestSubPrincipal_IssueDeviceToken_Rejected(t *testing.T) {
	ctx := context.Background()
	dir, fs, _ := newDir(t)
	parent, err := dir.CreatePrincipal(ctx, "devtok-parent@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("CreatePrincipal(parent): %v", err)
	}
	email := forceCredentialOntoSubPrincipal(t, ctx, dir, fs, parent, "devtok-sub@example.test", "sub-account-password-2")

	_, _, err = dir.IssueDeviceToken(ctx, email, "sub-account-password-2", "", "test-device")
	if !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("IssueDeviceToken(sub-principal) = %v, want ErrUnauthorized", err)
	}
}

// TestSubPrincipal_IssueAuthorizationCode_Rejected proves the OAuth2
// authorization-code grant (issue #199) refuses a sub-principal.
func TestSubPrincipal_IssueAuthorizationCode_Rejected(t *testing.T) {
	ctx := context.Background()
	dir, fs, clk := newDir(t)
	client := mustRegisterAndroidClient(t, dir)
	_ = client
	parent, err := dir.CreatePrincipal(ctx, "oauthsub-parent@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("CreatePrincipal(parent): %v", err)
	}
	email := forceCredentialOntoSubPrincipal(t, ctx, dir, fs, parent, "oauthsub-sub@example.test", "sub-account-password-3")

	_, challenge := newPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	req := baseAuthorizeRequest(dir, clk.Now(), redirectURI, challenge)

	_, err = dir.IssueAuthorizationCode(ctx, email, "sub-account-password-3", "", req)
	if !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("IssueAuthorizationCode(sub-principal) = %v, want ErrUnauthorized", err)
	}
}
