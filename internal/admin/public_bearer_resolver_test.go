package admin

// public_bearer_resolver_test.go unit-tests
// newPublicBearerOrCookieResolver directly against a real sqlite store
// (re #332): a valid Bearer token resolves to its principal, an
// invalid or revoked one is rejected outright (no fall-through to the
// cookie), and a request without an Authorization header still goes
// through the cookie resolver.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// newBearerResolverTestStore opens a fresh sqlite store with one
// domain and one principal, returning the store, the directory
// adapter (to mint additional principals if a test needs them), the
// seeded principal ID, and the clock the store was opened with.
func newBearerResolverTestStore(t *testing.T) (store.Store, *directory.Directory, store.PrincipalID, clock.Clock) {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "store.db"), discardLogger(), clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true, CreatedAt: clk.Now()}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(st.Meta(), discardLogger(), clk, nil)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	return st, dir, pid, clk
}

// mintBearerToken inserts an api_keys row directly (mirroring how
// directory.IssueDeviceToken and protoadmin's API-key CRUD persist a
// key) and returns the plaintext token.
func mintBearerToken(t *testing.T, st store.Store, pid store.PrincipalID, name string) (plaintext string, id store.APIKeyID) {
	t.Helper()
	plaintext = "hk_bearer_resolver_test_" + name
	row, err := st.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: pid,
		Hash:        protoadmin.HashAPIKey(plaintext),
		Name:        name,
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	return plaintext, row.ID
}

func TestPublicBearerOrCookieResolver_ValidBearer(t *testing.T) {
	st, _, pid, clk := newBearerResolverTestStore(t)
	token, _ := mintBearerToken(t, st, pid, "valid")

	cookieCalled := false
	cookieResolver := func(*http.Request) (store.PrincipalID, bool) {
		cookieCalled = true
		return 0, false
	}
	resolve := newPublicBearerOrCookieResolver(st, clk, discardLogger(), cookieResolver)

	r := httptest.NewRequest(http.MethodGet, "/proxy/image?url=https://example.org/a.png", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	gotPID, ok := resolve(r)
	if !ok {
		t.Fatalf("resolve(valid bearer) = false, want true")
	}
	if gotPID != pid {
		t.Fatalf("resolve(valid bearer) principal = %d, want %d", gotPID, pid)
	}
	if cookieCalled {
		t.Fatalf("cookie resolver was consulted despite a valid Bearer header")
	}
}

func TestPublicBearerOrCookieResolver_InvalidBearer_NoFallThroughToCookie(t *testing.T) {
	st, _, _, clk := newBearerResolverTestStore(t)

	cookieCalled := false
	cookieResolver := func(*http.Request) (store.PrincipalID, bool) {
		cookieCalled = true
		return 42, true
	}
	resolve := newPublicBearerOrCookieResolver(st, clk, discardLogger(), cookieResolver)

	r := httptest.NewRequest(http.MethodGet, "/proxy/image?url=https://example.org/a.png", nil)
	r.Header.Set("Authorization", "Bearer hk_this_token_does_not_exist")
	_, ok := resolve(r)
	if ok {
		t.Fatalf("resolve(invalid bearer) = true, want false")
	}
	if cookieCalled {
		t.Fatalf("cookie resolver was consulted after an invalid Bearer header; " +
			"an Authorization header must be authoritative, not a soft fallback")
	}
}

func TestPublicBearerOrCookieResolver_RevokedBearer(t *testing.T) {
	st, _, pid, clk := newBearerResolverTestStore(t)
	token, id := mintBearerToken(t, st, pid, "revoked")

	resolve := newPublicBearerOrCookieResolver(st, clk, discardLogger(),
		func(*http.Request) (store.PrincipalID, bool) { return 0, false })

	r := httptest.NewRequest(http.MethodGet, "/proxy/image?url=https://example.org/a.png", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, ok := resolve(r); !ok {
		t.Fatalf("resolve(token) before revocation = false, want true")
	}

	if err := st.Meta().DeleteAPIKey(context.Background(), id); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}

	if _, ok := resolve(r); ok {
		t.Fatalf("resolve(token) after revocation = true, want false")
	}
}

func TestPublicBearerOrCookieResolver_NoAuthorizationHeader_UsesCookie(t *testing.T) {
	st, _, _, clk := newBearerResolverTestStore(t)

	cookieCalled := false
	cookieResolver := func(*http.Request) (store.PrincipalID, bool) {
		cookieCalled = true
		return 7, true
	}
	resolve := newPublicBearerOrCookieResolver(st, clk, discardLogger(), cookieResolver)

	r := httptest.NewRequest(http.MethodGet, "/proxy/image?url=https://example.org/a.png", nil)
	gotPID, ok := resolve(r)
	if !ok || gotPID != 7 {
		t.Fatalf("resolve(no Authorization header) = (%d, %v), want (7, true)", gotPID, ok)
	}
	if !cookieCalled {
		t.Fatalf("cookie resolver was not consulted for a request without an Authorization header")
	}
}
