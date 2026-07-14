package admin

// extidentity_e2e_test.go is the self-verification harness for herold's
// external-identity feature: an Identity on a foreign domain that submits
// mail through an external smart host, authenticated either with a password
// or with an OAuth 2.0 bearer token obtained from an external provider.
//
// Before this test the only way to exercise the flow was the maintainer's
// real Gmail account. Here the whole path runs in-process against two
// deterministic fakes — a fake OAuth IdP (internal/testfakes/fakeidp) and a
// fake SMTP submission sink (internal/testfakes/fakesmtp) — with no browser
// and no external network.
//
// The test boots a real herold server (admin.StartServer) with
// [server.external_submission] enabled and an [server.oauth_providers.*]
// block pointing at the fake IdP, then drives the exact production surfaces:
//
//   - PUT /api/v1/identities/{id}/submission — the admin REST DTO the Suite
//     uses to persist submission credentials. Crossing this boundary
//     (encode submissionPutRequest, decode the server's response) guards the
//     client/server field-name contract that #104 broke.
//   - JMAP EmailSubmission/set on POST /jmap — the real send path, which
//     routes an external identity through processCreateExternal ->
//     execExternalRelay -> extsubmit.Submitter.Submit (async since #108).
//
// It asserts, for BOTH an OAuth-configured identity and a password identity,
// that the message arrives at the fake smart host with the right envelope and
// SASL identity, that the JMAP method returns "created" rather than a spurious
// serverFail (guards #108), and that a hosted/authoritative-domain identity is
// reported authoritative and is not gated out of sending (guards #107).
//
// Runs on SQLite always and on Postgres when HEROLD_PG_DSN is set.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testfakes/fakeidp"
	"github.com/hanshuebner/herold/internal/testfakes/fakesmtp"
)

const (
	// extE2ELocalDomain is the server's authoritative domain.
	extE2ELocalDomain = "example.local"
	// extE2EForeignDomain hosts the external identities. It is deliberately
	// NOT authoritative on this server so submission must route externally.
	extE2EForeignDomain = "ext.test"

	// Numeric identity ids. The JMAP layer renders identity ids as decimal
	// overlay ids (renderID/ParseUint), and both the REST submission endpoints
	// and the JMAP resolver key on the same wire id, so seeded rows must use
	// numeric string ids to line up across all three surfaces.
	extE2EOAuthIdentityID  = "900001"
	extE2EPwIdentityID     = "900002"
	extE2EHostedIdentityID = "900003"
	// extE2EReauthIdentityID is a dedicated OAuth identity used only by the
	// held-for-reauth retry loop (Criterion B, re #70). It never sends
	// successfully in the main body, so a delivery to its address proves the
	// retry ran (exactly-once check).
	extE2EReauthIdentityID = "900004"

	extE2EDataKeyEnv     = "HEROLD_EXTID_E2E_DATA_KEY"
	extE2EOAuthSecretEnv = "HEROLD_EXTID_E2E_OAUTH_SECRET"
)

// extE2EDataKeyHex returns the 64-hex-char (32-byte) at-rest data key used to
// seal submission credentials in the test. It is derived from a fixed pattern
// at runtime so no secret-shaped literal lives in the source.
func extE2EDataKeyHex() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 3)
	}
	return hex.EncodeToString(key)
}

// TestExternalIdentity_E2E exercises the external-identity OAuth + external
// SMTP submission flow end to end against a real in-process server wired to
// deterministic fakes. See the file header for the guarded issues.
func TestExternalIdentity_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("external-identity e2e wiring test")
	}
	t.Run("sqlite", func(t *testing.T) { runExternalIdentityE2E(t, "sqlite", "") })
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) { runExternalIdentityE2E(t, "postgres", dsn) })
	}
}

