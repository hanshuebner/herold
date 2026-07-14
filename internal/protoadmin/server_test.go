package protoadmin_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

type harness struct {
	t       *testing.T
	h       *testharness.Server
	srv     *protoadmin.Server
	client  *http.Client
	baseURL string
	clk     *clock.FakeClock
	dir     *directory.Directory
	rp      *directoryoidc.RP
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &harness{
		t: t, h: h, srv: srv, client: client, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
}

// doRequest builds and executes a request. The body, if non-nil, is
// JSON-encoded. When key is non-empty, adds Authorization.
func (h *harness) doRequest(method, path, key string, body any) (*http.Response, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.baseURL+path, rdr)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read: %v", err)
	}
	return res, buf
}

// bootstrap performs the bootstrap call and returns (principalID, apiKey).
func (h *harness) bootstrap(email string) (uint64, string) {
	h.t.Helper()
	res, buf := h.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
		"email":        email,
		"display_name": "Initial Admin",
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("bootstrap: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		PrincipalID   uint64 `json:"principal_id"`
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		h.t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	return out.PrincipalID, out.InitialAPIKey
}

// createPrincipal creates a non-admin principal, returning its ID.
func (h *harness) createPrincipal(adminKey, email string) uint64 {
	h.t.Helper()
	res, buf := h.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("create %s: %d: %s", email, res.StatusCode, buf)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &p); err != nil {
		h.t.Fatalf("decode: %v", err)
	}
	return p.ID
}

// createAPIKey creates an admin-scoped API key for the given principal, using
// the caller's key to authenticate. Returns (keyID, keyPlaintext).
func (h *harness) createAPIKey(callerKey string, pid uint64) (uint64, string) {
	h.t.Helper()
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), callerKey, map[string]any{
		"label":             "operator-key",
		"scope":             []string{"admin"},
		"allow_admin_scope": true,
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("createAPIKey for principal %d: %d: %s", pid, res.StatusCode, buf)
	}
	var created struct {
		ID  uint64 `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		h.t.Fatalf("createAPIKey decode: %v", err)
	}
	return created.ID, created.Key
}

func TestHealthz_Live_Ready(t *testing.T) {
	h := newHarness(t)
	res, _ := h.doRequest("GET", "/api/v1/healthz/live", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", res.StatusCode)
	}
	res, _ = h.doRequest("GET", "/api/v1/healthz/ready", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ready = %d", res.StatusCode)
	}
}

// TestAdminMetrics_RequestsTotalIncrements drives one health probe and
// asserts the herold_admin_requests_total counter advanced for the
// matched route template + method + 200. Proves the path-pattern
// metrics middleware is wired correctly: the label is the route
// template, never the resolved path.
func TestAdminMetrics_RequestsTotalIncrements(t *testing.T) {
	observe.RegisterAdminMetrics()
	const pattern = "/api/v1/healthz/live"
	before := testutil.ToFloat64(observe.AdminRequestsTotal.WithLabelValues(pattern, "GET", "200"))

	h := newHarness(t)
	res, _ := h.doRequest("GET", pattern, "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", res.StatusCode)
	}

	after := testutil.ToFloat64(observe.AdminRequestsTotal.WithLabelValues(pattern, "GET", "200"))
	if after <= before {
		t.Fatalf("herold_admin_requests_total{path_pattern=%q,method=GET,status=200}: before=%v after=%v; want strict increase", pattern, before, after)
	}
}

func TestBootstrap_CreatesFirstAdminAndKey(t *testing.T) {
	h := newHarness(t)
	pid, key := h.bootstrap("admin@example.com")
	if pid == 0 {
		t.Fatalf("pid zero")
	}
	if !strings.HasPrefix(key, protoadmin.APIKeyPrefix) {
		t.Fatalf("api key missing prefix: %q", key)
	}
	// Bootstrap principal must have both Admin and SuperAdmin flags
	// (re #145, re #142 — the first principal is the server owner).
	p, err := h.h.Store.Meta().GetPrincipalByID(context.Background(), store.PrincipalID(pid))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if !p.Flags.Has(store.PrincipalFlagAdmin) {
		t.Errorf("bootstrap principal missing PrincipalFlagAdmin; flags=%v", p.Flags)
	}
	if !p.Flags.Has(store.PrincipalFlagSuperAdmin) {
		t.Errorf("bootstrap principal missing PrincipalFlagSuperAdmin; flags=%v", p.Flags)
	}

	// Second call must fail with 409.
	res, buf := h.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
		"email": "admin2@example.com",
	})
	// With 1 req / window rate limit we may either see 429 or 409
	// depending on the order. Default limit is 1 so the second call
	// is rate-limited; advance to clear the window, then expect 409.
	if res.StatusCode == http.StatusTooManyRequests {
		h.clk.Advance(6 * time.Minute)
		res, buf = h.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
			"email": "admin2@example.com",
		})
	}
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("second bootstrap = %d: %s", res.StatusCode, buf)
	}
}

func TestPrincipals_CRUD(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")

	// Create.
	res, buf := h.doRequest("POST", "/api/v1/principals", key, map[string]any{
		"email":    "alice@example.com",
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Get.
	res, buf = h.doRequest("GET", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get = %d: %s", res.StatusCode, buf)
	}
	// PATCH display name.
	res, buf = h.doRequest("PATCH", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, map[string]any{
		"display_name": "Alice",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d: %s", res.StatusCode, buf)
	}
	var patched struct {
		DisplayName string `json:"display_name"`
	}
	_ = json.Unmarshal(buf, &patched)
	if patched.DisplayName != "Alice" {
		t.Fatalf("display_name = %q", patched.DisplayName)
	}
	// Add an alias so we can assert cascade.
	res, buf = h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":               "alias",
		"domain":              "example.com",
		"target_principal_id": created.ID,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("alias create = %d: %s", res.StatusCode, buf)
	}
	// Delete principal.
	res, _ = h.doRequest("DELETE", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	// Get should now 404.
	res, _ = h.doRequest("GET", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d", res.StatusCode)
	}
	// List should not include the principal.
	res, buf = h.doRequest("GET", "/api/v1/principals", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []struct {
			ID uint64 `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	for _, it := range page.Items {
		if it.ID == created.ID {
			t.Fatalf("deleted principal still listed")
		}
	}
}

// TestPrincipals_DeleteRefusedWhenOwnsMailingList asserts issue #247:
// DELETE on a principal that still owns a mailing list is refused with
// a 409 naming the blocking list, never a 500, and the principal
// survives the refused attempt.
func TestPrincipals_DeleteRefusedWhenOwnsMailingList(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")

	res, buf := h.doRequest("POST", "/api/v1/principals", key, map[string]any{
		"email":    "listowner@example.com",
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	var owner struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &owner); err != nil {
		t.Fatalf("decode: %v", err)
	}

	group, err := h.h.Store.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindGroup,
		CanonicalEmail: "team@example.com",
		DisplayName:    "team@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	list, err := h.h.Store.Meta().InsertMailingList(context.Background(), store.MailingList{
		PrincipalID:    group.ID,
		PostingAddress: "team@example.com",
		DisplayName:    "Team",
		OwnerID:        store.PrincipalID(owner.ID),
		ARCSeal:        true,
	})
	if err != nil {
		t.Fatalf("InsertMailingList: %v", err)
	}

	res, buf = h.doRequest("DELETE", fmt.Sprintf("/api/v1/principals/%d", owner.ID), key, nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete owner of mailing list = %d: %s", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), list.PostingAddress) {
		t.Fatalf("409 body does not name the blocking list %q: %s", list.PostingAddress, buf)
	}

	// The principal must still exist -- the refusal must not have
	// partially applied.
	res, _ = h.doRequest("GET", fmt.Sprintf("/api/v1/principals/%d", owner.ID), key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get after refused delete = %d", res.StatusCode)
	}
}

