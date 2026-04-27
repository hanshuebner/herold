package protoui_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestSafeRedirect_AdminListener exercises the redirect-target
// allowlist for the admin listener. Any path outside /ui/... must be
// rejected; absolute URLs, protocol-relative URLs, and other origins
// are always rejected.
func TestSafeRedirect_AdminListener(t *testing.T) {
	t.Parallel()
	// Use the scoped harness with listenerKind="admin" so safeRedirect
	// uses the admin policy (not the public policy from newUIHarness).
	cl, baseURL := startScopedUIHarness(t, "admin")
	dir, _ := scopedHarnessDeps(t)
	if _, err := dir.CreatePrincipal(context.Background(), "admin@example.com", "correct-horse-battery-staple"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	doLogin := func(t *testing.T, target string) string {
		t.Helper()
		form := url.Values{
			"email":    []string{"admin@example.com"},
			"password": []string{"correct-horse-battery-staple"},
			"redirect": []string{target},
		}
		req, _ := http.NewRequest("POST", baseURL+"/ui/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := cl.Do(req)
		if err != nil {
			t.Fatalf("POST /ui/login: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("status=%d, want 303", res.StatusCode)
		}
		return res.Header.Get("Location")
	}

	cases := []struct {
		target  string
		allowed bool
	}{
		// Allowed: within /ui/
		{"/ui/dashboard", true},
		{"/ui/principals", true},
		// Not allowed on admin: root and hash routes
		{"/", false},
		{"/#/mail", false},
		{"/#/cal", false},
		// Rejected: absolute URLs
		{"https://evil.example/", false},
		{"http://evil.example/foo", false},
		// Rejected: protocol-relative
		{"//evil.example/foo", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.target, func(t *testing.T) {
			t.Parallel()
			loc := doLogin(t, tc.target)
			if tc.allowed {
				if loc != tc.target {
					t.Fatalf("redirect=%q, want %q", loc, tc.target)
				}
			} else {
				// Must redirect to the default dashboard, not the supplied target.
				if loc == tc.target {
					t.Fatalf("unsafe redirect %q was accepted; Location=%q", tc.target, loc)
				}
				if loc != "/ui/dashboard" {
					t.Fatalf("unexpected fallback redirect=%q, want /ui/dashboard", loc)
				}
			}
		})
	}
}

// TestSafeRedirect_PublicListener exercises the redirect-target
// allowlist for the public listener. Root, SPA hash routes, and
// /ui/... are allowed; arbitrary paths and external URLs are not.
func TestSafeRedirect_PublicListener(t *testing.T) {
	t.Parallel()
	cl, baseURL := startScopedUIHarness(t, "public")
	dir, _ := scopedHarnessDeps(t)
	if _, err := dir.CreatePrincipal(context.Background(), "user@example.test", "hunter2hunter2hunter2"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	cases := []struct {
		target  string
		allowed bool
	}{
		// Allowed on public: root and SPA hash routes
		{"/", true},
		{"/#/mail", true},
		{"/#/cal", true},
		{"/#/chat", true},
		// Allowed on public: /ui/ paths
		{"/ui/dashboard", true},
		// Rejected: arbitrary server-side path
		{"/foo", false},
		{"/api/v1/something", false},
		// Rejected: absolute URLs
		{"https://evil.example/", false},
		// Rejected: protocol-relative
		{"//evil.example/foo", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.target, func(t *testing.T) {
			t.Parallel()
			form := url.Values{
				"email":    []string{"user@example.test"},
				"password": []string{"hunter2hunter2hunter2"},
				"redirect": []string{tc.target},
			}
			req, _ := http.NewRequest("POST", baseURL+"/ui/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			res, err := cl.Do(req)
			if err != nil {
				t.Fatalf("POST /ui/login: %v", err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusSeeOther {
				t.Fatalf("status=%d, want 303", res.StatusCode)
			}
			loc := res.Header.Get("Location")
			if tc.allowed {
				if loc != tc.target {
					t.Fatalf("redirect=%q, want %q", loc, tc.target)
				}
			} else {
				if loc == tc.target {
					t.Fatalf("unsafe redirect %q was accepted; Location=%q", tc.target, loc)
				}
				if loc != "/ui/dashboard" {
					t.Fatalf("unexpected fallback redirect=%q, want /ui/dashboard", loc)
				}
			}
		})
	}
}

// TestParamParity_ReturnParam verifies that ?return= is accepted as a
// fallback when ?redirect= is absent (the suite sends ?return=).
func TestParamParity_ReturnParam(t *testing.T) {
	t.Parallel()
	cl, baseURL := startScopedUIHarness(t, "public")
	dir, _ := scopedHarnessDeps(t)
	if _, err := dir.CreatePrincipal(context.Background(), "parity@example.test", "hunter2hunter2hunter2"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	t.Run("GET_return_param_populates_form", func(t *testing.T) {
		req, _ := http.NewRequest("GET", baseURL+"/ui/login?return=%2F%23%2Fmail", nil)
		res, err := cl.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if !strings.Contains(string(body), `value="/#/mail"`) {
			t.Fatalf("login form missing redirect value; body excerpt:\n%s",
				bodyExcerpt(string(body), "redirect"))
		}
	})

	t.Run("POST_return_only", func(t *testing.T) {
		form := url.Values{
			"email":    []string{"parity@example.test"},
			"password": []string{"hunter2hunter2hunter2"},
			"return":   []string{"/#/mail"},
			// no "redirect" key
		}
		req, _ := http.NewRequest("POST", baseURL+"/ui/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := cl.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("status=%d, want 303", res.StatusCode)
		}
		if loc := res.Header.Get("Location"); loc != "/#/mail" {
			t.Fatalf("redirect=%q, want /#/mail", loc)
		}
	})

	t.Run("POST_redirect_only", func(t *testing.T) {
		form := url.Values{
			"email":    []string{"parity@example.test"},
			"password": []string{"hunter2hunter2hunter2"},
			"redirect": []string{"/#/cal"},
		}
		req, _ := http.NewRequest("POST", baseURL+"/ui/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := cl.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		if loc := res.Header.Get("Location"); loc != "/#/cal" {
			t.Fatalf("redirect=%q, want /#/cal", loc)
		}
	})

	t.Run("POST_redirect_wins_over_return", func(t *testing.T) {
		form := url.Values{
			"email":    []string{"parity@example.test"},
			"password": []string{"hunter2hunter2hunter2"},
			"redirect": []string{"/#/mail"},
			"return":   []string{"/#/chat"}, // redirect takes precedence
		}
		req, _ := http.NewRequest("POST", baseURL+"/ui/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := cl.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		if loc := res.Header.Get("Location"); loc != "/#/mail" {
			t.Fatalf("redirect=%q, want /#/mail (redirect must win over return)", loc)
		}
	})
}

// bodyExcerpt returns the first line of body that contains substr, or
// an empty string when not found. Used for terse diagnostics.
func bodyExcerpt(body, substr string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, substr) {
			return strings.TrimSpace(line)
		}
	}
	return "(not found)"
}
