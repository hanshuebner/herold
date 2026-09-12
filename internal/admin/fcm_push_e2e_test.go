package admin

// fcm_push_e2e_test.go is the self-verification harness for the FCM push
// transport (#200) wired into scripts/dev-instance.sh (#334): it boots a
// real herold server (admin.StartServer) with [server.push] fcm_base_url
// pointed at the in-tree fake FCM endpoint (internal/testfakes/fakefcm),
// registers a PushSubscription of kind "fcm" over the real JMAP /jmap
// endpoint, delivers a message over real SMTP, and asserts the fake
// received a messages:send call for the registered token carrying the
// expected data payload.
//
// This is the acceptance test #334 sets out: the same [server.push]
// fcm_base_url + fcm_service_account_json_file wiring scripts/dev-
// instance.sh generates is exercised here in-process, so a regression in
// either the config plumbing or the dispatcher's FCM path fails a fast
// test rather than only surfacing against a real Firebase project.
//
// Runs on SQLite always and on Postgres when HEROLD_PG_DSN is set.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testfakes/fakefcm"
)

const fcmE2EDomain = "example.local"

// TestFCMPush_E2E drives the fake-FCM end-to-end scenario on both store
// backends (re #334).
func TestFCMPush_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("fcm push e2e wiring test")
	}
	t.Run("sqlite", func(t *testing.T) { runFCMPushE2E(t, "sqlite", "") })
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) { runFCMPushE2E(t, "postgres", dsn) })
	}
}