// TestCreatePrincipal_RandomPassword asserts the random_password
// field on POST /api/v1/principals: the server mints a password,
// embeds it in the response as generated_password, and rejects
// requests that pair random_password with an explicit password
// (issue #115).
func TestCreatePrincipal_RandomPassword(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")

	// Happy path: random_password = true mints a password.
	res, buf := h.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":           "alice@example.com",
		"random_password": true,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID                uint64 `json:"id"`
		Email             string `json:"canonical_email"`
		GeneratedPassword string `json:"generated_password"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if out.ID == 0 {
		t.Errorf("expected non-zero id")
	}
	if out.Email != "alice@example.com" {
		t.Errorf("email: got %q", out.Email)
	}
	if len(out.GeneratedPassword) < 12 {
		t.Errorf("generated_password too short: %q (len %d)", out.GeneratedPassword, len(out.GeneratedPassword))
	}

	// Mutual exclusion: password + random_password is rejected.
	res, buf = h.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":           "bob@example.com",
		"password":        "correct-horse-battery-staple",
		"random_password": true,
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("password+random: status %d, want 400: %s", res.StatusCode, buf)
	}

	// Missing both is also rejected.
	res, buf = h.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email": "carol@example.com",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("no password: status %d, want 400: %s", res.StatusCode, buf)
	}
}

// TestCreatePrincipal_ProvisionesDefaultMailboxes asserts that
// POST /api/v1/principals immediately creates the six standard
// mailboxes for the new user, with the correct Attributes bits, so
// that JMAP and IMAP clients see a populated hierarchy without needing
// a first SMTP delivery (REQ-ADM-MAILBOX-INIT).
func TestCreatePrincipal_ProvisionesDefaultMailboxes(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	aliceID := h.createPrincipal(adminKey, "alice@example.com")

	mbs, err := h.h.Store.Meta().ListMailboxes(context.Background(), store.PrincipalID(aliceID))
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}

	type wantSpec struct {
		name string
		attr store.MailboxAttributes
	}
	want := []wantSpec{
		{"INBOX", store.MailboxAttrInbox},
		{"Sent", store.MailboxAttrSent},
		{"Drafts", store.MailboxAttrDrafts},
		{"Trash", store.MailboxAttrTrash},
		{"Junk", store.MailboxAttrJunk},
		{"Archive", store.MailboxAttrArchive},
	}

	byName := make(map[string]store.Mailbox, len(mbs))
	for _, mb := range mbs {
		byName[mb.Name] = mb
	}

	for _, w := range want {
		mb, ok := byName[w.name]
		if !ok {
			t.Errorf("mailbox %q not found; got %v", w.name, byName)
			continue
		}
		if mb.Attributes&w.attr == 0 {
			t.Errorf("mailbox %q: attributes = %b, want bit %b set", w.name, mb.Attributes, w.attr)
		}
	}

	if t.Failed() {
		return
	}
	if len(mbs) != len(want) {
		t.Errorf("got %d mailboxes, want %d", len(mbs), len(want))
	}
}

func TestPrincipals_Keyset_Pagination(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	// Seed 50 principals.
	for i := 0; i < 50; i++ {
		h.createPrincipal(key, fmt.Sprintf("user%02d@example.com", i))
	}
	seen := map[uint64]bool{}
	cursor := ""
	for {
		path := "/api/v1/principals?limit=10"
		if cursor != "" {
			path += "&after=" + cursor
		}
		res, buf := h.doRequest("GET", path, key, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list = %d: %s", res.StatusCode, buf)
		}
		var page struct {
			Items []struct {
				ID uint64 `json:"id"`
			} `json:"items"`
			Next *string `json:"next"`
		}
		if err := json.Unmarshal(buf, &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, it := range page.Items {
			if seen[it.ID] {
				t.Fatalf("id %d repeated", it.ID)
			}
			seen[it.ID] = true
		}
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	// 50 created + 1 admin bootstrap = 51
	if len(seen) != 51 {
		t.Fatalf("saw %d principals, want 51", len(seen))
	}
}

func TestPasswords_AdminSet_SelfChange_BothWork(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	bobID := h.createPrincipal(adminKey, "bob@example.com")
	// Admin-set: no current_password required.
	res, buf := h.doRequest("PUT", fmt.Sprintf("/api/v1/principals/%d/password", bobID), adminKey, map[string]any{
		"new_password": "new-strong-password-1",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("admin set = %d: %s", res.StatusCode, buf)
	}
	// Verify via directory Authenticate.
	if _, err := h.dir.Authenticate(context.Background(), "bob@example.com", "new-strong-password-1"); err != nil {
		t.Fatalf("authenticate after admin set: %v", err)
	}
	// Self-change: bob needs an API key to call the endpoint. Mint one
	// as admin (create API key for bob).
	res, buf = h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", bobID), adminKey, map[string]any{
		"label": "bobs-cli",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create bob key = %d: %s", res.StatusCode, buf)
	}
	var keyDoc struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(buf, &keyDoc)
	if keyDoc.Key == "" {
		t.Fatalf("no plaintext key returned")
	}
	// Bob changes his own password.
	res, buf = h.doRequest("PUT", fmt.Sprintf("/api/v1/principals/%d/password", bobID), keyDoc.Key, map[string]any{
		"current_password": "new-strong-password-1",
		"new_password":     "even-stronger-password-2",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("self change = %d: %s", res.StatusCode, buf)
	}
	if _, err := h.dir.Authenticate(context.Background(), "bob@example.com", "even-stronger-password-2"); err != nil {
		t.Fatalf("authenticate after self change: %v", err)
	}
}

func TestTOTP_Enroll_Confirm_Verify_Disable(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	pid := h.createPrincipal(adminKey, "totp@example.com")
	// Enroll.
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/totp/enroll", pid), adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("enroll = %d: %s", res.StatusCode, buf)
	}
	var enroll struct {
		Secret          string `json:"secret"`
		ProvisioningURI string `json:"provisioning_uri"`
	}
	_ = json.Unmarshal(buf, &enroll)
	if enroll.Secret == "" || enroll.ProvisioningURI == "" {
		t.Fatalf("missing secret/uri: %+v", enroll)
	}
	// Generate a valid code for the current fake clock.
	code := totpCodeFor(t, enroll.Secret, h.clk.Now())
	res, buf = h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/totp/confirm", pid), adminKey, map[string]any{
		"code": code,
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("confirm = %d: %s", res.StatusCode, buf)
	}
	// Disable.
	res, buf = h.doRequest("DELETE", fmt.Sprintf("/api/v1/principals/%d/totp", pid), adminKey, map[string]any{
		"current_password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("disable = %d: %s", res.StatusCode, buf)
	}
}

// totpCodeFor generates a TOTP code for the given secret at instant t
// using the same parameters the directory package uses (SHA-1, 6
// digits, 30 s period). Duplicated here rather than reaching into the
// directory's private helpers.
func totpCodeFor(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	// pquerna/otp lives under the directory's transitive deps; use it
	// the same way that package does.
	code, err := totpGenerate(secret, at)
	if err != nil {
		t.Fatalf("totp generate: %v", err)
	}
	return code
}

func TestDomains_CRUD(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	// Bootstrap auto-registers example.com so the admin can be created.
	// Use a different domain name for the create/list/delete cycle.
	res, buf := h.doRequest("POST", "/api/v1/domains", key, map[string]any{
		"name": "extra.test",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	res, buf = h.doRequest("GET", "/api/v1/domains", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), "extra.test") {
		t.Fatalf("list missing domain: %s", buf)
	}
	res, _ = h.doRequest("DELETE", "/api/v1/domains/extra.test", key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	res, _ = h.doRequest("DELETE", "/api/v1/domains/extra.test", key, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again = %d", res.StatusCode)
	}
}

func TestAliases_CRUD_ScopedByDomain(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	pid := h.createPrincipal(key, "target@example.com")
	// Create alias.
	res, buf := h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":               "support",
		"domain":              "example.com",
		"target_principal_id": pid,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(buf, &created)
	// Create an alias in a different domain so filter is exercised.
	_, _ = h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":               "info",
		"domain":              "other.test",
		"target_principal_id": pid,
	})
	// List scoped.
	res, buf = h.doRequest("GET", "/api/v1/aliases?domain=example.com", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	if len(page.Items) != 1 {
		t.Fatalf("list len = %d, want 1: %s", len(page.Items), buf)
	}
	// Delete.
	res, _ = h.doRequest("DELETE", fmt.Sprintf("/api/v1/aliases/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
}

// TestAliases_ExternalTarget covers issue #181: an alias may forward to
// an address outside this deployment instead of an internal principal.
func TestAliases_ExternalTarget(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")

	// Reproduction of the reported gap: creating an alias with only an
	// external address (no target_principal_id) used to be rejected.
	res, buf := h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":          "sales",
		"domain":         "example.com",
		"target_address": "Sales@External.Example",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create external-target alias = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID                uint64 `json:"id"`
		TargetPrincipalID uint64 `json:"target_principal_id"`
		TargetAddress     string `json:"target_address"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if created.TargetPrincipalID != 0 {
		t.Fatalf("TargetPrincipalID = %d, want 0", created.TargetPrincipalID)
	}
	if created.TargetAddress != "sales@external.example" {
		t.Fatalf("TargetAddress = %q, want lower-cased sales@external.example", created.TargetAddress)
	}

	// Neither target set is rejected.
	res, _ = h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":  "neither",
		"domain": "example.com",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with neither target = %d, want 400", res.StatusCode)
	}

	// Both targets set is rejected.
	pid := h.createPrincipal(key, "target2@example.com")
	res, _ = h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":               "both",
		"domain":              "example.com",
		"target_principal_id": pid,
		"target_address":      "x@external.example",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with both targets = %d, want 400", res.StatusCode)
	}

	// Malformed external address is rejected.
	res, _ = h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":          "badaddr",
		"domain":         "example.com",
		"target_address": "not-an-email",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with malformed target_address = %d, want 400", res.StatusCode)
	}

	// The list surface shows the external target.
	res, buf = h.doRequest("GET", "/api/v1/aliases?domain=example.com", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []struct {
			ID            uint64 `json:"id"`
			TargetAddress string `json:"target_address"`
		} `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	var found bool
	for _, it := range page.Items {
		if it.ID == created.ID {
			found = true
			if it.TargetAddress != "sales@external.example" {
				t.Fatalf("list TargetAddress = %q", it.TargetAddress)
			}
		}
	}
	if !found {
		t.Fatalf("external-target alias %d not found in list: %s", created.ID, buf)
	}
}

func TestOIDCProviders_CRUD(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")

	stub := newOIDCStubMini(t)
	// Create provider against the stub.
	res, buf := h.doRequest("POST", "/api/v1/oidc/providers", key, map[string]any{
		"name":          "google",
		"issuer":        stub.URL,
		"client_id":     "cid",
		"client_secret": "csecret",
		"scopes":        []string{"email"},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	// List.
	res, buf = h.doRequest("GET", "/api/v1/oidc/providers", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), `"google"`) {
		t.Fatalf("list missing provider: %s", buf)
	}
	// Delete.
	res, _ = h.doRequest("DELETE", "/api/v1/oidc/providers/google", key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
}

// oidcProviderWireDTO mirrors protoadmin's unexported oidcProviderDTO
// wire shape for tests in the external protoadmin_test package.
type oidcProviderWireDTO struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	AutoProvision       bool   `json:"auto_provision"`
	AutoProvisionDomain string `json:"auto_provision_domain"`
}

// TestOIDCProviders_AutoProvisionGate exercises the REQ-AUTH-56 REST
// surface (issue #230): setting auto_provision=true, whether at create
// or via PATCH, requires a server:superadmin caller (mirrors the
// authz_trusted claim-mapping gate's posture -- letting anyone with an
// account at the configured IdP mint a local principal is a
// whole-deployment security decision). An admin-but-not-superadmin
// caller is refused; disabling it (a strictly safer direction) is not
// gated the same way.
func TestOIDCProviders_AutoProvisionGate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, superKey := h.bootstrap("superadmin-oidc@example.test")
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "superadmin-oidc@example.test")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	if err := h.h.Store.Meta().InsertDomain(ctx, store.Domain{Name: "auto.example", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}

	// A domain-scoped admin (admin, not super-admin).
	opID := h.createPrincipal(superKey, "op-oidc@example.test")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal op: %v", err)
	}
	_, opKey := h.createAPIKey(superKey, opID)

	stub := newOIDCStubMini(t)

	// A domain-admin (not superadmin) cannot create a provider with
	// auto_provision=true.
	res, buf := h.doRequest("POST", "/api/v1/oidc/providers", opKey, map[string]any{
		"name":                  "autoprov-forbidden",
		"issuer":                stub.URL,
		"client_id":             "cid",
		"client_secret":         "csecret",
		"auto_provision":        true,
		"auto_provision_domain": "auto.example",
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("domain-admin create with auto_provision = %d, want 403: %s", res.StatusCode, buf)
	}

	// A superadmin can. auto_provision_domain is required.
	res, buf = h.doRequest("POST", "/api/v1/oidc/providers", superKey, map[string]any{
		"name":           "autoprov",
		"issuer":         stub.URL,
		"client_id":      "cid",
		"client_secret":  "csecret",
		"auto_provision": true,
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("superadmin create with auto_provision, no domain = %d, want 400: %s", res.StatusCode, buf)
	}
	res, buf = h.doRequest("POST", "/api/v1/oidc/providers", superKey, map[string]any{
		"name":                  "autoprov",
		"issuer":                stub.URL,
		"client_id":             "cid",
		"client_secret":         "csecret",
		"auto_provision":        true,
		"auto_provision_domain": "auto.example",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("superadmin create with auto_provision = %d: %s", res.StatusCode, buf)
	}
	var created oidcProviderWireDTO
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !created.AutoProvision || created.AutoProvisionDomain != "auto.example" {
		t.Fatalf("created DTO = %+v", created)
	}

	// A domain-admin (not superadmin) cannot flip an existing provider's
	// auto_provision to true via PATCH.
	res, buf = h.doRequest("PATCH", "/api/v1/oidc/providers/autoprov", opKey, map[string]any{
		"auto_provision": true,
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("domain-admin PATCH auto_provision=true = %d, want 403: %s", res.StatusCode, buf)
	}

	// But a domain-admin CAN disable it (reducing access is always
	// safe) -- no superadmin gate on the false direction.
	res, buf = h.doRequest("PATCH", "/api/v1/oidc/providers/autoprov", opKey, map[string]any{
		"auto_provision": false,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("domain-admin PATCH auto_provision=false = %d: %s", res.StatusCode, buf)
	}
	var patched oidcProviderWireDTO
	if err := json.Unmarshal(buf, &patched); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if patched.AutoProvision {
		t.Fatalf("patched DTO still auto_provision=true: %+v", patched)
	}

	// A superadmin can re-enable it.
	res, buf = h.doRequest("PATCH", "/api/v1/oidc/providers/autoprov", superKey, map[string]any{
		"auto_provision": true,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("superadmin PATCH auto_provision=true = %d: %s", res.StatusCode, buf)
	}

	// A PATCH naming no recognised field is a deterministic 501, not a
	// silent success.
	res, buf = h.doRequest("PATCH", "/api/v1/oidc/providers/autoprov", superKey, map[string]any{})
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("empty PATCH = %d, want 501: %s", res.StatusCode, buf)
	}
}

// newOIDCStubMini returns an httptest.Server speaking just enough of
// the OIDC discovery endpoint for RP.AddProvider to succeed. Full
// token-exchange flow is tested in directoryoidc; here we only need
// AddProvider to find a valid issuer.
func newOIDCStubMini(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	srv := httptest.NewServer(mux)
	issuer = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func TestAPIKeys_Create_Returns_Plaintext_Once(t *testing.T) {
	h := newHarness(t)
	pid, adminKey := h.bootstrap("admin@example.com")
	// Admin creates an API key for self.
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), adminKey, map[string]any{
		"label": "machine-1",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID    uint64   `json:"id"`
		Key   string   `json:"key"`
		Scope []string `json:"scope"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Key == "" {
		t.Fatalf("no plaintext key returned")
	}
	// REQ-AUTH-SCOPE-04: default scope is [mail.send] when the
	// request omits the scope field.
	if len(created.Scope) != 1 || created.Scope[0] != "mail.send" {
		t.Errorf("default scope = %v, want [mail.send]", created.Scope)
	}
	// GET on listing does NOT include the plaintext.
	res, buf = h.doRequest("GET", "/api/v1/api-keys", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	if strings.Contains(string(buf), created.Key) {
		t.Fatalf("listing leaked plaintext key: %s", buf)
	}
}

// TestAPIKeys_Create_AdminScope_RequiresAcknowledgement covers
// REQ-AUTH-SCOPE-04: requesting admin scope without
// allow_admin_scope=true is rejected with 400.
func TestAPIKeys_Create_AdminScope_RequiresAcknowledgement(t *testing.T) {
	h := newHarness(t)
	pid, adminKey := h.bootstrap("admin@example.com")
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), adminKey, map[string]any{
		"label": "ops-key",
		"scope": []string{"admin"},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("admin scope w/o allow flag: status=%d body=%s; want 400", res.StatusCode, buf)
	}
	// Now retry with the explicit acknowledgement.
	res, buf = h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), adminKey, map[string]any{
		"label":             "ops-key",
		"scope":             []string{"admin"},
		"allow_admin_scope": true,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("admin scope w/ allow flag: status=%d body=%s; want 201", res.StatusCode, buf)
	}
	var created struct {
		Scope []string `json:"scope"`
	}
	_ = json.Unmarshal(buf, &created)
	if len(created.Scope) != 1 || created.Scope[0] != "admin" {
		t.Errorf("scope = %v, want [admin]", created.Scope)
	}
}

