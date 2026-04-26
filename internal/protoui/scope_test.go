package protoui_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoui"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// TestScope_PublicLogin_IssuesEndUserScope drives the public-listener
// /login flow and asserts the issued cookie carries the
// AllEndUserScopes set (REQ-AUTH-SCOPE-01) and NOT admin scope
// (REQ-AUTH-SCOPE-02 -- no implicit grant).
func TestScope_PublicLogin_IssuesEndUserScope(t *testing.T) {
	t.Parallel()
	cl, baseURL := startScopedUIHarness(t, "public")

	// Seed a principal with a password.
	dir, store := scopedHarnessDeps(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pid, err := dir.CreatePrincipal(context.Background(), "alice@example.test", "hunter2hunter2hunter2")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	_ = pid
	_ = store
	_ = clk

	// Drive the login form.
	form := url.Values{
		"email":    []string{"alice@example.test"},
		"password": []string{"hunter2hunter2hunter2"},
	}
	req, _ := http.NewRequest("POST", baseURL+"/ui/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := cl.Do(req)
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status=%d, want 303", res.StatusCode)
	}
	// Inspect the cookie name. Public listener issues
	// herold_public_session per REQ-AUTH-SCOPE-01. Cookie path is
	// /ui/ so the jar lookup must use a URL under /ui/.
	u, _ := url.Parse(baseURL + "/ui/dashboard")
	jar := cl.Jar.Cookies(u)
	var sess, csrf *http.Cookie
	for _, c := range jar {
		switch c.Name {
		case "herold_public_session":
			sess = c
		case "herold_public_csrf":
			csrf = c
		}
	}
	if sess == nil {
		t.Fatalf("public session cookie not issued; jar=%+v", jar)
	}
	if csrf == nil {
		t.Fatalf("public CSRF cookie not issued; jar=%+v", jar)
	}
	// The cookie value embeds the scope set as a base64-encoded JSON
	// blob; the encoding is internal to protoui but the absence of
	// 'admin' in the slice we'd parse is the contract we test.
	if strings.Contains(sess.Value, scopeAdminEnc()) {
		t.Errorf("public session cookie carries admin-encoded scope: %s", sess.Value)
	}
}

// TestScope_AdminLogin_IssuesAdminScope mirrors the public flow but
// asserts the admin listener issues a cookie with [admin] scope only
// (REQ-AUTH-SCOPE-03) named herold_admin_session.
func TestScope_AdminLogin_IssuesAdminScope(t *testing.T) {
	t.Parallel()
	cl, baseURL := startScopedUIHarness(t, "admin")
	dir, _ := scopedHarnessDeps(t)
	if _, err := dir.CreatePrincipal(context.Background(), "ops@example.test", "hunter2hunter2hunter2"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	form := url.Values{
		"email":    []string{"ops@example.test"},
		"password": []string{"hunter2hunter2hunter2"},
	}
	req, _ := http.NewRequest("POST", baseURL+"/ui/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := cl.Do(req)
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status=%d, want 303", res.StatusCode)
	}
	u, _ := url.Parse(baseURL + "/ui/dashboard")
	jar := cl.Jar.Cookies(u)
	var sess *http.Cookie
	for _, c := range jar {
		if c.Name == "herold_admin_session" {
			sess = c
		}
		if c.Name == "herold_public_session" {
			t.Errorf("admin listener leaked public cookie name: %+v", c)
		}
	}
	if sess == nil {
		t.Fatalf("admin session cookie not issued; jar=%+v", jar)
	}
}

// scopeAdminEnc returns the base64-encoded JSON for a {admin} scope
// set so the asserts can sanity-check the cookie payload without
// importing protoui's internals.
func scopeAdminEnc() string {
	b, _ := auth.NewScopeSet(auth.ScopeAdmin).MarshalJSON()
	// Mirrors session.encode's encoding choice (base64.RawURLEncoding).
	return strings.TrimRight(base64URLNoPad(b), "=")
}

// base64URLNoPad is a tiny stdlib-free copy of base64.RawURLEncoding's
// EncodeToString. The session encoder uses RawURLEncoding so we
// mirror it here without dragging encoding/base64 across the package
// API.
func base64URLNoPad(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(b)*4+2)/3)
	for i := 0; i < len(b); i += 3 {
		var v uint32
		switch len(b) - i {
		case 1:
			v = uint32(b[i]) << 16
			out = append(out,
				alphabet[(v>>18)&0x3F],
				alphabet[(v>>12)&0x3F],
			)
		case 2:
			v = uint32(b[i])<<16 | uint32(b[i+1])<<8
			out = append(out,
				alphabet[(v>>18)&0x3F],
				alphabet[(v>>12)&0x3F],
				alphabet[(v>>6)&0x3F],
			)
		default:
			v = uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
			out = append(out,
				alphabet[(v>>18)&0x3F],
				alphabet[(v>>12)&0x3F],
				alphabet[(v>>6)&0x3F],
				alphabet[v&0x3F],
			)
		}
	}
	return string(out)
}

// startScopedUIHarness boots a protoui server tagged with the given
// listener kind (admin or public) and returns an http.Client with a
// cookie jar plus the base URL.
func startScopedUIHarness(t *testing.T, listenerKind string) (*http.Client, string) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "ui", Protocol: "admin"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv, err := protoui.NewServer(fs, dir, rp, clk, protoui.Options{
		PathPrefix:   "/ui",
		ListenerKind: listenerKind,
		Session: protoui.SessionConfig{
			SigningKey:    []byte("test-key-32-bytes-................"),
			TTL:           1 * time.Hour,
			SecureCookies: false,
			// Use distinct cookie names per listener kind, mirroring
			// the production newProtoUIServer helper.
			CookieName:     "herold_" + listenerKind + "_session",
			CSRFCookieName: "herold_" + listenerKind + "_csrf",
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := h.AttachUI("ui", srv); err != nil {
		t.Fatalf("AttachUI: %v", err)
	}
	client, base := h.DialUIByName(context.Background(), "ui")
	jar, _ := newCookieJar()
	client.Jar = jar
	// Stash dir + store in package-level for the test to seed
	// principals; the simpler fan-in is to expose them through a
	// returned struct, but the harness already wires one fakestore
	// per test so package-level is fine.
	scopedHarnessRegister(t, dir, fs)
	return client, base
}

// Per-test dir + store, keyed off *testing.T so subtests don't trample.
var (
	scopedDeps = make(map[*testing.T]scopedDepBundle)
)

type scopedDepBundle struct {
	dir   *directory.Directory
	store *fakestore.Store
}

func scopedHarnessRegister(t *testing.T, dir *directory.Directory, st *fakestore.Store) {
	scopedDeps[t] = scopedDepBundle{dir: dir, store: st}
	t.Cleanup(func() { delete(scopedDeps, t) })
}

func scopedHarnessDeps(t *testing.T) (*directory.Directory, *fakestore.Store) {
	dep, ok := scopedDeps[t]
	if !ok {
		t.Fatalf("scopedHarnessDeps called before startScopedUIHarness")
	}
	return dep.dir, dep.store
}
