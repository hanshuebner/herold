package protoui_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoui"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// uiHarness wires a protoui.Server against a fakestore + the test
// listener provided by testharness.Server. Tests use the helper jar
// to carry the session cookie across requests; CheckRedirect is
// disabled (see attach_ui.go) so assertions on 303 Location headers
// work.
type uiHarness struct {
	t       *testing.T
	h       *testharness.Server
	srv     *protoui.Server
	client  *http.Client
	baseURL string
	clk     *clock.FakeClock
	dir     *directory.Directory
	store   store.Store
}

func newUIHarness(t *testing.T) *uiHarness {
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
		PathPrefix: "/ui",
		Session: protoui.SessionConfig{
			SigningKey: []byte("test-key-32-bytes-................"),
			TTL:        1 * time.Hour,
			// Tests run over plain HTTP; opt out of Secure so the
			// cookies survive the test client.
			SecureCookies: false,
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
	return &uiHarness{
		t: t, h: h, srv: srv, client: client, baseURL: base,
		clk: clk, dir: dir, store: fs,
	}
}

// createPrincipal seeds an admin principal with a known password and
// returns its id.
func (h *uiHarness) createAdmin(email, password string) store.PrincipalID {
	h.t.Helper()
	pid, err := h.dir.CreatePrincipal(context.Background(), email, password)
	if err != nil {
		h.t.Fatalf("CreatePrincipal: %v", err)
	}
	p, err := h.store.Meta().GetPrincipalByID(context.Background(), pid)
	if err != nil {
		h.t.Fatalf("GetPrincipalByID: %v", err)
	}
	p.Flags |= store.PrincipalFlagAdmin
	if err := h.store.Meta().UpdatePrincipal(context.Background(), p); err != nil {
		h.t.Fatalf("UpdatePrincipal: %v", err)
	}
	return pid
}

// loginPasswordOnly performs the login flow against the UI and
// returns the resulting response. The test client's jar carries the
// session cookie forward.
func (h *uiHarness) loginPasswordOnly(email, password string) *http.Response {
	h.t.Helper()
	form := url.Values{
		"email":    []string{email},
		"password": []string{password},
	}
	req, err := http.NewRequest("POST", h.baseURL+"/ui/login", strings.NewReader(form.Encode()))
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("login: %v", err)
	}
	return res
}

// get issues a GET that follows session cookies. Returns the response and body.
func (h *uiHarness) get(path string) (*http.Response, string) {
	h.t.Helper()
	req, err := http.NewRequest("GET", h.baseURL+path, nil)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

// post posts a form, automatically setting Content-Type. The CSRF
// cookie is read from the jar and added to both the form (_csrf) and
// the X-CSRF-Token header so either path works.
func (h *uiHarness) post(path string, form url.Values, attachCSRF bool) *http.Response {
	h.t.Helper()
	if attachCSRF {
		u, _ := url.Parse(h.baseURL + path)
		for _, c := range h.client.Jar.Cookies(u) {
			if c.Name == "herold_ui_csrf" {
				form.Set("_csrf", c.Value)
				break
			}
		}
	}
	req, err := http.NewRequest("POST", h.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("post: %v", err)
	}
	return res
}

// =========== TESTS ===========

func TestLogin_Password_SetsCookie_Redirects(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/ui/dashboard" {
		t.Fatalf("redirect = %q, want /ui/dashboard", loc)
	}
	// Cookie set?
	u, _ := url.Parse(h.baseURL + "/ui/dashboard")
	var found bool
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == "herold_ui_session" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no session cookie in jar")
	}
}

func TestLogin_BadPassword_Rejected(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "wrong-password-here-x")
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Email or password is incorrect") {
		t.Fatalf("body missing error message: %s", body)
	}
}