// TestAPIKeys_Create_RejectsUnknownScope covers REQ-AUTH-SCOPE-01:
// scope values outside the closed enum are rejected at create time.
func TestAPIKeys_Create_RejectsUnknownScope(t *testing.T) {
	h := newHarness(t)
	pid, adminKey := h.bootstrap("admin@example.com")
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), adminKey, map[string]any{
		"label": "bogus",
		"scope": []string{"unknown-scope"},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown scope: status=%d body=%s; want 400", res.StatusCode, buf)
	}
}

// TestScopeEnforcement_AdminEndpointRejectsMailSendOnly covers
// REQ-AUTH-SCOPE-02: an API key with scope [mail.send] hitting an
// admin-only endpoint receives 403 + insufficient_scope problem.
func TestScopeEnforcement_AdminEndpointRejectsMailSendOnly(t *testing.T) {
	h := newHarness(t)
	pid, adminKey := h.bootstrap("admin@example.com")

	// Create a mail.send-only API key for the admin principal.
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), adminKey, map[string]any{
		"label": "transactional-app",
		"scope": []string{"mail.send"},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", res.StatusCode, buf)
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Hit GET /api/v1/principals (admin-only) with the new key.
	res, buf = h.doRequest("GET", "/api/v1/principals", created.Key, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("mail.send key on admin endpoint: status=%d body=%s; want 403", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), "insufficient_scope") {
		t.Errorf("body should reference insufficient_scope: %s", buf)
	}
}

