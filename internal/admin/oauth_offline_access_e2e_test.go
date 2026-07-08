package admin

// oauth_offline_access_e2e_test.go exercises herold issue #131: two bug paths
// in the external-SMTP OAuth flow for Gmail-class providers.
//
// Criterion A (offline access): the authorization URL must include
// extra_auth_params configured by the operator (e.g. access_type=offline for
// Google). Without them the provider never issues a refresh token and every
// access token eventually expires permanently.
//
// Criterion B (operator credentials at refresh): accessToken must look up
// operator-level client credentials from OAuthCredsByTokenURL (keyed by the
// row's token endpoint URL) rather than using the row's OAuthClientID (= user
// email) and nil OAuthClientSecretCT. Before the fix, token refresh fails with
// invalid_client because the email is not a valid client_id.
//
// Runs on SQLite always and on Postgres when HEROLD_PG_DSN is set.

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testfakes/fakeidp"
	"github.com/hanshuebner/herold/internal/testfakes/fakesmtp"
)

const (
	offlineE2ELocalDomain   = "offline.local"
	offlineE2EForeignDomain = "offline-ext.test"

	// offlineE2EOfflineIdentityID is used for criterion A: drive OAuth flow
	// and verify that a refresh token is stored when access_type=offline is
	// included in the authorization request.
	offlineE2EOfflineIdentityID = "910001"
	// offlineE2EOperatorCredsID is used for criterion B: the pre-seeded row
	// has a valid refresh token but nil OAuthClientSecretCT; the server must
	// use OAuthCredsByTokenURL to refresh successfully.
	offlineE2EOperatorCredsID = "910002"

	offlineE2EDataKeyEnv     = "HEROLD_OFFLINE_E2E_DATA_KEY"
	offlineE2EOAuthSecretEnv = "HEROLD_OFFLINE_E2E_OAUTH_SECRET"
)

func offlineE2EDataKeyHex() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*11 + 7)
	}
	return hex.EncodeToString(key)
}

func TestOAuthOfflineAccess_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("oauth offline access e2e test (re #131)")
	}
	t.Run("sqlite", func(t *testing.T) { runOAuthOfflineAccessE2E(t, "sqlite", "") })
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) { runOAuthOfflineAccessE2E(t, "postgres", dsn) })
	}
}

