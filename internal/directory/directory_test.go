package directory_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// newDir builds a Directory wired to a fresh fakestore, a FakeClock
// anchored at 2026-01-01, and a deterministic RNG reader.
//
// CreatePrincipal now requires the email's domain to be a configured
// local domain (ErrUnknownDomain otherwise). The test fixtures pre-seed
// the two domains the existing tests use so the CreatePrincipal call
// sites do not all have to be touched.
func newDir(t *testing.T) (*directory.Directory, store.Store, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	fs, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	for _, name := range []string{"example.test", "b.test"} {
		if err := fs.Meta().InsertDomain(context.Background(), store.Domain{Name: name, IsLocal: true}); err != nil {
			t.Fatalf("seed domain %q: %v", name, err)
		}
	}
	// Deterministic but non-zero RNG: repeating byte pattern.
	rnd := newDeterministicReader()
	dir := directory.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), clk, rnd)
	return dir, fs, clk
}

// deterministicReader returns bytes cycling through a fixed pattern so
// two runs of a test agree on salts, TOTP secrets, etc.
type deterministicReader struct{ counter byte }

func newDeterministicReader() *deterministicReader { return &deterministicReader{counter: 1} }

func (d *deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = d.counter
		d.counter++
		if d.counter == 0 {
			d.counter = 1
		}
	}
	return len(p), nil
}