func runFCMPushE2E(t *testing.T, backend, pgDSN string) {
	dir := t.TempDir()
	clk := clock.NewReal()

	fake := fakefcm.New(t, fakefcm.Options{ProjectID: "e2e-test"})

	// A placeholder service-account credential: fcm_base_url below makes
	// internal/admin/server.go bypass the JWT/OAuth flow entirely (re
	// #334), so this file's content is never parsed — it only needs to
	// exist so [server.push]'s "is FCM configured" gate is satisfied,
	// exactly like scripts/dev-instance.sh's placeholder.
	saPath := filepath.Join(dir, "fcm-service-account.json")
	if err := os.WriteFile(saPath, []byte(`{"type":"service_account","project_id":"e2e-test"}`), 0o600); err != nil {
		t.Fatalf("write placeholder service account: %v", err)
	}

	var storageTOML string
	var openPreseed func() store.Store
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
source = "none"

%s

[server.push]
fcm_service_account_json_file = %q
fcm_base_url = %q
dispatcher_poll_interval_seconds = 1

[server.push.network]
allow_insecure = true
allowed_hosts = ["127.0.0.1"]
allowed_ports = [%d]

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "none"

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
		fcmE2EDomain, dir, filepath.Join(dir, "ports.toml"),
		storageTOML,
		saPath, fake.SendURL(), fake.Port(),
	)
	if err := os.WriteFile(systomlPath, []byte(systoml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Seed: a local domain, a mail principal, and an API key for it (the
	// JMAP bearer credential).
	const apiKeyPlain = protoadmin.APIKeyPrefix + "fcm_e2e_test_key_000000000000001"
	const email = "alice@" + fcmE2EDomain
	pid := seedFCMPushStore(t, openPreseed(), clk, email, apiKeyPlain)
	accountID := string(protojmap.AccountIDForPrincipal(pid))

	publicAddr, smtpAddr := startFCMPushServer(t, cfg)

	// Register a PushSubscription of kind "fcm" over the real /jmap
	// endpoint, and complete the RFC 8620 §7.2 verification handshake --
	// the dispatcher skips unverified subscriptions.
	const fcmToken = "e2e-registration-token-1"
	subID, verificationCode := jmapPushCreateFCM(t, publicAddr, apiKeyPlain, fcmToken)
	jmapPushVerify(t, publicAddr, apiKeyPlain, subID, verificationCode)

	// Deliver a message over real SMTP to the principal owning the
	// subscription. The change-feed-driven dispatcher picks up the
	// resulting Email/Add event and, since no classifier plugin is
	// configured, the message defaults to category "primary" -- inside
	// DefaultRules' MailCategoryAllowlist -- so the push fires.
	const rawFrom = "bob@external.example"
	deliverFCMPushTestMessage(t, smtpAddr, email, rawFrom)

	// Assert the fake FCM endpoint received the mail push. The create
	// handshake above already produced one messages:send call carrying a
	// "verification" data field (RFC 8620 §7.2.2); the mail push carries
	// "payload" instead, so find that one specifically rather than
	// asserting on index 0.
	m := waitForFCMPayload(t, fake, nil)
	if m.Token != fcmToken {
		t.Errorf("fake FCM message token = %q; want %q", m.Token, fcmToken)
	}
	if m.AuthHeader == "" {
		t.Errorf("fake FCM message carried no Authorization header")
	}
	if m.Data["payload"] == "" {
		t.Errorf("fake FCM message data.payload is empty; want the built StateChange envelope")
	}
	if !bytes.Contains([]byte(m.Data["payload"]), []byte("Email")) {
		t.Errorf("fake FCM message data.payload = %q; want it to reference the Email state change", m.Data["payload"])
	}

	var payload struct {
		EmailID        string `json:"emailId"`
		InboxMailboxID string `json:"inboxMailboxId"`
	}
	if err := json.Unmarshal([]byte(m.Data["payload"]), &payload); err != nil {
		t.Fatalf("decode data.payload: %v: %s", err, m.Data["payload"])
	}
	if payload.EmailID == "" || payload.InboxMailboxID == "" {
		t.Fatalf("payload missing emailId/inboxMailboxId: %+v", payload)
	}

	// re #346: archiving the message (moving it out of the Inbox-role
	// mailbox) must not resurrect as a "new mail" push.
	archiveID := jmapFindMailboxByRole(t, publicAddr, apiKeyPlain, accountID, "archive")
	before := len(fake.Messages())
	jmapCall(t, publicAddr, apiKeyPlain, "Email/set", map[string]any{
		"accountId": accountID,
		"update": map[string]any{
			payload.EmailID: map[string]any{
				"mailboxIds/" + archiveID:              true,
				"mailboxIds/" + payload.InboxMailboxID: false,
			},
		},
	})
	assertNoNewFCMPayload(t, fake, before, "archiving a message")

	// re #346: a message created directly in Sent (a submission's sent
	// copy) must not push either -- it never sat in the Inbox-role
	// mailbox.
	sentID := jmapFindMailboxByRole(t, publicAddr, apiKeyPlain, accountID, "sent")
	sentRaw := "From: " + email + "\r\n" +
		"To: bob@external.example\r\n" +
		"Subject: fcm push e2e sent copy\r\n" +
		"Message-ID: <fcm-push-e2e-sent@" + fcmE2EDomain + ">\r\n" +
		"\r\n" +
		"a message the principal sent.\r\n"
	blobID := jmapUploadBlob(t, publicAddr, apiKeyPlain, accountID, []byte(sentRaw))
	before = len(fake.Messages())
	jmapCall(t, publicAddr, apiKeyPlain, "Email/import", map[string]any{
		"accountId": accountID,
		"emails": map[string]any{
			"s1": map[string]any{
				"blobId":     blobID,
				"mailboxIds": map[string]bool{sentID: true},
				"keywords":   map[string]bool{"$seen": true},
			},
		},
	})
	assertNoNewFCMPayload(t, fake, before, "a message created directly in Sent")
}

// seedFCMPushStore inserts the local domain, one mail principal, and an
// API key for it. Returns the principal id.
func seedFCMPushStore(t *testing.T, st store.Store, clk clock.Clock, email, apiKeyPlain string) store.PrincipalID {
	t.Helper()
	ctx := context.Background()
	defer func() {
		if err := st.Close(); err != nil {
			t.Fatalf("seed store close: %v", err)
		}
	}()

	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: fcmE2EDomain, IsLocal: true, CreatedAt: clk.Now(),
	}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}

	dirAdapter := directory.New(st.Meta(), discardLogger(), clk, nil)
	pid, err := dirAdapter.CreatePrincipal(ctx, email, "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid,
		Hash:        protoadmin.HashAPIKey(apiKeyPlain),
		Name:        "fcm-push-e2e",
		CreatedAt:   clk.Now(),
		ScopeJSON:   `["end-user"]`,
	}); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return pid
}