func runOAuthOfflineAccessE2E(t *testing.T, backend, pgDSN string) {
	t.Helper()
	dir := t.TempDir()

	// Fake IdP with RequireOfflineAccess=true: only issues a refresh_token
	// when access_type=offline is present in the authorization request.
	// This mirrors Google's behaviour for Gmail (re #131).
	idp := fakeidp.New(t, fakeidp.Options{RequireOfflineAccess: true})
	smtp := fakesmtp.New(t, fakesmtp.Options{
		Security: fakesmtp.Plain,
		Hostname: "smtp.offline-ext.test",
	})

	t.Setenv(offlineE2EDataKeyEnv, offlineE2EDataKeyHex())
	t.Setenv(offlineE2EOAuthSecretEnv, idp.ClientSecret())

	dataKeyBytes, err := hex.DecodeString(offlineE2EDataKeyHex())
	if err != nil {
		t.Fatalf("decode data key: %v", err)
	}

	// Criterion B pre-seed: obtain a real refresh token from the fake IdP
	// using AuthenticateOffline before the server boots. The seeded submission
	// row will carry this token but nil OAuthClientSecretCT.
	operatorCredsUser := "eve@" + offlineE2EForeignDomain
	seedTok := idp.AuthenticateOffline(t,
		"http://localhost/oauth/callback", "state-preseed", "https://mail.google.com/")

	const offlineAPIKey = protoadmin.APIKeyPrefix + "offline_e2e_admin_key_0000000000001"
	adminEmail := "admin@" + offlineE2ELocalDomain

	// Storage config and store opener/reader closures.
	var storageTOML string
	// openSeed opens a store and (for Postgres) truncates. Used before boot.
	var openSeed func() store.Store
	// openRead opens a store without truncating. Used after boot for assertions.
	var openRead func() store.Store

	clk := clock.NewReal()
	switch backend {
	case "sqlite":
		dbPath := filepath.Join(dir, "db.sqlite")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"sqlite\"\n[server.storage.sqlite]\npath = %q\n", dbPath)
		newSQLiteStore := func() store.Store {
			st, err := storesqlite.Open(context.Background(), dbPath, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			return st
		}
		openSeed = newSQLiteStore
		openRead = newSQLiteStore
	case "postgres":
		blobDir := filepath.Join(dir, "blobs")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"postgres\"\n[server.storage.postgres]\ndsn = %q\nblob_dir = %q\n", pgDSN, blobDir)
		openSeed = func() store.Store {
			st, err := storepg.Open(context.Background(), pgDSN, blobDir, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storepg.Open (seed): %v", err)
			}
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
		openRead = func() store.Store {
			st, err := storepg.Open(context.Background(), pgDSN, blobDir, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storepg.Open (read): %v", err)
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

[server.oauth_providers.fakeidp.extra_auth_params]
access_type = "offline"

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
		offlineE2ELocalDomain, dir, filepath.Join(dir, "ports.toml"),
		certPath, keyPath,
		storageTOML,
		offlineE2EDataKeyEnv,
		idp.ClientID(), offlineE2EOAuthSecretEnv, idp.AuthorizeURL(), idp.TokenURL(),
		certPath, keyPath, certPath, keyPath)
	if err := os.WriteFile(systomlPath, []byte(systoml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Seed principal, identities, message, and the criterion-B submission row.
	emailID := seedOfflineE2EStore(t, openSeed(), clk, adminEmail, offlineAPIKey,
		smtp, dataKeyBytes, operatorCredsUser, seedTok.RefreshToken, idp.TokenURL())

	// Boot the server against the seeded store.
	publicAddr, adminAddr := startOfflineE2EServer(t, cfg)
	accountID := jmapAccountID(t, publicAddr, offlineAPIKey)

	// ---- Criterion A: extra_auth_params → refresh token stored after OAuth ----
	// Drive the full start→callback flow. The fake IdP (RequireOfflineAccess=true)
	// only issues a refresh_token when access_type=offline is in the auth URL.
	//
	// Before fix: handleOAuthStart ignores extra_auth_params → no access_type in
	// auth URL → no refresh_token in token response → OAuthRefreshCT remains nil.
	// After fix: extra_auth_params are appended to the auth URL → fakeidp sees
	// access_type=offline → refresh_token issued → OAuthRefreshCT is sealed.
	reauthenticateViaOAuth(t, adminAddr, offlineAPIKey, offlineE2EOfflineIdentityID)

	// Read the stored row and verify the refresh token was persisted.
	// WAL mode (SQLite) and standard isolation (Postgres) allow a concurrent
	// reader after the synchronous callback completes.
	func() {
		st := openRead()
		defer func() { _ = st.Close() }()
		row, err := st.Meta().GetIdentitySubmission(context.Background(), offlineE2EOfflineIdentityID)
		if err != nil {
			t.Fatalf("criterion A: GetIdentitySubmission: %v", err)
		}
		if len(row.OAuthRefreshCT) == 0 {
			t.Errorf("criterion A: OAuthRefreshCT is nil after OAuth callback; " +
				"handleOAuthStart must append extra_auth_params (access_type=offline) " +
				"to the authorization URL so the provider issues a refresh token (re #131)")
		}
	}()

	// ---- Criterion B: operator creds → on-demand refresh succeeds at send ----
	// The pre-seeded row (offlineE2EOperatorCredsID) has RefreshDue in the past
	// and nil OAuthClientSecretCT. accessToken must look up the operator
	// credentials from OAuthCredsByTokenURL[sub.OAuthTokenEndpoint].
	//
	// Before fix: creds.ClientID = row.OAuthClientID (email), creds.ClientSecret
	// = "" → fake IdP returns invalid_client → OutcomeAuthFailed → message parked
	// → waitForMessage times out → TEST FAILS (RED).
	// After fix: OAuthCredsByTokenURL carries the operator creds resolved from
	// [server.oauth_providers.fakeidp] → refresh succeeds → send reaches smtp.
	submitViaJMAP(t, publicAddr, offlineAPIKey, accountID, offlineE2EOperatorCredsID, emailID)
	msg := waitForMessage(t, smtp, operatorCredsUser)
	if msg.AuthMechanism != "XOAUTH2" {
		t.Errorf("criterion B: AuthMechanism = %q; want XOAUTH2", msg.AuthMechanism)
	}
}

// seedOfflineE2EStore seeds principal, JMAP identities, a draft message, and
// the criterion-B identity_submission row. It closes the store before returning.
func seedOfflineE2EStore(
	t *testing.T,
	st store.Store,
	clk clock.Clock,
	adminEmail, apiKeyPlain string,
	smtp *fakesmtp.Server,
	dataKey []byte,
	operatorCredsUser, refreshToken, tokenEndpoint string,
) string {
	t.Helper()
	ctx := context.Background()
	defer func() {
		if err := st.Close(); err != nil {
			t.Fatalf("seedOfflineE2EStore: close: %v", err)
		}
	}()

	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: offlineE2ELocalDomain, IsLocal: true, CreatedAt: clk.Now(),
	}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	mgr := keymgmt.NewManager(st.Meta(), discardLogger(), clk, nil)
	if _, err := mgr.GenerateKey(ctx, offlineE2ELocalDomain, store.DKIMAlgorithmRSASHA256); err != nil {
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
		Name:        "offline-e2e",
		CreatedAt:   clk.Now(),
		ScopeJSON:   `["admin","mail.send","end-user"]`,
	}); err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	offlineUser := "olivia@" + offlineE2EForeignDomain
	for _, id := range []struct{ id, email string }{
		{offlineE2EOfflineIdentityID, offlineUser},
		{offlineE2EOperatorCredsID, operatorCredsUser},
	} {
		if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
			ID:           id.id,
			PrincipalID:  princ.ID,
			Name:         "Offline E2E Identity",
			Email:        id.email,
			MayDelete:    true,
			VerifiedAtUs: clk.Now().UnixMicro(),
		}); err != nil {
			t.Fatalf("insert identity %s: %v", id.id, err)
		}
	}

	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: princ.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	body := "From: " + offlineUser + "\r\n" +
		"To: bob@remote.test\r\n" +
		"Subject: offline access e2e\r\n" +
		"Message-ID: <offline-e2e@" + offlineE2EForeignDomain + ">\r\n" +
		"\r\n" +
		"hello from the offline-access e2e harness.\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "offline access e2e",
			From:    offlineUser,
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

	// Criterion A: pre-seed a minimal submission row for the offline-access
	// identity so the OAuth callback's probe knows which SMTP host to use.
	// The access token is a stub; the OAuth callback will overwrite the token
	// material. The SMTP host/port/security survive through the re-auth path.
	stubAtCT, err := secrets.Seal(dataKey, []byte("stub-access-token"))
	if err != nil {
		t.Fatalf("seal stub access token: %v", err)
	}
	subA := store.IdentitySubmission{
		IdentityID:       offlineE2EOfflineIdentityID,
		SubmitHost:       smtp.Host(),
		SubmitPort:       smtp.Port(),
		SubmitSecurity:   "none",
		SubmitAuthMethod: "oauth2",
		OAuthAccessCT:    stubAtCT,
		OAuthClientID:    offlineUser,
		State:            store.IdentitySubmissionStateOK,
		StateAt:          clk.Now(),
		CreatedAt:        clk.Now(),
	}
	if err := st.Meta().UpsertIdentitySubmission(ctx, subA); err != nil {
		t.Fatalf("upsert criterion-A submission stub: %v", err)
	}

	// Criterion B: pre-seeded submission row with a valid refresh token,
	// RefreshDue in the past, and nil OAuthClientSecretCT. Simulates a row
	// written before the operator-creds fix (re #131).
	rtCT, err := secrets.Seal(dataKey, []byte(refreshToken))
	if err != nil {
		t.Fatalf("seal refresh token: %v", err)
	}
	atCT, err := secrets.Seal(dataKey, []byte("stale-access-token"))
	if err != nil {
		t.Fatalf("seal stale access token: %v", err)
	}
	sub := store.IdentitySubmission{
		IdentityID:         offlineE2EOperatorCredsID,
		SubmitHost:         smtp.Host(),
		SubmitPort:         smtp.Port(),
		SubmitSecurity:     "none",
		SubmitAuthMethod:   "oauth2",
		OAuthAccessCT:      atCT,
		OAuthRefreshCT:     rtCT,
		OAuthTokenEndpoint: tokenEndpoint,
		OAuthClientID:      operatorCredsUser, // XOAUTH2 user= field (correct)
		// OAuthClientSecretCT intentionally nil: simulates pre-fix row.
		RefreshDue: clk.Now().Add(-10 * time.Minute), // immediately due
		State:      store.IdentitySubmissionStateOK,
		StateAt:    clk.Now(),
		CreatedAt:  clk.Now(),
	}
	if err := st.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("upsert criterion-B submission: %v", err)
	}

	return fmt.Sprintf("%d", mid)
}

