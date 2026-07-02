package protoadmin_test

// identity_verify_test.go covers the REST verification endpoints
// (REQ-IDENT-40..43):
//
//   - GET  /verify-identity?token=<token>          (link callback, no auth)
//   - POST /api/v1/identities/{id}/verify          (code POST, self-only)
//
// The handlers themselves live in internal/protoadmin/identity_verify.go.
// The test matrix mirrors the requirements:
//
//   - happy path link redeem -> 302 + verified_at_us set, token cleared
//   - happy path code redeem -> 204 + verified_at_us set, token cleared
//   - malformed / empty token -> 400 + HTML body
//   - expired token (link)   -> 400 + HTML body
//   - expired code           -> 400 + ProblemDetail
//   - code for wrong identity (cross-principal) -> 403 (requireSelfOnly)
//   - idempotent retry of a consumed token (re-click stale link) -> 400
//     because the row's token was cleared on the first redeem; a fresh
//     redeem of a still-live row by the same caller (race-window) -> 302
//
// The harness reuses the submission harness scaffolding for setup and
// the same store-driven test helpers for seeding the verification trio
// directly (no JMAP wire path used; task #14 ships the issuer).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// vhash returns the sha256 of s as a 32-byte slice. Matches the
// invariant on the store's verification-hash columns.
func vhash(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// issueVerification writes the verification trio for identityID into
// the store directly. Tests use this in lieu of the mail-driven issuer
// from task #14; the contract here is identical (sha256 over the raw
// token / code, an expiry in unix-micros).
func (sh *submissionHarness) issueVerification(t *testing.T, identityID, token, code string, ttl time.Duration) {
	t.Helper()
	exp := sh.clk.Now().Add(ttl).UnixMicro()
	if err := sh.fs.Meta().IssueIdentityVerificationToken(
		context.Background(), identityID, vhash(token), vhash(code), exp,
	); err != nil {
		t.Fatalf("IssueIdentityVerificationToken: %v", err)
	}
}

// doRequestNoFollow issues an HTTP request without following redirects
// so the test can assert on the 302 (the default http.Client follows
// redirects automatically, which would mask the redeem-then-redirect
// contract).
func (sh *submissionHarness) doRequestNoFollow(method, path string, body io.Reader) *http.Response {
	sh.t.Helper()
	req, err := http.NewRequest(method, sh.baseURL+path, body)
	if err != nil {
		sh.t.Fatalf("new request: %v", err)
	}
	// Clone the client so we don't disturb the cookie jar configuration
	// other tests rely on. CheckRedirect signals to net/http that the
	// caller wants the response of the redirector itself, not the
	// destination.
	c := *sh.client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	res, err := c.Do(req)
	if err != nil {
		sh.t.Fatalf("do: %v", err)
	}
	return res
}

// TestVerifyIdentity_Link_HappyPath_Redirects302 verifies that a valid
// token on an unverified, unexpired identity row produces a 302 to
// /#/settings AND clears the row's token columns / sets verified_at_us
// (REQ-IDENT-40, REQ-IDENT-42).
func TestVerifyIdentity_Link_HappyPath_Redirects302(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)

	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")
	const token = "raw-link-token-happy"
	sh.issueVerification(t, identityID, token, "123456", 24*time.Hour)

	res := sh.doRequestNoFollow("GET", "/verify-identity?token="+token, nil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusFound {
		t.Fatalf("expected 302; got %d: %s", res.StatusCode, body)
	}
	if loc := res.Header.Get("Location"); loc != "/#/settings" {
		t.Errorf("Location = %q; want /#/settings", loc)
	}

	// State invariants: verified_at_us > 0 AND trio cleared.
	got, err := sh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if got.VerifiedAtUs == 0 {
		t.Errorf("VerifiedAtUs = 0; want > 0 after successful verify")
	}
	if got.VerificationTokenHash != nil {
		t.Errorf("VerificationTokenHash = %x; want nil after verify", got.VerificationTokenHash)
	}
	if got.VerificationCodeHash != nil {
		t.Errorf("VerificationCodeHash = %x; want nil after verify", got.VerificationCodeHash)
	}
	if got.VerificationTokenExpiresAtUs != 0 {
		t.Errorf("VerificationTokenExpiresAtUs = %d; want 0 after verify", got.VerificationTokenExpiresAtUs)
	}
}

// TestVerifyIdentity_Link_Malformed_400HTML verifies that an empty
// token query returns 400 with an HTML failure page that links to
// /#/settings (REQ-IDENT-40).
func TestVerifyIdentity_Link_Malformed_400HTML(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, _ = sh.bootstrap("admin@example.com")

	res := sh.doRequestNoFollow("GET", "/verify-identity?token=", nil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d: %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html", ct)
	}
	if !strings.Contains(string(body), `href="/#/settings"`) {
		t.Errorf("body missing href=\"/#/settings\" anchor: %s", body)
	}
	// Also probe the omitted-query case (no ?token= at all).
	res2 := sh.doRequestNoFollow("GET", "/verify-identity", nil)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest {
		t.Errorf("omitted token: expected 400; got %d", res2.StatusCode)
	}
}

// TestVerifyIdentity_Link_UnknownToken_400HTML verifies that a
// well-formed but non-matching token returns 400 + HTML (REQ-IDENT-40
// "Reject if not found, expired, or already consumed").
func TestVerifyIdentity_Link_UnknownToken_400HTML(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, _ = sh.bootstrap("admin@example.com")

	res := sh.doRequestNoFollow("GET", "/verify-identity?token=does-not-match-anything", nil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html", ct)
	}
	if !strings.Contains(string(body), "Settings") {
		t.Errorf("body missing the Settings link")
	}
}

// TestVerifyIdentity_Link_Expired_400HTML verifies that an expired
// token returns 400 + HTML referencing "expired" so the user understands
// they must re-request (REQ-IDENT-34/40).
func TestVerifyIdentity_Link_Expired_400HTML(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")

	const token = "raw-link-token-expired"
	sh.issueVerification(t, identityID, token, "654321", 1*time.Minute)
	// Advance the clock past expiry.
	sh.clk.Advance(2 * time.Minute)

	res := sh.doRequestNoFollow("GET", "/verify-identity?token="+token, nil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(strings.ToLower(string(body)), "expired") {
		t.Errorf("expected 'expired' in body; got: %s", body)
	}
	// The row must NOT be marked verified on expiry.
	got, _ := sh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if got.VerifiedAtUs != 0 {
		t.Errorf("VerifiedAtUs = %d; want 0 after expired token", got.VerifiedAtUs)
	}
}

// TestVerifyIdentity_Link_IdempotentSecondRedeem verifies that a
// re-clicked stale link (the row's token columns were cleared by the
// first successful redeem) renders the failure page — and that the row
// remains verified. REQ-IDENT-42 says the SECOND valid token/code on
// an already-verified row is idempotent; the practical implementation
// is: the reverse lookup ErrNotFound's because the hash was cleared,
// so the user sees the failure page but the row is still verified and
// the SPA already shows that state.
func TestVerifyIdentity_Link_IdempotentSecondRedeem(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")
	const token = "raw-link-token-twice"
	sh.issueVerification(t, identityID, token, "242424", 24*time.Hour)

	res := sh.doRequestNoFollow("GET", "/verify-identity?token="+token, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("first redeem: expected 302; got %d", res.StatusCode)
	}

	// Second redeem of the same (now consumed) token: hash no longer
	// matches any row, so the handler renders the failure page. The
	// row is still verified — that is the user-observable invariant.
	res2 := sh.doRequestNoFollow("GET", "/verify-identity?token="+token, nil)
	defer res2.Body.Close()
	body, _ := io.ReadAll(res2.Body)
	if res2.StatusCode != http.StatusBadRequest {
		t.Errorf("second redeem: expected 400 (failure page); got %d: %s",
			res2.StatusCode, body)
	}
	// The failure message must not instruct an already-verified user
	// to request a new link; it must acknowledge both cases (re #105).
	if strings.Contains(string(body), "Please request a new one") {
		t.Errorf("consumed-token message must not say 'Please request a new one': %s", body)
	}
	if !strings.Contains(string(body), "not yet verified") {
		t.Errorf("consumed-token message must include 'not yet verified' hint: %s", body)
	}
	got, _ := sh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if got.VerifiedAtUs == 0 {
		t.Errorf("VerifiedAtUs = 0 after first redeem; want > 0")
	}
}

// TestVerifyIdentity_Link_ConsumedByOAuth_400HTML verifies that when an
// OAuth callback calls MarkIdentityVerified (NULLing the token hash) and
// the user then clicks the emailed verification link, the failure page does
// not tell an already-verified user to request a new link (re #105).
func TestVerifyIdentity_Link_ConsumedByOAuth_400HTML(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@gmail.com")
	const token = "raw-link-token-oauth-consumed"
	sh.issueVerification(t, identityID, token, "111222", 24*time.Hour)

	// Simulate the OAuth callback calling MarkIdentityVerified, which
	// NULLs the token/code hash columns (re #99). The link below is now
	// stale: the hash resolves to ErrNotFound.
	if err := sh.fs.Meta().MarkIdentityVerified(context.Background(), identityID); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}

	res := sh.doRequestNoFollow("GET", "/verify-identity?token="+token, nil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d: %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html", ct)
	}
	// Must NOT say "Please request a new one" — the identity is already
	// verified; requesting a new link is unnecessary and confusing.
	if strings.Contains(string(body), "Please request a new one") {
		t.Errorf("message must not say 'Please request a new one' when already verified: %s", body)
	}
	// Must acknowledge that verification may not be needed.
	if !strings.Contains(string(body), "not yet verified") {
		t.Errorf("message must include 'not yet verified' hint: %s", body)
	}
	// Row must still be verified.
	got, _ := sh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if got.VerifiedAtUs == 0 {
		t.Errorf("VerifiedAtUs = 0 after MarkIdentityVerified; want > 0")
	}
}

// TestVerifyIdentity_Code_HappyPath_204 verifies that a valid code
// POST returns 204 and sets verified_at_us on the row (REQ-IDENT-41).
func TestVerifyIdentity_Code_HappyPath_204(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")
	sh.issueVerification(t, identityID, "irrelevant-token", "987654", 24*time.Hour)

	res, body := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/verify",
		adminKey, map[string]any{"code": "987654"})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("POST verify: expected 204; got %d: %s", res.StatusCode, body)
	}

	got, _ := sh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if got.VerifiedAtUs == 0 {
		t.Errorf("VerifiedAtUs = 0; want > 0 after successful code redeem")
	}
	if got.VerificationCodeHash != nil {
		t.Errorf("VerificationCodeHash should be cleared after verify")
	}
}

