package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// seedRecoverFixture writes a system.toml, opens the SQLite store, seeds
// one admin principal (no TOTP), closes the store, and returns the config
// path and the loaded sysconfig. The test must use --system-config with the
// returned path so that `herold recover` opens the same database.
func seedRecoverFixture(t *testing.T) (cfgPath string, cfg *sysconfig.Config) {
	t.Helper()
	cfgPath, cfg = minimalConfigFixture(t)

	// Direct store open to seed the principal; close before the CLI runs.
	clk := clock.NewReal()
	ctx := context.Background()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("seedRecoverFixture openStore: %v", err)
	}
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "test.local", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	if _, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "admin@test.local",
		Flags:          store.PrincipalFlagAdmin,
	}); err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("st.Close: %v", err)
	}
	// Keep credentials writes out of the operator's home directory.
	home := t.TempDir()
	SetCredentialsPath(filepath.Join(home, "credentials.toml"))
	t.Cleanup(func() { SetCredentialsPath("") })
	return cfgPath, cfg
}

// runRecoverArgs executes `herold --system-config cfgPath recover <extraArgs>`
// and returns stdout, stderr, and the error.
func runRecoverArgs(t *testing.T, cfgPath string, extraArgs ...string) (stdout, stderr string, err error) {
	t.Helper()
	args := append([]string{"--system-config", cfgPath, "recover"}, extraArgs...)
	root := NewRootCmd()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errBuf)
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	return out.String(), errBuf.String(), err
}

// TestRecoverCmd_NoAction verifies that omitting both action flags is an
// error with a clear message.
func TestRecoverCmd_NoAction(t *testing.T) {
	cfgPath, _ := seedRecoverFixture(t)
	_, _, err := runRecoverArgs(t, cfgPath, "--email", "admin@test.local")
	if err == nil {
		t.Fatal("want error when no action flag supplied")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--reset-totp") || !strings.Contains(msg, "--mint-api-key") {
		t.Fatalf("error should mention both action flags, got: %v", err)
	}
}

// TestRecoverCmd_NoEmail verifies that omitting --email is an error.
func TestRecoverCmd_NoEmail(t *testing.T) {
	cfgPath, _ := seedRecoverFixture(t)
	_, _, err := runRecoverArgs(t, cfgPath, "--reset-totp")
	if err == nil {
		t.Fatal("want error when --email is missing")
	}
	if !strings.Contains(err.Error(), "--email") {
		t.Fatalf("error should mention --email: %v", err)
	}
}

// TestRecoverCmd_UnknownPrincipal verifies a clear error for a missing email.
func TestRecoverCmd_UnknownPrincipal(t *testing.T) {
	cfgPath, _ := seedRecoverFixture(t)
	_, _, err := runRecoverArgs(t, cfgPath, "--email", "nobody@test.local", "--reset-totp")
	if err == nil {
		t.Fatal("want error for unknown principal")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error should say 'not found': %v", err)
	}
}

// TestRecoverCmd_ResetTOTP verifies --reset-totp clears the TOTP secret and
// the PrincipalFlagTOTPEnabled flag.
func TestRecoverCmd_ResetTOTP(t *testing.T) {
	cfgPath, cfg := seedRecoverFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	// Pre-enroll TOTP directly in the store.
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore (enroll): %v", err)
	}
	p, err := st.Meta().GetPrincipalByEmail(ctx, "admin@test.local")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail (enroll): %v", err)
	}
	p.TOTPSecret = []byte("totp-secret-bytes")
	p.Flags |= store.PrincipalFlagTOTPEnabled
	if err := st.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal (enroll): %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("st.Close (after enroll): %v", err)
	}

	out, _, err := runRecoverArgs(t, cfgPath,
		"--email", "admin@test.local",
		"--reset-totp",
		"--save-credentials=false",
	)
	if err != nil {
		t.Fatalf("recover --reset-totp: %v", err)
	}
	if !strings.Contains(out, "TOTP enrollment cleared") {
		t.Fatalf("output does not confirm TOTP reset: %s", out)
	}

	// Verify the store reflects the change.
	st2, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore (verify): %v", err)
	}
	defer st2.Close()
	p2, err := st2.Meta().GetPrincipalByEmail(ctx, "admin@test.local")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail (verify): %v", err)
	}
	if p2.TOTPSecret != nil {
		t.Fatalf("TOTPSecret not cleared: got %v", p2.TOTPSecret)
	}
	if p2.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("PrincipalFlagTOTPEnabled still set after reset")
	}
}