func startOfflineE2EServer(t *testing.T, cfg *sysconfig.Config) (publicAddr, adminAddr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger: slog.New(slog.NewTextHandler(os.Stderr,
				&slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrs,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("offline e2e: StartServer exited: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("offline e2e: server did not shut down")
		}
	})
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatalf("offline e2e: server not ready")
	}
	addrsMu.Lock()
	publicAddr = addrs["public"]
	adminAddr = addrs["admin"]
	addrsMu.Unlock()
	if publicAddr == "" || adminAddr == "" {
		t.Fatalf("offline e2e: listeners not bound; addrs=%+v", addrs)
	}
	return
}

// TestOAuthGmailAutoOffline_E2E verifies that a provider named "gmail" receives
// access_type=offline and prompt=consent automatically — with NO extra_auth_params
// in system.toml — so Gmail issues a refresh token without any operator config.
//
// The test is RED without providerDefaultAuthParams("gmail") and GREEN after.
func TestOAuthGmailAutoOffline_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("gmail auto offline access e2e test (re #131)")
	}
	t.Run("sqlite", func(t *testing.T) { runGmailAutoOfflineE2E(t, "sqlite", "") })
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) { runGmailAutoOfflineE2E(t, "postgres", dsn) })
	}
}

// gmailAutoIdentityID is the JMAP identity id used in TestOAuthGmailAutoOffline_E2E.
const gmailAutoIdentityID = "920001"