// TestVerifyIdentity_Code_MalformedCode_400 verifies that a body whose
// "code" field isn't exactly 6 digits returns 400 with the invalid_code
// ProblemDetail type. Several malformed inputs are exercised.
func TestVerifyIdentity_Code_MalformedCode_400(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")
	sh.issueVerification(t, identityID, "tok", "111111", 24*time.Hour)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty", map[string]any{"code": ""}},
		{"short", map[string]any{"code": "12345"}},
		{"long", map[string]any{"code": "1234567"}},
		{"letters", map[string]any{"code": "1234ab"}},
		{"space", map[string]any{"code": "12 345"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := sh.doRequest("POST",
				"/api/v1/identities/"+identityID+"/verify", adminKey, tc.body)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400; got %d: %s", res.StatusCode, body)
			}
			if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q; want application/problem+json", ct)
			}
			var prob struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(body, &prob); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if !strings.HasSuffix(prob.Type, "invalid_code") {
				t.Errorf("type = %q; want suffix invalid_code", prob.Type)
			}
		})
	}
}

// TestVerifyIdentity_Code_Mismatch_400Invalid verifies that a well-
// formed but mismatched code returns 400 with type=invalid_code
// (REQ-IDENT-41 + REQ-IDENT-43 audit failure).
func TestVerifyIdentity_Code_Mismatch_400Invalid(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")
	sh.issueVerification(t, identityID, "tok", "777777", 24*time.Hour)

	res, body := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/verify",
		adminKey, map[string]any{"code": "000000"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d: %s", res.StatusCode, body)
	}
	var prob struct {
		Type  string `json:"type"`
		Title string `json:"title"`
	}
	json.Unmarshal(body, &prob)
	if !strings.HasSuffix(prob.Type, "invalid_code") {
		t.Errorf("type = %q; want invalid_code", prob.Type)
	}
	// Row must remain unverified.
	got, _ := sh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if got.VerifiedAtUs != 0 {
		t.Errorf("VerifiedAtUs = %d; want 0 after wrong-code POST", got.VerifiedAtUs)
	}
}

