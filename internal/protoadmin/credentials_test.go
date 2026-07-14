package protoadmin_test

// credentials_test.go covers the unified active-credentials view (issue
// #224): GET /api/v1/auth/credentials and DELETE
// /api/v1/auth/credentials/{kind}/{id}, composing sessions, device
// tokens, and OAuth2 grants into one self-service list + revoke action.
//
// Test matrix:
//   - lists all three kinds for the caller, marks the current session
//   - never leaks secret material (plaintext token/refresh-token/hash)
//   - IDOR: listing never surfaces another principal's rows, and a
//     principal_id query parameter on GET is ignored
//   - IDOR: revoking another principal's session/device-token/oauth2-grant
//     by id returns 404, and the target credential still authenticates
//     afterward (a real request, not a store read)
//   - revocation is immediate: the very next request with a revoked
//     session cookie / device-token Bearer / oauth2 access token or
//     refresh token is refused
//   - revoking the current session clears cookies and logs the caller out
//   - unknown kind -> 404

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// credEntry mirrors the wire shape of one item in the unified list.
type credEntry struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Label      string `json:"label"`
	ClientID   string `json:"client_id"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	ExpiresAt  string `json:"expires_at"`
	LastSeenIP string `json:"last_seen_ip"`
	UserAgent  string `json:"user_agent"`
	IsCurrent  bool   `json:"is_current"`
}

type credPage struct {
	Items []credEntry `json:"items"`
}

// mintedCreds bundles every credential minted for one principal by
// mintAllKinds, including the plaintext secrets, so a test can both
// exercise the unified endpoint and prove liveness/death with a real
// authenticated request against the underlying protocol.
type mintedCreds struct {
	sessionID          string
	deviceTokenID      string
	deviceTokenPlain   string
	oauth2FamilyID     string
	oauth2AccessPlain  string
	oauth2RefreshPlain string
	oauth2ClientID     string
	oauth2RedirectURI  string
}

// mustInsertLocalDomain registers domain as a local domain so
// directory.CreatePrincipal accepts email addresses under it (the
// bootstrap flow does this automatically for the bootstrap principal's
// own domain; these tests create plain non-admin principals directly,
// with no bootstrap call, so the domain needs registering explicitly).
func mustInsertLocalDomain(t *testing.T, sh *sessionHarness, domain string) {
	t.Helper()
	if err := sh.h.Store.Meta().InsertDomain(context.Background(), store.Domain{Name: domain, IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain(%s): %v", domain, err)
	}
}

// doWithClient issues a JSON request through client (which carries its own
// cookie jar), optionally adding X-CSRF-Token when csrfTok is non-empty.
func doWithClient(t *testing.T, client *http.Client, baseURL, method, path string, body any, csrfTok string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, rdr)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrfTok != "" {
		req.Header.Set("X-CSRF-Token", csrfTok)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

func csrfTokenForJar(t *testing.T, jar *cookiejar.Jar, baseURL string) string {
	t.Helper()
	u, _ := url.Parse(baseURL + "/")
	for _, c := range jar.Cookies(u) {
		if c.Name == "herold_public_csrf" {
			return c.Value
		}
	}
	t.Fatal("herold_public_csrf not in cookie jar")
	return ""
}

func currentSessionIDForClient(t *testing.T, baseURL string, client *http.Client) string {
	t.Helper()
	code, raw := doWithClient(t, client, baseURL, "GET", "/api/v1/auth/sessions", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET sessions: want 200, got %d body=%s", code, raw)
	}
	var page struct {
		Items []struct {
			SessionID string `json:"session_id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal sessions: %v", err)
	}
	for _, it := range page.Items {
		if it.IsCurrent {
			return it.SessionID
		}
	}
	t.Fatal("no current session found")
	return ""
}

