package protoadmin_test

// subaccount_bearer_test.go reproduces and closes the sub-account
// Bearer-API-key bypass (issue #227, REQ-SUBACCT-02 follow-up): a
// sub-principal must not be able to authenticate on the protoadmin
// Bearer-API-key path, and an admin must not be able to establish a
// credential (API key, OIDC link) targeting a sub-principal's id in the
// first place.
//
// TestSubPrincipal_CreateAPIKey_Returns400 and
// TestSubPrincipal_BeginOIDCLink_Returns400 pin the two creation-time
// guards (internal/protoadmin/apikeys.go handleCreateAPIKey,
// internal/protoadmin/oidc.go handleBeginOIDCLink): each is RED without
// its fix (the endpoint succeeded for a sub-principal id) and GREEN
// with it (400).
//
// TestSubPrincipal_APIKeyBearer_Rejected pins the auth-time guard
// (store.Principal.IsAuthenticatable, checked by
// protoadmin/auth.go authenticateBearer) independently of the
// creation-time guard: it inserts an API-key row directly at the store
// layer -- bypassing the HTTP creation endpoint entirely, simulating
// any future call site that mints a key without going through
// handleCreateAPIKey -- and presents it as a Bearer token against GET
// /api/v1/auth/me, the exact endpoint and exploit shape reported: RED
// without the auth-time fix (200, authenticated as the sub-principal),
// GREEN with it (401).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// mustInsertSubPrincipal creates a sub-principal owned by parentID via
// the store directly.
func mustInsertSubPrincipal(t *testing.T, fs store.Store, parentID store.PrincipalID, email string) store.Principal {
	t.Helper()
	sub, err := fs.Meta().InsertSubPrincipal(context.Background(), parentID, store.Principal{
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	return sub
}

// TestSubPrincipal_CreateAPIKey_Returns400 asserts POST
// /api/v1/principals/{pid}/api-keys refuses a sub-principal id
// (REQ-SUBACCT-02 "no credential may be set on it").
func TestSubPrincipal_CreateAPIKey_Returns400(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h := newHarnessWithStore(t, fs, clk)
	_, adminKey := h.bootstrap("subacct-apikey-admin@example.com")
	parentPid := h.createPrincipal(adminKey, "subacct-apikey-parent@example.com")
	sub := mustInsertSubPrincipal(t, fs, store.PrincipalID(parentPid), "subacct-apikey-sub@example.com")

	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", sub.ID), adminKey, map[string]any{
		"label": "exploit-key",
		"scope": []string{"mail.send"},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create API key for sub-principal id=%d: status=%d body=%s, want 400", sub.ID, res.StatusCode, buf)
	}
}

// TestSubPrincipal_APIKeyBearer_Rejected reproduces the reported
// exploit shape end-to-end at the auth-resolution seam: a Bearer API
// key whose PrincipalID names a sub-principal must not authenticate
// GET /api/v1/auth/me, independent of how the key came to exist.
func TestSubPrincipal_APIKeyBearer_Rejected(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h := newHarnessWithStore(t, fs, clk)
	_, adminKey := h.bootstrap("subacct-bearer-admin@example.com")
	parentPid := h.createPrincipal(adminKey, "subacct-bearer-parent@example.com")
	sub := mustInsertSubPrincipal(t, fs, store.PrincipalID(parentPid), "subacct-bearer-sub@example.com")

	// Mint a key directly at the store layer -- bypassing
	// handleCreateAPIKey's own creation-time guard entirely, so this
	// test isolates the auth-time check (store.Principal.
	// IsAuthenticatable via protoadmin/auth.go authenticateBearer) from
	// the creation-time one covered by TestSubPrincipal_CreateAPIKey_Returns400.
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	plaintext := protoadmin.APIKeyPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	scopeJSON, err := json.Marshal([]string{"mail.send"})
	if err != nil {
		t.Fatalf("marshal scope: %v", err)
	}
	if _, err := fs.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: sub.ID,
		Hash:        protoadmin.HashAPIKey(plaintext),
		Name:        "forced-sub-key",
		ScopeJSON:   string(scopeJSON),
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	res, buf := h.doRequest("GET", "/api/v1/auth/me", plaintext, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/auth/me as sub-principal Bearer: status=%d body=%s, want 401", res.StatusCode, buf)
	}
}

// TestSubPrincipal_BeginOIDCLink_Returns400 asserts POST
// /api/v1/principals/{pid}/oidc-links/begin refuses a sub-principal id
// (REQ-SUBACCT-02 "no credential may be set on it"). The guard runs
// before the provider_id is even validated, so no provider needs to be
// registered for this test.
func TestSubPrincipal_BeginOIDCLink_Returns400(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h := newHarnessWithStore(t, fs, clk)
	_, adminKey := h.bootstrap("subacct-oidclink-admin@example.com")
	parentPid := h.createPrincipal(adminKey, "subacct-oidclink-parent@example.com")
	sub := mustInsertSubPrincipal(t, fs, store.PrincipalID(parentPid), "subacct-oidclink-sub@example.com")

	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/oidc-links/begin", sub.ID), adminKey, map[string]any{
		"provider_id": "whatever",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("begin OIDC link for sub-principal id=%d: status=%d body=%s, want 400", sub.ID, res.StatusCode, buf)
	}
}
