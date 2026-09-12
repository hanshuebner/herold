package e2e

// dev_sink_reply_attachment_test.go is the acceptance test for issue #336:
// a client can submit mail from the dev instance's seeded foreign identity
// (alice-work@foreign.example, the identity scripts/dev-instance.sh seeds
// with HEROLD_DEV_EXTERNAL_SUBMISSION=1 -- see internal/admin/cmd_dev.go's
// devIdentityWorkingExt/devSeedForeignDomain), have it relayed to the fake
// SMTP sink (internal/testfakes/fakesmtp), and read the delivered message's
// raw bytes back over the sink's HTTP status API to check headers and MIME
// structure -- not just the envelope.
//
// It boots a real herold server (admin.StartServer) configured exactly the
// way scripts/dev-instance.sh configures HEROLD_DEV_EXTERNAL_SUBMISSION=1
// ([server.external_submission] enabled), pre-seeds the store with the same
// identity + IdentitySubmission shape `herold dev seed-external-identities
// --sink-addr` produces for the working-external identity, then drives
// EmailSubmission/set on the real /jmap endpoint for a reply (In-Reply-To /
// References set) carrying a file attachment. It polls the fake sink's HTTP
// API -- mounted the same way heroldfakesmtp exposes it -- until the message
// arrives, fetches it via GET /messages/{n}/raw, and asserts the delivered
// bytes carry the reply headers and the attachment part. GET /messages?raw=1
// is exercised too, asserting its base64 "raw" field decodes to the same
// bytes.
//
// Runs on SQLite always and on Postgres when HEROLD_PG_DSN is set.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/admin"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testfakes/fakesmtp"
)

const (
	// devSinkLocalDomain mirrors scripts/dev-instance.sh's SEED_DOMAIN
	// default -- the server's authoritative domain.
	devSinkLocalDomain = "example.local"
	// devSinkForeignDomain mirrors internal/admin/cmd_dev.go's
	// devSeedForeignDomain -- deliberately not registered as local, so
	// the seeded identity must route through external submission.
	devSinkForeignDomain = "foreign.example"
	// devSinkIdentityID and devSinkIdentityEmail mirror cmd_dev.go's
	// devIdentityWorkingExt / the "working-external" seed entry: the
	// exact identity `herold dev seed-external-identities --sink-addr`
	// seeds for alice@example.local when HEROLD_DEV_EXTERNAL_SUBMISSION=1
	// is set.
	devSinkIdentityID    = "800002"
	devSinkIdentityEmail = "alice-work@" + devSinkForeignDomain

	devSinkDataKeyEnv = "HEROLD_DEVSINK_E2E_DATA_KEY"
)

// devSinkDataKeyHex returns a fixed 32-byte at-rest data key (hex-encoded)
// so no secret-shaped literal lives in the source.
func devSinkDataKeyHex() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*11 + 5)
	}
	return hex.EncodeToString(key)
}

// TestDevSinkExternalSubmission_ReplyWithAttachment is the Go acceptance
// test for issue #336. See the file header for what it exercises.
func TestDevSinkExternalSubmission_ReplyWithAttachment(t *testing.T) {
	if testing.Short() {
		t.Skip("dev-sink external-submission e2e wiring test")
	}
	t.Run("sqlite", func(t *testing.T) { runDevSinkReplyE2E(t, "sqlite", "") })
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) { runDevSinkReplyE2E(t, "postgres", dsn) })
	}
}

