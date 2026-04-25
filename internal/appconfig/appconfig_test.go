package appconfig_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanshuebner/herold/internal/appconfig"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := storesqlite.Open(context.Background(), filepath.Join(dir, "db.sqlite"),
		slog.New(slog.NewTextHandler(os.Stderr, nil)), clock.NewReal())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAppConfigDumpLoad_RoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newStore(t)

	// Seed source store with a domain, two principals, an OIDC provider,
	// and a Sieve script.
	if err := src.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	p1, err := src.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
		PasswordHash:   "argon2id-placeholder",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("seed principal alice: %v", err)
	}
	if _, err := src.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
		DisplayName:    "Bob",
		PasswordHash:   "argon2id-placeholder",
	}); err != nil {
		t.Fatalf("seed principal bob: %v", err)
	}
	if err := src.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name:            "google",
		IssuerURL:       "https://accounts.google.com",
		ClientID:        "cid",
		ClientSecretRef: "$GOOGLE_SECRET",
		Scopes:          []string{"openid", "email"},
	}); err != nil {
		t.Fatalf("seed oidc: %v", err)
	}
	if err := src.Meta().SetSieveScript(ctx, p1.ID, `require ["fileinto"];
fileinto "Folder";`); err != nil {
		t.Fatalf("seed sieve: %v", err)
	}

	// Export.
	var buf bytes.Buffer
	if err := appconfig.Export(ctx, src, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	dumped := buf.Bytes()
	if len(dumped) == 0 {
		t.Fatalf("export produced no output")
	}
	if !bytes.Contains(dumped, []byte("alice@example.test")) {
		t.Fatalf("export missing alice: %s", dumped)
	}
	if !bytes.Contains(dumped, []byte("google")) {
		t.Fatalf("export missing google provider: %s", dumped)
	}

	// Load into a fresh store.
	dst := newStore(t)
	if err := appconfig.Import(ctx, dst, bytes.NewReader(dumped), appconfig.ImportOptions{Mode: appconfig.ImportMerge}); err != nil {
		t.Fatalf("import: %v", err)
	}
	// Assertions: domain, principals, OIDC provider, Sieve script all
	// present on the destination.
	if _, err := dst.Meta().GetDomain(ctx, "example.test"); err != nil {
		t.Fatalf("domain not imported: %v", err)
	}
	if _, err := dst.Meta().GetPrincipalByEmail(ctx, "alice@example.test"); err != nil {
		t.Fatalf("alice not imported: %v", err)
	}
	if _, err := dst.Meta().GetPrincipalByEmail(ctx, "bob@example.test"); err != nil {
		t.Fatalf("bob not imported: %v", err)
	}
	if _, err := dst.Meta().GetOIDCProvider(ctx, "google"); err != nil {
		t.Fatalf("google not imported: %v", err)
	}
	// Sieve script survives round-trip, keyed by canonical email.
	imported, err := dst.Meta().GetPrincipalByEmail(ctx, "alice@example.test")
	if err != nil {
		t.Fatalf("refetch alice: %v", err)
	}
	sc, err := dst.Meta().GetSieveScript(ctx, imported.ID)
	if err != nil {
		t.Fatalf("get sieve: %v", err)
	}
	if sc == "" {
		t.Fatalf("sieve script empty after import")
	}
}

// TestImport_EmitsAuditEntries pins the contract from STANDARDS §9
// and the Wave-4 security review: every Import mutation
// (domain/principal/sieve/oidc) lands an entry in the audit log so an
// operator running `herold app-config load` cannot silently flip
// admin flags or add OIDC providers. Closes the "appconfig.Import
// unaudited mutations" finding.
func TestImport_EmitsAuditEntries(t *testing.T) {
	ctx := context.Background()
	dst := newStore(t)

	// Minimal snapshot exercising every mutation kind: a local domain,
	// one principal, an OIDC provider, and a Sieve script for the
	// imported principal (keyed by the snapshot's principal_id).
	snap := []byte(`format_version = 1
exported_at = 2026-04-24T12:00:00Z

[[domains]]
name = "example.test"
is_local = true

[[principals]]
id = 7
kind = 1
canonical_email = "alice@example.test"
display_name = "Alice"
quota_bytes = 1073741824
flags = 0

[[oidc_providers]]
name = "google"
issuer_url = "https://accounts.google.com"
client_id = "cid"
scopes = ["openid", "email"]

[[sieve_scripts]]
principal_id = 7
script = "require [\"fileinto\"]; fileinto \"Folder\";"
`)
	if err := appconfig.Import(ctx, dst, bytes.NewReader(snap), appconfig.ImportOptions{Mode: appconfig.ImportMerge}); err != nil {
		t.Fatalf("import: %v", err)
	}

	entries, err := dst.Meta().ListAuditLog(ctx, store.AuditLogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	wantActions := map[string]bool{
		"appconfig.domain.upsert":    false,
		"appconfig.principal.upsert": false,
		"appconfig.oidc.upsert":      false,
		"appconfig.sieve.upsert":     false,
	}
	for _, e := range entries {
		if _, ok := wantActions[e.Action]; ok {
			wantActions[e.Action] = true
			if e.ActorKind != store.ActorSystem {
				t.Errorf("audit %q: ActorKind = %v, want ActorSystem", e.Action, e.ActorKind)
			}
			if e.ActorID != "appconfig-import" {
				t.Errorf("audit %q: ActorID = %q, want appconfig-import", e.Action, e.ActorID)
			}
			if e.Subject == "" {
				t.Errorf("audit %q: Subject empty", e.Action)
			}
			if e.Outcome != store.OutcomeSuccess {
				t.Errorf("audit %q: Outcome = %v, want OutcomeSuccess", e.Action, e.Outcome)
			}
		}
	}
	for action, found := range wantActions {
		if !found {
			t.Errorf("expected audit entry for action %q; got entries=%+v", action, entries)
		}
	}
}
