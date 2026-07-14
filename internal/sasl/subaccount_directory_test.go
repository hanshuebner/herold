package sasl_test

// subaccount_directory_test.go proves REQ-SUBACCT-02 for SASL PLAIN and
// LOGIN backed by a real *directory.Directory -- the exact construction
// internal/protoimap, internal/protosmtp, and internal/protomanagesieve
// use (sasl.NewPLAIN(dir) / sasl.NewLOGIN(dir), each with the session's
// *directory.Directory as the sasl.Authenticator). Since all three
// protocols wire the identical constructor to the identical directory
// type, a rejection here covers IMAP AUTH, SMTP submission AUTH, and
// ManageSieve AUTHENTICATE without needing three separate protocol-level
// harnesses.
//
// The sub-principal here carries a force-written, verifiably-correct
// password hash (something no application code path ever does --
// InsertSubPrincipal refuses a credential outright) so the assertion
// proves the store-level Kind check inside Directory.Authenticate is
// what blocks the mechanism, not merely the absence of a password.

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// newSubAccountDirectory builds a real Directory (backed by a fresh
// storesqlite instance) with one individual parent principal and one
// sub-principal under it whose row has been force-written with a valid
// password hash for the given plaintext. Returns the directory and the
// sub-principal's email.
func newSubAccountDirectory(t *testing.T, password string) (*directory.Directory, string) {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	fs, err := storesqlite.OpenWithRand(ctx, dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(fs.Meta(), slog.Default(), clk, rand.Reader)

	parent, err := dir.CreatePrincipal(ctx, "sasl-parent@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("CreatePrincipal(parent): %v", err)
	}
	// Borrow a valid Argon2id hash for `password` from a disposable
	// ordinary principal (directory's password hashing helper is
	// unexported).
	donor, err := dir.CreatePrincipal(ctx, "sasl-donor@example.test", password)
	if err != nil {
		t.Fatalf("CreatePrincipal(donor): %v", err)
	}
	donorRow, err := fs.Meta().GetPrincipalByID(ctx, donor)
	if err != nil {
		t.Fatalf("GetPrincipalByID(donor): %v", err)
	}

	subEmail := "sasl-sub@example.test"
	sub, err := fs.Meta().InsertSubPrincipal(ctx, parent, store.Principal{CanonicalEmail: subEmail})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	sub.PasswordHash = donorRow.PasswordHash
	if err := fs.Meta().UpdatePrincipal(ctx, sub); err != nil {
		t.Fatalf("UpdatePrincipal(force credential): %v", err)
	}
	return dir, subEmail
}

// TestSubPrincipal_PLAIN_Rejected proves SASL PLAIN (IMAP AUTH PLAIN,
// SMTP submission AUTH PLAIN, ManageSieve AUTHENTICATE "PLAIN") rejects
// a sub-principal even with a verifiably-correct password.
func TestSubPrincipal_PLAIN_Rejected(t *testing.T) {
	dir, email := newSubAccountDirectory(t, "sub-account-password-plain")
	m := sasl.NewPLAIN(dir)
	ctx := sasl.WithTLS(context.Background(), true)
	ir := []byte("\x00" + email + "\x00" + "sub-account-password-plain")
	_, _, err := m.Start(ctx, ir)
	if !errors.Is(err, sasl.ErrAuthFailed) {
		t.Fatalf("PLAIN(sub-principal, correct forced password) = %v, want ErrAuthFailed", err)
	}
}

// TestSubPrincipal_LOGIN_Rejected proves SASL LOGIN (IMAP AUTH LOGIN,
// SMTP submission AUTH LOGIN, ManageSieve AUTHENTICATE "LOGIN") rejects
// a sub-principal even with a verifiably-correct password.
func TestSubPrincipal_LOGIN_Rejected(t *testing.T) {
	dir, email := newSubAccountDirectory(t, "sub-account-password-login")
	m := sasl.NewLOGIN(dir)
	ctx := sasl.WithTLS(context.Background(), true)
	if _, _, err := m.Start(ctx, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, _, err := m.Next(ctx, []byte(email)); err != nil {
		t.Fatalf("Next(username): %v", err)
	}
	_, _, err := m.Next(ctx, []byte("sub-account-password-login"))
	if !errors.Is(err, sasl.ErrAuthFailed) {
		t.Fatalf("LOGIN(sub-principal, correct forced password) = %v, want ErrAuthFailed", err)
	}
}