func TestLogin_RequiresTOTPWhenEnrolled(t *testing.T) {
	h := newUIHarness(t)
	pid := h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	// Enroll TOTP and confirm it.
	secret, _, err := h.dir.EnrollTOTP(context.Background(), pid)
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	// Generate a valid code at the fake clock's now to confirm.
	code := totpCodeAt(t, secret, h.clk.Now())
	if err := h.dir.ConfirmTOTP(context.Background(), pid, code); err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}
	// First login: password without code -> NeedTOTP form.
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (TOTP prompt)", res.StatusCode)
	}
	if !strings.Contains(string(body), "Two-factor code") {
		t.Fatalf("body missing TOTP prompt: %s", body)
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	res = h.post("/ui/logout", url.Values{}, false)
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", res.StatusCode)
	}
	// Session-cookie should be cleared (Set-Cookie with MaxAge<0).
	u, _ := url.Parse(h.baseURL + "/ui/dashboard")
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == "herold_ui_session" && c.Value != "" {
			t.Fatalf("session cookie still set: %v", c)
		}
	}
}

func TestPrincipals_List_Renders(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	res2, body := h.get("/ui/principals")
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("list = %d", res2.StatusCode)
	}
	if !strings.Contains(body, "admin@example.com") {
		t.Fatalf("body missing admin email: %s", body)
	}
}

func TestPrincipals_New_CreateRoundTrip(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()

	// Fetch the new-form page to populate the CSRF cookie via the
	// session bootstrap.
	r, body := h.get("/ui/principals/new")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("new form = %d", r.StatusCode)
	}
	if !strings.Contains(body, "name=\"_csrf\"") {
		t.Fatalf("form missing csrf input: %s", body)
	}

	form := url.Values{
		"email":        []string{"alice@example.com"},
		"password":     []string{"correct-horse-battery-staple"},
		"display_name": []string{"Alice"},
	}
	res2 := h.post("/ui/principals", form, true)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusSeeOther {
		bbb, _ := io.ReadAll(res2.Body)
		t.Fatalf("create = %d: %s", res2.StatusCode, bbb)
	}
	loc := res2.Header.Get("Location")
	if !strings.HasPrefix(loc, "/ui/principals/") {
		t.Fatalf("redirect = %q", loc)
	}
	// Verify the principal exists in the store.
	p, err := h.store.Meta().GetPrincipalByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("get principal: %v", err)
	}
	if p.DisplayName != "Alice" {
		t.Fatalf("display name = %q, want Alice", p.DisplayName)
	}
}

func TestPrincipals_TOTPEnrollFlow(t *testing.T) {
	h := newUIHarness(t)
	pid := h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	// Trigger enroll.
	res2 := h.post(fmt.Sprintf("/ui/principals/%d/totp/enroll", pid), url.Values{}, true)
	defer res2.Body.Close()
	body, _ := io.ReadAll(res2.Body)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("enroll = %d: %s", res2.StatusCode, body)
	}
	if !strings.Contains(string(body), "data:image/png;base64,") {
		t.Fatalf("body missing QR data URL: %s", body)
	}
	// Pull the secret from the principal row to compute a code.
	p, err := h.store.Meta().GetPrincipalByID(context.Background(), pid)
	if err != nil {
		t.Fatalf("get pid: %v", err)
	}
	secret := string(p.TOTPSecret)
	if secret == "" {
		t.Fatalf("totp secret not stored")
	}
	code := totpCodeAt(t, secret, h.clk.Now())
	form := url.Values{"code": []string{code}}
	res3 := h.post(fmt.Sprintf("/ui/principals/%d/totp/confirm", pid), form, true)
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusSeeOther {
		bbb, _ := io.ReadAll(res3.Body)
		t.Fatalf("confirm = %d: %s", res3.StatusCode, bbb)
	}
	// Verify the flag flipped.
	p, _ = h.store.Meta().GetPrincipalByID(context.Background(), pid)
	if !p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("totp_enabled flag not set after confirm")
	}
}

func TestDomains_AddCreate(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	r, _ := h.get("/ui/domains")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("domains list = %d", r.StatusCode)
	}
	form := url.Values{"name": []string{"example.com"}}
	res2 := h.post("/ui/domains", form, true)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusSeeOther {
		bbb, _ := io.ReadAll(res2.Body)
		t.Fatalf("domain create = %d: %s", res2.StatusCode, bbb)
	}
	d, err := h.store.Meta().GetDomain(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("get domain: %v", err)
	}
	if d.Name != "example.com" {
		t.Fatalf("name = %q", d.Name)
	}
}

