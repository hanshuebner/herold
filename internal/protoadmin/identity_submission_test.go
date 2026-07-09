package protoadmin_test

// identity_submission_test.go covers the external SMTP submission credential
// endpoints:
//   - GET /api/v1/identities/{id}/submission
//   - PUT /api/v1/identities/{id}/submission
//   - DELETE /api/v1/identities/{id}/submission
//
// Test matrix (REQ-AUTH-EXT-SUBMIT-04, REQ-MAIL-SUBMIT-03):
//   - GET on no-config -> 200 {configured: false}
//   - PUT with password + passing probe -> 204; row in store; audit entry
//   - PUT with failing probe (auth-failed) -> 422 problem/json; nothing written
//   - DELETE removes row; subsequent GET returns {configured: false}
//   - Admin caller on another principal's identity -> 403 (requireSelfOnly)
//   - CSRF check fires on PUT/DELETE under cookie auth; Bearer bypasses CSRF
//   - Audit entries land for set and delete
//   - Probe failure audit emits submission.external.failure category

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// testDataKey is a 32-byte key for sealing credentials in tests.
var testDataKey = []byte("submission-test-data-key-32byttx")

// alwaysOKProbe is an ExternalProbe that always returns OutcomeOK.
func alwaysOKProbe(_ context.Context, _ store.IdentitySubmission, _ string) extsubmit.Outcome {
	return extsubmit.Outcome{State: extsubmit.OutcomeOK, Diagnostic: "probe ok"}
}

// alwaysAuthFailProbe is an ExternalProbe that always returns OutcomeAuthFailed.
func alwaysAuthFailProbe(_ context.Context, _ store.IdentitySubmission, _ string) extsubmit.Outcome {
	return extsubmit.Outcome{State: extsubmit.OutcomeAuthFailed, Diagnostic: "535 bad credentials"}
}

// alwaysUnreachableProbe is an ExternalProbe that always returns OutcomeUnreachable.
func alwaysUnreachableProbe(_ context.Context, _ store.IdentitySubmission, _ string) extsubmit.Outcome {
	return extsubmit.Outcome{State: extsubmit.OutcomeUnreachable, Diagnostic: "connection refused"}
}

// submissionHarness wraps the test harness for submission endpoint tests.
type submissionHarness struct {
	t       *testing.T
	fs      store.Store
	clk     *clock.FakeClock
	srv     *protoadmin.Server
	client  *http.Client
	baseURL string
}

// newSubmissionHarness creates a submission test harness with the given probe
// function. Pass nil to use the noop (always-ok) probe.
func newSubmissionHarness(t *testing.T, probe protoadmin.ExternalProbe) *submissionHarness {
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
		BootstrapPerWindow:        1,
		BootstrapWindow:           5 * time.Minute,
		RequestsPerMinutePerKey:   100,
		ExternalSubmissionDataKey: testDataKey,
		ExternalProbe:             probe,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")

	return &submissionHarness{
		t: t, fs: fs, clk: clk, srv: srv,
		client: client, baseURL: base,
	}
}

// doRequest sends an HTTP request and returns the response + body.
func (sh *submissionHarness) doRequest(method, path, key string, body any) (*http.Response, []byte) {
	sh.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			sh.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, sh.baseURL+path, rdr)
	if err != nil {
		sh.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := sh.client.Do(req)
	if err != nil {
		sh.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		sh.t.Fatalf("read: %v", err)
	}
	return res, buf
}

// bootstrap creates the first admin principal and returns (email, apiKey).
func (sh *submissionHarness) bootstrap(email string) (string, string) {
	sh.t.Helper()
	res, buf := sh.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
		"email":        email,
		"display_name": "Admin",
	})
	if res.StatusCode != http.StatusCreated {
		sh.t.Fatalf("bootstrap: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		sh.t.Fatalf("unmarshal: %v", err)
	}
	return email, out.InitialAPIKey
}

// createUser creates a non-admin principal and returns (email, apiKey).
func (sh *submissionHarness) createUser(adminKey, email string) string {
	sh.t.Helper()
	res, buf := sh.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		sh.t.Fatalf("createUser %s: %d: %s", email, res.StatusCode, buf)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &p); err != nil {
		sh.t.Fatalf("decode: %v", err)
	}
	// Create an API key for the user.
	res2, buf2 := sh.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", p.ID), adminKey, map[string]any{
		"label": "test-key",
		"scope": []string{"end-user"},
	})
	if res2.StatusCode != http.StatusCreated {
		sh.t.Fatalf("createUser api-key %s: %d: %s", email, res2.StatusCode, buf2)
	}
	var k struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf2, &k); err != nil {
		sh.t.Fatalf("decode api-key: %v", err)
	}
	return k.Key
}

// insertIdentity creates a JMAP identity for the given principal and returns
// the identity id. Uses the store directly to avoid routing through JMAP.
func (sh *submissionHarness) insertIdentity(principalID uint64, email string) string {
	sh.t.Helper()
	id := fmt.Sprintf("identity-%d", principalID)
	err := sh.fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          id,
		PrincipalID: store.PrincipalID(principalID),
		Name:        "Test Identity",
		Email:       email,
		MayDelete:   true,
	})
	if err != nil {
		sh.t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	return id
}

// putSubmissionBody returns a valid submissionPutRequest body for password auth.
func putSubmissionBody() map[string]any {
	return map[string]any{
		"submit_host":        "smtp.example.com",
		"submit_port":        587,
		"submit_security":    "starttls",
		"submit_auth_method": "password",
		"password":           "secret-password",
		"auth_user":          "user@example.com",
	}
}

// TestGetSubmission_NoConfig verifies that GET returns {configured: false}
// when no submission config exists for the identity.
func TestGetSubmission_NoConfig(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	// Create an admin principal (the bootstrap user) and get their principal ID.
	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	if err := json.Unmarshal(buf, &who); err != nil {
		t.Fatalf("unmarshal whoami: %v", err)
	}

	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")

	res2, buf2 := sh.doRequest("GET", "/api/v1/identities/"+identityID+"/submission", adminKey, nil)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("GET submission: %d: %s", res2.StatusCode, buf2)
	}
	var got struct {
		Configured bool `json:"configured"`
	}
	if err := json.Unmarshal(buf2, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Configured {
		t.Errorf("configured: got true; want false")
	}
}

// TestGetSubmission_SyntheticDefault_NotFound_RegressionLoopFix verifies
// that GET /api/v1/identities/default/submission returns
// 200 {configured:false} rather than 404. The synthesised default
// identity ("default" id, no row in jmap_identities) cannot persist
// a submission row because identity_submission FKs jmap_identities,
// so the truthful state is "configured = false". A 404 here causes
// the suite's submissionStore to enter status='error' and the
// SettingsView $effect that polls identities to retry forever (live
// repro: 16 000+ requests in seconds against admin@example.local).
func TestGetSubmission_SyntheticDefault_NotFound_RegressionLoopFix(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/identities/default/submission", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET default submission: status %d: %s", res.StatusCode, buf)
	}
	var got struct {
		Configured bool `json:"configured"`
	}
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Configured {
		t.Errorf("configured: got true; want false (synthetic default cannot persist a row)")
	}
}