func TestAuditLog_Filters(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	// Produce a few audit entries by creating principals.
	_ = h.createPrincipal(adminKey, "a@example.com")
	_ = h.createPrincipal(adminKey, "b@example.com")
	// GET audit with action filter.
	res, buf := h.doRequest("GET", "/api/v1/audit?action=principal.create&limit=10", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	if len(page.Items) == 0 {
		t.Fatalf("no audit rows: %s", buf)
	}
	for _, it := range page.Items {
		if it["action"] != "principal.create" {
			t.Fatalf("filter leaked: %v", it)
		}
	}
}

// TestAuditLog_RequestIDPropagated asserts that the request ID set by the
// withRequestLog middleware (whether echoed from the X-Request-ID header
// or generated server-side) lands in the audit entry's metadata so that
// log lines and audit rows can be cross-referenced. Covers the contract
// between middleware.requestID() and Server.appendAudit().
func TestAuditLog_RequestIDPropagated(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	// Send a mutating request with a caller-supplied X-Request-ID; the
	// middleware echoes it back and threads it through ctx so appendAudit
	// records it in metadata.
	const wantRID = "rid-test-0123456789abcdef"
	body, err := json.Marshal(map[string]any{
		"email":    "rid@example.com",
		"password": "correct-horse-battery-staple",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest("POST", h.baseURL+"/api/v1/principals", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("X-Request-ID", wantRID)
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", res.StatusCode)
	}
	if got := res.Header.Get("X-Request-ID"); got != wantRID {
		t.Fatalf("X-Request-ID echoed = %q, want %q", got, wantRID)
	}
	// Now scan audit for the matching row.
	res2, buf := h.doRequest("GET", "/api/v1/audit?action=principal.create&limit=50", adminKey, nil)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d: %s", res2.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, it := range page.Items {
		md, _ := it["metadata"].(map[string]any)
		if md == nil {
			continue
		}
		if md["request_id"] == wantRID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no audit row carried request_id=%q; rows=%s", wantRID, buf)
	}
}

// TestListQueue_OperatorScope verifies that a domain-scoped operator sees only
// queue items whose mail_from domain matches their managed-domain set
// (REQ-ADM-307, re #145). The fix was that handleListQueue did not previously
// call ResolveOperatorScope, so operators received unrestricted cross-domain data.
func TestListQueue_OperatorScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Bootstrap and promote to super-admin.
	_, adminKey := h.bootstrap("sa-queue@example.com")
	sa, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-queue@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	sa.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, sa); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	// Create a domain-scoped operator managing only alpha.example.
	opID := h.createPrincipal(adminKey, "op-queue@example.com")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}
	grantDomainOperator(t, h, store.PrincipalID(opID), "alpha.example")
	_, opKey := h.createAPIKey(adminKey, opID)

	// Enqueue items from two sender domains.
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-queue@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail for enqueue: %v", err)
	}
	for _, tc := range []struct {
		mailFrom, rcptTo string
	}{
		{"sender@alpha.example", "dest@remote.test"},
		{"sender@beta.example", "dest2@remote.test"},
	} {
		if _, err := h.h.Store.Meta().EnqueueMessage(ctx, store.QueueItem{
			PrincipalID: p.ID,
			MailFrom:    tc.mailFrom,
			RcptTo:      tc.rcptTo,
			EnvelopeID:  "env-scope-test",
		}); err != nil {
			t.Fatalf("EnqueueMessage %s: %v", tc.mailFrom, err)
		}
	}

	// Operator must see only alpha.example items.
	res, buf := h.doRequest("GET", "/api/v1/queue", opKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("operator GET /api/v1/queue: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	for _, item := range page.Items {
		mailFrom, _ := item["mail_from"].(string)
		if !strings.Contains(mailFrom, "@alpha.example") {
			t.Errorf("operator scope: got mail_from=%q; want only @alpha.example items", mailFrom)
		}
	}
	if len(page.Items) == 0 {
		t.Error("operator: got 0 items; want >= 1 from alpha.example")
	}
}