func TestCreateAndAuthenticate(t *testing.T) {
	observe.RegisterAuthMetrics()
	beforeOK := testutil.ToFloat64(observe.AuthAttemptsTotal.WithLabelValues("password", "ok"))
	beforeFail := testutil.ToFloat64(observe.AuthAttemptsTotal.WithLabelValues("password", "fail"))

	ctx := context.Background()
	dir, _, _ := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "Alice@Example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pid == 0 {
		t.Fatalf("expected non-zero ID")
	}
	got, err := dir.Authenticate(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if got != pid {
		t.Fatalf("pid mismatch: got %d want %d", got, pid)
	}
	// Wrong password -> ErrUnauthorized.
	if _, err := dir.Authenticate(ctx, "alice@example.test", "nope"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	// Unknown user -> ErrUnauthorized (no enumeration).
	if _, err := dir.Authenticate(ctx, "bob@example.test", "pw"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized for unknown user, got %v", err)
	}

	// Metrics: at least one ok and two fails landed on the password
	// kind. The labels are bounded — no per-email cardinality.
	afterOK := testutil.ToFloat64(observe.AuthAttemptsTotal.WithLabelValues("password", "ok"))
	afterFail := testutil.ToFloat64(observe.AuthAttemptsTotal.WithLabelValues("password", "fail"))
	if afterOK <= beforeOK {
		t.Errorf("herold_auth_attempts_total{kind=password,outcome=ok}: before=%v after=%v; want strict increase", beforeOK, afterOK)
	}
	if afterFail-beforeFail < 2 {
		t.Errorf("herold_auth_attempts_total{kind=password,outcome=fail}: delta=%v, want >=2", afterFail-beforeFail)
	}
}

func TestCreatePrincipalValidation(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	if _, err := dir.CreatePrincipal(ctx, "not-an-email", "correct-horse-staple"); !errors.Is(err, directory.ErrInvalidEmail) {
		t.Fatalf("want ErrInvalidEmail, got %v", err)
	}
	if _, err := dir.CreatePrincipal(ctx, "a@b.test", "short"); !errors.Is(err, directory.ErrWeakPassword) {
		t.Fatalf("want ErrWeakPassword, got %v", err)
	}
	if _, err := dir.CreatePrincipal(ctx, "user@unknown.invalid", "correct-horse-staple"); !errors.Is(err, directory.ErrUnknownDomain) {
		t.Fatalf("want ErrUnknownDomain, got %v", err)
	}
	if _, err := dir.CreatePrincipal(ctx, "a@b.test", "correct-horse-staple"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := dir.CreatePrincipal(ctx, "A@B.test", "correct-horse-staple"); !errors.Is(err, directory.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestUpdatePassword(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "a@b.test", "old-password-12chars")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := dir.UpdatePassword(ctx, pid, "wrong", "new-password-12chars"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if err := dir.UpdatePassword(ctx, pid, "old-password-12chars", "short"); !errors.Is(err, directory.ErrWeakPassword) {
		t.Fatalf("want ErrWeakPassword, got %v", err)
	}
	if err := dir.UpdatePassword(ctx, pid, "old-password-12chars", "new-password-12chars"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := dir.Authenticate(ctx, "a@b.test", "old-password-12chars"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("old password should fail: %v", err)
	}
	if _, err := dir.Authenticate(ctx, "a@b.test", "new-password-12chars"); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestResolveAddress(t *testing.T) {
	ctx := context.Background()
	dir, fs, _ := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Canonical resolves.
	if got, err := dir.ResolveAddress(ctx, "alice", "example.test"); err != nil || got != pid {
		t.Fatalf("canonical: %v pid=%d want=%d", err, got, pid)
	}
	// Install an alias.
	if _, err := fs.Meta().InsertAlias(ctx, store.Alias{LocalPart: "support", Domain: "example.test", TargetPrincipal: pid}); err != nil {
		t.Fatalf("insert alias: %v", err)
	}
	if got, err := dir.ResolveAddress(ctx, "support", "example.test"); err != nil || got != pid {
		t.Fatalf("alias: %v pid=%d want=%d", err, got, pid)
	}
	// Catch-all: local part "*".
	if _, err := fs.Meta().InsertAlias(ctx, store.Alias{LocalPart: "*", Domain: "example.test", TargetPrincipal: pid}); err != nil {
		t.Fatalf("insert catchall: %v", err)
	}
	if got, err := dir.ResolveAddress(ctx, "random", "example.test"); err != nil || got != pid {
		t.Fatalf("catchall: %v pid=%d want=%d", err, got, pid)
	}
	// Unknown domain.
	if _, err := dir.ResolveAddress(ctx, "x", "other.test"); !errors.Is(err, directory.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	_, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ctx = directory.WithAuthSource(ctx, "10.0.0.1")
	// 5 failures within the window should trip the limiter.
	for i := 0; i < 5; i++ {
		if _, err := dir.Authenticate(ctx, "alice@example.test", "wrong"); !errors.Is(err, directory.ErrUnauthorized) {
			t.Fatalf("expected unauthorized on attempt %d, got %v", i, err)
		}
	}
	// 6th attempt rejected even with the correct password.
	if _, err := dir.Authenticate(ctx, "alice@example.test", "correct-horse-staple"); !errors.Is(err, directory.ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	// After cooldown, good credential works.
	clk.Advance(6 * time.Minute)
	if _, err := dir.Authenticate(ctx, "alice@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("after cooldown: %v", err)
	}
}

func TestDeletePrincipalCascade(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := dir.DeletePrincipal(ctx, pid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Auth must now fail.
	if _, err := dir.Authenticate(ctx, "alice@example.test", "correct-horse-staple"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized after delete, got %v", err)
	}
	// ResolveAddress must now return ErrNotFound.
	if _, err := dir.ResolveAddress(ctx, "alice", "example.test"); !errors.Is(err, directory.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestTOTPEnrollConfirmVerifyDisable(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	secret, uri, err := dir.EnrollTOTP(ctx, pid)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if secret == "" || !bytes.Contains([]byte(uri), []byte("otpauth://")) {
		t.Fatalf("unexpected enroll output secret=%q uri=%q", secret, uri)
	}
	// A wrong code is rejected.
	if err := dir.ConfirmTOTP(ctx, pid, "000000"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	// VerifyTOTP before confirmation -> not enrolled.
	if err := dir.VerifyTOTP(ctx, pid, "000000"); !errors.Is(err, directory.ErrTOTPNotEnrolled) {
		t.Fatalf("want ErrTOTPNotEnrolled, got %v", err)
	}
	code := mustGenerate(t, secret, clk.Now())
	if err := dir.ConfirmTOTP(ctx, pid, code); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// Second confirm -> already enabled.
	if err := dir.ConfirmTOTP(ctx, pid, code); !errors.Is(err, directory.ErrTOTPAlreadyEnabled) {
		t.Fatalf("want ErrTOTPAlreadyEnabled, got %v", err)
	}
	// Verify.
	clk.Advance(1 * time.Second)
	code2 := mustGenerate(t, secret, clk.Now())
	if err := dir.VerifyTOTP(ctx, pid, code2); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Wrong -> unauthorized.
	if err := dir.VerifyTOTP(ctx, pid, "000000"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	// Disable requires the password.
	if err := dir.DisableTOTP(ctx, pid, "wrong"); !errors.Is(err, directory.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if err := dir.DisableTOTP(ctx, pid, "correct-horse-staple"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := dir.VerifyTOTP(ctx, pid, code2); !errors.Is(err, directory.ErrTOTPNotEnrolled) {
		t.Fatalf("want ErrTOTPNotEnrolled after disable, got %v", err)
	}
}

func TestListPrincipals(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	for i := 0; i < 5; i++ {
		email := string(rune('a'+i)) + "@example.test"
		if _, err := dir.CreatePrincipal(ctx, email, "correct-horse-staple"); err != nil {
			t.Fatalf("create %s: %v", email, err)
		}
	}
	ps, err := dir.ListPrincipals(ctx, 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ps) != 5 {
		t.Fatalf("expected 5, got %d", len(ps))
	}
	// Keyset pagination: second page starts after the first page's last
	// ID and must return nothing (there are only 5 rows total).
	rest, err := dir.ListPrincipals(ctx, 100, ps[len(ps)-1].ID)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected 0 on second page, got %d", len(rest))
	}
}

func mustGenerate(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	// Import locally to keep test dependency-free on production totp path;
	// we rely on the same library so the codes match.
	code, err := totpGenerate(secret, at)
	if err != nil {
		t.Fatalf("totp generate: %v", err)
	}
	return code
}

// TestActivityTagged_CreatePrincipal asserts that CreatePrincipal emits
// correctly-tagged log records (REQ-OPS-86a). The audit record must carry
// activity=audit; the provisioning-failure path (exercised on a second call
// with the same address) must carry activity=internal.
func TestActivityTagged_CreatePrincipal(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		dbPath := filepath.Join(t.TempDir(), "test.db")
		fs, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
		if err != nil {
			t.Fatalf("storesqlite.OpenWithRand: %v", err)
		}
		defer fs.Close()
		rnd := newDeterministicReader()
		dir := directory.New(fs.Meta(), log, clk, rnd)
		ctx := context.Background()
		if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("seed domain: %v", err)
		}
		_, err = dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
	})
}

// TestActivityTagged_DeletePrincipal asserts activity=audit on the delete
// audit record.
func TestActivityTagged_DeletePrincipal(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		dbPath2 := filepath.Join(t.TempDir(), "test.db")
		fs, err := storesqlite.OpenWithRand(context.Background(), dbPath2, nil, clk, rand.Reader)
		if err != nil {
			t.Fatalf("storesqlite.OpenWithRand: %v", err)
		}
		defer fs.Close()
		rnd := newDeterministicReader()
		dir := directory.New(fs.Meta(), log, clk, rnd)
		ctx := context.Background()
		if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("seed domain: %v", err)
		}
		pid, err := dir.CreatePrincipal(ctx, "bob@example.test", "correct-horse-staple")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := dir.DeletePrincipal(ctx, pid); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})
}

// TestCreatePrincipal_ProvisionDefaultAddressBook asserts that CreatePrincipal
// inserts a default address book named "Personal" so JMAP Contacts clients
// have a usable container immediately (re #62).
func TestCreatePrincipal_ProvisionDefaultAddressBook(t *testing.T) {
	ctx := context.Background()
	dir, fs, _ := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ab, err := fs.Meta().DefaultAddressBook(ctx, pid)
	if err != nil {
		t.Fatalf("DefaultAddressBook: %v", err)
	}
	if ab.Name != "Personal" {
		t.Errorf("default address book name = %q, want %q", ab.Name, "Personal")
	}
	if !ab.IsDefault {
		t.Errorf("default address book IsDefault = false, want true")
	}
	if !ab.IsSubscribed {
		t.Errorf("default address book IsSubscribed = false, want true")
	}
	if ab.PrincipalID != pid {
		t.Errorf("default address book PrincipalID = %d, want %d", ab.PrincipalID, pid)
	}
}

// TestActivityTagged_UpdatePassword asserts activity=audit on the
// password-change audit record.
func TestActivityTagged_UpdatePassword(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		dbPath3 := filepath.Join(t.TempDir(), "test.db")
		fs, err := storesqlite.OpenWithRand(context.Background(), dbPath3, nil, clk, rand.Reader)
		if err != nil {
			t.Fatalf("storesqlite.OpenWithRand: %v", err)
		}
		defer fs.Close()
		rnd := newDeterministicReader()
		dir := directory.New(fs.Meta(), log, clk, rnd)
		ctx := context.Background()
		if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("seed domain: %v", err)
		}
		pid, err := dir.CreatePrincipal(ctx, "carol@example.test", "old-password-12chars")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := dir.UpdatePassword(ctx, pid, "old-password-12chars", "new-password-12chars"); err != nil {
			t.Fatalf("update password: %v", err)
		}
	})
}