func TestQueue_Retry_CallsReschedule(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	// Seed a queue row directly so we don't need the SMTP server.
	id, err := h.store.Meta().EnqueueMessage(context.Background(), store.QueueItem{
		MailFrom:      "from@example.com",
		RcptTo:        "to@example.com",
		EnvelopeID:    "env-test",
		BodyBlobHash:  "deadbeef",
		State:         store.QueueStateInflight,
		NextAttemptAt: h.clk.Now(),
		CreatedAt:     h.clk.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	res2 := h.post(fmt.Sprintf("/ui/queue/%d/retry", id), url.Values{}, true)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusSeeOther {
		bbb, _ := io.ReadAll(res2.Body)
		t.Fatalf("retry = %d: %s", res2.StatusCode, bbb)
	}
	got, err := h.store.Meta().GetQueueItem(context.Background(), id)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if got.State != store.QueueStateDeferred {
		t.Fatalf("state after retry = %v, want deferred", got.State)
	}
	if got.LastError != "operator-retry" {
		t.Fatalf("last_error = %q", got.LastError)
	}
}

func TestResearch_BySender_RendersResults(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	if _, err := h.store.Meta().EnqueueMessage(context.Background(), store.QueueItem{
		MailFrom:      "alice@example.com",
		RcptTo:        "bob@example.org",
		EnvelopeID:    "env-research",
		BodyBlobHash:  "deadbeef",
		State:         store.QueueStateQueued,
		NextAttemptAt: h.clk.Now(),
		CreatedAt:     h.clk.Now(),
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	r, body := h.get("/ui/research?sender=alice")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("research = %d", r.StatusCode)
	}
	if !strings.Contains(body, "alice@example.com") {
		t.Fatalf("body missing sender match: %s", body)
	}
	if !strings.Contains(body, "bob@example.org") {
		t.Fatalf("body missing recipient: %s", body)
	}
}

func TestCSRF_RejectsPOSTWithoutToken(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	form := url.Values{"name": []string{"oops.com"}}
	// attachCSRF=false: omit the token entirely.
	res2 := h.post("/ui/domains", form, false)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusForbidden {
		bbb, _ := io.ReadAll(res2.Body)
		t.Fatalf("status = %d, want 403: %s", res2.StatusCode, bbb)
	}
}

func TestCSRF_AcceptsPOSTWithMatchingToken(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	form := url.Values{"name": []string{"ok.com"}}
	res2 := h.post("/ui/domains", form, true)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusSeeOther {
		bbb, _ := io.ReadAll(res2.Body)
		t.Fatalf("status = %d, want 303: %s", res2.StatusCode, bbb)
	}
}

func TestSession_ExpiredCookie_RedirectsToLogin(t *testing.T) {
	h := newUIHarness(t)
	h.createAdmin("admin@example.com", "correct-horse-battery-staple")
	res := h.loginPasswordOnly("admin@example.com", "correct-horse-battery-staple")
	res.Body.Close()
	// Advance the fake clock past the 1-hour TTL configured in the
	// harness. Sliding renewal stamps a fresh ExpiresAt at every
	// authenticated request, so we must advance AND avoid making a
	// request in between.
	h.clk.Advance(2 * time.Hour)
	r, _ := h.get("/ui/dashboard")
	defer r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", r.StatusCode)
	}
	loc := r.Header.Get("Location")
	if !strings.HasPrefix(loc, "/ui/login") {
		t.Fatalf("redirect = %q, want /ui/login...", loc)
	}
}

// totpCodeAt computes the canonical 6-digit TOTP for secret at instant t.
// Implementation is a thin wrapper around pquerna/otp; we duplicate it
// rather than exporting a directory test helper so the test surface
// stays narrow.
func totpCodeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totpGenerate(secret, at)
	if err != nil {
		t.Fatalf("totp gen: %v", err)
	}
	return code
}
