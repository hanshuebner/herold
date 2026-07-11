package directory_test

// devicetoken_test.go exercises the bearer-token grant (issue #199):
// issuance (with and without TOTP), verification (hash + scope shape),
// and revocation (via the existing APIKey delete path) and expiry
// (device tokens do not expire on their own -- see devicetoken.go).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

func TestIssueDeviceToken_Success(t *testing.T) {
	ctx := context.Background()
	dir, fs, _ := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	plaintext, key, err := dir.IssueDeviceToken(ctx, "alice@example.test", "correct-horse-staple", "", "Pixel 8")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(plaintext, directory.DeviceTokenPrefix) {
		t.Fatalf("plaintext token missing prefix %q: %q", directory.DeviceTokenPrefix, plaintext)
	}
	if key.PrincipalID != pid {
		t.Fatalf("key.PrincipalID = %d, want %d", key.PrincipalID, pid)
	}
	if label, ok := directory.DeviceTokenLabel(key.Name); !ok || label != "Pixel 8" {
		t.Fatalf("DeviceTokenLabel(%q) = (%q, %v), want (\"Pixel 8\", true)", key.Name, label, ok)
	}

	// Verification: the plaintext hashes to exactly the stored Hash, and
	// the stored row round-trips through the store by hash (the same
	// lookup protojmap/protoadmin's Bearer-auth path performs).
	if got := directory.HashDeviceToken(plaintext); got != key.Hash {
		t.Fatalf("HashDeviceToken(plaintext) = %q, want stored hash %q", got, key.Hash)
	}
	stored, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(plaintext))
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if stored.ID != key.ID {
		t.Fatalf("stored.ID = %d, want %d", stored.ID, key.ID)
	}

	// Scope: full end-user set, matching a Suite session cookie -- not a
	// mail-only scope, and never admin.
	var scopes auth.ScopeSet
	if err := json.Unmarshal([]byte(stored.ScopeJSON), &scopes); err != nil {
		t.Fatalf("unmarshal scope: %v", err)
	}
	for _, want := range auth.AllEndUserScopes {
		if !scopes.Has(want) {
			t.Errorf("scope set missing %q: %v", want, scopes)
		}
	}
	if scopes.Has(auth.ScopeAdmin) {
		t.Errorf("device token scope must never include admin: %v", scopes)
	}
}

func TestIssueDeviceToken_DefaultLabel(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	if _, err := dir.CreatePrincipal(ctx, "bob@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, key, err := dir.IssueDeviceToken(ctx, "bob@example.test", "correct-horse-staple", "", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if label, ok := directory.DeviceTokenLabel(key.Name); !ok || label != "device" {
		t.Fatalf("empty label should default to \"device\", got (%q, %v)", label, ok)
	}
}

func TestIssueDeviceToken_WrongPassword(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	if _, err := dir.CreatePrincipal(ctx, "carol@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err := dir.IssueDeviceToken(ctx, "carol@example.test", "wrong-password", "", "")
	if !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestIssueDeviceToken_UnknownEmail(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	_, _, err := dir.IssueDeviceToken(ctx, "nobody@example.test", "whatever12345", "", "")
	if !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized (anti-enumeration), got %v", err)
	}
}

// TestIssueDeviceToken_TOTPRequired asserts that a principal with TOTP
// enrolled cannot obtain a device token from password alone (mirrors
// the step_up_required signal of POST /api/v1/auth/login).
func TestIssueDeviceToken_TOTPRequired(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "dave@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	secret, _, err := dir.EnrollTOTP(ctx, pid)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	code := mustGenerate(t, secret, clk.Now())
	if err := dir.ConfirmTOTP(ctx, pid, code); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// No code supplied at all.
	_, _, err = dir.IssueDeviceToken(ctx, "dave@example.test", "correct-horse-staple", "", "phone")
	if !errors.Is(err, directory.ErrTOTPRequired) {
		t.Fatalf("want ErrTOTPRequired, got %v", err)
	}

	// Wrong code: mirrors REQ-AUTH-JSON-LOGIN, which maps "absent or
	// incorrect" to the same step_up_required signal once the password
	// has already been proven correct.
	_, _, err = dir.IssueDeviceToken(ctx, "dave@example.test", "correct-horse-staple", "000000", "phone")
	if !errors.Is(err, directory.ErrTOTPRequired) {
		t.Fatalf("want ErrTOTPRequired for wrong TOTP code, got %v", err)
	}

	// Correct code succeeds.
	clk.Advance(1 * time.Second)
	code2 := mustGenerate(t, secret, clk.Now())
	plaintext, key, err := dir.IssueDeviceToken(ctx, "dave@example.test", "correct-horse-staple", code2, "phone")
	if err != nil {
		t.Fatalf("issue with valid totp: %v", err)
	}
	if plaintext == "" || key.ID == 0 {
		t.Fatalf("expected a minted token, got plaintext=%q key=%+v", plaintext, key)
	}
}

// TestIssueDeviceToken_Revocation asserts that deleting the minted
// APIKey row (the existing self-service / admin revoke path, DELETE
// /api/v1/api-keys/{id}) makes the token unverifiable, and that revoking
// one device token does not affect another token issued to the same
// principal (REQ-AND-AUTH-21/22: revoking one device signs out only that
// device).
func TestIssueDeviceToken_Revocation(t *testing.T) {
	ctx := context.Background()
	dir, fs, _ := newDir(t)
	if _, err := dir.CreatePrincipal(ctx, "erin@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}

	plaintextA, keyA, err := dir.IssueDeviceToken(ctx, "erin@example.test", "correct-horse-staple", "", "device A")
	if err != nil {
		t.Fatalf("issue A: %v", err)
	}
	plaintextB, keyB, err := dir.IssueDeviceToken(ctx, "erin@example.test", "correct-horse-staple", "", "device B")
	if err != nil {
		t.Fatalf("issue B: %v", err)
	}

	if err := fs.Meta().DeleteAPIKey(ctx, keyA.ID); err != nil {
		t.Fatalf("revoke A: %v", err)
	}

	if _, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(plaintextA)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked token A should be unverifiable, got err=%v", err)
	}
	stillValid, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(plaintextB))
	if err != nil {
		t.Fatalf("token B should still verify after revoking A: %v", err)
	}
	if stillValid.ID != keyB.ID {
		t.Fatalf("token B resolved to the wrong key: got %d want %d", stillValid.ID, keyB.ID)
	}
}

// TestIssueDeviceToken_NoBuiltInExpiry documents that a minted device
// token, unlike a Suite session cookie, has no encoded expiry: it is a
// long-lived credential whose only lifecycle control is revocation
// (matches the existing "hk_" API-key model; see devicetoken.go for the
// rationale and the open question on a future short-lived access +
// refresh token pair).
func TestIssueDeviceToken_NoBuiltInExpiry(t *testing.T) {
	ctx := context.Background()
	dir, fs, clk := newDir(t)
	if _, err := dir.CreatePrincipal(ctx, "frank@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	plaintext, key, err := dir.IssueDeviceToken(ctx, "frank@example.test", "correct-horse-staple", "", "laptop")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	clk.Advance(365 * 24 * time.Hour)
	stored, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(plaintext))
	if err != nil {
		t.Fatalf("token should still verify a year later (no expiry): %v", err)
	}
	if stored.ID != key.ID {
		t.Fatalf("stored.ID = %d, want %d", stored.ID, key.ID)
	}
}