func runGmailAutoOfflineE2E(t *testing.T, backend, pgDSN string) {
	t.Helper()
	dir := t.TempDir()

	// RequireOfflineAccess=true: the fake IdP only issues a refresh_token when
	// access_type=offline is present in the authorization URL. Without the
	// built-in Gmail defaults the refresh_token is absent and OAuthRefreshCT
	// stays nil.
	idp := fakeidp.New(t, fakeidp.Options{RequireOfflineAccess: true})
	// The OAuth callback probes the SMTP endpoint to confirm reachability.
	// A fakesmtp server supplies a real listener for the probe.
	smtpProbe := fakesmtp.New(t, fakesmtp.Options{
		Security: fakesmtp.Plain,
		Hostname: "smtp.gmail-auto-probe.test",
	})

	const gmailAutoAPIKey = protoadmin.APIKeyPrefix + "gmail_auto_offline_e2e_key_00001"
	const gmailAutoAdminEmail = "admin@gmail-auto.local"

	dataKeyBytes, err := hex.DecodeString(offlineE2EDataKeyHex())
	if err != nil {
		t.Fatalf("decode data key: %v", err)
	}

	t.Setenv(offlineE2EDataKeyEnv, offlineE2EDataKeyHex())
	// Reuse the same secret env var name for simplicity; both tests share the
	// same key derivation, they just run in separate processes / t.Setenv scopes.
	t.Setenv(offlineE2EOAuthSecretEnv, idp.ClientSecret())

	var storageTOML string
	var openSeed func() store.Store
	var openRead func() store.Store

	clk := clock.NewReal()
	switch backend {
	case "sqlite":
		dbPath := filepath.Join(dir, "db.sqlite")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"sqlite\"\n[server.storage.sqlite]\npath = %q\n", dbPath)
		newSQLiteStore := func() store.Store {
			st, err := storesqlite.Open(context.Background(), dbPath, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			return st
		}
		openSeed = newSQLiteStore
		openRead = newSQLiteStore
	case "postgres":
		blobDir := filepath.Join(dir, "blobs")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"postgres\"\n[server.storage.postgres]\ndsn = %q\nblob_dir = %q\n", pgDSN, blobDir)
		openSeed = func() store.Store {
			st, err := storepg.Open(context.Background(), pgDSN, blobDir, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storepg.Open (seed): %v", err)
			}
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
		openRead = func() store.Store {
			st, err := storepg.Open(context.Background(), pgDSN, blobDir, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storepg.Open (read): %v", err)
			}
			return st
		}
	default:
		t.Fatalf("unknown backend %q", backend)
	}

	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	systomlPath := filepath.Join(dir, "system.toml")

	// Note: the provider name is "gmail" and there is NO extra_auth_params block.
	// The built-in defaults (access_type=offline, prompt=consent) must fire
	// automatically so the fake IdP issues a refresh_token.
	systoml := fmt.Sprintf(`
[server]
hostname = "gmail-auto.local"
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

[server.oauth_providers.gmail]
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
		dir, filepath.Join(dir, "ports.toml"),
		certPath, keyPath,
		storageTOML,
		offlineE2EDataKeyEnv,
		idp.ClientID(), offlineE2EOAuthSecretEnv, idp.AuthorizeURL(), idp.TokenURL(),
		certPath, keyPath, certPath, keyPath)

	if err := os.WriteFile(systomlPath, []byte(systoml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Seed domain, principal, api key, and one JMAP identity.
	gmailAutoUser := "alice@gmail.com"
	func() {
		st := openSeed()
		defer func() { _ = st.Close() }()
		ctx := context.Background()

		if err := st.Meta().InsertDomain(ctx, store.Domain{
			Name: "gmail-auto.local", IsLocal: true, CreatedAt: clk.Now(),
		}); err != nil {
			t.Fatalf("insert domain: %v", err)
		}
		mgr := keymgmt.NewManager(st.Meta(), discardLogger(), clk, nil)
		if _, err := mgr.GenerateKey(ctx, "gmail-auto.local", store.DKIMAlgorithmRSASHA256); err != nil {
			t.Fatalf("generate dkim key: %v", err)
		}
		princ, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: gmailAutoAdminEmail,
			Flags:          store.PrincipalFlagAdmin,
			CreatedAt:      clk.Now(),
		})
		if err != nil {
			t.Fatalf("insert principal: %v", err)
		}
		if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
			PrincipalID: princ.ID,
			Hash:        protoadmin.HashAPIKey(gmailAutoAPIKey),
			Name:        "gmail-auto-e2e",
			CreatedAt:   clk.Now(),
			ScopeJSON:   `["admin","mail.send","end-user"]`,
		}); err != nil {
			t.Fatalf("insert api key: %v", err)
		}
		if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
			ID:           gmailAutoIdentityID,
			PrincipalID:  princ.ID,
			Name:         "Gmail Auto Test",
			Email:        gmailAutoUser,
			MayDelete:    true,
			VerifiedAtUs: clk.Now().UnixMicro(),
		}); err != nil {
			t.Fatalf("insert identity: %v", err)
		}

		// Pre-seed a minimal submission row so the OAuth callback probe can
		// connect to the fake SMTP server and verify reachability.
		stubAtCT, err := secrets.Seal(dataKeyBytes, []byte("stub-access-token-gmail-auto"))
		if err != nil {
			t.Fatalf("seal stub access token: %v", err)
		}
		subA := store.IdentitySubmission{
			IdentityID:       gmailAutoIdentityID,
			SubmitHost:       smtpProbe.Host(),
			SubmitPort:       smtpProbe.Port(),
			SubmitSecurity:   "none",
			SubmitAuthMethod: "oauth2",
			OAuthAccessCT:    stubAtCT,
			OAuthClientID:    gmailAutoUser,
			State:            store.IdentitySubmissionStateOK,
			StateAt:          clk.Now(),
			CreatedAt:        clk.Now(),
		}
		if err := st.Meta().UpsertIdentitySubmission(ctx, subA); err != nil {
			t.Fatalf("upsert gmail-auto submission stub: %v", err)
		}
	}()

	// Boot the server.
	_, adminAddr := startOfflineE2EServer(t, cfg)

	// Drive the OAuth start → callback flow using provider "gmail".
	// The handler must include access_type=offline automatically from the
	// built-in Gmail defaults (no extra_auth_params in config).
	reauthenticateViaOAuthProvider(t, adminAddr, gmailAutoAPIKey, gmailAutoIdentityID, "gmail")

	// Read the stored row. OAuthRefreshCT must be non-nil.
	// If it is nil the built-in Gmail defaults were not applied (RED).
	func() {
		st := openRead()
		defer func() { _ = st.Close() }()
		row, err := st.Meta().GetIdentitySubmission(context.Background(), gmailAutoIdentityID)
		if err != nil {
			t.Fatalf("GetIdentitySubmission: %v", err)
		}
		if len(row.OAuthRefreshCT) == 0 {
			t.Errorf("OAuthRefreshCT is nil after OAuth callback with provider=gmail and NO " +
				"extra_auth_params; providerDefaultAuthParams(\"gmail\") must supply " +
				"access_type=offline so the provider issues a refresh token (re #131)")
		}
	}()
}