// mintAllKinds creates one session (via cookie login on client/jar), one
// device token, and one OAuth2 grant, all for the same (email, password)
// principal, and returns every id/plaintext a test needs.
func mintAllKinds(t *testing.T, sth *sessionTestHarness, client *http.Client, email, password string) mintedCreds {
	t.Helper()
	sh := sth.sh
	ctx := context.Background()

	// Session, via cookie login (no TOTP: these are plain non-admin
	// principals in this file's tests).
	sth.loginWithClient(t, client, email, password, "")
	sessionID := currentSessionIDForClient(t, sh.baseURL, client)

	// Device token.
	devPlain, devKey, err := sh.dir.IssueDeviceToken(ctx, email, password, "", "Test Device")
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}

	// OAuth2 grant: register a distinct client per principal so client
	// names/ids are unambiguous across the two principals in a test.
	clientID := "cred-client-" + email
	if _, _, err := sh.dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID:     clientID,
		Name:         "Credentials Test Client " + email,
		RedirectURIs: []string{"https://example.test/cb"},
	}); err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}
	verifier, challenge := oauthPKCE(t)
	redirectURI := "https://example.test/cb"
	req := directory.AuthorizeRequest{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		State:               "st",
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		CSRFToken:           "csrf",
		ExpiresAt:           sh.clk.Now().Add(directory.AuthorizeRequestTTL),
	}
	code, err := sh.dir.IssueAuthorizationCode(ctx, email, password, "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	result, err := sh.dir.ExchangeAuthorizationCode(ctx, clientID, "", code, redirectURI, verifier)
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode: %v", err)
	}

	p, err := sh.h.Store.Meta().GetPrincipalByEmail(ctx, email)
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	grants, err := sh.h.Store.Meta().ListOAuthRefreshTokensByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListOAuthRefreshTokensByPrincipal: %v", err)
	}
	var familyID string
	for _, g := range grants {
		if g.ClientID == clientID {
			familyID = g.FamilyID
			break
		}
	}
	if familyID == "" {
		t.Fatalf("could not resolve family id for client %s", clientID)
	}

	return mintedCreds{
		sessionID:          sessionID,
		deviceTokenID:      strconv.FormatUint(uint64(devKey.ID), 10),
		deviceTokenPlain:   devPlain,
		oauth2FamilyID:     familyID,
		oauth2AccessPlain:  result.AccessToken,
		oauth2RefreshPlain: result.RefreshToken,
		oauth2ClientID:     clientID,
		oauth2RedirectURI:  redirectURI,
	}
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

func TestCredentials_ListsAllThreeKinds(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	const email, password = "creds-a@example.test", "correct-horse-battery-1"
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	if _, err := sh.dir.CreatePrincipal(ctx, email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	m := mintAllKinds(t, sth, sh.cookieJarClient, email, password)

	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "GET", "/api/v1/auth/credentials", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET credentials: want 200, got %d body=%s", code, raw)
	}
	var page credPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, raw)
	}

	var sawSession, sawDevice, sawGrant bool
	for _, it := range page.Items {
		switch it.Kind {
		case "session":
			if it.ID == m.sessionID {
				sawSession = true
				if !it.IsCurrent {
					t.Errorf("session entry IsCurrent = false, want true")
				}
			}
		case "device_token":
			if it.ID == m.deviceTokenID {
				sawDevice = true
				if it.Label != "Test Device" {
					t.Errorf("device_token Label = %q, want %q", it.Label, "Test Device")
				}
			}
		case "oauth2_grant":
			if it.ID == m.oauth2FamilyID {
				sawGrant = true
				if it.ClientID != m.oauth2ClientID {
					t.Errorf("oauth2_grant ClientID = %q, want %q", it.ClientID, m.oauth2ClientID)
				}
				if it.Label != "Credentials Test Client "+email {
					t.Errorf("oauth2_grant Label = %q, want the registered client name", it.Label)
				}
			}
		default:
			t.Errorf("unexpected kind %q", it.Kind)
		}
	}
	if !sawSession || !sawDevice || !sawGrant {
		t.Fatalf("missing kind(s): session=%v device=%v grant=%v; items=%+v", sawSession, sawDevice, sawGrant, page.Items)
	}
}