// TestListQueue_NoDomainOperatorSeesEmpty is the fail-closed regression test for
// the queue surface: an admin with no managed domains must get an empty queue
// list (REQ-ADM-307, re #145).
func TestListQueue_NoDomainOperatorSeesEmpty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, superKey := h.bootstrap("sa-qnd@example.com")

	// Create a no-domain operator.
	ndID := h.createPrincipal(superKey, "nd-queue@example.com")
	nd, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(ndID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	nd.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, nd); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	_, ndKey := h.createAPIKey(superKey, ndID)

	// Enqueue a real item so there is something to leak.
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-qnd@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	if _, err := h.h.Store.Meta().EnqueueMessage(ctx, store.QueueItem{
		PrincipalID: p.ID,
		MailFrom:    "sender@secret.example",
		RcptTo:      "dest@remote.test",
		EnvelopeID:  "env-nd-test",
	}); err != nil {
		t.Fatalf("EnqueueMessage: %v", err)
	}

	res, buf := h.doRequest("GET", "/api/v1/queue", ndKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("no-domain GET /api/v1/queue: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if len(page.Items) != 0 {
		t.Errorf("no-domain operator leaked %d queue item(s); want 0 (fail-closed): %+v",
			len(page.Items), page.Items)
	}
}

// TestListQueue_NewestFirst verifies that GET /api/v1/queue returns items in
// descending ID order (most recently enqueued first), matching the audit log
// and system-events conventions, and that pagination via the returned "next"
// cursor walks toward progressively older items (re #217). Before the fix,
// handleListQueue never set QueueFilter.Newest, so the endpoint returned
// items oldest-first and a cursor comparison bug would have paged in the
// wrong direction once Newest was set.
func TestListQueue_NewestFirst(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, adminKey := h.bootstrap("sa-newest@example.com")
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-newest@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}

	const n = 5
	var ids []uint64
	for i := 0; i < n; i++ {
		id, err := h.h.Store.Meta().EnqueueMessage(ctx, store.QueueItem{
			PrincipalID: p.ID,
			MailFrom:    "sender@newest.example",
			RcptTo:      fmt.Sprintf("dest%d@remote.test", i),
			EnvelopeID:  "env-newest-test",
		})
		if err != nil {
			t.Fatalf("EnqueueMessage %d: %v", i, err)
		}
		ids = append(ids, uint64(id))
	}

	type page struct {
		Items []struct {
			ID uint64 `json:"id"`
		} `json:"items"`
		Next *string `json:"next"`
	}

	// First page, small limit to force pagination across the 5 rows.
	res, buf := h.doRequest("GET", "/api/v1/queue?limit=2", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/queue: %d: %s", res.StatusCode, buf)
	}
	var pg1 page
	if err := json.Unmarshal(buf, &pg1); err != nil {
		t.Fatalf("decode page 1: %v: %s", err, buf)
	}
	if len(pg1.Items) != 2 {
		t.Fatalf("page 1: got %d items, want 2", len(pg1.Items))
	}
	if pg1.Items[0].ID != ids[n-1] || pg1.Items[1].ID != ids[n-2] {
		t.Errorf("page 1: got ids [%d %d], want [%d %d] (newest first)",
			pg1.Items[0].ID, pg1.Items[1].ID, ids[n-1], ids[n-2])
	}
	if pg1.Next == nil {
		t.Fatal("page 1: want a next cursor")
	}

	// Second page must continue toward older items, not repeat or loop.
	res, buf = h.doRequest("GET", "/api/v1/queue?limit=2&after_id="+*pg1.Next, adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/queue page 2: %d: %s", res.StatusCode, buf)
	}
	var pg2 page
	if err := json.Unmarshal(buf, &pg2); err != nil {
		t.Fatalf("decode page 2: %v: %s", err, buf)
	}
	if len(pg2.Items) != 2 {
		t.Fatalf("page 2: got %d items, want 2", len(pg2.Items))
	}
	if pg2.Items[0].ID != ids[n-3] || pg2.Items[1].ID != ids[n-4] {
		t.Errorf("page 2: got ids [%d %d], want [%d %d] (continuing older)",
			pg2.Items[0].ID, pg2.Items[1].ID, ids[n-3], ids[n-4])
	}
}

// TestAuditLog_OperatorScope verifies that a domain-scoped operator sees only
// audit entries whose domain matches their managed-domain set (REQ-ADM-307, re #145).
func TestAuditLog_OperatorScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, adminKey := h.bootstrap("sa-audit@example.com")
	sa, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-audit@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	sa.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, sa); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	// Create a domain operator managing alpha.example.
	opID := h.createPrincipal(adminKey, "op-audit@example.com")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}
	grantDomainOperator(t, h, store.PrincipalID(opID), "alpha.example")
	_, opKey := h.createAPIKey(adminKey, opID)

	// Seed audit entries: one for alpha.example, one for beta.example, one global.
	for _, tc := range []struct {
		action, domain string
	}{
		{"queue.scope.alpha", "alpha.example"},
		{"queue.scope.beta", "beta.example"},
		{"queue.scope.global", ""},
	} {
		if err := h.h.Store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:        h.clk.Now(),
			ActorKind: store.ActorSystem,
			ActorID:   "test",
			Action:    tc.action,
			Subject:   "test:scope",
			Outcome:   store.OutcomeSuccess,
			Domain:    tc.domain,
		}); err != nil {
			t.Fatalf("AppendAuditLog %s: %v", tc.action, err)
		}
	}

	res, buf := h.doRequest("GET", "/api/v1/audit", opKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("operator GET /api/v1/audit: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	// Operator must see only alpha.example entries.
	for _, item := range page.Items {
		dom, _ := item["domain"].(string)
		if dom != "alpha.example" {
			t.Errorf("operator scope leaked domain=%q; want only alpha.example", dom)
		}
	}
}

// TestAuditLog_NoDomainOperatorSeesEmpty is the fail-closed regression test for
// the audit log surface: an admin with no managed domains must see an empty
// audit log (REQ-ADM-307, re #145).
func TestAuditLog_NoDomainOperatorSeesEmpty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, superKey := h.bootstrap("sa-andos@example.com")

	ndID := h.createPrincipal(superKey, "nd-audit@example.com")
	nd, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(ndID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	nd.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, nd); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	_, ndKey := h.createAPIKey(superKey, ndID)

	// Seed an audit entry with a domain so there is data to potentially leak.
	if err := h.h.Store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:        h.clk.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "test",
		Action:    "audit.nodomain.regression",
		Subject:   "test:scope",
		Outcome:   store.OutcomeSuccess,
		Domain:    "secret.example",
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}

	res, buf := h.doRequest("GET", "/api/v1/audit", ndKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("no-domain GET /api/v1/audit: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if len(page.Items) != 0 {
		t.Errorf("no-domain operator leaked %d audit entry/entries; want 0 (fail-closed): %+v",
			len(page.Items), page.Items)
	}
}