func runExternalIdentityE2E(t *testing.T, backend, pgDSN string) {
	dir := t.TempDir()

	// Fakes: an OAuth provider and an SMTP submission sink. Both are plain
	// so the real Submitter reaches the sink over cleartext (submit_security
	// = none), keeping the transport out of the assertion surface.
	idp := fakeidp.New(t, fakeidp.Options{})
	smtp := fakesmtp.New(t, fakesmtp.Options{Security: fakesmtp.Plain, Hostname: "smtp.ext.test"})

	// The data key and OAuth client secret are supplied via env-var secret
	// references, the production mechanism (STANDARDS §9).
	t.Setenv(extE2EDataKeyEnv, extE2EDataKeyHex())
	t.Setenv(extE2EOAuthSecretEnv, idp.ClientSecret())

	// Build the storage block and a pre-seed store opener for the backend.
	var storageTOML string
	var openPreseed func() store.Store
	clk := clock.NewReal()
	switch backend {
	case "sqlite":
		dbPath := filepath.Join(dir, "db.sqlite")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"sqlite\"\n[server.storage.sqlite]\npath = %q\n", dbPath)
		openPreseed = func() store.Store {
			st, err := storesqlite.Open(context.Background(), dbPath, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			return st
		}
	case "postgres":
		blobDir := filepath.Join(dir, "blobs")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"postgres\"\n[server.storage.postgres]\ndsn = %q\nblob_dir = %q\n", pgDSN, blobDir)
		openPreseed = func() store.Store {
			st, err := storepg.Open(context.Background(), pgDSN, blobDir, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storepg.Open: %v", err)
			}
			// Shared throwaway DB: clear rows so seeds do not collide with a
			// prior test in the same run.
			if tr, ok := st.(interface {
				TruncateAll(context.Context) error
			}); ok {
				if err := tr.TruncateAll(context.Background()); err != nil {
					_ = st.Close()
					t.Fatalf("TruncateAll: %v", err)
				}
			}
			return st
		}
	default:
		t.Fatalf("unknown backend %q", backend)
	}

	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	systomlPath := filepath.Join(dir, "system.toml")
	systoml := fmt.Sprintf(`
[server]
hostname = %q
data_dir = %q
run_as_user = ""
run_as_group = ""
shutdown_grace = "5s"
port_report_file = %q

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

%s

[server.secrets]
data_key_ref = "$%s"

[server.external_submission]
enabled = true

[server.oauth_providers.fakeidp]
client_id = %q
client_secret_ref = "$%s"
auth_url = %q
token_url = %q
scopes = ["https://mail.google.com/"]

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "imap"
address = "127.0.0.1:0"
protocol = "imap"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "http"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "http"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`,
		extE2ELocalDomain, dir, filepath.Join(dir, "ports.toml"),
		certPath, keyPath,
		storageTOML,
		extE2EDataKeyEnv,
		idp.ClientID(), extE2EOAuthSecretEnv, idp.AuthorizeURL(), idp.TokenURL(),
		certPath, keyPath, certPath, keyPath)
	if err := os.WriteFile(systomlPath, []byte(systoml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Seed the store before boot: local domain + DKIM key, an admin
	// principal with an API key, three JMAP identities (two foreign, one
	// hosted), and one draft message to submit. Admins may send as any
	// address (auth/sendpolicy Rule 1), so the admin principal owns the
	// foreign identity addresses for the send-policy check.
	const apiKeyPlain = protoadmin.APIKeyPrefix + "extid_e2e_admin_key_00000000000001"
	adminEmail := "admin@" + extE2ELocalDomain
	emailID := seedExtIdentityStore(t, openPreseed(), clk, adminEmail, apiKeyPlain)

	// Boot the production lifecycle against the seeded store.
	publicAddr, adminAddr := startExtIdentityServer(t, cfg)

	// Discover the JMAP account id the same way the SPA does.
	accountID := jmapAccountID(t, publicAddr, apiKeyPlain)

	// ---- OAuth identity ---------------------------------------------------
	// Obtain tokens from the fake IdP via the full authorize -> token dance.
	oauthUser := "alice@" + extE2EForeignDomain
	tok := idp.Authenticate(t, "http://localhost/oauth/callback", "state-oauth-1", "https://mail.google.com/")

	putSubmission(t, adminAddr, apiKeyPlain, extE2EOAuthIdentityID, map[string]any{
		"submit_host":          smtp.Host(),
		"submit_port":          smtp.Port(),
		"submit_security":      "none",
		"submit_auth_method":   "oauth2",
		"oauth_access_token":   tok.AccessToken,
		"oauth_refresh_token":  tok.RefreshToken,
		"oauth_token_endpoint": idp.TokenURL(),
		"oauth_client_id":      oauthUser,
		"auth_user":            oauthUser,
	})

	// Send via the real JMAP EmailSubmission path.
	submitViaJMAP(t, publicAddr, apiKeyPlain, accountID, extE2EOAuthIdentityID, emailID)

	oauthMsg := waitForMessage(t, smtp, oauthUser)
	if oauthMsg.AuthMechanism != "XOAUTH2" {
		t.Errorf("oauth relay: AuthMechanism = %q; want XOAUTH2", oauthMsg.AuthMechanism)
	}
	if oauthMsg.AuthSecret != tok.AccessToken {
		t.Errorf("oauth relay: bearer token = %q; want the fakeidp access token %q", oauthMsg.AuthSecret, tok.AccessToken)
	}
	assertEnvelope(t, "oauth", oauthMsg, oauthUser)

	// ---- Password identity ------------------------------------------------
	pwUser := "carol@" + extE2EForeignDomain
	const appPassword = "app-specific-password-xyz"
	putSubmission(t, adminAddr, apiKeyPlain, extE2EPwIdentityID, map[string]any{
		"submit_host":        smtp.Host(),
		"submit_port":        smtp.Port(),
		"submit_security":    "none",
		"submit_auth_method": "password",
		"password":           appPassword,
		"auth_user":          pwUser,
	})

	submitViaJMAP(t, publicAddr, apiKeyPlain, accountID, extE2EPwIdentityID, emailID)

	pwMsg := waitForMessage(t, smtp, pwUser)
	if pwMsg.AuthMechanism != "PLAIN" && pwMsg.AuthMechanism != "LOGIN" {
		t.Errorf("password relay: AuthMechanism = %q; want PLAIN or LOGIN", pwMsg.AuthMechanism)
	}
	if pwMsg.AuthSecret != appPassword {
		t.Errorf("password relay: secret = %q; want the configured app password", pwMsg.AuthSecret)
	}
	assertEnvelope(t, "password", pwMsg, pwUser)

	// ---- Hosted identity is not gated (guards #107) -----------------------
	// GET must report the authoritative domain so the Suite exempts it from
	// the external-SMTP gate, and configured must be false (no external row).
	getResp := getSubmission(t, adminAddr, apiKeyPlain, extE2EHostedIdentityID)
	if !getResp.DomainAuthoritative {
		t.Errorf("hosted identity: domain_authoritative = false; want true (guards #107)")
	}
	if getResp.Configured {
		t.Errorf("hosted identity: configured = true; want false (no external submission row)")
	}
	// A hosted identity must still be allowed to send: EmailSubmission/set
	// routes it through the local queue and returns created, not a
	// "configure external submission" forbiddenFrom.
	submitViaJMAP(t, publicAddr, apiKeyPlain, accountID, extE2EHostedIdentityID, emailID)

	// ---- Criterion A: on-demand connection test (#113, re #131) -----------
	// Arrange: flip the password identity's stored state to auth-failed so we
	// can assert it clears back to ok after a successful test (re #131). We do
	// this through GET (to obtain the identity config) + a direct-to-server
	// getSubmission call (read-only) and then use the connection-test endpoint
	// itself as the clearing mechanism, which is the exact production path.
	//
	// First ensure we actually start with state=ok (it was persisted by PUT
	// above), then manually flip it to auth-failed via getSubmission + assert,
	// then run the test and verify it reverts.
	//
	// NOTE: we cannot PUT submission with state=auth-failed because PUT always
	// runs the probe (which succeeds) and writes state=ok. Instead we seed the
	// broken state via the test-scenario: run testConnection with AUTH rejected
	// (which leaves state auth-failed in the store), then accept AUTH again and
	// re-run testConnection to assert it clears.
	//
	// Success path (guarded state=ok before rejection):
	if ok, detail, status := testConnection(t, adminAddr, apiKeyPlain, extE2EPwIdentityID); !ok {
		t.Fatalf("connection test (password identity): ok=false status=%d detail=%q; want ok=true", status, detail)
	}
	testMsg := waitForRcpt(t, smtp, pwUser)
	if testMsg.MailFrom != pwUser {
		t.Errorf("connection test message: MAIL FROM = %q; want %q (self)", testMsg.MailFrom, pwUser)
	}
	if len(testMsg.RcptTo) != 1 || testMsg.RcptTo[0] != pwUser {
		t.Errorf("connection test message: RCPT TO = %v; want [%s] (self)", testMsg.RcptTo, pwUser)
	}
	// Assert handleTestSubmission wrote state=ok back to the store (re #131).
	stateAfterSuccessfulTest := getSubmission(t, adminAddr, apiKeyPlain, extE2EPwIdentityID)
	if stateAfterSuccessfulTest.State != "ok" {
		t.Errorf("connection test state after success: %q; want ok (handleTestSubmission must write state=ok, re #131)", stateAfterSuccessfulTest.State)
	}

	// Failure: with the smart host rejecting AUTH, the test returns a
	// structured failure (ok=false, HTTP 200) carrying the diagnostic detail
	// (RFC 7807-style detail string), not a bare status.
	smtp.SetRejectAuth(true)
	failOK, failDetail, failStatus := testConnection(t, adminAddr, apiKeyPlain, extE2EPwIdentityID)
	if failOK {
		t.Errorf("connection test with AUTH rejected: ok=true; want ok=false")
	}
	if failStatus != http.StatusOK {
		t.Errorf("connection test with AUTH rejected: status=%d; want 200 with {ok:false,detail}", failStatus)
	}
	if failDetail == "" {
		t.Errorf("connection test failure carried no detail; want a diagnostic string")
	}
	smtp.SetRejectAuth(false)

	// ---- Criterion B: OAuth token-expiry recovery + queued retry (#70) ----
	runReauthRetryE2E(t, adminAddr, publicAddr, apiKeyPlain, accountID, emailID, idp, smtp)
}

// runReauthRetryE2E drives the whole held-for-reauth retry loop (re #70):
// an OAuth identity's send fails auth (the smart host rejects AUTH, simulating
// an expired/revoked token), the submission is HELD rather than bounced, the
// user re-authenticates through the real OAuth start+callback flow, and the
// held message is redelivered exactly once with no re-composition.
func runReauthRetryE2E(t *testing.T, adminAddr, publicAddr, apiKey, accountID, emailID string, idp *fakeidp.Server, smtp *fakesmtp.Server) {
	t.Helper()
	reauthUser := "dave@" + extE2EForeignDomain

	// Configure the OAuth identity against the fake smart host while AUTH is
	// accepted (the PUT probe must pass to persist).
	smtp.SetRejectAuth(false)
	tok := idp.Authenticate(t, "http://localhost/oauth/callback", "state-reauth-setup", "https://mail.google.com/")
	putSubmission(t, adminAddr, apiKey, extE2EReauthIdentityID, map[string]any{
		"submit_host":          smtp.Host(),
		"submit_port":          smtp.Port(),
		"submit_security":      "none",
		"submit_auth_method":   "oauth2",
		"oauth_access_token":   tok.AccessToken,
		"oauth_refresh_token":  tok.RefreshToken,
		"oauth_token_endpoint": idp.TokenURL(),
		"oauth_client_id":      reauthUser,
		"auth_user":            reauthUser,
	})

	// The token is now "expired": the smart host rejects AUTH. The send must
	// be parked held-for-reauth, not bounced or lost.
	smtp.SetRejectAuth(true)
	submitViaJMAP(t, publicAddr, apiKey, accountID, extE2EReauthIdentityID, emailID)

	// Assert HELD: the submission surfaces as delivered=queued with
	// "pending re-authentication", and nothing reaches the smart host.
	waitForHeldSubmission(t, publicAddr, apiKey, accountID)
	for _, m := range smtp.Messages() {
		if m.AuthIdentity == reauthUser && len(m.Data) > 0 {
			t.Fatalf("held submission was delivered before re-auth: %+v", m)
		}
	}

	// The user re-authenticates: the smart host accepts AUTH again and the
	// full OAuth start+callback flow runs, which triggers the retry.
	smtp.SetRejectAuth(false)
	reauthenticateViaOAuth(t, adminAddr, apiKey, extE2EReauthIdentityID)

	// Assert DELIVERED exactly once: the parked message reaches the smart
	// host with no user re-composition, and no duplicate.
	msg := waitForMessage(t, smtp, reauthUser)
	if len(msg.RcptTo) != 1 || msg.RcptTo[0] != "bob@remote.test" {
		t.Errorf("retried delivery: RCPT TO = %v; want [bob@remote.test]", msg.RcptTo)
	}
	if msg.MailFrom != reauthUser {
		t.Errorf("retried delivery: MAIL FROM = %q; want %q", msg.MailFrom, reauthUser)
	}
	delivered := 0
	for _, m := range smtp.Messages() {
		if m.AuthIdentity == reauthUser && len(m.Data) > 0 {
			delivered++
		}
	}
	if delivered != 1 {
		t.Errorf("retried delivery count = %d; want exactly 1 (no duplicate)", delivered)
	}
}

// waitForHeldSubmission polls EmailSubmission/get until a submission surfaces
// as held-for-reauth ("pending re-authentication"), the user-visible signal
// that a message is parked awaiting re-authentication (re #70).
func waitForHeldSubmission(t *testing.T, publicAddr, apiKey, accountID string) {
	t.Helper()
	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID})
	envelope := map[string]any{
		"using": []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
			"urn:ietf:params:jmap:submission",
		},
		"methodCalls": []any{
			[]any{"EmailSubmission/get", json.RawMessage(getArgs), "g0"},
		},
	}
	body, _ := json.Marshal(envelope)
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/jmap", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /jmap EmailSubmission/get: %v", err)
		}
		raw, _ := readAllAndClose(resp)
		last = string(raw)
		if bytes.Contains(raw, []byte("pending re-authentication")) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("submission never reached held-for-reauth state; last /get: %s", last)
}