// TestCredentials_NeverLeaksSecretMaterial asserts the plaintext device
// token, OAuth2 access token, and OAuth2 refresh token never appear
// anywhere in the listing response body.
func TestCredentials_NeverLeaksSecretMaterial(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	const email, password = "creds-secret@example.test", "correct-horse-battery-2"
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	if _, err := sh.dir.CreatePrincipal(ctx, email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	m := mintAllKinds(t, sth, sh.cookieJarClient, email, password)

	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "GET", "/api/v1/auth/credentials", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET credentials: want 200, got %d", code)
	}
	body := string(raw)
	for _, secret := range []string{m.deviceTokenPlain, m.oauth2AccessPlain, m.oauth2RefreshPlain} {
		if secret == "" {
			t.Fatalf("test setup produced an empty secret")
		}
		if strings.Contains(body, secret) {
			t.Fatalf("credentials list leaked secret material: %q found in body", secret)
		}
	}
}

// TestCredentials_IDOR_List_OnlyOwnCredentials tries to defeat the
// listing endpoint's principal scoping: another principal's session,
// device-token, and oauth2-grant ids must never appear in the caller's
// list, and a principal_id query parameter -- an obvious lever to try --
// is silently ignored (the caller is always derived from the
// authenticated context, never from request input).
func TestCredentials_IDOR_List_OnlyOwnCredentials(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const emailA, passwordA = "creds-idor-a@example.test", "correct-horse-battery-3"
	const emailB, passwordB = "creds-idor-b@example.test", "correct-horse-battery-4"
	if _, err := sh.dir.CreatePrincipal(ctx, emailA, passwordA); err != nil {
		t.Fatalf("CreatePrincipal A: %v", err)
	}
	if _, err := sh.dir.CreatePrincipal(ctx, emailB, passwordB); err != nil {
		t.Fatalf("CreatePrincipal B: %v", err)
	}
	mintAllKinds(t, sth, sh.cookieJarClient, emailA, passwordA)
	mB := mintAllKinds(t, sth, sth.client2, emailB, passwordB)

	// A's plain GET must not contain any of B's ids.
	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "GET", "/api/v1/auth/credentials", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET credentials (A): want 200, got %d", code)
	}
	assertNoBID(t, raw, mB)

	// Try to widen the result with a principal_id query parameter aimed
	// at B -- must have zero effect.
	pB, err := sh.h.Store.Meta().GetPrincipalByEmail(ctx, emailB)
	if err != nil {
		t.Fatalf("GetPrincipalByEmail B: %v", err)
	}
	path := "/api/v1/auth/credentials?principal_id=" + strconv.FormatUint(uint64(pB.ID), 10)
	code, raw = doWithClient(t, sh.cookieJarClient, sh.baseURL, "GET", path, nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET credentials (A, with principal_id param): want 200, got %d", code)
	}
	assertNoBID(t, raw, mB)
}

func assertNoBID(t *testing.T, raw []byte, mB mintedCreds) {
	t.Helper()
	var page credPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, raw)
	}
	for _, it := range page.Items {
		if it.ID == mB.sessionID || it.ID == mB.deviceTokenID || it.ID == mB.oauth2FamilyID {
			t.Fatalf("caller's credentials list leaked another principal's row: %+v", it)
		}
	}
}