// TestAuditLog_DomainMutations_TaggedAndOperatorScoped exercises the real
// admin mutation handlers (not a hand-seeded AuditLogEntry) to prove
// protoadmin.appendAuditDomain actually populates AuditLogEntry.Domain at
// the domain-scoped call sites (alias.*, domain.*), closing the gap left
// open by 50df8989 where appendAudit always wrote domain="" (re #145).
//
// Security properties asserted:
//   - A domain-scoped operator sees the domain-tagged entries for ITS OWN
//     domain (positive: alias.create/domain.create for alpha.example).
//   - The same operator does NOT see entries for a domain it does not
//     manage (beta.example) -- no leak.
//   - The same operator does NOT see a server-wide action (principal.create,
//     domain="") -- an unscoped action never becomes visible to an operator
//     by accident.
//   - A super-admin still sees every domain's entries AND the server-wide
//     one.
func TestAuditLog_DomainMutations_TaggedAndOperatorScoped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, adminKey := h.bootstrap("sa-audittag@example.com")
	sa, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-audittag@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	sa.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, sa); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	// Two real domains, created through the REST endpoint so domain.create
	// is exercised (and audit-tagged) too.
	for _, d := range []string{"alpha.example", "beta.example"} {
		res, buf := h.doRequest("POST", "/api/v1/domains", adminKey, map[string]any{"name": d})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create domain %s: %d: %s", d, res.StatusCode, buf)
		}
	}

	// An alias on each domain, through the REST endpoint (alias.create).
	for _, d := range []string{"alpha.example", "beta.example"} {
		res, buf := h.doRequest("POST", "/api/v1/aliases", adminKey, map[string]any{
			"local":               "info",
			"domain":              d,
			"target_principal_id": sa.ID,
		})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create alias on %s: %d: %s", d, res.StatusCode, buf)
		}
	}

	// A server-wide action: creating an ordinary principal. Must stay
	// domain="" and therefore invisible to any domain-scoped operator.
	_ = h.createPrincipal(adminKey, "someone@example.com")

	// Domain-scoped operator managing alpha.example only.
	opID := h.createPrincipal(adminKey, "op-audittag@example.com")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}
	grantDomainOperator(t, h, store.PrincipalID(opID), "alpha.example")
	_, opKey := h.createAPIKey(adminKey, opID)

	type entry struct {
		Action string `json:"action"`
		Domain string `json:"domain"`
	}
	fetchAudit := func(key string, query string) []entry {
		t.Helper()
		res, buf := h.doRequest("GET", "/api/v1/audit"+query, key, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/v1/audit%s: %d: %s", query, res.StatusCode, buf)
		}
		var page struct {
			Items []entry `json:"items"`
		}
		if err := json.Unmarshal(buf, &page); err != nil {
			t.Fatalf("decode: %v: %s", err, buf)
		}
		return page.Items
	}

	// Super-admin sees everything: both domains' entries and the
	// server-wide one.
	saItems := fetchAudit(adminKey, "?limit=1000")
	wantSA := map[string]bool{
		"domain.create alpha.example": false,
		"domain.create beta.example":  false,
		"alias.create alpha.example":  false,
		"alias.create beta.example":   false,
		"principal.create ":           false,
	}
	for _, it := range saItems {
		key := it.Action + " " + it.Domain
		if _, ok := wantSA[key]; ok {
			wantSA[key] = true
		}
	}
	for k, seen := range wantSA {
		if !seen {
			t.Errorf("super-admin missing expected audit entry %q", k)
		}
	}

	// Domain-scoped operator: only alpha.example, never beta.example, never
	// the server-wide principal.create.
	opItems := fetchAudit(opKey, "?limit=1000")
	if len(opItems) == 0 {
		t.Fatal("operator: got 0 audit entries; want alpha.example's domain.create + alias.create")
	}
	sawAlphaDomainCreate := false
	sawAlphaAliasCreate := false
	for _, it := range opItems {
		if it.Domain != "alpha.example" {
			t.Errorf("operator scope leaked domain=%q action=%q; want only alpha.example", it.Domain, it.Action)
		}
		if it.Action == "principal.create" {
			t.Errorf("operator saw server-wide action %q; must never be visible to a domain operator", it.Action)
		}
		if it.Action == "domain.create" && it.Domain == "alpha.example" {
			sawAlphaDomainCreate = true
		}
		if it.Action == "alias.create" && it.Domain == "alpha.example" {
			sawAlphaAliasCreate = true
		}
	}
	if !sawAlphaDomainCreate {
		t.Error("operator did not see its own domain.create entry for alpha.example")
	}
	if !sawAlphaAliasCreate {
		t.Error("operator did not see its own alias.create entry for alpha.example")
	}
}