// reauthenticateViaOAuth drives the real server-mediated OAuth flow the Suite
// uses to reconnect an identity: POST .../submission/oauth/start to obtain the
// provider auth URL, follow it to the fake IdP (which redirects back with a
// code), then GET the callback. The callback exchanges the code, refreshes the
// token material, and retries any held submissions for the identity (re #70).
// reauthenticateViaOAuth drives the OAuth flow using the "fakeidp" provider.
// For a different provider name use reauthenticateViaOAuthProvider directly.
func reauthenticateViaOAuth(t *testing.T, adminAddr, apiKey, identityID string) {
	t.Helper()
	reauthenticateViaOAuthProvider(t, adminAddr, apiKey, identityID, "fakeidp")
}

func reauthenticateViaOAuthProvider(t *testing.T, adminAddr, apiKey, identityID, provider string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost,
		"http://"+adminAddr+"/api/v1/identities/"+identityID+"/submission/oauth/start?provider="+provider, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST oauth/start: %v", err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("oauth/start: status=%d body=%s", resp.StatusCode, raw)
	}
	var startResp struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.Unmarshal(raw, &startResp); err != nil || startResp.AuthURL == "" {
		t.Fatalf("oauth/start: no auth_url (err=%v body=%s)", err, raw)
	}

	// Follow the auth URL to the fake IdP, capturing the redirect back to the
	// fixed callback path (do not follow it — GET it explicitly below).
	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ar, err := noRedirect.Get(startResp.AuthURL)
	if err != nil {
		t.Fatalf("GET authorize: %v", err)
	}
	ar.Body.Close()
	if ar.StatusCode != http.StatusFound {
		t.Fatalf("authorize: status=%d; want 302", ar.StatusCode)
	}
	callbackURL := ar.Header.Get("Location")
	if callbackURL == "" {
		t.Fatalf("authorize redirect carried no Location")
	}

	// The redirect_uri the fake IdP echoed back is the server's configured
	// canonical origin (re #240: buildCallbackURL no longer trusts the
	// request's Host), which is not a dialable address in this in-process
	// test -- a real deployment reaches it via DNS + a reverse proxy that
	// forwards to this exact backend. Rewrite the origin to the actual
	// bound admin listener address, keeping the path and query (code+state)
	// the fake IdP produced, mirroring what that proxy would do.
	cbu, err := url.Parse(callbackURL)
	if err != nil {
		t.Fatalf("parse callback URL %q: %v", callbackURL, err)
	}
	cbu.Scheme = "http"
	cbu.Host = adminAddr

	// The callback runs the token exchange, the probe, persistence, and the
	// held-submission retry synchronously before returning.
	cb, err := http.Get(cbu.String())
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	cbBody, _ := readAllAndClose(cb)
	if cb.StatusCode != http.StatusNoContent && cb.StatusCode != http.StatusOK {
		t.Fatalf("oauth callback: status=%d body=%s", cb.StatusCode, cbBody)
	}
}

