package authsession_test

// resolve_test.go covers ResolveSession and ResolveSessionWithScope --
// the stateless cookie-to-principal resolver pair added in Phase 3c-i.
//
// Four cases per the task spec:
//   1. no cookie        -> (0, nil, false)
//   2. bad signature    -> (0, nil, false)
//   3. expired cookie   -> (0, nil, false)
//   4. ok               -> (pid, scopes, true)
// Plus a fifth case:
//   5. disabled principal -> (0, nil, false)

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

var resolveTestKey = []byte("resolve-test-signing-key-32bytes")

func resolveTestConfig() authsession.SessionConfig {
	return authsession.SessionConfig{
		SigningKey:     resolveTestKey,
		CookieName:     "herold_resolve_test_session",
		CSRFCookieName: "herold_resolve_test_csrf",
		TTL:            24 * time.Hour,
		SecureCookies:  false,
	}
}

// insertTestPrincipal writes a minimal principal into fs and returns its ID.
func insertTestPrincipal(t *testing.T, fs *fakestore.Store, email string, disabled bool) store.PrincipalID {
	t.Helper()
	var flags store.PrincipalFlags
	if disabled {
		flags = store.PrincipalFlagDisabled
	}
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
		DisplayName:    "Test User",
		Flags:          flags,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return p.ID
}

// requestWithSessionCookie builds an *http.Request with a session cookie
// whose value is the encoded wire form of sess.
func requestWithSessionCookie(t *testing.T, cfg authsession.SessionConfig, sess authsession.Session) *http.Request {
	t.Helper()
	wire := authsession.EncodeSession(sess, cfg.SigningKey)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.CookieName, Value: wire})
	return req
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

// TestResolveSession_NoCookie returns false when no cookie is present.
func TestResolveSession_NoCookie(t *testing.T) {
	t.Parallel()
	fs, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	cfg := resolveTestConfig()
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	req := httptest.NewRequest("GET", "/", nil) // no cookie
	pid, ok := authsession.ResolveSession(req, cfg, fs, clk)
	if ok {
		t.Errorf("ResolveSession: got ok=true with no cookie, want false")
	}
	if pid != 0 {
		t.Errorf("ResolveSession: got pid=%d, want 0", pid)
	}
}

// TestResolveSession_BadSignature returns false when the cookie has a bad HMAC.
func TestResolveSession_BadSignature(t *testing.T) {
	t.Parallel()
	fs, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	cfg := resolveTestConfig()
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	// Encode a valid session with a different key so the signature is wrong.
	wrongKey := []byte("wrong-signing-key-for-resolve-xx")
	sess := authsession.Session{
		PrincipalID: store.PrincipalID(1),
		ExpiresAt:   clk.Now().Add(time.Hour),
		CSRFToken:   "csrf-bad-sig",
		Scopes:      auth.NewScopeSet(auth.ScopeEndUser),
	}
	wire := authsession.EncodeSession(sess, wrongKey)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.CookieName, Value: wire})

	pid, ok := authsession.ResolveSession(req, cfg, fs, clk)
	if ok {
		t.Errorf("ResolveSession with bad sig: got ok=true, want false")
	}
	if pid != 0 {
		t.Errorf("ResolveSession with bad sig: got pid=%d, want 0", pid)
	}
}

// TestResolveSession_Expired returns false when the cookie is expired.
func TestResolveSession_Expired(t *testing.T) {
	t.Parallel()
	fs, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	cfg := resolveTestConfig()
	issueAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(issueAt)

	_ = insertTestPrincipal(t, fs, "expire@example.com", false)

	sess := authsession.Session{
		PrincipalID: store.PrincipalID(1),
		ExpiresAt:   issueAt.Add(time.Hour),
		CSRFToken:   "csrf-expire",
		Scopes:      auth.NewScopeSet(auth.ScopeEndUser),
	}
	req := requestWithSessionCookie(t, cfg, sess)

	// Advance clock past expiry.
	clk.Advance(2 * time.Hour)

	pid, ok := authsession.ResolveSession(req, cfg, fs, clk)
	if ok {
		t.Errorf("ResolveSession with expired cookie: got ok=true, want false")
	}
	if pid != 0 {
		t.Errorf("ResolveSession with expired cookie: got pid=%d, want 0", pid)
	}
}

// TestResolveSession_OK returns (pid, true) on a valid cookie for an active principal.
func TestResolveSession_OK(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	cfg := resolveTestConfig()
	pid := insertTestPrincipal(t, fs, "ok@example.com", false)

	sess := authsession.Session{
		PrincipalID: pid,
		ExpiresAt:   clk.Now().Add(time.Hour),
		CSRFToken:   "csrf-ok",
		Scopes:      auth.NewScopeSet(auth.ScopeEndUser, auth.ScopeMailSend),
	}
	req := requestWithSessionCookie(t, cfg, sess)

	gotPID, ok := authsession.ResolveSession(req, cfg, fs, clk)
	if !ok {
		t.Fatalf("ResolveSession: got ok=false, want true")
	}
	if gotPID != pid {
		t.Errorf("ResolveSession: got pid=%d, want %d", gotPID, pid)
	}
}

// TestResolveSessionWithScope_OK returns (pid, scopes, true) on success.
func TestResolveSessionWithScope_OK(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	cfg := resolveTestConfig()
	pid := insertTestPrincipal(t, fs, "scope@example.com", false)

	wantScopes := auth.NewScopeSet(auth.ScopeEndUser, auth.ScopeMailSend, auth.ScopeChatRead)
	sess := authsession.Session{
		PrincipalID: pid,
		ExpiresAt:   clk.Now().Add(time.Hour),
		CSRFToken:   "csrf-scopes",
		Scopes:      wantScopes,
	}
	req := requestWithSessionCookie(t, cfg, sess)

	gotPID, gotScopes, ok := authsession.ResolveSessionWithScope(req, cfg, fs, clk)
	if !ok {
		t.Fatalf("ResolveSessionWithScope: got ok=false, want true")
	}
	if gotPID != pid {
		t.Errorf("ResolveSessionWithScope: got pid=%d, want %d", gotPID, pid)
	}
	for _, sc := range []auth.Scope{auth.ScopeEndUser, auth.ScopeMailSend, auth.ScopeChatRead} {
		if !gotScopes.Has(sc) {
			t.Errorf("ResolveSessionWithScope: scope %q missing from returned set %v", sc, gotScopes.Slice())
		}
	}
}

// TestResolveSession_DisabledPrincipal returns false when the principal is disabled.
func TestResolveSession_DisabledPrincipal(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	cfg := resolveTestConfig()
	pid := insertTestPrincipal(t, fs, "disabled@example.com", true /* disabled */)

	sess := authsession.Session{
		PrincipalID: pid,
		ExpiresAt:   clk.Now().Add(time.Hour),
		CSRFToken:   "csrf-disabled",
		Scopes:      auth.NewScopeSet(auth.ScopeEndUser),
	}
	req := requestWithSessionCookie(t, cfg, sess)

	gotPID, ok := authsession.ResolveSession(req, cfg, fs, clk)
	if ok {
		t.Errorf("ResolveSession with disabled principal: got ok=true, want false")
	}
	if gotPID != 0 {
		t.Errorf("ResolveSession with disabled principal: got pid=%d, want 0", gotPID)
	}
}