// startFCMPushServer boots StartServer and returns the public and smtp
// listener addresses.
func startFCMPushServer(t *testing.T, cfg *sysconfig.Config) (publicAddr, smtpAddr string) {
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
	smtpAddr = addrs["smtp"]
	addrsMu.Unlock()
	if publicAddr == "" || smtpAddr == "" {
		t.Fatalf("listeners not bound; addrs=%+v", addrs)
	}
	return publicAddr, smtpAddr
}

// jmapPushCreateFCM issues PushSubscription/set { create } for a kind="fcm"
// subscription over the real /jmap endpoint and returns the created
// subscription's id and RFC 8620 §7.2 verificationCode.
func jmapPushCreateFCM(t *testing.T, publicAddr, apiKey, fcmToken string) (id, verificationCode string) {
	t.Helper()
	args := map[string]any{
		"create": map[string]any{
			"c1": map[string]any{
				"deviceClientId": "e2e-android-device",
				"kind":           "fcm",
				"fcmToken":       fcmToken,
				"types":          []string{"Email"},
			},
		},
	}
	raw := jmapCall(t, publicAddr, apiKey, "PushSubscription/set", args)
	var out struct {
		Created map[string]struct {
			ID               string `json:"id"`
			VerificationCode string `json:"verificationCode"`
		} `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode PushSubscription/set create response: %v body=%s", err, raw)
	}
	created, ok := out.Created["c1"]
	if !ok {
		t.Fatalf("PushSubscription/set create: no created entry; notCreated=%v body=%s", out.NotCreated, raw)
	}
	if created.ID == "" || created.VerificationCode == "" {
		t.Fatalf("PushSubscription/set create: missing id/verificationCode: %+v", created)
	}
	return created.ID, created.VerificationCode
}

// jmapPushVerify completes the RFC 8620 §7.2 verification handshake by
// echoing verificationCode back via PushSubscription/set { update }.
func jmapPushVerify(t *testing.T, publicAddr, apiKey, id, verificationCode string) {
	t.Helper()
	args := map[string]any{
		"update": map[string]any{
			id: map[string]any{"verificationCode": verificationCode},
		},
	}
	raw := jmapCall(t, publicAddr, apiKey, "PushSubscription/set", args)
	var out struct {
		Updated    map[string]any             `json:"updated"`
		NotUpdated map[string]json.RawMessage `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode PushSubscription/set update response: %v body=%s", err, raw)
	}
	if _, ok := out.Updated[id]; !ok {
		t.Fatalf("PushSubscription/set update: verification rejected; notUpdated=%v body=%s", out.NotUpdated, raw)
	}
}

// jmapCall POSTs a single-method-call JMAP request against the real
// /jmap endpoint and returns the method response's argument object,
// asserting the response invocation name matches method (i.e. it is not
// a serverFail/error method-error envelope).
func jmapCall(t *testing.T, publicAddr, apiKey, method string, args map[string]any) json.RawMessage {
	t.Helper()
	argsBytes, _ := json.Marshal(args)
	envelope := map[string]any{
		"using": []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		"methodCalls": []any{
			[]any{method, json.RawMessage(argsBytes), "c0"},
		},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/jmap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /jmap %s: %v", method, err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /jmap %s: status=%d body=%s", method, resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode /jmap response for %s: %v body=%s", method, err, raw)
	}
	if len(out.MethodResponses) != 1 {
		t.Fatalf("%s: got %d method responses, want 1: %s", method, len(out.MethodResponses), raw)
	}
	var name string
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		t.Fatalf("decode invocation name for %s: %v", method, err)
	}
	if name != method {
		t.Fatalf("%s: response invocation = %q (want %q); body=%s", method, name, method, raw)
	}
	return out.MethodResponses[0][1]
}

