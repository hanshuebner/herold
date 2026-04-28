package authsession_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/store"
)

func testConfig() authsession.SessionConfig {
	return authsession.SessionConfig{
		SigningKey:     testKey,
		CookieName:     "herold_test_session",
		CSRFCookieName: "herold_test_csrf",
		TTL:            24 * time.Hour,
		SecureCookies:  false,
	}
}

func TestWriteSessionCookie_SetsBothCookies(t *testing.T) {
	cfg := testConfig()
	sess := authsession.Session{
		PrincipalID: store.PrincipalID(1),
		ExpiresAt:   time.Now().Add(time.Hour),
		CSRFToken:   "test-csrf-token",
		Scopes:      auth.NewScopeSet(auth.ScopeAdmin),
	}

	w := httptest.NewRecorder()
	authsession.WriteSessionCookie(w, cfg, sess)

	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected 2 Set-Cookie headers, got %d", len(cookies))
	}

	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range cookies {
		switch c.Name {
		case cfg.CookieName:
			sessionCookie = c
		case cfg.CSRFCookieName:
			csrfCookie = c
		}
	}

	if sessionCookie == nil {
		t.Fatal("session cookie not set")
	}
	if csrfCookie == nil {
		t.Fatal("CSRF cookie not set")
	}

	// Session cookie must be HttpOnly; CSRF cookie must not.
	if !sessionCookie.HttpOnly {
		t.Error("session cookie: HttpOnly should be true")
	}
	if csrfCookie.HttpOnly {
		t.Error("CSRF cookie: HttpOnly should be false so JS can read it")
	}

	// Both cookies must use Path="/".
	if sessionCookie.Path != "/" {
		t.Errorf("session cookie Path=%q, want /", sessionCookie.Path)
	}
	if csrfCookie.Path != "/" {
		t.Errorf("CSRF cookie Path=%q, want /", csrfCookie.Path)
	}

	// CSRF cookie value must equal the session's CSRFToken.
	if csrfCookie.Value != sess.CSRFToken {
		t.Errorf("CSRF cookie value=%q, want %q", csrfCookie.Value, sess.CSRFToken)
	}
}

func TestClearSessionCookies_ExpiresImmediately(t *testing.T) {
	cfg := testConfig()

	w := httptest.NewRecorder()
	authsession.ClearSessionCookies(w, cfg)

	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected 2 Set-Cookie headers, got %d", len(cookies))
	}

	for _, c := range cookies {
		if c.MaxAge != -1 {
			t.Errorf("cookie %q: MaxAge=%d, want -1", c.Name, c.MaxAge)
		}
		if c.Path != "/" {
			t.Errorf("cookie %q: Path=%q, want /", c.Name, c.Path)
		}
	}
}

func TestWriteThenClear_PathConsistency(t *testing.T) {
	// The path at issuance and at clear must match so the browser
	// drops the correct cookies (REQ-AUTH-COOKIE-PATH).
	cfg := testConfig()

	wWrite := httptest.NewRecorder()
	sess := authsession.Session{
		PrincipalID: store.PrincipalID(2),
		ExpiresAt:   time.Now().Add(time.Hour),
		CSRFToken:   "another-csrf-token",
		Scopes:      auth.NewScopeSet(auth.ScopeAdmin),
	}
	authsession.WriteSessionCookie(wWrite, cfg, sess)

	wClear := httptest.NewRecorder()
	authsession.ClearSessionCookies(wClear, cfg)

	writeCookies := wWrite.Result().Cookies()
	clearCookies := wClear.Result().Cookies()

	paths := map[string]string{}
	for _, c := range writeCookies {
		paths[c.Name] = c.Path
	}
	for _, c := range clearCookies {
		wPath, ok := paths[c.Name]
		if !ok {
			t.Errorf("clear sent unexpected cookie %q", c.Name)
			continue
		}
		if c.Path != wPath {
			t.Errorf("cookie %q: write path=%q, clear path=%q (must match for browser to drop it)",
				c.Name, wPath, c.Path)
		}
	}
}