// TestAuditLog_OperatorScope_DefeatAttempts tries to defeat the REQ-ADM-307
// domain filter on GET /api/v1/audit from the perspective of a malicious or
// confused domain-scoped operator: an explicit (unsupported) domain query
// parameter, an action filter combined with domain scope, and keyset
// pagination. Every attempt MUST fail closed -- the operator never sees a
// beta.example entry through any of these paths (re #145).
func TestAuditLog_OperatorScope_DefeatAttempts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, adminKey := h.bootstrap("sa-defeat@example.com")
	sa, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-defeat@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	sa.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, sa); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}
	for _, d := range []string{"alpha.example", "beta.example"} {
		res, buf := h.doRequest("POST", "/api/v1/domains", adminKey, map[string]any{"name": d})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create domain %s: %d: %s", d, res.StatusCode, buf)
		}
	}
	// Several alpha-domain entries (for pagination) and one beta entry
	// (the thing that must never leak).
	for i := 0; i < 3; i++ {
		res, buf := h.doRequest("POST", "/api/v1/aliases", adminKey, map[string]any{
			"local":               fmt.Sprintf("info%d", i),
			"domain":              "alpha.example",
			"target_principal_id": sa.ID,
		})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create alpha alias %d: %d: %s", i, res.StatusCode, buf)
		}
	}
	res, buf := h.doRequest("POST", "/api/v1/aliases", adminKey, map[string]any{
		"local":               "secret",
		"domain":              "beta.example",
		"target_principal_id": sa.ID,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create beta alias: %d: %s", res.StatusCode, buf)
	}

	opID := h.createPrincipal(adminKey, "op-defeat@example.com")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}
	grantDomainOperator(t, h, store.PrincipalID(opID), "alpha.example")
	_, opKey := h.createAPIKey(adminKey, opID)

	type entry struct {
		ID     uint64 `json:"id"`
		Action string `json:"action"`
		Domain string `json:"domain"`
	}
	assertNoLeak := func(t *testing.T, items []entry) {
		t.Helper()
		for _, it := range items {
			if it.Domain == "beta.example" {
				t.Errorf("defeat attempt succeeded: leaked beta.example entry action=%q", it.Action)
			}
		}
	}

	t.Run("explicit_domain_query_param_ignored", func(t *testing.T) {
		res, buf := h.doRequest("GET", "/api/v1/audit?domain=beta.example", opKey, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/v1/audit?domain=beta.example: %d: %s", res.StatusCode, buf)
		}
		var page struct {
			Items []entry `json:"items"`
		}
		if err := json.Unmarshal(buf, &page); err != nil {
			t.Fatalf("decode: %v: %s", err, buf)
		}
		assertNoLeak(t, page.Items)
	})

	t.Run("action_filter_intersects_not_replaces_scope", func(t *testing.T) {
		res, buf := h.doRequest("GET", "/api/v1/audit?action=alias.create", opKey, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/v1/audit?action=alias.create: %d: %s", res.StatusCode, buf)
		}
		var page struct {
			Items []entry `json:"items"`
		}
		if err := json.Unmarshal(buf, &page); err != nil {
			t.Fatalf("decode: %v: %s", err, buf)
		}
		assertNoLeak(t, page.Items)
		if len(page.Items) == 0 {
			t.Error("action filter combined with domain scope returned 0 rows; want the 3 alpha.example alias.create entries")
		}
		for _, it := range page.Items {
			if it.Action != "alias.create" {
				t.Errorf("got action=%q; want only alias.create", it.Action)
			}
		}
	})

	t.Run("pagination_stays_scoped", func(t *testing.T) {
		var beforeID uint64
		seen := map[uint64]bool{}
		for page := 0; page < 20; page++ {
			path := "/api/v1/audit?limit=1"
			if beforeID != 0 {
				path += fmt.Sprintf("&before_id=%d", beforeID)
			}
			res, buf := h.doRequest("GET", path, opKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: %d: %s", path, res.StatusCode, buf)
			}
			var pageBody struct {
				Items []entry `json:"items"`
				Next  *string `json:"next"`
			}
			if err := json.Unmarshal(buf, &pageBody); err != nil {
				t.Fatalf("decode: %v: %s", err, buf)
			}
			assertNoLeak(t, pageBody.Items)
			for _, it := range pageBody.Items {
				seen[it.ID] = true
			}
			if pageBody.Next == nil {
				break
			}
			n, err := strconv.ParseUint(*pageBody.Next, 10, 64)
			if err != nil {
				t.Fatalf("parse next cursor %q: %v", *pageBody.Next, err)
			}
			beforeID = n
		}
		if len(seen) < 3 {
			t.Errorf("pagination collected %d alpha.example entries across pages; want >= 3", len(seen))
		}
	})
}

// TestAuditLog_QueueMutationsTaggedWithSenderDomain proves queue.hold (and
// by the same code path retry/release/delete) is domain-tagged from the
// queue item's sender domain, so a domain-scoped operator's audit page
// shows its own queue actions without leaking another domain's (re #145).
func TestAuditLog_QueueMutationsTaggedWithSenderDomain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, adminKey := h.bootstrap("sa-qaudit@example.com")
	sa, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa-qaudit@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	sa.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, sa); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	opID := h.createPrincipal(adminKey, "op-qaudit@example.com")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}
	grantDomainOperator(t, h, store.PrincipalID(opID), "alpha.example")
	_, opKey := h.createAPIKey(adminKey, opID)

	var alphaID, betaID store.QueueItemID
	for _, tc := range []struct {
		mailFrom string
		dst      *store.QueueItemID
	}{
		{"sender@alpha.example", &alphaID},
		{"sender@beta.example", &betaID},
	} {
		id, err := h.h.Store.Meta().EnqueueMessage(ctx, store.QueueItem{
			PrincipalID: sa.ID,
			MailFrom:    tc.mailFrom,
			RcptTo:      "dest@remote.test",
			EnvelopeID:  store.EnvelopeID("env-" + tc.mailFrom),
		})
		if err != nil {
			t.Fatalf("EnqueueMessage %s: %v", tc.mailFrom, err)
		}
		*tc.dst = id
	}

	for _, id := range []store.QueueItemID{alphaID, betaID} {
		res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/queue/%d/hold", id), adminKey, nil)
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("hold queue item %d: %d: %s", id, res.StatusCode, buf)
		}
	}

	type entry struct {
		Action  string `json:"action"`
		Domain  string `json:"domain"`
		Subject string `json:"subject"`
	}
	fetchAudit := func(key string) []entry {
		t.Helper()
		res, buf := h.doRequest("GET", "/api/v1/audit?action=queue.hold&limit=1000", key, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/v1/audit: %d: %s", res.StatusCode, buf)
		}
		var page struct {
			Items []entry `json:"items"`
		}
		if err := json.Unmarshal(buf, &page); err != nil {
			t.Fatalf("decode: %v: %s", err, buf)
		}
		return page.Items
	}

	saItems := fetchAudit(adminKey)
	wantSubjects := map[string]bool{
		fmt.Sprintf("queue:%d", alphaID): false,
		fmt.Sprintf("queue:%d", betaID):  false,
	}
	for _, it := range saItems {
		if _, ok := wantSubjects[it.Subject]; ok {
			wantSubjects[it.Subject] = true
		}
	}
	for subj, seen := range wantSubjects {
		if !seen {
			t.Errorf("super-admin missing queue.hold audit entry for %s", subj)
		}
	}

	opItems := fetchAudit(opKey)
	if len(opItems) == 0 {
		t.Fatal("operator: got 0 queue.hold audit entries; want alpha.example's")
	}
	alphaSubject := fmt.Sprintf("queue:%d", alphaID)
	betaSubject := fmt.Sprintf("queue:%d", betaID)
	sawAlpha := false
	for _, it := range opItems {
		if it.Domain != "alpha.example" {
			t.Errorf("operator scope leaked domain=%q subject=%q", it.Domain, it.Subject)
		}
		if it.Subject == betaSubject {
			t.Errorf("operator scope leaked beta.example's queue.hold entry: %+v", it)
		}
		if it.Subject == alphaSubject {
			sawAlpha = true
		}
	}
	if !sawAlpha {
		t.Error("operator did not see its own queue.hold entry for alpha.example")
	}
}

func TestAuthentication_Bearer_APIKey_Required(t *testing.T) {
	h := newHarness(t)
	_, _ = h.bootstrap("admin@example.com")
	// No auth -> 401.
	res, _ := h.doRequest("GET", "/api/v1/principals", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth = %d, want 401", res.StatusCode)
	}
	// Wrong scheme -> 401.
	req, _ := http.NewRequest("GET", h.baseURL+"/api/v1/principals", nil)
	req.Header.Set("Authorization", "Basic Zm9vOmJhcg==")
	res, _ = h.client.Do(req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("basic auth = %d, want 401", res.StatusCode)
	}
	res.Body.Close()
}