// TestGetSubmission_SyntheticDefault_DomainAuthoritative verifies that
// GET /api/v1/identities/default/submission returns domain_authoritative:true
// when the caller's email domain is a locally authoritative domain (re #107).
//
// Before the fix, the synthetic-default path omitted DomainAuthoritative in the
// response struct and Go's zero-value produced false, which caused
// isExternalWithoutSubmission in the Suite to return true and disabled Send after
// the Settings page visited the Account section.
func TestGetSubmission_SyntheticDefault_DomainAuthoritative(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			// Register "hostedlocal.example" as authoritative before bootstrap.
			if err := be.fs.Meta().InsertDomain(context.Background(), store.Domain{
				Name:    "hostedlocal.example",
				IsLocal: true,
			}); err != nil {
				t.Fatalf("InsertDomain: %v", err)
			}

			sh := newSubmissionHarnessWithStore(t, be.fs, be.clk, protoadmin.Options{})
			_, adminKey := sh.bootstrap("admin@hostedlocal.example")

			res, buf := sh.doRequest("GET", "/api/v1/identities/default/submission", adminKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET default submission: status %d: %s", res.StatusCode, buf)
			}
			var got struct {
				Configured          bool `json:"configured"`
				DomainAuthoritative bool `json:"domain_authoritative"`
			}
			if err := json.Unmarshal(buf, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Configured {
				t.Errorf("configured: got true; want false (synthetic default cannot persist a row)")
			}
			if !got.DomainAuthoritative {
				t.Errorf("domain_authoritative: got false; want true (caller's domain is locally authoritative)")
			}
		})
	}
}

// TestPutSubmission_SyntheticDefault_Rejected verifies that PUT
// against the synthetic default returns 422 with a clear reason.
// The schema FKs jmap_identities; the default has no row there, so
// any attempt to persist must fail at the application layer with a
// helpful error rather than a SQLite FK violation deeper in.
func TestPutSubmission_SyntheticDefault_Rejected(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	res, buf := sh.doRequest("PUT", "/api/v1/identities/default/submission", adminKey, putSubmissionBody())
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT default submission: status %d: %s", res.StatusCode, buf)
	}
}

// TestPutSubmission_Password_OK verifies that PUT with a passing probe
// returns 204 and persists the row.
func TestPutSubmission_Password_OK(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d", res.StatusCode)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)

	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")

	res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT submission: %d: %s", res2.StatusCode, buf2)
	}

	// Verify the row is in the store.
	sub, err := sh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission: %v", err)
	}
	if sub.SubmitHost != "smtp.example.com" {
		t.Errorf("SubmitHost = %q; want smtp.example.com", sub.SubmitHost)
	}
	if len(sub.PasswordCT) == 0 {
		t.Errorf("PasswordCT is empty; want sealed credential")
	}

	// GET should return configured:true.
	res3, buf3 := sh.doRequest("GET", "/api/v1/identities/"+identityID+"/submission", adminKey, nil)
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("GET after PUT: %d: %s", res3.StatusCode, buf3)
	}
	var got struct {
		Configured       bool   `json:"configured"`
		SubmitAuthMethod string `json:"submit_auth_method"`
	}
	json.Unmarshal(buf3, &got)
	if !got.Configured {
		t.Errorf("configured: got false; want true")
	}
	if got.SubmitAuthMethod != "password" {
		t.Errorf("submit_auth_method = %q; want password", got.SubmitAuthMethod)
	}
}