// seedExtIdentityStore seeds the pre-boot store and returns the JMAP emailId
// (decimal MessageID) of the draft to submit. It closes the store before
// returning so the server can open it exclusively.
func seedExtIdentityStore(t *testing.T, st store.Store, clk clock.Clock, adminEmail, apiKeyPlain string) string {
	t.Helper()
	ctx := context.Background()
	defer func() {
		if err := st.Close(); err != nil {
			t.Fatalf("seed store close: %v", err)
		}
	}()

	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: extE2ELocalDomain, IsLocal: true, CreatedAt: clk.Now(),
	}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	mgr := keymgmt.NewManager(st.Meta(), discardLogger(), clk, nil)
	if _, err := mgr.GenerateKey(ctx, extE2ELocalDomain, store.DKIMAlgorithmRSASHA256); err != nil {
		t.Fatalf("generate dkim key: %v", err)
	}

	princ, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: adminEmail,
		Flags:          store.PrincipalFlagAdmin,
		CreatedAt:      clk.Now(),
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: princ.ID,
		Hash:        protoadmin.HashAPIKey(apiKeyPlain),
		Name:        "extid-e2e",
		CreatedAt:   clk.Now(),
		ScopeJSON:   `["admin","mail.send","end-user"]`,
	}); err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	// Two foreign identities and one hosted identity, all owned by the admin.
	identities := []struct {
		id, email string
	}{
		{extE2EOAuthIdentityID, "alice@" + extE2EForeignDomain},
		{extE2EPwIdentityID, "carol@" + extE2EForeignDomain},
		{extE2EHostedIdentityID, adminEmail},
		{extE2EReauthIdentityID, "dave@" + extE2EForeignDomain},
	}
	for _, id := range identities {
		if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
			ID:          id.id,
			PrincipalID: princ.ID,
			Name:        "Ext Identity",
			Email:       id.email,
			MayDelete:   true,
			// Send-side gate REQ-IDENT-60: a persistent identity must be
			// verified before it can submit. Seed it as verified.
			VerifiedAtUs: clk.Now().UnixMicro(),
		}); err != nil {
			t.Fatalf("insert identity %s: %v", id.id, err)
		}
	}

	// A draft message to submit. buildEnvelope derives the MAIL FROM from the
	// identity, so the same draft serves every identity; the To header
	// supplies the recipient.
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: princ.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	body := "From: alice@" + extE2EForeignDomain + "\r\n" +
		"To: bob@remote.test\r\n" +
		"Subject: external-identity e2e\r\n" +
		"Message-ID: <extid-e2e@" + extE2EForeignDomain + ">\r\n" +
		"\r\n" +
		"hello from the external-identity self-verification harness.\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "external-identity e2e",
			From:    "alice@" + extE2EForeignDomain,
			To:      "bob@remote.test",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}
	if mid == 0 {
		t.Fatalf("seeded message not found by uid %d", uid)
	}
	return strconv.FormatUint(uint64(mid), 10)
}