// TestVerifyIdentity_Code_Expired_400ProblemDetail verifies that an
// expired code returns 400 + ProblemDetail with type=expired_code
// (REQ-IDENT-34/41).
func TestVerifyIdentity_Code_Expired_400ProblemDetail(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")
	sh.issueVerification(t, identityID, "tok", "333333", 1*time.Minute)
	sh.clk.Advance(2 * time.Minute)

	res, body := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/verify",
		adminKey, map[string]any{"code": "333333"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d: %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q; want application/problem+json", ct)
	}
	var prob struct {
		Type string `json:"type"`
	}
	json.Unmarshal(body, &prob)
	if !strings.HasSuffix(prob.Type, "expired_code") {
		t.Errorf("type = %q; want expired_code", prob.Type)
	}
}

// TestVerifyIdentity_Code_CrossPrincipal_403Forbidden verifies that a
// caller who does not own the identity is rejected with 403 via
// requireSelfOnly (REQ-IDENT-41 + REQ-AUTH-EXT-SUBMIT-04 self-only
// posture). The identity is owned by an unrelated user; the admin
// caller is denied even though they are admin (no impersonation in v1).
func TestVerifyIdentity_Code_CrossPrincipal_403Forbidden(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	// Create a second user (the identity owner).
	_ = sh.createUser(adminKey, "user@example.com")
	// Resolve the user's principal id via the admin REST surface.
	resList, listBuf := sh.doRequest("GET", "/api/v1/principals", adminKey, nil)
	if resList.StatusCode != http.StatusOK {
		t.Fatalf("list principals: %d", resList.StatusCode)
	}
	var page struct {
		Items []struct {
			ID    uint64 `json:"id"`
			Email string `json:"canonical_email"`
		} `json:"items"`
	}
	json.Unmarshal(listBuf, &page)
	var userPID uint64
	for _, p := range page.Items {
		if p.Email == "user@example.com" {
			userPID = p.ID
			break
		}
	}
	if userPID == 0 {
		t.Fatalf("user@example.com not found in principals list")
	}

	identityID := sh.insertIdentity(userPID, "user@example.com")
	sh.issueVerification(t, identityID, "tok", "555555", 24*time.Hour)

	// Admin caller (different principal) attempts to redeem the code
	// for the user's identity -> 403 self-only.
	res, body := sh.doRequest("POST", "/api/v1/identities/"+identityID+"/verify",
		adminKey, map[string]any{"code": "555555"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403; got %d: %s", res.StatusCode, body)
	}
	// Row must NOT be verified.
	got, _ := sh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if got.VerifiedAtUs != 0 {
		t.Errorf("VerifiedAtUs = %d; want 0 after cross-principal attempt", got.VerifiedAtUs)
	}
}

// TestVerifyIdentity_Code_UnknownIdentity_404 verifies that a non-
// existent identity id returns 404 (resolveIdentityOwner emits
// ErrNotFound -> not_found ProblemDetail).
func TestVerifyIdentity_Code_UnknownIdentity_404(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	res, body := sh.doRequest("POST", "/api/v1/identities/does-not-exist/verify",
		adminKey, map[string]any{"code": "123456"})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404; got %d: %s", res.StatusCode, body)
	}
}