// TestCredentials_IDOR_Revoke_Session_WrongOwner asserts B cannot revoke
// A's session by id (404, not 403 -- anti-enumeration) and that A's
// session still authenticates a real request afterward.
func TestCredentials_IDOR_Revoke_Session_WrongOwner(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const emailA, passwordA = "creds-idor-sess-a@example.test", "correct-horse-battery-5"
	const emailB, passwordB = "creds-idor-sess-b@example.test", "correct-horse-battery-6"
	if _, err := sh.dir.CreatePrincipal(ctx, emailA, passwordA); err != nil {
		t.Fatalf("CreatePrincipal A: %v", err)
	}
	if _, err := sh.dir.CreatePrincipal(ctx, emailB, passwordB); err != nil {
		t.Fatalf("CreatePrincipal B: %v", err)
	}
	mA := mintAllKinds(t, sth, sh.cookieJarClient, emailA, passwordA)
	sth.loginWithClient(t, sth.client2, emailB, passwordB, "")

	csrfB := csrfTokenForJar(t, sth.jar2, sh.baseURL)
	code, raw := doWithClient(t, sth.client2, sh.baseURL, "DELETE", "/api/v1/auth/credentials/session/"+mA.sessionID, nil, csrfB)
	if code != http.StatusNotFound {
		t.Fatalf("B revoking A's session: want 404, got %d body=%s", code, raw)
	}

	// A's session must still work (a real request, not a store read).
	code, _ = doWithClient(t, sh.cookieJarClient, sh.baseURL, "GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Fatalf("A whoami after B's failed cross-owner revoke: want 200, got %d", code)
	}
}

// TestCredentials_IDOR_Revoke_DeviceToken_WrongOwner mirrors the session
// case for a device token: B cannot revoke A's device-token id, and A's
// token still authenticates afterward.
func TestCredentials_IDOR_Revoke_DeviceToken_WrongOwner(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const emailA, passwordA = "creds-idor-dev-a@example.test", "correct-horse-battery-7"
	const emailB, passwordB = "creds-idor-dev-b@example.test", "correct-horse-battery-8"
	if _, err := sh.dir.CreatePrincipal(ctx, emailA, passwordA); err != nil {
		t.Fatalf("CreatePrincipal A: %v", err)
	}
	if _, err := sh.dir.CreatePrincipal(ctx, emailB, passwordB); err != nil {
		t.Fatalf("CreatePrincipal B: %v", err)
	}
	mA := mintAllKinds(t, sth, sh.cookieJarClient, emailA, passwordA)
	sth.loginWithClient(t, sth.client2, emailB, passwordB, "")

	csrfB := csrfTokenForJar(t, sth.jar2, sh.baseURL)
	code, raw := doWithClient(t, sth.client2, sh.baseURL, "DELETE", "/api/v1/auth/credentials/device_token/"+mA.deviceTokenID, nil, csrfB)
	if code != http.StatusNotFound {
		t.Fatalf("B revoking A's device token: want 404, got %d body=%s", code, raw)
	}

	res, _ := sh.doRequest("GET", "/api/v1/auth/whoami", mA.deviceTokenPlain, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("A's device token after B's failed cross-owner revoke: want 200, got %d", res.StatusCode)
	}
}

// TestCredentials_IDOR_Revoke_OAuth2Grant_WrongOwner mirrors the session
// case for an OAuth2 grant: B cannot revoke A's grant by family id, and
// A's access token still authenticates afterward.
func TestCredentials_IDOR_Revoke_OAuth2Grant_WrongOwner(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const emailA, passwordA = "creds-idor-oa-a@example.test", "correct-horse-battery-9"
	const emailB, passwordB = "creds-idor-oa-b@example.test", "correct-horse-battery-10"
	if _, err := sh.dir.CreatePrincipal(ctx, emailA, passwordA); err != nil {
		t.Fatalf("CreatePrincipal A: %v", err)
	}
	if _, err := sh.dir.CreatePrincipal(ctx, emailB, passwordB); err != nil {
		t.Fatalf("CreatePrincipal B: %v", err)
	}
	mA := mintAllKinds(t, sth, sh.cookieJarClient, emailA, passwordA)
	sth.loginWithClient(t, sth.client2, emailB, passwordB, "")

	csrfB := csrfTokenForJar(t, sth.jar2, sh.baseURL)
	code, raw := doWithClient(t, sth.client2, sh.baseURL, "DELETE", "/api/v1/auth/credentials/oauth2_grant/"+mA.oauth2FamilyID, nil, csrfB)
	if code != http.StatusNotFound {
		t.Fatalf("B revoking A's oauth2 grant: want 404, got %d body=%s", code, raw)
	}

	res, _ := sh.doRequest("GET", "/api/v1/auth/whoami", mA.oauth2AccessPlain, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("A's oauth2 access token after B's failed cross-owner revoke: want 200, got %d", res.StatusCode)
	}
}