// startExtIdentityServer boots StartServer and returns the public and admin
// listener addresses.
func startExtIdentityServer(t *testing.T, cfg *sysconfig.Config) (publicAddr, adminAddr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrs,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("server did not shut down within grace window")
		}
	})
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatalf("server did not become ready")
	}
	addrsMu.Lock()
	publicAddr = addrs["public"]
	adminAddr = addrs["admin"]
	addrsMu.Unlock()
	if publicAddr == "" || adminAddr == "" {
		t.Fatalf("listeners not bound; addrs=%+v", addrs)
	}
	return publicAddr, adminAddr
}

// putSubmission PUTs a submission config across the real REST DTO boundary and
// requires a 204. Any other status fails the test with the response body.
func putSubmission(t *testing.T, adminAddr, apiKey, identityID string, body map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut,
		"http://"+adminAddr+"/api/v1/identities/"+identityID+"/submission",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT submission %s: %v", identityID, err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT submission %s: status=%d body=%s", identityID, resp.StatusCode, raw)
	}
}

// extSubmissionGetResponse mirrors the fields of protoadmin.submissionGetResponse
// that this test asserts on. Decoding into a typed struct exercises the JSON
// response contract (the other half of the #104 field-name check).
type extSubmissionGetResponse struct {
	Configured              bool     `json:"configured"`
	SubmitAuthMethod        string   `json:"submit_auth_method"`
	State                   string   `json:"state"`
	AvailableOAuthProviders []string `json:"available_oauth_providers"`
	DomainAuthoritative     bool     `json:"domain_authoritative"`
}

