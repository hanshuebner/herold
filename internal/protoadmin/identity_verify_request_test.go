package protoadmin_test

// identity_verify_request_test.go covers POST
// /api/v1/identities/{id}/verify-request (REQ-IDENT-36, REQ-IDENT-41
// resend surface). The resender is injected as a fake; the rate-limit
// gate semantics are exercised end-to-end at the store level in the
// storetest compliance suite and at the dispatcher level in
// internal/identityverify. Here we focus on the HTTP surface:
//
//   - 204 on success when the resender returns nil
//   - 429 + Retry-After when the resender returns store.ErrRateLimited
//   - 503 when no resender is wired
//   - 404 when the identity does not exist
//   - 403 when the caller does not own the identity (cross-principal)
//   - 409 when the identity is already verified
//   - audit-log entry shape: identity.verify.failure for rate_limited

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fakeResender records each Resend invocation and returns whatever
// `respErr` is set to. The store-side rate-limit gate is not exercised
// here; the dispatcher tests own that.
type fakeResender struct {
	mu      sync.Mutex
	calls   []store.JMAPIdentity
	respErr error
}

func (f *fakeResender) Resend(_ context.Context, row store.JMAPIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, row)
	return f.respErr
}

// resendHarness wraps the test harness for the resend endpoint. Mirrors
// submissionHarness but stays focused on the verification-request
// surface so the two test files don't co-evolve.
type resendHarness struct {
	t        *testing.T
	fs       store.Store
	clk      *clock.FakeClock
	srv      *protoadmin.Server
	resender *fakeResender
	client   *http.Client
	baseURL  string
}

func newResendHarness(t *testing.T, cooldown time.Duration, dailyCap int, withResender bool) *resendHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs, Clock: clk,
		Listeners: []testharness.ListenerSpec{{Name: "admin", Protocol: "http"}},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 1000,
	})
	r := &fakeResender{}
	if withResender {
		srv.SetVerificationResender(r, cooldown, dailyCap)
	}
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &resendHarness{
		t: t, fs: fs, clk: clk, srv: srv, resender: r,
		client: client, baseURL: base,
	}
}

// post issues a JSON POST with the given bearer key. Returns the
// response and the body bytes.
func (h *resendHarness) post(path, key string) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.baseURL+path, nil)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := res.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return res, buf
}