// TestCredentials_RevokeSession_Immediate proves revoking a non-current
// session via the unified endpoint refuses that session's very next
// request (not merely leaves it to expire).
func TestCredentials_RevokeSession_Immediate(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const email, password = "creds-immed-sess@example.test", "correct-horse-battery-11"
	if _, err := sh.dir.CreatePrincipal(ctx, email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	// Two sessions for the same principal: client1 (jar1) and client2 (jar2).
	sth.loginWithClient(t, sh.cookieJarClient, email, password, "")
	otherSessionID := currentSessionIDForClient(t, sh.baseURL, sh.cookieJarClient)
	sth.loginWithClient(t, sth.client2, email, password, "")

	// Revoke the FIRST session (now "other" from client2's perspective)
	// using client2's own credential endpoint.
	csrf2 := csrfTokenForJar(t, sth.jar2, sh.baseURL)
	code, raw := doWithClient(t, sth.client2, sh.baseURL, "DELETE", "/api/v1/auth/credentials/session/"+otherSessionID, nil, csrf2)
	if code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d body=%s", code, raw)
	}

	code, raw = doWithClient(t, sh.cookieJarClient, sh.baseURL, "GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked session's next request: want 401, got %d body=%s", code, raw)
	}
	var prob struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &prob)
	if !strings.HasSuffix(prob.Type, "session_revoked") {
		t.Errorf("problem type = %q, want suffix session_revoked", prob.Type)
	}
}

// TestCredentials_RevokeCurrentSession_ClearsCookies proves revoking the
// CALLER'S OWN current session via the unified endpoint clears cookies
// and logs the caller out cleanly (no half-authenticated state).
func TestCredentials_RevokeCurrentSession_ClearsCookies(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const email, password = "creds-immed-self@example.test", "correct-horse-battery-12"
	if _, err := sh.dir.CreatePrincipal(ctx, email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	sth.loginWithClient(t, sh.cookieJarClient, email, password, "")
	currentID := currentSessionIDForClient(t, sh.baseURL, sh.cookieJarClient)

	csrf := csrfTokenForJar(t, sh.cookieJar, sh.baseURL)
	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "DELETE", "/api/v1/auth/credentials/session/"+currentID, nil, csrf)
	if code != http.StatusNoContent {
		t.Fatalf("revoke current session: want 204, got %d body=%s", code, raw)
	}

	code, _ = doWithClient(t, sh.cookieJarClient, sh.baseURL, "GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("whoami after self-revoke via credentials endpoint: want 401, got %d", code)
	}
}

// TestCredentials_RevokeDeviceToken_Immediate proves revoking a device
// token via the unified endpoint refuses its very next authenticated use.
func TestCredentials_RevokeDeviceToken_Immediate(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const email, password = "creds-immed-dev@example.test", "correct-horse-battery-13"
	if _, err := sh.dir.CreatePrincipal(ctx, email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	m := mintAllKinds(t, sth, sh.cookieJarClient, email, password)

	// The device token authenticates before revocation.
	res, _ := sh.doRequest("GET", "/api/v1/auth/whoami", m.deviceTokenPlain, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("device token before revoke: want 200, got %d", res.StatusCode)
	}

	csrf := csrfTokenForJar(t, sh.cookieJar, sh.baseURL)
	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "DELETE", "/api/v1/auth/credentials/device_token/"+m.deviceTokenID, nil, csrf)
	if code != http.StatusNoContent {
		t.Fatalf("revoke device token: want 204, got %d body=%s", code, raw)
	}

	res, _ = sh.doRequest("GET", "/api/v1/auth/whoami", m.deviceTokenPlain, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("device token after revoke: want 401, got %d", res.StatusCode)
	}
}