func runDevSinkReplyE2E(t *testing.T, backend, pgDSN string) {
	dir := t.TempDir()

	// The fake SMTP sink, its raw bytes and envelope exposed over HTTP the
	// same way cmd/heroldfakesmtp mounts fakesmtp.Server.HTTPHandler() on
	// its own listener.
	smtp := fakesmtp.New(t, fakesmtp.Options{Security: fakesmtp.Plain, Hostname: "smtp.dev-sink.test"})
	sinkHTTP := httptest.NewServer(smtp.HTTPHandler())
	t.Cleanup(sinkHTTP.Close)

	t.Setenv(devSinkDataKeyEnv, devSinkDataKeyHex())

	var storageTOML string
	var openPreseed func() store.Store
	clk := clock.NewReal()
	switch backend {
	case "sqlite":
		dbPath := filepath.Join(dir, "db.sqlite")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"sqlite\"\n[server.storage.sqlite]\npath = %q\n", dbPath)
		openPreseed = func() store.Store {
			st, err := storesqlite.Open(context.Background(), dbPath, discardTestLogger(), clk)
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			return st
		}
	case "postgres":
		blobDir := filepath.Join(dir, "blobs")
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"postgres\"\n[server.storage.postgres]\ndsn = %q\nblob_dir = %q\n", pgDSN, blobDir)
		openPreseed = func() store.Store {
			st, err := storepg.Open(context.Background(), pgDSN, blobDir, discardTestLogger(), clk)
			if err != nil {
				t.Fatalf("storepg.Open: %v", err)
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
	default:
		t.Fatalf("unknown backend %q", backend)
	}

	certPath, keyPath := generateDevSinkCert(t, dir, []string{"localhost"})
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
		devSinkLocalDomain, dir, filepath.Join(dir, "ports.toml"),
		certPath, keyPath,
		storageTOML,
		devSinkDataKeyEnv,
		certPath, keyPath, certPath, keyPath)
	if err := os.WriteFile(systomlPath, []byte(systoml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	const apiKeyPlain = protoadmin.APIKeyPrefix + "devsink_e2e_alice_key_0000000001"
	aliceEmail := "alice@" + devSinkLocalDomain
	const originalMessageID = "<original-336@" + devSinkForeignDomain + ">"
	const replyMessageID = "<reply-336@" + devSinkForeignDomain + ">"
	const attachmentBody = "attachment contents for issue 336\n"
	const recipient = "bob@remote.test"

	emailID := seedDevSinkStore(t, openPreseed(), clk, aliceEmail, apiKeyPlain, smtp.Host(), smtp.Port(),
		originalMessageID, replyMessageID, recipient, attachmentBody)

	publicAddr, _ := startDevSinkServer(t, cfg)
	accountID := devSinkAccountID(t, publicAddr, apiKeyPlain)

	devSinkSubmitViaJMAP(t, publicAddr, apiKeyPlain, accountID, devSinkIdentityID, emailID)

	// Bounded poll: no fixed sleep. Wait for the message to reach the sink
	// via its /count endpoint, the same signal a puppeteer/curl flow uses.
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(sinkHTTP.URL + "/count")
		if err == nil {
			var out struct {
				Count int `json:"count"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			_ = resp.Body.Close()
			if out.Count >= 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("message never reached the fake SMTP sink within the deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}

	// GET /messages/{n}/raw (n=1, the only recorded message) -- REQ for
	// issue #336's Work item 1 and the acceptance criterion.
	resp, err := http.Get(sinkHTTP.URL + "/messages/1/raw")
	if err != nil {
		t.Fatalf("GET /messages/1/raw: %v", err)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /messages/1/raw: status=%d body=%s", resp.StatusCode, rawBody)
	}
	assertDevSinkReply(t, "GET /messages/1/raw", rawBody, originalMessageID, attachmentBody)

	// 404 for an out-of-range index.
	resp, err = http.Get(sinkHTTP.URL + "/messages/2/raw")
	if err != nil {
		t.Fatalf("GET /messages/2/raw: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /messages/2/raw: status=%d; want 404", resp.StatusCode)
	}

	// GET /messages?raw=1 -- the base64 "raw" field must decode to the
	// same bytes fetched via /messages/{n}/raw above.
	resp, err = http.Get(sinkHTTP.URL + "/messages?raw=1")
	if err != nil {
		t.Fatalf("GET /messages?raw=1: %v", err)
	}
	var records []struct {
		MailFrom string `json:"mail_from"`
		Raw      string `json:"raw"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		t.Fatalf("decode /messages?raw=1: %v", err)
	}
	_ = resp.Body.Close()
	if len(records) != 1 {
		t.Fatalf("/messages?raw=1: got %d records; want 1", len(records))
	}
	if records[0].MailFrom != devSinkIdentityEmail {
		t.Errorf("/messages?raw=1: mail_from = %q; want %q", records[0].MailFrom, devSinkIdentityEmail)
	}
	decodedRaw, err := base64.StdEncoding.DecodeString(records[0].Raw)
	if err != nil {
		t.Fatalf("decode raw field: %v", err)
	}
	if !bytes.Equal(decodedRaw, rawBody) {
		t.Errorf("/messages?raw=1 raw field does not match /messages/1/raw body")
	}
}

// assertDevSinkReply asserts the delivered raw message carries the reply
// headers and the attachment part.
func assertDevSinkReply(t *testing.T, label string, raw []byte, originalMessageID, attachmentBody string) {
	t.Helper()
	s := string(raw)
	if !strings.Contains(s, "In-Reply-To: "+originalMessageID) {
		t.Errorf("%s: missing In-Reply-To header referencing %s:\n%s", label, originalMessageID, s)
	}
	if !strings.Contains(s, "References: "+originalMessageID) {
		t.Errorf("%s: missing References header referencing %s:\n%s", label, originalMessageID, s)
	}
	if !strings.Contains(s, `Content-Disposition: attachment; filename="notes.txt"`) {
		t.Errorf("%s: missing attachment Content-Disposition:\n%s", label, s)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte(attachmentBody))
	if !strings.Contains(s, wantB64) {
		t.Errorf("%s: attachment part does not carry the expected base64 payload:\n%s", label, s)
	}
}

// seedDevSinkStore seeds the pre-boot store the way scripts/dev-instance.sh
// + `herold dev seed-external-identities --sink-addr` seed alice's
// working-external identity, plus a draft reply (with In-Reply-To /
// References and a file attachment) in alice's Drafts mailbox. Returns the
// JMAP emailId (decimal MessageID) of the draft to submit. Closes the store
// before returning so the server can open it exclusively.
func seedDevSinkStore(
	t *testing.T, st store.Store, clk clock.Clock, aliceEmail, apiKeyPlain, sinkHost string, sinkPort int,
	originalMessageID, replyMessageID, recipient, attachmentBody string,
) string {
	t.Helper()
	ctx := context.Background()
	defer func() {
		if err := st.Close(); err != nil {
			t.Fatalf("seed store close: %v", err)
		}
	}()

	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: devSinkLocalDomain, IsLocal: true, CreatedAt: clk.Now(),
	}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}

	alice, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: aliceEmail,
		CreatedAt:      clk.Now(),
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: alice.ID,
		Hash:        protoadmin.HashAPIKey(apiKeyPlain),
		Name:        "devsink-e2e",
		CreatedAt:   clk.Now(),
		ScopeJSON:   `["mail.send","end-user"]`,
	}); err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	now := clk.Now()
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           devSinkIdentityID,
		PrincipalID:  alice.ID,
		Name:         "Alice (working-external)",
		Email:        devSinkIdentityEmail,
		MayDelete:    true,
		VerifiedAtUs: now.UnixMicro(),
	}); err != nil {
		t.Fatalf("insert identity: %v", err)
	}

	dataKey, err := secrets.LoadDataKey(sysconfig.SecretsConfig{DataKeyRef: "$" + devSinkDataKeyEnv})
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	pwCT, err := secrets.Seal(dataKey, []byte("dev-placeholder-password"))
	if err != nil {
		t.Fatalf("seal placeholder password: %v", err)
	}
	if err := st.Meta().UpsertIdentitySubmission(ctx, store.IdentitySubmission{
		IdentityID:       devSinkIdentityID,
		SubmitHost:       sinkHost,
		SubmitPort:       sinkPort,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       pwCT,
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("upsert identity submission: %v", err)
	}

	// Alias the working-external address to alice, mirroring the fix in
	// internal/admin/cmd_dev.go's runDevSeedExternalIdentities (re #336):
	// auth/sendpolicy.CheckFrom's ownership gate only ever passes for a
	// CanonicalEmail match or an alias row, never for a foreign-domain
	// Identity alone, so without this alias only an admin principal could
	// submit from this identity.
	if _, err := st.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "alice-work",
		Domain:          devSinkForeignDomain,
		TargetPrincipal: alice.ID,
	}); err != nil {
		t.Fatalf("insert alias: %v", err)
	}

	// The draft reply: In-Reply-To / References set to the (unstored)
	// original message's Message-ID, multipart/mixed with a text part and
	// a file attachment.
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: alice.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	if err != nil {
		t.Fatalf("insert drafts mailbox: %v", err)
	}
	const boundary = "e2e-336-boundary"
	attachmentB64 := base64.StdEncoding.EncodeToString([]byte(attachmentBody))
	body := "From: " + devSinkIdentityEmail + "\r\n" +
		"To: " + recipient + "\r\n" +
		"Subject: Re: original subject\r\n" +
		"Message-ID: " + replyMessageID + "\r\n" +
		"In-Reply-To: " + originalMessageID + "\r\n" +
		"References: " + originalMessageID + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n" +
		"\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Thanks, see attached.\r\n" +
		"\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: application/octet-stream; name=\"notes.txt\"\r\n" +
		"Content-Disposition: attachment; filename=\"notes.txt\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		attachmentB64 + "\r\n" +
		"\r\n" +
		"--" + boundary + "--\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "Re: original subject",
			From:    devSinkIdentityEmail,
			To:      recipient,
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
		t.Fatalf("seeded draft not found by uid %d", uid)
	}
	return strconv.FormatUint(uint64(mid), 10)
}