func (h *resendHarness) bootstrap(email string) string {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.baseURL+"/api/v1/bootstrap",
		strings.NewReader(`{"email":"`+email+`","display_name":"Admin"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("bootstrap: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("bootstrap status %d", res.StatusCode)
	}
	var out struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out.InitialAPIKey
}

func (h *resendHarness) createUser(adminKey, email string) (uint64, string) {
	h.t.Helper()
	// Create the principal.
	body := strings.NewReader(`{"email":"` + email + `","password":"correct-horse-battery-staple"}`)
	req, _ := http.NewRequest(http.MethodPost, h.baseURL+"/api/v1/principals", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminKey)
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("createUser: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("createUser status %d", res.StatusCode)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		h.t.Fatalf("decode principal: %v", err)
	}
	// Mint an API key for the new principal.
	keyReq, _ := http.NewRequest(http.MethodPost,
		h.baseURL+"/api/v1/principals/"+strconv.FormatUint(p.ID, 10)+"/api-keys",
		strings.NewReader(`{"label":"test-key","scope":["end-user"]}`))
	keyReq.Header.Set("Content-Type", "application/json")
	keyReq.Header.Set("Authorization", "Bearer "+adminKey)
	keyRes, err := h.client.Do(keyReq)
	if err != nil {
		h.t.Fatalf("create api-key: %v", err)
	}
	defer keyRes.Body.Close()
	if keyRes.StatusCode != http.StatusCreated {
		h.t.Fatalf("create api-key status %d", keyRes.StatusCode)
	}
	var k struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(keyRes.Body).Decode(&k); err != nil {
		h.t.Fatalf("api-key decode: %v", err)
	}
	return p.ID, k.Key
}

// seedIdentity inserts an unverified Identity owned by pid and returns
// its id.
func (h *resendHarness) seedIdentity(pid store.PrincipalID, idSuffix, email string) string {
	h.t.Helper()
	id := "iv-" + idSuffix
	if err := h.fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID: id, PrincipalID: pid, Email: email, MayDelete: true,
	}); err != nil {
		h.t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	return id
}

func TestVerifyRequest_HappyPath_204(t *testing.T) {
	h := newResendHarness(t, 60*time.Second, 5, true)
	adminKey := h.bootstrap("admin@example.local")
	uid, userKey := h.createUser(adminKey, "alice@example.local")
	id := h.seedIdentity(store.PrincipalID(uid), "happy", "alice@external.test")

	res, body := h.post("/api/v1/identities/"+id+"/verify-request", userKey)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", res.StatusCode, body)
	}
	if len(h.resender.calls) != 1 {
		t.Fatalf("resender calls: got %d, want 1", len(h.resender.calls))
	}
	if h.resender.calls[0].ID != id {
		t.Fatalf("resender saw id %q, want %q", h.resender.calls[0].ID, id)
	}
}

func TestVerifyRequest_RateLimited_429_RetryAfter(t *testing.T) {
	h := newResendHarness(t, 60*time.Second, 5, true)
	adminKey := h.bootstrap("admin@example.local")
	uid, userKey := h.createUser(adminKey, "bob@example.local")
	id := h.seedIdentity(store.PrincipalID(uid), "rate", "bob@external.test")

	// Seed the bookkeeping so the cooldown gate would fire on a real
	// rotate. The fake resender returns ErrRateLimited unconditionally;
	// the handler reads the stats to compute Retry-After.
	exp := h.clk.Now().Add(24 * time.Hour).UnixMicro()
	if err := h.fs.Meta().IssueIdentityVerificationToken(context.Background(),
		id, vhash("rl-tok"), vhash("110000"), exp); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	h.resender.respErr = store.ErrRateLimited

	res, body := h.post("/api/v1/identities/"+id+"/verify-request", userKey)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", res.StatusCode, body)
	}
	ra := res.Header.Get("Retry-After")
	if ra == "" {
		t.Fatalf("missing Retry-After header")
	}
	secs, err := strconv.Atoi(ra)
	if err != nil || secs <= 0 || secs > 60 {
		t.Fatalf("Retry-After = %q (parsed %d), want positive <= 60", ra, secs)
	}
	if got := res.Header.Get("Content-Type"); !strings.Contains(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("problem body unmarshal: %v body=%s", err, body)
	}
	if doc["status"] != float64(http.StatusTooManyRequests) {
		t.Fatalf("problem status = %v", doc["status"])
	}
	if doc["limit_kind"] != "cooldown" {
		t.Fatalf("limit_kind = %v, want cooldown", doc["limit_kind"])
	}
	if doc["retry_after_seconds"] == nil {
		t.Fatalf("retry_after_seconds missing: %v", doc)
	}

	// Audit log: a failure entry under identity.verify.failure with
	// result_class = rate_limited.
	entries, err := h.fs.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "identity.verify.failure" && e.Metadata["result_class"] == "rate_limited" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no rate_limited audit entry: %+v", entries)
	}
}

func TestVerifyRequest_ResenderUnwired_503(t *testing.T) {
	h := newResendHarness(t, 60*time.Second, 5, false /* no resender */)
	adminKey := h.bootstrap("admin@example.local")
	uid, userKey := h.createUser(adminKey, "carol@example.local")
	id := h.seedIdentity(store.PrincipalID(uid), "unwired", "carol@external.test")

	res, _ := h.post("/api/v1/identities/"+id+"/verify-request", userKey)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
}

func TestVerifyRequest_MissingIdentity_404(t *testing.T) {
	h := newResendHarness(t, 60*time.Second, 5, true)
	adminKey := h.bootstrap("admin@example.local")
	_, userKey := h.createUser(adminKey, "dave@example.local")

	res, _ := h.post("/api/v1/identities/iv-missing/verify-request", userKey)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestVerifyRequest_CrossPrincipal_403(t *testing.T) {
	h := newResendHarness(t, 60*time.Second, 5, true)
	adminKey := h.bootstrap("admin@example.local")
	uid1, _ := h.createUser(adminKey, "owner@example.local")
	_, intruderKey := h.createUser(adminKey, "intruder@example.local")
	id := h.seedIdentity(store.PrincipalID(uid1), "cross", "owner@external.test")

	res, _ := h.post("/api/v1/identities/"+id+"/verify-request", intruderKey)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
}

func TestVerifyRequest_AlreadyVerified_409(t *testing.T) {
	h := newResendHarness(t, 60*time.Second, 5, true)
	adminKey := h.bootstrap("admin@example.local")
	uid, userKey := h.createUser(adminKey, "eve@example.local")
	id := h.seedIdentity(store.PrincipalID(uid), "verified", "eve@external.test")
	if err := h.fs.Meta().MarkIdentityVerified(context.Background(), id); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}

	res, _ := h.post("/api/v1/identities/"+id+"/verify-request", userKey)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if len(h.resender.calls) != 0 {
		t.Fatalf("resender invoked on verified row: %+v", h.resender.calls)
	}
}