// TestVerifyIdentity_AuditOnSuccess verifies that a successful redeem
// emits an identity.verify.success audit entry tagged with the
// identity id, principal id, and result_class=ok (REQ-IDENT-43).
func TestVerifyIdentity_AuditOnSuccess(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, adminKey := sh.bootstrap("admin@example.com")

	_, buf := sh.doRequest("GET", "/api/v1/auth/whoami", adminKey, nil)
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &who)
	identityID := sh.insertIdentity(who.PrincipalID, "admin@example.com")
	const token = "raw-token-audit-success"
	sh.issueVerification(t, identityID, token, "171717", 24*time.Hour)

	res := sh.doRequestNoFollow("GET", "/verify-identity?token="+token, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("expected 302; got %d", res.StatusCode)
	}

	// The audit log should contain identity.verify.success for
	// subject=identity:<id> with result_class=ok metadata.
	entries := listAuditFor(t, sh, fmt.Sprintf("identity:%s", identityID))
	var sawSuccess bool
	for _, e := range entries {
		if e.Action == "identity.verify.success" {
			sawSuccess = true
			if e.Outcome != store.OutcomeSuccess {
				t.Errorf("outcome = %v; want success", e.Outcome)
			}
			if e.Metadata["result_class"] != "ok" {
				t.Errorf("metadata.result_class = %q; want ok",
					e.Metadata["result_class"])
			}
			if e.Metadata["via"] != "link" {
				t.Errorf("metadata.via = %q; want link", e.Metadata["via"])
			}
			if e.Metadata["identity_id"] != identityID {
				t.Errorf("metadata.identity_id = %q; want %q",
					e.Metadata["identity_id"], identityID)
			}
		}
	}
	if !sawSuccess {
		t.Errorf("did not see identity.verify.success audit entry; got %+v", entries)
	}
}