// startDevSinkServer boots admin.StartServer -- the exact codepath
// `herold server start` (and so scripts/dev-instance.sh) runs -- and
// returns the public and admin listener addresses.
func startDevSinkServer(t *testing.T, cfg *sysconfig.Config) (publicAddr, adminAddr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := admin.StartServer(ctx, cfg, admin.StartOpts{
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

// devSinkAccountID fetches the JMAP session descriptor and returns the
// primary mail account id, mirroring the Suite's boot flow.
func devSinkAccountID(t *testing.T, publicAddr, apiKey string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
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

// devSinkSubmitViaJMAP creates one EmailSubmission via the real /jmap
// endpoint and asserts the method returned a created entry.
func devSinkSubmitViaJMAP(t *testing.T, publicAddr, apiKey, accountID, identityID, emailID string) {
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
		t.Fatalf("POST /jmap EmailSubmission/set: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /jmap: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode /jmap response: %v body=%s", err, raw)
	}
	if len(out.MethodResponses) != 1 {
		t.Fatalf("EmailSubmission/set: got %d method responses, want 1: %s", len(out.MethodResponses), raw)
	}
	var name string
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		t.Fatalf("decode invocation name: %v", err)
	}
	if name != "EmailSubmission/set" {
		t.Fatalf("EmailSubmission/set: response name = %q (want EmailSubmission/set); body=%s", name, raw)
	}
	argsRaw := out.MethodResponses[0][1]
	var parsed struct {
		Created    map[string]json.RawMessage `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	if err := json.Unmarshal(argsRaw, &parsed); err != nil {
		t.Fatalf("decode set args: %v", err)
	}
	if len(parsed.Created) == 0 {
		t.Fatalf("EmailSubmission/set: created empty; notCreated=%v", parsed.NotCreated)
	}
	if len(parsed.NotCreated) != 0 {
		t.Fatalf("EmailSubmission/set: unexpected notCreated=%v", parsed.NotCreated)
	}
}

// generateDevSinkCert writes a self-signed cert+key pair under dir for
// dnsNames and returns their paths.
func generateDevSinkCert(t *testing.T, dir string, dnsNames []string) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// discardTestLogger returns a logger that drops everything, for preseed
// store opens where log output would just be noise.
func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