// TestCredentials_RevokeOAuth2Grant_Immediate proves revoking an OAuth2
// grant via the unified endpoint immediately kills both the live access
// token (no waiting out its 1h TTL) and the refresh token (rejected as
// invalid_grant on its next use), matching the issue's "revoking an
// access token revokes its refresh-token family" requirement.
func TestCredentials_RevokeOAuth2Grant_Immediate(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const email, password = "creds-immed-oa@example.test", "correct-horse-battery-14"
	if _, err := sh.dir.CreatePrincipal(ctx, email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	m := mintAllKinds(t, sth, sh.cookieJarClient, email, password)

	res, _ := sh.doRequest("GET", "/api/v1/auth/whoami", m.oauth2AccessPlain, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("access token before revoke: want 200, got %d", res.StatusCode)
	}

	csrf := csrfTokenForJar(t, sh.cookieJar, sh.baseURL)
	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "DELETE", "/api/v1/auth/credentials/oauth2_grant/"+m.oauth2FamilyID, nil, csrf)
	if code != http.StatusNoContent {
		t.Fatalf("revoke oauth2 grant: want 204, got %d body=%s", code, raw)
	}

	res, _ = sh.doRequest("GET", "/api/v1/auth/whoami", m.oauth2AccessPlain, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("access token after revoke: want 401, got %d", res.StatusCode)
	}

	refreshForm := url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {m.oauth2RefreshPlain},
		"client_id": {m.oauth2ClientID},
	}
	refreshRes, refreshBody := sh.doRequestForm("POST", "/oauth2/token", refreshForm)
	if refreshRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("refresh after revoke: want 400, got %d body=%s", refreshRes.StatusCode, refreshBody)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(refreshBody, &errBody)
	if errBody.Error != "invalid_grant" {
		t.Errorf("refresh after revoke error = %q, want invalid_grant", errBody.Error)
	}
}

// TestCredentials_RevokeUnknownKind_404 exercises the closed-enum kind
// check.
func TestCredentials_RevokeUnknownKind_404(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const email, password = "creds-unknown-kind@example.test", "correct-horse-battery-15"
	if _, err := sh.dir.CreatePrincipal(ctx, email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	sth.loginWithClient(t, sh.cookieJarClient, email, password, "")
	csrf := csrfTokenForJar(t, sh.cookieJar, sh.baseURL)

	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "DELETE", "/api/v1/auth/credentials/not-a-kind/123", nil, csrf)
	if code != http.StatusNotFound {
		t.Fatalf("unknown kind: want 404, got %d body=%s", code, raw)
	}
}

// TestCredentials_RevokeDeviceToken_WrongKindID_404 asserts that a
// generic (non-device-token) API key id cannot be deleted through the
// device_token kind -- a caller must not be able to use kind confusion
// to reach a different revocation surface than the one it names.
func TestCredentials_RevokeDeviceToken_WrongKindID_404(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh
	ctx := context.Background()
	mustInsertLocalDomain(t, sh, "example.test")
	const email, password = "creds-wrongkind@example.test", "correct-horse-battery-16"
	pid, err := sh.dir.CreatePrincipal(ctx, email, password)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	// A plain operator-issued API key (not a device token: no prefix,
	// zero ExpiresAt).
	plainKey, err := sh.h.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: store.PrincipalID(pid),
		Hash:        "creds-wrongkind-hash",
		Name:        "operator key",
		ScopeJSON:   `["mail.send"]`,
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	sth.loginWithClient(t, sh.cookieJarClient, email, password, "")
	csrf := csrfTokenForJar(t, sh.cookieJar, sh.baseURL)
	code, raw := doWithClient(t, sh.cookieJarClient, sh.baseURL, "DELETE",
		"/api/v1/auth/credentials/device_token/"+strconv.FormatUint(uint64(plainKey.ID), 10), nil, csrf)
	if code != http.StatusNotFound {
		t.Fatalf("revoke plain API key via device_token kind: want 404, got %d body=%s", code, raw)
	}
	// The key must still exist (untouched).
	if _, err := sh.h.Store.Meta().GetAPIKeyByHash(ctx, "creds-wrongkind-hash"); err != nil {
		t.Fatalf("plain API key was deleted despite kind mismatch: %v", err)
	}
}