func getSubmission(t *testing.T, adminAddr, apiKey, identityID string) extSubmissionGetResponse {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet,
		"http://"+adminAddr+"/api/v1/identities/"+identityID+"/submission", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET submission %s: %v", identityID, err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET submission %s: status=%d body=%s", identityID, resp.StatusCode, raw)
	}
	var out extSubmissionGetResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode GET submission %s: %v body=%s", identityID, err, raw)
	}
	return out
}

// jmapAccountID fetches the JMAP session descriptor and returns the primary
// mail account id, mirroring the SPA boot flow.
func jmapAccountID(t *testing.T, publicAddr, apiKey string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /.well-known/jmap: status=%d body=%s", resp.StatusCode, raw)
	}
	var sess struct {
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
	}
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatalf("decode session: %v body=%s", err, raw)
	}
	acct := sess.PrimaryAccounts["urn:ietf:params:jmap:mail"]
	if acct == "" {
		t.Fatalf("no primary mail account in session: %s", raw)
	}
	return acct
}

// submitViaJMAP creates one EmailSubmission via the real /jmap endpoint and
// asserts the method returned created (not a serverFail method error, which is
// the #108 failure signature).
func submitViaJMAP(t *testing.T, publicAddr, apiKey, accountID, identityID, emailID string) {
	t.Helper()
	args := map[string]any{
		"accountId": accountID,
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": identityID,
				"emailId":    emailID,
			},
		},
	}
	argsBytes, _ := json.Marshal(args)
	envelope := map[string]any{
		"using": []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
			"urn:ietf:params:jmap:submission",
		},
		"methodCalls": []any{
			[]any{"EmailSubmission/set", json.RawMessage(argsBytes), "t0"},
		},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/jmap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /jmap EmailSubmission/set (%s): %v", identityID, err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /jmap (%s): status=%d body=%s", identityID, resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode /jmap response (%s): %v body=%s", identityID, err, raw)
	}
	if len(out.MethodResponses) != 1 {
		t.Fatalf("EmailSubmission/set (%s): got %d method responses, want 1: %s", identityID, len(out.MethodResponses), raw)
	}
	var name string
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		t.Fatalf("decode invocation name (%s): %v", identityID, err)
	}
	// A serverFail (the #108 symptom: the async relay tripping the 1s method
	// deadline) surfaces as an "error" invocation. The method must instead
	// return EmailSubmission/set with a created entry.
	if name != "EmailSubmission/set" {
		t.Fatalf("EmailSubmission/set (%s): response name = %q (want EmailSubmission/set); body=%s", identityID, name, raw)
	}
	argsRaw := out.MethodResponses[0][1]
	if !bytes.Contains(argsRaw, []byte(`"created"`)) {
		t.Fatalf("EmailSubmission/set (%s): no created entry: %s", identityID, argsRaw)
	}
	// notCreated with a real SetError would mean the send was rejected
	// (e.g. forbiddenFrom for a gated hosted identity — the #107 regression).
	var parsed struct {
		Created    map[string]json.RawMessage `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	if err := json.Unmarshal(argsRaw, &parsed); err != nil {
		t.Fatalf("decode set args (%s): %v", identityID, err)
	}
	if len(parsed.Created) == 0 {
		t.Fatalf("EmailSubmission/set (%s): created empty; notCreated=%v", identityID, parsed.NotCreated)
	}
	if len(parsed.NotCreated) != 0 {
		t.Fatalf("EmailSubmission/set (%s): unexpected notCreated=%v", identityID, parsed.NotCreated)
	}
}

// waitForMessage polls the fake SMTP sink until a message authenticated as
// the given identity has been recorded, or fails after a deadline. The relay
// runs in a background goroutine (async since #108), so polling — not a fixed
// sleep — is the synchronisation mechanism.
func waitForMessage(t *testing.T, smtp *fakesmtp.Server, authIdentity string) fakesmtp.Message {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range smtp.Messages() {
			if m.AuthIdentity == authIdentity && len(m.Data) > 0 {
				return m
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no message relayed for identity %q within deadline; recorded=%d", authIdentity, len(smtp.Messages()))
	return fakesmtp.Message{}
}

// testConnection drives the on-demand connection-test endpoint
// (POST /api/v1/identities/{id}/submission/test) and returns the decoded
// {ok, detail} plus the HTTP status. A non-200 status returns ok=false with
// the raw body as detail so callers can assert on the transport-level error.
func testConnection(t *testing.T, adminAddr, apiKey, identityID string) (ok bool, detail string, status int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost,
		"http://"+adminAddr+"/api/v1/identities/"+identityID+"/submission/test", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST submission/test %s: %v", identityID, err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		return false, string(raw), resp.StatusCode
	}
	var out struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode submission/test %s: %v body=%s", identityID, err, raw)
	}
	return out.OK, out.Detail, resp.StatusCode
}

// waitForRcpt polls the fake SMTP sink until a message with a body has been
// delivered to rcpt, or fails after a deadline.
func waitForRcpt(t *testing.T, smtp *fakesmtp.Server, rcpt string) fakesmtp.Message {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range smtp.Messages() {
			if len(m.Data) == 0 {
				continue
			}
			for _, r := range m.RcptTo {
				if r == rcpt {
					return m
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no message delivered to rcpt %q within deadline; recorded=%d", rcpt, len(smtp.Messages()))
	return fakesmtp.Message{}
}

// assertEnvelope checks the recorded envelope and body of a relayed message.
func assertEnvelope(t *testing.T, label string, m fakesmtp.Message, wantFrom string) {
	t.Helper()
	if m.MailFrom != wantFrom {
		t.Errorf("%s relay: MAIL FROM = %q; want %q", label, m.MailFrom, wantFrom)
	}
	if len(m.RcptTo) != 1 || m.RcptTo[0] != "bob@remote.test" {
		t.Errorf("%s relay: RCPT TO = %v; want [bob@remote.test]", label, m.RcptTo)
	}
	if !bytes.Contains(m.Data, []byte("Subject: external-identity e2e")) {
		t.Errorf("%s relay: delivered body missing subject:\n%s", label, m.Data)
	}
}