// TestRecoverCmd_MintAPIKey verifies that --mint-api-key prints a one-shot
// Bearer token that authenticates against the admin REST surface.
func TestRecoverCmd_MintAPIKey(t *testing.T) {
	cfgPath, cfg := seedRecoverFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	out, _, err := runRecoverArgs(t, cfgPath,
		"--email", "admin@test.local",
		"--mint-api-key",
		"--save-credentials=false",
	)
	if err != nil {
		t.Fatalf("recover --mint-api-key: %v", err)
	}
	if !strings.Contains(out, "api_key:") {
		t.Fatalf("output does not contain api_key: %s", out)
	}
	if !strings.Contains(out, "one-shot") {
		t.Fatalf("output does not mention one-shot: %s", out)
	}

	keyPlain := extractPrintedAPIKey(t, out)
	if !strings.HasPrefix(keyPlain, protoadmin.APIKeyPrefix) {
		t.Fatalf("printed key has wrong prefix: %q", keyPlain)
	}

	// Wire a protoadmin server against the seeded store and verify the key
	// authenticates before TOTP confirm (the key is one-shot but not yet
	// consumed at this point).
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore (verify): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dir := directory.New(st.Meta(), discardLogger(), clk, nil)
	rp := directoryoidc.New(st.Meta(), discardLogger(), &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(st, dir, rp, discardLogger(), clk, protoadmin.Options{})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+keyPlain)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery key auth: got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Verify the key is stored as one-shot in the DB.
	p, err := st.Meta().GetPrincipalByEmail(ctx, "admin@test.local")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	keys, err := st.Meta().ListAPIKeysByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAPIKeysByPrincipal: %v", err)
	}
	var foundOneShot bool
	for _, k := range keys {
		if k.Name == "recovery" && k.OneShot {
			foundOneShot = true
		}
	}
	if !foundOneShot {
		t.Fatalf("no one-shot recovery key in store after --mint-api-key; keys=%+v", keys)
	}
}

// TestRecoverCmd_BothActions exercises the combined --reset-totp --mint-api-key
// path in one invocation.
func TestRecoverCmd_BothActions(t *testing.T) {
	cfgPath, cfg := seedRecoverFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	// Pre-enroll TOTP.
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore (enroll): %v", err)
	}
	p, err := st.Meta().GetPrincipalByEmail(ctx, "admin@test.local")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.TOTPSecret = []byte("some-secret")
	p.Flags |= store.PrincipalFlagTOTPEnabled
	if err := st.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("st.Close: %v", err)
	}

	out, _, err := runRecoverArgs(t, cfgPath,
		"--email", "admin@test.local",
		"--reset-totp",
		"--mint-api-key",
		"--save-credentials=false",
	)
	if err != nil {
		t.Fatalf("recover --reset-totp --mint-api-key: %v", err)
	}
	if !strings.Contains(out, "TOTP enrollment cleared") {
		t.Fatalf("missing TOTP reset confirmation in output: %s", out)
	}
	if !strings.Contains(out, "api_key:") {
		t.Fatalf("missing api_key in output: %s", out)
	}

	// Verify store state.
	st2, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore (verify): %v", err)
	}
	defer st2.Close()

	p2, err := st2.Meta().GetPrincipalByEmail(ctx, "admin@test.local")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail (verify): %v", err)
	}
	if p2.TOTPSecret != nil {
		t.Fatalf("TOTPSecret not cleared after reset: got %v", p2.TOTPSecret)
	}
	if p2.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("TOTPEnabled flag still set after reset")
	}

	keys, err := st2.Meta().ListAPIKeysByPrincipal(ctx, p2.ID)
	if err != nil {
		t.Fatalf("ListAPIKeysByPrincipal: %v", err)
	}
	var foundOneShot bool
	for _, k := range keys {
		if k.Name == "recovery" && k.OneShot {
			foundOneShot = true
		}
	}
	if !foundOneShot {
		t.Fatalf("no one-shot recovery key in store; keys=%+v", keys)
	}
}
