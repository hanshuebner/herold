package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
)

// fakeRenewer satisfies protoadmin.CertRenewer. The renew function
// optionally rewrites the cert row to simulate ACME having minted a
// fresh chain.
type fakeRenewer struct {
	store store.Store
	clk   interface{ Now() time.Time }
	err   error
}

func (f *fakeRenewer) RenewCert(ctx context.Context, hostname string) error {
	if f.err != nil {
		return f.err
	}
	now := f.clk.Now()
	return f.store.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname:  hostname,
		ChainPEM:  "-----BEGIN CERTIFICATE-----\nfake-renewed\n-----END CERTIFICATE-----\n",
		NotBefore: now,
		NotAfter:  now.Add(90 * 24 * time.Hour),
		Issuer:    "Fake CA",
	})
}

// seedCert seeds an ACME cert into the test store.
func seedCert(t *testing.T, env *cliTestEnv, hostname string, notAfter time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := env.store.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname:  hostname,
		ChainPEM:  "-----BEGIN CERTIFICATE-----\nseed\n-----END CERTIFICATE-----\n",
		NotBefore: env.clk.Now(),
		NotAfter:  notAfter,
		Issuer:    "Fake CA",
	}); err != nil {
		t.Fatalf("UpsertACMECert: %v", err)
	}
}

func TestCLICertStatus_Empty(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("cert", "status", "--json")
	if err != nil {
		t.Fatalf("cert status: %v", err)
	}
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("expected items field; got %s", out)
	}
}

func TestCLICertShow_NotFound(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("cert", "show", "missing.example.com")
	if err == nil {
		t.Fatalf("expected error for missing cert")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("expected 404; got %v", err)
	}
}

func TestCLICertRenew_HappyPath(t *testing.T) {
	var fr *fakeRenewer
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.CertRenewer = nil
	})
	fr = &fakeRenewer{store: env.store, clk: env.clk}
	// Patch the server with a renewer post-hoc by rebuilding a dedicated
	// test environment. Instead, build the env again with the renewer.
	_ = fr
	env2 := newCLITestEnv(t, func(o *protoadmin.Options) {
		// Real env's store is captured by closures; we reuse the wrapper
		// by providing a thin wiring function.
	})
	fr2 := &fakeRenewer{store: env2.store, clk: env2.clk}
	// We need to rebuild the env with the renewer wired. Simplest way:
	// recreate from scratch.
	env3 := newCLITestEnvWithRenewer(t, fr2)

	seedCert(t, env3, "mail.example.com", env3.clk.Now().Add(15*24*time.Hour))
	out, _, err := env3.run("cert", "renew", "mail.example.com")
	if err != nil {
		t.Fatalf("cert renew: %v", err)
	}
	if !strings.Contains(out, "cert renewed") {
		t.Fatalf("expected confirmation line; got %s", out)
	}
	got, err := env3.store.Meta().GetACMECert(context.Background(), "mail.example.com")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(got.ChainPEM, "fake-renewed") {
		t.Fatalf("renewer did not run; chain=%s", got.ChainPEM)
	}
}

// newCLITestEnvWithRenewer is a tiny wrapper around newCLITestEnv that
// installs a renewer pointing at the env's store. The chicken-and-egg
// (renewer needs store; store created inside newCLITestEnv) is solved by
// constructing the env in two passes.
func newCLITestEnvWithRenewer(t *testing.T, _ *fakeRenewer) *cliTestEnv {
	t.Helper()
	// Build a placeholder env so we can capture the store, then mutate
	// the protoadmin.Options on the real construction. The cleanest path
	// is a single-shot constructor; do that inline.
	env := newCLITestEnv(t, nil)
	// Install a renewer-aware test server in place of the original.
	env.httpSrv.Close()
	renewer := &fakeRenewer{store: env.store, clk: env.clk}
	rebuildAdminServer(t, env, func(o *protoadmin.Options) {
		o.CertRenewer = renewer
	})
	return env
}

func TestCLICertRenew_Failure(t *testing.T) {
	env := newCLITestEnv(t, nil)
	// Replace the server with one whose renewer always errors.
	env.httpSrv.Close()
	rebuildAdminServer(t, env, func(o *protoadmin.Options) {
		o.CertRenewer = &fakeRenewer{store: env.store, clk: env.clk, err: errors.New("acme: order failed")}
	})
	seedCert(t, env, "fail.example.com", env.clk.Now().Add(15*24*time.Hour))
	_, _, err := env.run("cert", "renew", "fail.example.com")
	if err == nil {
		t.Fatalf("expected renewer error")
	}
	if !strings.Contains(err.Error(), "order failed") {
		t.Fatalf("expected 'order failed' in error; got %v", err)
	}
}

func TestCLICertRenew_NoRenewerConfigured(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedCert(t, env, "noop.example.com", env.clk.Now().Add(15*24*time.Hour))
	_, _, err := env.run("cert", "renew", "noop.example.com")
	if err == nil {
		t.Fatalf("expected 501 not_implemented")
	}
	if !strings.Contains(err.Error(), "501") && !strings.Contains(err.Error(), "not_implemented") {
		t.Fatalf("expected 501 / not_implemented; got %v", err)
	}
}