// waitForFCMPayload polls fake.Messages() until it finds a
// messages:send call carrying a non-empty data.payload field whose raw
// bytes were not already recorded in skip (the RFC 8620 §7.2.2
// verification-ping call carries a "verification" field instead of
// "payload", so any payload-bearing call not already seen is the mail
// push under test). Fails the test after 15s with no match.
func waitForFCMPayload(t *testing.T, fake *fakefcm.Server, skip map[string]bool) fakefcm.Message {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, cand := range fake.Messages() {
			p := cand.Data["payload"]
			if p == "" || skip[p] {
				continue
			}
			return cand
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("fake FCM endpoint received no new data.payload call within 15s; recorded=%+v", fake.Messages())
	return fakefcm.Message{}
}

// assertNoNewFCMPayload waits briefly (long enough for the 1s
// dispatcher poll interval configured by runFCMPushE2E to run several
// ticks) and fails the test if fake.Messages() grew past before,
// labeling the failure with what.
func assertNoNewFCMPayload(t *testing.T, fake *fakefcm.Server, before int, what string) {
	t.Helper()
	time.Sleep(3 * time.Second)
	if got := len(fake.Messages()); got != before {
		t.Fatalf("%s produced %d new fake FCM messages:send call(s); want 0 (re #346)", what, got-before)
	}
}

// jmapFindMailboxByRole calls Mailbox/get with no ids (return-all) and
// returns the id of the mailbox whose JMAP role matches want (e.g.
// "sent", "archive"). Fails the test when no mailbox has that role.
func jmapFindMailboxByRole(t *testing.T, publicAddr, apiKey, accountID, want string) string {
	t.Helper()
	raw := jmapCall(t, publicAddr, apiKey, "Mailbox/get", map[string]any{
		"accountId": accountID,
		"ids":       nil,
	})
	var out struct {
		List []struct {
			ID   string  `json:"id"`
			Role *string `json:"role"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode Mailbox/get: %v body=%s", err, raw)
	}
	for _, mb := range out.List {
		if mb.Role != nil && *mb.Role == want {
			return mb.ID
		}
	}
	t.Fatalf("no mailbox with role=%q found: %s", want, raw)
	return ""
}

// jmapUploadBlob uploads raw bytes to the JMAP upload endpoint and
// returns the server-assigned blobId.
func jmapUploadBlob(t *testing.T, publicAddr, apiKey, accountID string, data []byte) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		"http://"+publicAddr+"/jmap/upload/"+accountID,
		bytes.NewReader(data))
	if err != nil {
		t.Fatalf("jmapUploadBlob: new request: %v", err)
	}
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("jmapUploadBlob: do: %v", err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("jmapUploadBlob: status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		BlobID string `json:"blobId"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("jmapUploadBlob: decode: %v body=%s", err, raw)
	}
	if out.BlobID == "" {
		t.Fatalf("jmapUploadBlob: blobId empty: %s", raw)
	}
	return out.BlobID
}

// deliverFCMPushTestMessage drives a minimal EHLO/MAIL/RCPT/DATA dialogue
// against the smtp listener, delivering one message to rcpt with the
// given raw From header value.
func deliverFCMPushTestMessage(t *testing.T, smtpAddr, rcpt, from string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", smtpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	send := func(line string) {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
			t.Fatalf("smtp write %q: %v", line, err)
		}
	}
	expect := func(want int) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read smtp reply: %v", err)
			}
			l = strings.TrimRight(l, "\r\n")
			if len(l) < 4 {
				t.Fatalf("short smtp line: %q", l)
			}
			if l[3] == ' ' {
				var code int
				fmt.Sscanf(l[:3], "%d", &code)
				if code != want {
					t.Fatalf("expected %d, got %d: %s", want, code, l)
				}
				return
			}
		}
	}
	expect(220) // greeting
	send("EHLO sender.external")
	expect(250)
	send("MAIL FROM:<bob@external.example>")
	expect(250)
	send("RCPT TO:<" + rcpt + ">")
	expect(250)
	send("DATA")
	expect(354)
	rawMsg := "From: " + from + "\r\n" +
		"To: " + rcpt + "\r\n" +
		"Subject: fcm push e2e\r\n" +
		"Message-ID: <fcm-push-e2e@external.example>\r\n" +
		"\r\n" +
		"hello from the fake-FCM push e2e test.\r\n" +
		".\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte(rawMsg)); err != nil {
		t.Fatalf("smtp write data: %v", err)
	}
	expect(250) // DATA accepted
	send("QUIT")
}