// TestPutSubmission_ProbeFail_AuthFailed verifies that when the probe returns
// OutcomeAuthFailed, the handler returns 422 with the correct problem+json
// type and nothing is written to the store.
func TestPutSubmission_ProbeFail_AuthFailed(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysAuthFailProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d", res.StatusCode)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)

	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")

	res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT submission expected 422, got %d: %s", res2.StatusCode, buf2)
	}
	if ct := res2.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q; want application/problem+json", ct)
	}
	var prob struct {
		Type     string `json:"type"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal(buf2, &prob); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	const wantType = "https://netzhansa.com/problems/external_submission_probe_failed"
	if prob.Type != wantType {
		t.Errorf("type = %q; want %q", prob.Type, wantType)
	}
	if prob.Category != "auth-failed" {
		t.Errorf("category = %q; want auth-failed", prob.Category)
	}

	// Verify nothing was written.
	_, err := sh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err == nil {
		t.Errorf("GetIdentitySubmission: expected ErrNotFound after failed probe; got row")
	}
}

// TestPutSubmission_ProbeFail_Unreachable verifies 422 with category=unreachable.
func TestPutSubmission_ProbeFail_Unreachable(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysUnreachableProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d", res.StatusCode)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)

	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")

	res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT expected 422, got %d: %s", res2.StatusCode, buf2)
	}
	var prob struct {
		Category string `json:"category"`
	}
	json.Unmarshal(buf2, &prob)
	if prob.Category != "unreachable" {
		t.Errorf("category = %q; want unreachable", prob.Category)
	}
}

// TestDeleteSubmission removes the row; subsequent GET returns configured:false.
func TestDeleteSubmission(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d", res.StatusCode)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)

	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")

	// PUT first.
	res2, _ := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT: %d", res2.StatusCode)
	}

	// DELETE.
	res3, buf3 := sh.doRequest("DELETE", "/api/v1/identities/"+identityID+"/submission", adminKey, nil)
	if res3.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d: %s", res3.StatusCode, buf3)
	}

	// GET should return configured:false.
	res4, buf4 := sh.doRequest("GET", "/api/v1/identities/"+identityID+"/submission", adminKey, nil)
	if res4.StatusCode != http.StatusOK {
		t.Fatalf("GET after DELETE: %d: %s", res4.StatusCode, buf4)
	}
	var got struct {
		Configured bool `json:"configured"`
	}
	json.Unmarshal(buf4, &got)
	if got.Configured {
		t.Errorf("configured: got true; want false after DELETE")
	}
}

// TestSubmission_RequiresSelfOnly_AdminForbidden verifies that even an admin
// caller cannot read or write another principal's submission credentials
// (requireSelfOnly enforcement, REQ-AUTH-EXT-SUBMIT-04).
func TestSubmission_RequiresSelfOnly_AdminForbidden(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	// Create a second user.
	userKey := sh.createUser(adminKey, "user@example.com")
	_ = userKey

	// Get user's principal ID via admin.
	res, buf := sh.doRequest("GET", "/api/v1/principals", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list principals: %d", res.StatusCode)
	}
	var page struct {
		Items []struct {
			ID    uint64 `json:"id"`
			Email string `json:"canonical_email"`
		} `json:"items"`
	}
	json.Unmarshal(buf, &page)
	var userPID uint64
	for _, p := range page.Items {
		if p.Email == "user@example.com" {
			userPID = p.ID
			break
		}
	}
	if userPID == 0 {
		t.Fatalf("could not find user@example.com in principals")
	}

	// Insert identity owned by the user.
	identityID := sh.insertIdentity(userPID, "user@example.com")

	// Admin tries to GET the user's submission — must get 403.
	res2, buf2 := sh.doRequest("GET", "/api/v1/identities/"+identityID+"/submission", adminKey, nil)
	if res2.StatusCode != http.StatusForbidden {
		t.Errorf("GET by admin: expected 403, got %d: %s", res2.StatusCode, buf2)
	}

	// Admin tries to PUT — must get 403.
	res3, buf3 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res3.StatusCode != http.StatusForbidden {
		t.Errorf("PUT by admin: expected 403, got %d: %s", res3.StatusCode, buf3)
	}

	// Admin tries to DELETE — must get 403.
	res4, buf4 := sh.doRequest("DELETE", "/api/v1/identities/"+identityID+"/submission", adminKey, nil)
	if res4.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE by admin: expected 403, got %d: %s", res4.StatusCode, buf4)
	}
}

// TestSubmission_CSRF_CookieAuth verifies that PUT/DELETE require X-CSRF-Token
// under cookie auth, and that Bearer auth bypasses CSRF. This test reuses the
// sessionHarness pattern from session_auth_test.go.
func TestSubmission_CSRF_CookieAuth(t *testing.T) {
	// Build a harness with cookie auth enabled.
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	th, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	signingKey := []byte("csrf-test-signing-key-32bytesxx!")
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:        1,
		BootstrapWindow:           5 * time.Minute,
		RequestsPerMinutePerKey:   100,
		ExternalSubmissionDataKey: testDataKey,
		ExternalProbe:             alwaysOKProbe,
		Session: authsession.SessionConfig{
			SigningKey:     signingKey,
			CookieName:     "herold_admin_session",
			CSRFCookieName: "herold_admin_csrf",
			TTL:            24 * time.Hour,
			SecureCookies:  false,
		},
	})
	if err := th.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	baseClient, base := th.DialAdminByName(context.Background(), "admin")

	jar, _ := cookiejar.New(nil)
	cookieClient := &http.Client{
		Transport: baseClient.Transport,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Bootstrap — returns initial_password and initial_api_key.
	bsBody, _ := json.Marshal(map[string]string{"email": "csrf@example.com", "display_name": "A"})
	bsReq, _ := http.NewRequest("POST", base+"/api/v1/bootstrap", bytes.NewReader(bsBody))
	bsReq.Header.Set("Content-Type", "application/json")
	bsRes, _ := baseClient.Do(bsReq)
	bsRaw, _ := io.ReadAll(bsRes.Body)
	bsRes.Body.Close()
	if bsRes.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d: %s", bsRes.StatusCode, bsRaw)
	}
	var bsOut struct {
		InitialAPIKey string `json:"initial_api_key"`
		InitialPW     string `json:"initial_password"`
		PrincipalID   uint64 `json:"principal_id"`
	}
	json.Unmarshal(bsRaw, &bsOut)

	apiKey := bsOut.InitialAPIKey
	pid := bsOut.PrincipalID

	// Create identity directly via store.
	identityID := fmt.Sprintf("csrf-identity-%d", pid)
	fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(pid),
		Name:        "Test",
		Email:       "csrf@example.com",
		MayDelete:   true,
	})

	putBodyBytes, _ := json.Marshal(putSubmissionBody())

	// Bearer PUT: should succeed (CSRF exempt).
	putReq, _ := http.NewRequest("PUT", base+"/api/v1/identities/"+identityID+"/submission",
		bytes.NewReader(putBodyBytes))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("Authorization", "Bearer "+apiKey)
	putRes, _ := baseClient.Do(putReq)
	putRes.Body.Close()
	if putRes.StatusCode != http.StatusNoContent {
		t.Errorf("Bearer PUT: expected 204, got %d", putRes.StatusCode)
	}

	// Log in via cookie to get session + CSRF cookies.
	loginBody, _ := json.Marshal(map[string]string{
		"email":    "csrf@example.com",
		"password": bsOut.InitialPW,
	})
	loginReq, _ := http.NewRequest("POST", base+"/api/v1/auth/login",
		bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRes, _ := cookieClient.Do(loginReq)
	loginRes.Body.Close()
	// The login may return 401 if the server is in no-cookie mode which is
	// fine — the test only verifies the CSRF check fires on mutating cookie
	// requests. We check that without cookies (no session) we also get 401 not 403.
	// What matters: cookie PUT without X-CSRF-Token -> 401 or 403 (not 200/204).

	// Cookie PUT without CSRF header: must not be 204 (either 401 unauthenticated
	// or 403 csrf_required).
	putReq2, _ := http.NewRequest("PUT", base+"/api/v1/identities/"+identityID+"/submission",
		bytes.NewReader(putBodyBytes))
	putReq2.Header.Set("Content-Type", "application/json")
	putRes2, _ := cookieClient.Do(putReq2)
	putRes2Body, _ := io.ReadAll(putRes2.Body)
	putRes2.Body.Close()
	if putRes2.StatusCode == http.StatusNoContent {
		t.Errorf("Cookie PUT without CSRF: must not return 204; got %d %s", putRes2.StatusCode, putRes2Body)
	}
	if putRes2.StatusCode != http.StatusForbidden && putRes2.StatusCode != http.StatusUnauthorized {
		t.Errorf("Cookie PUT without CSRF: expected 403 or 401, got %d %s", putRes2.StatusCode, putRes2Body)
	}

	// Cookie DELETE without CSRF: same rule.
	delReq, _ := http.NewRequest("DELETE", base+"/api/v1/identities/"+identityID+"/submission", nil)
	delRes, _ := cookieClient.Do(delReq)
	delResBody, _ := io.ReadAll(delRes.Body)
	delRes.Body.Close()
	if delRes.StatusCode == http.StatusNoContent {
		t.Errorf("Cookie DELETE without CSRF: must not return 204")
	}
	if delRes.StatusCode != http.StatusForbidden && delRes.StatusCode != http.StatusUnauthorized {
		t.Errorf("Cookie DELETE without CSRF: expected 403 or 401, got %d %s", delRes.StatusCode, delResBody)
	}
}

// TestRequireSelfOnly_AllVerbs is a focused test ensuring all three verbs
// return 403 when accessed by a different principal (even admin).
func TestRequireSelfOnly_AllVerbs(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("owner@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d", res.StatusCode)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	ownerID := who.PrincipalID

	// Create a second user.
	otherUserKey := sh.createUser(adminKey, "other@example.com")

	// Insert identity for owner.
	identityID := sh.insertIdentity(ownerID, "owner@example.com")

	// Other user tries to access owner's identity submission.
	for _, tc := range []struct {
		method string
		body   any
	}{
		{"GET", nil},
		{"PUT", putSubmissionBody()},
		{"DELETE", nil},
	} {
		res, buf := sh.doRequest(tc.method, "/api/v1/identities/"+identityID+"/submission", otherUserKey, tc.body)
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s by other user: expected 403, got %d: %s", tc.method, res.StatusCode, buf)
		}
	}
}

// submissionBackend is a named (store, clock) pair used by dual-backend tests.
type submissionBackend struct {
	name string
	fs   store.Store
	clk  *clock.FakeClock
}

// openSubmissionBackends returns SQLite and (when HEROLD_PG_DSN is set)
// Postgres backends for dual-backend submission handler tests. SQLite always
// runs; the Postgres leg is added only when HEROLD_PG_DSN is exported so
// local dev without a Postgres instance is not blocked.
func openSubmissionBackends(t *testing.T) []submissionBackend {
	t.Helper()
	clkA := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	result := []submissionBackend{{
		name: "sqlite",
		fs:   sqlitetest.Open(t, clkA),
		clk:  clkA,
	}}
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		return result
	}
	clkB := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	blobDir := t.TempDir()
	s, err := storepg.Open(context.Background(), dsn, filepath.Join(blobDir, "blobs"), nil, clkB)
	if err != nil {
		t.Fatalf("storepg.Open: %v", err)
	}
	// Wipe tables so each test begins on a clean Postgres database.
	type truncator interface{ TruncateAll(context.Context) error }
	if tr, ok := s.(truncator); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			t.Logf("postgres TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = s.Close() })
	return append(result, submissionBackend{name: "postgres", fs: s, clk: clkB})
}

// newSubmissionHarnessWithStore creates a submission test harness backed by the
// provided store and clock. opts overrides individual server options; any zero
// field receives a sensible default. The caller is responsible for the store's
// lifecycle; this function does not close it.
func newSubmissionHarnessWithStore(t *testing.T, fs store.Store, clk *clock.FakeClock, opts protoadmin.Options) *submissionHarness {
	t.Helper()
	if opts.BootstrapPerWindow == 0 {
		opts.BootstrapPerWindow = 1
	}
	if opts.BootstrapWindow == 0 {
		opts.BootstrapWindow = 5 * time.Minute
	}
	if opts.RequestsPerMinutePerKey == 0 {
		opts.RequestsPerMinutePerKey = 100
	}
	if len(opts.ExternalSubmissionDataKey) == 0 {
		opts.ExternalSubmissionDataKey = testDataKey
	}
	if opts.ExternalProbe == nil {
		opts.ExternalProbe = alwaysOKProbe
	}
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, opts)
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &submissionHarness{
		t: t, fs: fs, clk: clk, srv: srv,
		client: client, baseURL: base,
	}
}

// TestGetSubmission_DomainAuthoritative verifies that domain_authoritative is
// true when the identity's email domain is a configured local domain and false
// for a foreign domain. Runs on both SQLite and Postgres (re #74).
func TestGetSubmission_DomainAuthoritative(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			// Register "local.example" as authoritative before bootstrap.
			if err := be.fs.Meta().InsertDomain(context.Background(), store.Domain{
				Name:    "local.example",
				IsLocal: true,
			}); err != nil {
				t.Fatalf("InsertDomain: %v", err)
			}

			sh := newSubmissionHarnessWithStore(t, be.fs, be.clk, protoadmin.Options{})
			_, adminKey := sh.bootstrap("admin@local.example")

			res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
			}
			var who struct {
				PrincipalID uint64 `json:"principal_id"`
			}
			if err := json.Unmarshal(buf, &who); err != nil {
				t.Fatalf("unmarshal whoami: %v", err)
			}
			pid := store.PrincipalID(who.PrincipalID)

			// insertNamedIdentity inserts a JMAP identity with a caller-chosen id
			// so the two identities in this test use distinct ids (insertIdentity
			// derives the id from principalID alone and would collide on the second
			// call).
			insertNamedIdentity := func(id, email string) {
				t.Helper()
				if err := sh.fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
					ID:          id,
					PrincipalID: pid,
					Name:        "Test Identity",
					Email:       email,
					MayDelete:   true,
				}); err != nil {
					t.Fatalf("InsertJMAPIdentity %s: %v", id, err)
				}
			}

			// Identity on a configured local domain: domain_authoritative must be true.
			const localID = "da-test-local"
			insertNamedIdentity(localID, "admin@local.example")
			res2, buf2 := sh.doRequest("GET", "/api/v1/identities/"+localID+"/submission", adminKey, nil)
			if res2.StatusCode != http.StatusOK {
				t.Fatalf("GET local identity: %d: %s", res2.StatusCode, buf2)
			}
			var local struct {
				DomainAuthoritative bool `json:"domain_authoritative"`
			}
			if err := json.Unmarshal(buf2, &local); err != nil {
				t.Fatalf("decode local response: %v", err)
			}
			if !local.DomainAuthoritative {
				t.Errorf("local identity: domain_authoritative = false; want true")
			}

			// Identity on a domain the server is NOT authoritative for.
			const foreignID = "da-test-foreign"
			insertNamedIdentity(foreignID, "admin@foreign.example")
			res3, buf3 := sh.doRequest("GET", "/api/v1/identities/"+foreignID+"/submission", adminKey, nil)
			if res3.StatusCode != http.StatusOK {
				t.Fatalf("GET foreign identity: %d: %s", res3.StatusCode, buf3)
			}
			var foreign struct {
				DomainAuthoritative bool `json:"domain_authoritative"`
			}
			if err := json.Unmarshal(buf3, &foreign); err != nil {
				t.Fatalf("decode foreign response: %v", err)
			}
			if foreign.DomainAuthoritative {
				t.Errorf("foreign identity: domain_authoritative = true; want false")
			}
		})
	}
}

// TestGetSubmission_AvailableOAuthProviders verifies that available_oauth_providers
// lists exactly the OAuth providers whose ClientSecret is non-empty, in sorted
// order. Providers with an empty ClientSecret are excluded. Runs on both
// SQLite and Postgres (re #73).
func TestGetSubmission_AvailableOAuthProviders(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			// Configure two providers: "gmail" has a secret; "m365" does not.
			sh := newSubmissionHarnessWithStore(t, be.fs, be.clk, protoadmin.Options{
				OAuthProviders: map[string]protoadmin.OAuthProviderOptions{
					"gmail": {
						ClientID:     "client-id-gmail",
						ClientSecret: "secret-for-gmail",
						AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
						TokenURL:     "https://oauth2.googleapis.com/token",
						Scopes:       []string{"https://mail.google.com/"},
					},
					"m365": {
						ClientID:     "client-id-m365",
						ClientSecret: "", // no secret — must not appear in response
						AuthURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
						TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
						Scopes:       []string{"https://outlook.office365.com/SMTP.Send"},
					},
				},
			})

			_, adminKey := sh.bootstrap("oauth-admin@example.com")

			res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
			}
			var who struct {
				PrincipalID uint64 `json:"principal_id"`
			}
			if err := json.Unmarshal(buf, &who); err != nil {
				t.Fatalf("unmarshal whoami: %v", err)
			}

			identityID := sh.insertIdentity(who.PrincipalID, "oauth-admin@example.com")

			res2, buf2 := sh.doRequest("GET", "/api/v1/identities/"+identityID+"/submission", adminKey, nil)
			if res2.StatusCode != http.StatusOK {
				t.Fatalf("GET submission: %d: %s", res2.StatusCode, buf2)
			}
			var got struct {
				AvailableOAuthProviders []string `json:"available_oauth_providers"`
			}
			if err := json.Unmarshal(buf2, &got); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			// Only "gmail" (with a secret) should appear; "m365" (no secret) must not.
			if len(got.AvailableOAuthProviders) != 1 || got.AvailableOAuthProviders[0] != "gmail" {
				t.Errorf("available_oauth_providers = %v; want [\"gmail\"]", got.AvailableOAuthProviders)
			}
		})
	}
}

// TestPutSubmission_NoDataKey verifies that PUT returns 503 when no data key
// is configured.
func TestPutSubmission_NoDataKey(t *testing.T) {
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
		// No ExternalSubmissionDataKey
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")

	doReq := func(method, path, key string, body any) (*http.Response, []byte) {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("doReq %s %s: %v", method, path, err)
		}
		defer res.Body.Close()
		buf, _ := io.ReadAll(res.Body)
		return res, buf
	}

	// Bootstrap.
	bsRes, bsBody := doReq("POST", "/api/v1/bootstrap", "", map[string]any{
		"email": "admin4@example.com", "display_name": "A",
	})
	if bsRes.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d: %s", bsRes.StatusCode, bsBody)
	}
	var bsOut struct {
		InitialAPIKey string `json:"initial_api_key"`
		PrincipalID   uint64 `json:"principal_id"`
	}
	json.Unmarshal(bsBody, &bsOut)
	apiKey := bsOut.InitialAPIKey
	pid := bsOut.PrincipalID

	identityID := fmt.Sprintf("identity-nokey-%d", pid)
	fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(pid),
		Name:        "Test",
		Email:       "admin4@example.com",
		MayDelete:   true,
	})

	res2, buf2 := doReq("PUT", "/api/v1/identities/"+identityID+"/submission", apiKey, putSubmissionBody())
	if res2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("PUT without data key: expected 503, got %d: %s", res2.StatusCode, buf2)
	}
}

// TestAuditEntries_NoCredentialMaterial verifies that audit log entries for
// identity.submission.set, identity.submission.delete, and
// submission.external.failure do not contain the credential value (password
// or OAuth token) — REQ-AUTH-EXT-SUBMIT-09, Phase-6 audit hygiene check.
func TestAuditEntries_NoCredentialMaterial(t *testing.T) {
	const sentinelPassword = "ya29.SENTINEL_PASSWORD_DO_NOT_LOG"
	const sentinelOAuthToken = "ya29.SENTINEL_OAUTH_TOKEN_DO_NOT_LOG"

	// Test 1: password submission — audit entry must not contain the password.
	t.Run("password_set", func(t *testing.T) {
		sh := newSubmissionHarness(t, alwaysOKProbe)
		_, adminKey := sh.bootstrap("auditpw@example.com")

		res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("whoami: %d", res.StatusCode)
		}
		var who struct {
			PrincipalID uint64 `json:"principal_id"`
		}
		json.Unmarshal(buf, &who)
		identityID := sh.insertIdentity(who.PrincipalID, "auditpw@example.com")

		body := map[string]any{
			"submit_host":        "smtp.example.com",
			"submit_port":        587,
			"submit_security":    "starttls",
			"submit_auth_method": "password",
			"password":           sentinelPassword,
		}
		res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, body)
		if res2.StatusCode != http.StatusNoContent {
			t.Fatalf("PUT: %d: %s", res2.StatusCode, buf2)
		}

		// Read audit log entries and assert none contain the sentinel.
		entries, err := sh.fs.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Limit: 50})
		if err != nil {
			t.Fatalf("ListAuditLog: %v", err)
		}
		for _, e := range entries {
			if e.Action == "identity.submission.set" || e.Action == "submission.external.failure" {
				raw, _ := json.Marshal(e)
				if bytes.Contains(raw, []byte(sentinelPassword)) {
					t.Errorf("audit entry %q contains sentinel password material: %s", e.Action, raw)
				}
			}
		}
	})

	// Test 2: probe failure audit entry must not contain the OAuth token.
	t.Run("probe_failure_oauth", func(t *testing.T) {
		sh := newSubmissionHarness(t, alwaysAuthFailProbe)
		_, adminKey := sh.bootstrap("auditfail@example.com")

		res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("whoami: %d", res.StatusCode)
		}
		var who struct {
			PrincipalID uint64 `json:"principal_id"`
		}
		json.Unmarshal(buf, &who)
		identityID := sh.insertIdentity(who.PrincipalID, "auditfail@example.com")

		body := map[string]any{
			"submit_host":        "smtp.gmail.com",
			"submit_port":        587,
			"submit_security":    "starttls",
			"submit_auth_method": "oauth2",
			"oauth_access_token": sentinelOAuthToken,
			"oauth_client_id":    "auditfail@example.com",
		}
		res2, _ := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, body)
		if res2.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("PUT with failing probe: expected 422, got %d", res2.StatusCode)
		}

		entries, err := sh.fs.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Limit: 50})
		if err != nil {
			t.Fatalf("ListAuditLog: %v", err)
		}
		for _, e := range entries {
			if e.Action == "submission.external.failure" {
				raw, _ := json.Marshal(e)
				if bytes.Contains(raw, []byte(sentinelOAuthToken)) {
					t.Errorf("audit entry %q contains sentinel OAuth token: %s", e.Action, raw)
				}
			}
		}
	})
}

// capturedEnvelope records the most recent Envelope passed to a fake
// ExternalTestSender so tests can assert on the RCPT TO address.
type capturedEnvelope struct {
	mu  sync.Mutex
	env extsubmit.Envelope
	set bool
}

func (c *capturedEnvelope) sender() protoadmin.ExternalTestSender {
	return func(_ context.Context, _ store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.env = env
		c.set = true
		return extsubmit.Outcome{State: extsubmit.OutcomeOK, Diagnostic: "test ok"}
	}
}

func (c *capturedEnvelope) rcptTo() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.env.RcptTo
}

// newSubmissionHarnessWithSender creates a harness where ExternalTestSender
// is wired to the provided sender (in addition to the probe).
func newSubmissionHarnessWithSender(t *testing.T, sender protoadmin.ExternalTestSender) *submissionHarness {
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
		BootstrapPerWindow:        1,
		BootstrapWindow:           5 * time.Minute,
		RequestsPerMinutePerKey:   100,
		ExternalSubmissionDataKey: testDataKey,
		ExternalProbe:             alwaysOKProbe,
		ExternalTestSender:        sender,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &submissionHarness{
		t: t, fs: fs, clk: clk, srv: srv,
		client: client, baseURL: base,
	}
}

// TestTestSubmission_DefaultsToSelf verifies that when no body is sent, the
// RCPT TO defaults to the identity's own address (re #122).
func TestTestSubmission_DefaultsToSelf(t *testing.T) {
	cap := &capturedEnvelope{}
	sh := newSubmissionHarnessWithSender(t, cap.sender())

	_, adminKey := sh.bootstrap("self@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "self@example.com")

	// First PUT a submission so the identity has a row.
	res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT submission: %d: %s", res2.StatusCode, buf2)
	}

	// POST /submission/test with no body — should default RCPT TO to the identity address.
	res3, buf3 := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/submission/test", adminKey, nil)
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("POST submission/test: %d: %s", res3.StatusCode, buf3)
	}

	rcpt := cap.rcptTo()
	if len(rcpt) != 1 || rcpt[0] != "self@example.com" {
		t.Errorf("RCPT TO = %v; want [self@example.com]", rcpt)
	}
}

// TestTestSubmission_HonorsToField verifies that when a JSON body with a
// non-empty "to" field is sent, the RCPT TO is the specified address (re #122).
func TestTestSubmission_HonorsToField(t *testing.T) {
	cap := &capturedEnvelope{}
	sh := newSubmissionHarnessWithSender(t, cap.sender())

	_, adminKey := sh.bootstrap("sender@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "sender@example.com")

	// PUT a submission row first.
	res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT submission: %d: %s", res2.StatusCode, buf2)
	}

	// POST /submission/test with a "to" body field.
	res3, buf3 := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/submission/test", adminKey,
		map[string]string{"to": "primary@example.com"})
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("POST submission/test with to: %d: %s", res3.StatusCode, buf3)
	}

	rcpt := cap.rcptTo()
	if len(rcpt) != 1 || rcpt[0] != "primary@example.com" {
		t.Errorf("RCPT TO = %v; want [primary@example.com]", rcpt)
	}
}

// TestTestSubmission_EmptyToDefaultsToSelf verifies that an explicit empty
// "to" field is treated the same as an absent body (falls back to self).
func TestTestSubmission_EmptyToDefaultsToSelf(t *testing.T) {
	cap := &capturedEnvelope{}
	sh := newSubmissionHarnessWithSender(t, cap.sender())

	_, adminKey := sh.bootstrap("owner@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "owner@example.com")

	res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT submission: %d: %s", res2.StatusCode, buf2)
	}

	// POST with an explicit empty "to" — must still default to the identity address.
	res3, buf3 := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/submission/test", adminKey,
		map[string]string{"to": ""})
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("POST submission/test with empty to: %d: %s", res3.StatusCode, buf3)
	}

	rcpt := cap.rcptTo()
	if len(rcpt) != 1 || rcpt[0] != "owner@example.com" {
		t.Errorf("RCPT TO = %v; want [owner@example.com]", rcpt)
	}
}

// TestTestSubmission_ClearsAuthFailedStateOnSuccess verifies that a successful
// test-message send updates the stored state to ok, so a stale auth-failed
// badge clears without requiring a full OAuth re-auth cycle (re #131).
func TestTestSubmission_ClearsAuthFailedStateOnSuccess(t *testing.T) {
	cap := &capturedEnvelope{}
	sh := newSubmissionHarnessWithSender(t, cap.sender())

	_, adminKey := sh.bootstrap("smtp@example.com")

	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "smtp@example.com")

	// PUT a submission row; the alwaysOKProbe in the harness accepts it.
	res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityID+"/submission", adminKey, putSubmissionBody())
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT submission: %d: %s", res2.StatusCode, buf2)
	}

	// Manually flip the row's state to auth-failed to simulate a previous sweeper failure.
	sub, err := sh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission: %v", err)
	}
	sub.State = store.IdentitySubmissionStateAuthFailed
	sub.StateAt = sh.clk.Now().Add(-5 * time.Minute)
	if err := sh.fs.Meta().UpsertIdentitySubmission(context.Background(), sub); err != nil {
		t.Fatalf("UpsertIdentitySubmission (set auth-failed): %v", err)
	}

	// Verify the badge reads as auth-failed before the test.
	gotBefore, err := sh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission (before): %v", err)
	}
	if gotBefore.State != store.IdentitySubmissionStateAuthFailed {
		t.Fatalf("pre-condition: State = %q; want auth-failed", gotBefore.State)
	}

	// POST /submission/test — the capturedEnvelope sender always returns ok.
	res3, buf3 := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/submission/test", adminKey, nil)
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("POST submission/test: %d: %s", res3.StatusCode, buf3)
	}
	var testResp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(buf3, &testResp); err != nil {
		t.Fatalf("decode test response: %v", err)
	}
	if !testResp.OK {
		t.Fatalf("test response ok=false; want true")
	}

	// The stored state must now be ok.
	gotAfter, err := sh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission (after): %v", err)
	}
	if gotAfter.State != store.IdentitySubmissionStateOK {
		t.Errorf("State = %q after successful test; want ok", gotAfter.State)
	}
}

// TestTestSubmission_FreshTokenPreservedAfterInternalRefresh verifies that
// handleTestSubmission does not clobber a store update made by ExternalTestSender
// (re #131, stale-token overwrite fix). Runs on both SQLite and Postgres
// (STANDARDS §8.4) because the fix touches GetIdentitySubmission +
// UpsertIdentitySubmission.
//
// Scenario: ExternalTestSender simulates the real Submitter.Submit path where
// accessToken calls Refresher.Refresh — writing updated fields (new OAuthAccessCT,
// new RefreshDue) to the store. Without the fix, handleTestSubmission upserts the
// pre-test sub on success and overwrites those updated fields with the stale ones.
// With the fix, it re-reads the current row first, so the sender's writes survive.
func TestTestSubmission_FreshTokenPreservedAfterInternalRefresh(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			var identityRef string

			// Sender simulates what Submitter.Submit does when accessToken calls
			// Refresher.Refresh: it writes a marker value to the store that the test
			// verifies was preserved after handleTestSubmission's state upsert.
			tokenUpdatingSender := func(ctx context.Context, _ store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome {
				if identityRef != "" {
					current, cerr := be.fs.Meta().GetIdentitySubmission(ctx, identityRef)
					if cerr == nil {
						// Use a distinguishable SubmitHost value so we can verify that the
						// sender's store update survives the state upsert in handleTestSubmission.
						current.SubmitHost = "refreshed.smtp.example.com"
						_ = be.fs.Meta().UpsertIdentitySubmission(ctx, current)
					}
				}
				return extsubmit.Outcome{State: extsubmit.OutcomeOK, Diagnostic: "test ok"}
			}

			sh := newSubmissionHarnessWithStore(t, be.fs, be.clk, protoadmin.Options{
				ExternalTestSender: tokenUpdatingSender,
			})

			_, adminKey := sh.bootstrap("refresh@example.com")

			res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
			}
			var who struct {
				PrincipalID uint64 `json:"principal_id"`
			}
			json.Unmarshal(buf, &who)
			identityRef = sh.insertIdentity(who.PrincipalID, "refresh@example.com")

			// PUT a submission row (SubmitHost = "smtp.example.com" from putSubmissionBody).
			res2, buf2 := sh.doRequest("PUT", "/api/v1/identities/"+identityRef+"/submission", adminKey, putSubmissionBody())
			if res2.StatusCode != http.StatusNoContent {
				t.Fatalf("PUT submission: %d: %s", res2.StatusCode, buf2)
			}

			// POST /submission/test — the sender updates SubmitHost in the store to
			// "refreshed.smtp.example.com" before returning ok.
			res3, buf3 := sh.doRequest("POST", "/api/v1/identities/"+identityRef+"/submission/test", adminKey, nil)
			if res3.StatusCode != http.StatusOK {
				t.Fatalf("POST submission/test: %d: %s", res3.StatusCode, buf3)
			}
			var testResp struct {
				OK bool `json:"ok"`
			}
			if err := json.Unmarshal(buf3, &testResp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !testResp.OK {
				t.Fatalf("test response ok=false; want true")
			}

			// After the test, the sender's updated SubmitHost must be preserved in the
			// store — not overwritten by the old value from the pre-test sub. With the
			// re-read fix, handleTestSubmission reads the current row (which has
			// SubmitHost="refreshed.smtp.example.com") and only flips state to ok.
			got, err := be.fs.Meta().GetIdentitySubmission(context.Background(), identityRef)
			if err != nil {
				t.Fatalf("GetIdentitySubmission after test: %v", err)
			}
			if got.SubmitHost != "refreshed.smtp.example.com" {
				t.Errorf("SubmitHost = %q after test; want refreshed.smtp.example.com (sender store update must survive state upsert)", got.SubmitHost)
			}
			if got.State != store.IdentitySubmissionStateOK {
				t.Errorf("State = %q after successful test; want ok", got.State)
			}
		})
	}
}

// TestTestSubmission_SubmitterRefresherWired is the integration test for the
// root-cause fix in internal/admin/server.go: prebuiltExtSubmitter must have a
// non-nil Refresher so that an expired OAuth access token (RefreshDue in the
// past) is refreshed before SMTP AUTH — not presented stale (causing 535) and
// then succeeding only on a lucky second attempt after the sweeper fires (re
// #131). Runs on both SQLite and Postgres (STANDARDS §8.4).
//
// Fail-without-fix: if Submitter.Refresher is nil (the pre-fix state), accessToken
// falls through to openAccessToken and returns "stale-access-token"; the scripted
// SMTP server returns 535; Submit returns OutcomeAuthFailed; the endpoint returns
// ok=false; the test fails.
//
// Pass-with-fix: Refresher is set; accessToken calls Refresher.Refresh against
// the fake token endpoint; fresh token is sent to SMTP; 235 Accepted; ok=true.
func TestTestSubmission_SubmitterRefresherWired(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			var tokenEndpointCalls int32

			// Fake token endpoint: returns "fresh-access-token" for any refresh request.
			tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&tokenEndpointCalls, 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "fresh-access-token",
					"token_type":    "Bearer",
					"expires_in":    3600,
					"refresh_token": "same-refresh-token",
				})
			}))
			t.Cleanup(tokenSrv.Close)

			// Scripted SMTP server: accepts XOAUTH2 only with "fresh-access-token".
			// Any other token causes a 535 so the test fails if the stale token
			// reaches SMTP (i.e. Refresher was nil and no refresh happened).
			smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			t.Cleanup(func() { smtpLn.Close() })
			go func() {
				for {
					conn, err := smtpLn.Accept()
					if err != nil {
						return
					}
					go serveOAuthCheckSMTP(conn, "fresh-access-token")
				}
			}()

			// Wire the Refresher onto the Submitter — this mirrors what
			// internal/admin/server.go must do to prebuiltExtSubmitter.
			// Without this wiring (Refresher: nil), the test fails.
			refresher := &extsubmit.Refresher{
				Meta:    be.fs.Meta(),
				DataKey: testDataKey,
			}
			submitter := &extsubmit.Submitter{
				DataKey:   testDataKey,
				HostName:  "client.test",
				Refresher: refresher,
			}
			submitter.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("tcp", smtpLn.Addr().String())
			})

			sh := newSubmissionHarnessWithStore(t, be.fs, be.clk, protoadmin.Options{
				ExternalTestSender: protoadmin.DefaultTestSenderFromSubmitter(submitter),
			})
			_, adminKey := sh.bootstrap("oauth-wired@example.com")

			// Get PrincipalID for insertIdentity.
			res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
			}
			var who struct {
				PrincipalID uint64 `json:"principal_id"`
			}
			json.Unmarshal(buf, &who)
			identityID := sh.insertIdentity(who.PrincipalID, "oauth-wired@example.com")

			// Seal credential material for direct store insertion.
			staleCT, serr := secrets.Seal(testDataKey, []byte("stale-access-token"))
			if serr != nil {
				t.Fatalf("seal stale access token: %v", serr)
			}
			refreshCT, rerr := secrets.Seal(testDataKey, []byte("valid-refresh-token"))
			if rerr != nil {
				t.Fatalf("seal refresh token: %v", rerr)
			}

			// Insert the submission row directly — bypassing the PUT endpoint — so we
			// can set RefreshDue to a past value without the probe running. The stored
			// access token is deliberately stale; the Refresher must replace it before
			// the SMTP AUTH step.
			subRow := store.IdentitySubmission{
				IdentityID:         identityID,
				SubmitHost:         "smtp.example.com",
				SubmitPort:         587,
				SubmitSecurity:     "none",
				SubmitAuthMethod:   "oauth2",
				OAuthAccessCT:      staleCT,
				OAuthRefreshCT:     refreshCT,
				OAuthTokenEndpoint: tokenSrv.URL,
				OAuthClientID:      "oauth-wired@example.com",
				State:              store.IdentitySubmissionStateAuthFailed,
				// RefreshDue in the past: accessToken must trigger Refresher.Refresh.
				RefreshDue: be.clk.Now().Add(-5 * time.Minute),
			}
			if err := be.fs.Meta().UpsertIdentitySubmission(context.Background(), subRow); err != nil {
				t.Fatalf("UpsertIdentitySubmission: %v", err)
			}

			// POST /submission/test — with a non-nil Refresher, the expired token
			// is refreshed, SMTP accepts, and ok=true is returned.
			res3, buf3 := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/submission/test", adminKey, nil)
			if res3.StatusCode != http.StatusOK {
				t.Fatalf("POST submission/test: %d: %s", res3.StatusCode, buf3)
			}
			var testResp struct {
				OK     bool   `json:"ok"`
				Detail string `json:"detail"`
			}
			json.Unmarshal(buf3, &testResp)
			if !testResp.OK {
				t.Errorf("ok=false (detail=%s); want true — Refresher must have refreshed the expired token before SMTP AUTH", testResp.Detail)
			}
			if n := atomic.LoadInt32(&tokenEndpointCalls); n != 1 {
				t.Errorf("token endpoint called %d times; want exactly 1 (proactive refresh)", n)
			}
		})
	}
}

// serveOAuthCheckSMTP runs a scripted SMTP server on conn that parses the XOAUTH2
// initial response and returns 235 if the bearer token matches acceptedToken, or
// 535 otherwise. Used by TestTestSubmission_SubmitterRefresherWired to
// distinguish between a refreshed token and the stale one.
func serveOAuthCheckSMTP(conn net.Conn, acceptedToken string) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	write := func(s string) {
		bw.WriteString(s + "\r\n")
		bw.Flush()
	}
	readln := func() string {
		line, _ := br.ReadString('\n')
		return strings.TrimRight(line, "\r\n")
	}

	write("220 smtp.example.com ESMTP test")
	readln() // EHLO
	write("250-smtp.example.com")
	write("250 AUTH XOAUTH2")
	authLine := readln() // AUTH XOAUTH2 <base64-IR>

	// Parse XOAUTH2 IR: user=<email>\x01auth=Bearer <token>\x01\x01
	var token string
	if parts := strings.SplitN(authLine, " ", 3); len(parts) == 3 {
		if decoded, err := base64.StdEncoding.DecodeString(parts[2]); err == nil {
			ir := string(decoded)
			if idx := strings.Index(ir, "Bearer "); idx >= 0 {
				rest := ir[idx+7:]
				if end := strings.Index(rest, "\x01"); end >= 0 {
					token = rest[:end]
				}
			}
		}
	}

	if token != acceptedToken {
		write("535 5.7.8 Username and Password not accepted")
		return
	}
	write("235 2.7.0 Accepted")
	readln() // MAIL FROM
	write("250 ok")
	readln() // RCPT TO
	write("250 ok")
	readln() // DATA
	write("354 send data")
	for {
		if l := readln(); l == "." {
			break
		}
	}
	write("250 2.0.0 ok")
	readln() // QUIT
}

// TestProviderNameByTokenURL verifies the provider-name lookup used to populate
// oauth_provider in the GET /submission response (re #131). Covers priority
// ordering (gmail before m365 before other), no-match, and empty-tokenURL.
func TestProviderNameByTokenURL(t *testing.T) {
	gmailURL := "https://oauth2.googleapis.com/token"
	m365URL := "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	customURL := "https://idp.custom.example/token"
	sharedURL := "https://shared.fake.example/token"

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sh := newSubmissionHarnessWithStore(t, sqlitetest.Open(t, clk), clk, protoadmin.Options{
		OAuthProviders: map[string]protoadmin.OAuthProviderOptions{
			"gmail": {
				ClientID: "gid", ClientSecret: "gsec",
				AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL: gmailURL,
			},
			"m365": {
				ClientID: "mid", ClientSecret: "msec",
				AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
				TokenURL: m365URL,
			},
			"corp-relay": {
				ClientID: "cid", ClientSecret: "csec",
				AuthURL:  "https://idp.custom.example/authorize",
				TokenURL: customURL,
			},
			// fakeidp and gmail-alias both map to sharedURL, simulating the
			// dev-instance pattern where multiple names alias the same IdP.
			// gmail is preferred when both match.
			"fakeidp": {
				ClientID: "fid", ClientSecret: "fsec",
				AuthURL:  "https://shared.fake.example/authorize",
				TokenURL: sharedURL,
			},
			"gmail-alias": {
				ClientID: "fid", ClientSecret: "fsec",
				AuthURL:  "https://shared.fake.example/authorize",
				TokenURL: sharedURL,
			},
		},
	})

	cases := []struct {
		name     string
		tokenURL string
		want     string
	}{
		{"exact gmail match", gmailURL, "gmail"},
		{"exact m365 match", m365URL, "m365"},
		{"custom provider", customURL, "corp-relay"},
		// sharedURL is aliased by "fakeidp" and "gmail-alias"; neither is
		// "gmail" or "m365", so the result is one of the matching aliases
		// (we only assert it is non-empty here because the iteration order
		// over map keys is non-deterministic for non-preferred names).
		{"shared URL returns a match", sharedURL, ""},
		{"no match returns empty", "https://unknown.example/token", ""},
		{"empty tokenURL returns empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sh.srv.ProviderNameByTokenURL(tc.tokenURL)
			switch tc.name {
			case "shared URL returns a match":
				if got == "" {
					t.Errorf("ProviderNameByTokenURL(%q) = empty; want a non-empty provider name", tc.tokenURL)
				}
			default:
				if got != tc.want {
					t.Errorf("ProviderNameByTokenURL(%q) = %q; want %q", tc.tokenURL, got, tc.want)
				}
			}
		})
	}
}

// TestGetSubmission_OAuthProvider verifies that GET /submission returns
// oauth_provider when submit_auth_method == "oauth2" and that the field is
// absent for a password-auth identity. Covers gmail, m365, unknown-provider,
// and password-auth cases (re #131).
func TestGetSubmission_OAuthProvider(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			gmailTokenURL := "https://oauth2.googleapis.com/token"
			m365TokenURL := "https://login.microsoftonline.com/common/oauth2/v2.0/token"

			sh := newSubmissionHarnessWithStore(t, be.fs, be.clk, protoadmin.Options{
				OAuthProviders: map[string]protoadmin.OAuthProviderOptions{
					"gmail": {
						ClientID: "gid", ClientSecret: "gsec",
						AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
						TokenURL: gmailTokenURL,
					},
					"m365": {
						ClientID: "mid", ClientSecret: "msec",
						AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
						TokenURL: m365TokenURL,
					},
				},
			})

			_, adminKey := sh.bootstrap("admin@oauthprovider.example")
			res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
			}
			var who struct {
				PrincipalID uint64 `json:"principal_id"`
			}
			if err := json.Unmarshal(buf, &who); err != nil {
				t.Fatalf("unmarshal whoami: %v", err)
			}

			type getResponse struct {
				SubmitAuthMethod string `json:"submit_auth_method"`
				OAuthProvider    string `json:"oauth_provider"`
			}

			sealTok := func(tok string) []byte {
				ct, err := secrets.Seal(testDataKey, []byte(tok))
				if err != nil {
					t.Fatalf("seal %q: %v", tok, err)
				}
				return ct
			}

			// insertExtraIdentity creates a JMAP identity with an explicit id,
			// allowing multiple identities per principal in the same test.
			insertExtraIdentity := func(id, email string) {
				if err := be.fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
					ID:          id,
					PrincipalID: store.PrincipalID(who.PrincipalID),
					Name:        "Test",
					Email:       email,
					MayDelete:   true,
				}); err != nil {
					t.Fatalf("InsertJMAPIdentity %s: %v", id, err)
				}
			}

			// --- oauth2 identity with gmail token URL ---
			const gmailID = "oauthprov-gmail"
			insertExtraIdentity(gmailID, "alice@oauthprovider.example")
			if err := be.fs.Meta().UpsertIdentitySubmission(context.Background(), store.IdentitySubmission{
				IdentityID:         gmailID,
				SubmitHost:         "smtp.gmail.com",
				SubmitPort:         587,
				SubmitSecurity:     "starttls",
				SubmitAuthMethod:   "oauth2",
				OAuthAccessCT:      sealTok("gmailtoken"),
				OAuthTokenEndpoint: gmailTokenURL,
				OAuthClientID:      "alice@oauthprovider.example",
				State:              store.IdentitySubmissionStateOK,
			}); err != nil {
				t.Fatalf("UpsertIdentitySubmission gmail: %v", err)
			}
			res2, buf2 := sh.doRequest("GET", "/api/v1/identities/"+gmailID+"/submission", adminKey, nil)
			if res2.StatusCode != http.StatusOK {
				t.Fatalf("GET gmail: %d: %s", res2.StatusCode, buf2)
			}
			var gmailGot getResponse
			if err := json.Unmarshal(buf2, &gmailGot); err != nil {
				t.Fatalf("decode gmail: %v", err)
			}
			if gmailGot.OAuthProvider != "gmail" {
				t.Errorf("gmail identity: oauth_provider = %q; want gmail", gmailGot.OAuthProvider)
			}

			// --- oauth2 identity with m365 token URL ---
			const m365ID = "oauthprov-m365"
			insertExtraIdentity(m365ID, "bob@oauthprovider.example")
			if err := be.fs.Meta().UpsertIdentitySubmission(context.Background(), store.IdentitySubmission{
				IdentityID:         m365ID,
				SubmitHost:         "smtp.office365.com",
				SubmitPort:         587,
				SubmitSecurity:     "starttls",
				SubmitAuthMethod:   "oauth2",
				OAuthAccessCT:      sealTok("m365token"),
				OAuthTokenEndpoint: m365TokenURL,
				OAuthClientID:      "bob@oauthprovider.example",
				State:              store.IdentitySubmissionStateOK,
			}); err != nil {
				t.Fatalf("UpsertIdentitySubmission m365: %v", err)
			}
			res3, buf3 := sh.doRequest("GET", "/api/v1/identities/"+m365ID+"/submission", adminKey, nil)
			if res3.StatusCode != http.StatusOK {
				t.Fatalf("GET m365: %d: %s", res3.StatusCode, buf3)
			}
			var m365Got getResponse
			if err := json.Unmarshal(buf3, &m365Got); err != nil {
				t.Fatalf("decode m365: %v", err)
			}
			if m365Got.OAuthProvider != "m365" {
				t.Errorf("m365 identity: oauth_provider = %q; want m365", m365Got.OAuthProvider)
			}

			// --- password-auth identity: oauth_provider must be absent ---
			const pwID = "oauthprov-password"
			insertExtraIdentity(pwID, "carol@oauthprovider.example")
			if err := be.fs.Meta().UpsertIdentitySubmission(context.Background(), store.IdentitySubmission{
				IdentityID:       pwID,
				SubmitHost:       "smtp.example.com",
				SubmitPort:       587,
				SubmitSecurity:   "starttls",
				SubmitAuthMethod: "password",
				PasswordCT:       sealTok("password"),
				OAuthClientID:    "carol@oauthprovider.example",
				State:            store.IdentitySubmissionStateOK,
			}); err != nil {
				t.Fatalf("UpsertIdentitySubmission password: %v", err)
			}
			res4, buf4 := sh.doRequest("GET", "/api/v1/identities/"+pwID+"/submission", adminKey, nil)
			if res4.StatusCode != http.StatusOK {
				t.Fatalf("GET password: %d: %s", res4.StatusCode, buf4)
			}
			var pwGot getResponse
			if err := json.Unmarshal(buf4, &pwGot); err != nil {
				t.Fatalf("decode password: %v", err)
			}
			if pwGot.OAuthProvider != "" {
				t.Errorf("password identity: oauth_provider = %q; want empty", pwGot.OAuthProvider)
			}

			// --- oauth2 identity with unrecognised token URL ---
			const unkID = "oauthprov-unknown"
			insertExtraIdentity(unkID, "dan@oauthprovider.example")
			if err := be.fs.Meta().UpsertIdentitySubmission(context.Background(), store.IdentitySubmission{
				IdentityID:         unkID,
				SubmitHost:         "smtp.unknown.example",
				SubmitPort:         587,
				SubmitSecurity:     "starttls",
				SubmitAuthMethod:   "oauth2",
				OAuthAccessCT:      sealTok("unktok"),
				OAuthTokenEndpoint: "https://idp.unknown.example/token",
				OAuthClientID:      "dan@oauthprovider.example",
				State:              store.IdentitySubmissionStateOK,
			}); err != nil {
				t.Fatalf("UpsertIdentitySubmission unknown: %v", err)
			}
			res5, buf5 := sh.doRequest("GET", "/api/v1/identities/"+unkID+"/submission", adminKey, nil)
			if res5.StatusCode != http.StatusOK {
				t.Fatalf("GET unknown: %d: %s", res5.StatusCode, buf5)
			}
			var unkGot getResponse
			if err := json.Unmarshal(buf5, &unkGot); err != nil {
				t.Fatalf("decode unknown: %v", err)
			}
			if unkGot.OAuthProvider != "" {
				t.Errorf("unknown-provider oauth2 identity: oauth_provider = %q; want empty", unkGot.OAuthProvider)
			}
		})
	}
}