func TestAuthorization_SelfVsAdmin_Scope(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	bobID := h.createPrincipal(adminKey, "bob@example.com")
	carolID := h.createPrincipal(adminKey, "carol@example.com")
	// Mint a key for bob.
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", bobID), adminKey, map[string]any{
		"label": "bob",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bob key create = %d: %s", res.StatusCode, buf)
	}
	var bobKeyDoc struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(buf, &bobKeyDoc)
	// Bob cannot DELETE Carol (not admin).
	res, _ = h.doRequest("DELETE", fmt.Sprintf("/api/v1/principals/%d", carolID), bobKeyDoc.Key, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("bob delete carol = %d, want 403", res.StatusCode)
	}
	// Bob CAN GET self.
	res, _ = h.doRequest("GET", fmt.Sprintf("/api/v1/principals/%d", bobID), bobKeyDoc.Key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bob get self = %d", res.StatusCode)
	}
	// Bob CANNOT list principals (admin-only).
	res, _ = h.doRequest("GET", "/api/v1/principals", bobKeyDoc.Key, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("bob list = %d, want 403", res.StatusCode)
	}
}

func TestRateLimit_PerKey_Burst_ReturnsRetryAfter(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	// With RequestsPerMinutePerKey=100 and a generous key, 100 calls
	// in a row should succeed; the 101st must 429. Run 100 then one.
	for i := 0; i < 100; i++ {
		res, buf := h.doRequest("GET", "/api/v1/principals", key, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("call %d = %d: %s", i, res.StatusCode, buf)
		}
	}
	res, buf := h.doRequest("GET", "/api/v1/principals", key, nil)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate-limited call = %d: %s", res.StatusCode, buf)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Fatalf("Retry-After missing")
	}
}

func TestPanic_InHandler_Returns500_NotCrash(t *testing.T) {
	// The panic-recover middleware is tested via the exported
	// WrapRecover helper so we can drive a stub handler that
	// intentionally panics without forging a store-level fault. One
	// panic must not crash the process: the outer http.Server would
	// normally catch goroutine panics, but the admin middleware
	// catches them earlier and emits a typed 500.
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{})

	panicker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	wrapped := protoadmin.WrapRecoverForTest(srv, panicker)
	rec := httptest.NewRecorder()
	rr, _ := http.NewRequest("GET", "/anything", nil)
	wrapped.ServeHTTP(rec, rr)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic wrapper status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("panic body = %s", rec.Body.String())
	}
}

// TestOIDCCallback_DispatchesLinkAndSignIn covers Wave 4 finding 10:
// the callback peeks the pending state's flow kind, then dispatches to
// CompleteLink or CompleteSignIn before consuming the state. Pre-fix
// the handler always tried CompleteLink first, which consumed state
// (regardless of flow type) so the SignIn branch was unreachable.
func TestOIDCCallback_DispatchesLinkAndSignIn(t *testing.T) {
	stub := newOIDCStubWithSigner(t, "herold-client")
	h := newHarness(t)
	// Register the provider directly via the RP (bypassing the admin
	// REST surface to keep this test focused on the callback dispatch).
	ctx := context.Background()
	if _, err := h.rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:        "stub",
		IssuerURL:   stub.issuer,
		ClientID:    "herold-client",
		RedirectURL: "http://localhost/cb",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	// Seed a local principal and pre-link "ext-sub-1" so SignIn resolves.
	pid, err := h.h.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	// === Link flow ===
	stub.subject = "ext-sub-1"
	authURL, linkState, err := h.rp.BeginLink(ctx, pid.ID, "stub")
	if err != nil {
		t.Fatalf("BeginLink: %v", err)
	}
	code, gotState := followAuthForCallback(t, authURL)
	if gotState != linkState {
		t.Fatalf("state mismatch: %q vs %q", gotState, linkState)
	}
	res, body := h.doRequest("POST", fmt.Sprintf("/api/v1/oidc/callback?state=%s&code=%s", linkState, code), "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("link callback: %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"linked"`) {
		t.Fatalf("link callback body lacks linked outcome: %s", body)
	}

	// === Sign-in flow ===
	stub.subject = "ext-sub-1"
	authURL, signinState, err := h.rp.BeginSignIn(ctx, "stub")
	if err != nil {
		t.Fatalf("BeginSignIn: %v", err)
	}
	code, gotState = followAuthForCallback(t, authURL)
	if gotState != signinState {
		t.Fatalf("signin state mismatch: %q vs %q", gotState, signinState)
	}
	res, body = h.doRequest("POST", fmt.Sprintf("/api/v1/oidc/callback?state=%s&code=%s", signinState, code), "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("signin callback: %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"signed_in"`) {
		t.Fatalf("signin callback body lacks signed_in outcome: %s", body)
	}

	// === State reuse: a state used twice returns 400 invalid_state ===
	res, _ = h.doRequest("POST", fmt.Sprintf("/api/v1/oidc/callback?state=%s&code=%s", signinState, code), "", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("reused state: %d, want 400", res.StatusCode)
	}
}

// -- helpers --------------------------------------------------------

// totpGenerate returns a RFC 6238 TOTP code at instant at for the given
// base32 secret, using the same parameter set the directory package
// uses (SHA-1, 6 digits, 30 s period). Kept as a thin test helper
// rather than reaching into directory's unexported helpers.
func totpGenerate(secret string, at time.Time) (string, error) {
	return otpGenerateCode(secret, at)
}

// oidcStubWithSigner is a minimal OIDC provider that signs RS256 ID
// tokens against a private key generated for the test. Used by the
// callback-dispatch test (TestOIDCCallback_DispatchesLinkAndSignIn) so
// the RP's full token-exchange flow exercises the dispatcher.
type oidcStubWithSigner struct {
	t        *testing.T
	srv      *httptest.Server
	issuer   string
	key      *rsa.PrivateKey
	kid      string
	clientID string
	subject  string
	nonce    string
}

func newOIDCStubWithSigner(t *testing.T, clientID string) *oidcStubWithSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	s := &oidcStubWithSigner{t: t, key: key, kid: "kid-1", clientID: clientID}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/jwks", s.handleJWKS)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)
	s.srv = httptest.NewServer(mux)
	s.issuer = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

func (s *oidcStubWithSigner) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + "/authorize",
		"token_endpoint":                        s.issuer + "/token",
		"jwks_uri":                              s.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (s *oidcStubWithSigner) handleJWKS(w http.ResponseWriter, r *http.Request) {
	n := s.key.PublicKey.N
	e := big.NewInt(int64(s.key.PublicKey.E))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": s.kid,
			"n": base64.RawURLEncoding.EncodeToString(n.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(e.Bytes()),
		}},
	})
}

func (s *oidcStubWithSigner) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	redirect := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	s.nonce = r.URL.Query().Get("nonce")
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect", 400)
		return
	}
	q := u.Query()
	q.Set("code", "test-code")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *oidcStubWithSigner) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	if r.Form.Get("code") != "test-code" {
		http.Error(w, "bad code", 400)
		return
	}
	tok, err := s.signIDToken()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": tok,
		"token_type":   "Bearer",
		"id_token":     tok,
		"expires_in":   3600,
	})
}

func (s *oidcStubWithSigner) signIDToken() (string, error) {
	header := map[string]any{"alg": "RS256", "kid": s.kid, "typ": "JWT"}
	now := time.Now().Unix()
	payload := map[string]any{
		"iss": s.issuer, "sub": s.subject, "aud": s.clientID,
		"iat": now, "exp": now + 3600, "nonce": s.nonce,
	}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	signingInput := enc(hb) + "." + enc(pb)
	hh := sha256.New()
	hh.Write([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, hh.Sum(nil))
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc(sig), nil
}

// followAuthForCallback walks the stub's auth URL one redirect deep and
// returns the (code, state) the user-agent would receive.
func followAuthForCallback(t *testing.T, authURL string) (code, state string) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("follow auth: %v", err)
	}
	defer resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("no redirect (%d): %v", resp.StatusCode, err)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}