// TestVerifyIdentity_AuditOnFailure verifies that a failed redeem
// (unknown token) emits identity.verify.failure with the right
// result_class (REQ-IDENT-43).
func TestVerifyIdentity_AuditOnFailure(t *testing.T) {
	sh := newSubmissionHarness(t, alwaysOKProbe)
	_, _ = sh.bootstrap("admin@example.com")

	res := sh.doRequestNoFollow("GET", "/verify-identity?token=not-a-real-token", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d", res.StatusCode)
	}
	// Audit subject for the unknown-token branch is "identity:" with
	// an empty id (we have no row to attribute it to). Query by
	// action instead.
	entries := listAuditByAction(t, sh, "identity.verify.failure")
	if len(entries) == 0 {
		t.Fatalf("expected at least one identity.verify.failure entry")
	}
	if entries[0].Metadata["result_class"] != string("invalid_token") {
		t.Errorf("metadata.result_class = %q; want invalid_token",
			entries[0].Metadata["result_class"])
	}
}

// listAuditFor reads the audit log entries whose Subject matches
// subject. The store.AppendAuditLog path writes the entries
// synchronously, so a sequential read after the request is safe.
func listAuditFor(t *testing.T, sh *submissionHarness, subject string) []store.AuditLogEntry {
	t.Helper()
	all, err := sh.fs.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	var out []store.AuditLogEntry
	for _, e := range all {
		if e.Subject == subject {
			out = append(out, e)
		}
	}
	return out
}

// listAuditByAction returns audit entries whose Action matches.
func listAuditByAction(t *testing.T, sh *submissionHarness, action string) []store.AuditLogEntry {
	t.Helper()
	all, err := sh.fs.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	var out []store.AuditLogEntry
	for _, e := range all {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}
