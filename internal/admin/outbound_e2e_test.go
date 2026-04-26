package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestOutbound_E2E_SmartHostDeliveryWithDKIM is the end-to-end
// production-wiring test for Phase 3 Wave 3.1.5.
//
// It boots admin.StartServer with a smart-host pointing at a fake SMTP
// receiver, seeds a domain + DKIM key + admin principal + API key,
// then drives the protosend HTTP /api/v1/mail/send-raw endpoint and
// asserts that the queue worker picked up the row, signed it, and
// dialled the smart host with DKIM-Signature in the DATA bytes.
//
// This test is the regression guard the wave brief identified: every
// JMAP EmailSubmission/set and HTTP send before this wave wrote a
// queue row that no worker ever drained, because cmd/herold serve
// never constructed the outbound SMTP client and never started the
// queue.Run loop.
func TestOutbound_E2E_SmartHostDeliveryWithDKIM(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}

	// 1. Spin up a fake SMTP receiver representing the smart host.
	rx := newFakeSMTPReceiver(t)
	t.Cleanup(rx.Close)

	// 2. Build a sysconfig pointed at it (auth=none, tls=none —
	//    REQ-FLOW-SMARTHOST validation permits this combination only
	//    when no credentials are sent in plaintext).
	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	systomlPath := filepath.Join(dir, "system.toml")
	rxHost, rxPortStr, _ := net.SplitHostPort(rx.Addr())
	systomlBody := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
shutdown_grace = "5s"

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[server.smart_host]
enabled = true
host = %q
port = %s
tls_mode = "none"
auth_method = "none"
fallback_policy = "smart_host_only"
tls_verify_mode = "insecure_skip_verify"

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
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`,
		dir, certPath, keyPath, filepath.Join(dir, "db.sqlite"),
		rxHost, rxPortStr,
		certPath, keyPath, certPath, keyPath)
	if err := os.WriteFile(systomlPath, []byte(systomlBody), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// 3. Seed the store: local domain, DKIM key, admin principal, API key.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	clk := clock.NewReal()
	st, err := storesqlite.Open(ctx,
		filepath.Join(dir, "db.sqlite"),
		discardLogger(),
		clk)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	const sourceDomain = "test.local"
	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: sourceDomain, IsLocal: true, CreatedAt: clk.Now(),
	}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	mgr := keymgmt.NewManager(st.Meta(), discardLogger(), clk, nil)
	if _, err := mgr.GenerateKey(ctx, sourceDomain, store.DKIMAlgorithmRSASHA256); err != nil {
		t.Fatalf("generate dkim key: %v", err)
	}
	princ, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@" + sourceDomain,
		Flags:          store.PrincipalFlagAdmin,
		CreatedAt:      clk.Now(),
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	apiKeyPlain := protoadmin.APIKeyPrefix + "outbound_e2e_test_key_for_alice_0001"
	apiKeyHash := protoadmin.HashAPIKey(apiKeyPlain)
	if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: princ.ID,
		Hash:        apiKeyHash,
		Name:        "e2e",
		CreatedAt:   clk.Now(),
		// REQ-AUTH-SCOPE-04: API key carries explicit scope. The
		// e2e test exercises the HTTP send-raw path so mail.send
		// is required; admin is included for parity with the
		// pre-3.6 implicit posture.
		ScopeJSON: `["admin","mail.send"]`,
	}); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store close: %v", err)
	}

	// 4. Boot the production lifecycle.
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
	case <-time.After(15 * time.Second):
		t.Fatalf("server did not become ready")
	}
	addrsMu.Lock()
	adminAddr := addrs["admin"]
	publicAddr := addrs["public"]
	addrsMu.Unlock()
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	// 5. POST a raw message to /api/v1/mail/send-raw on the
	//    public listener (REQ-OPS-ADMIN-LISTENER-01: send API
	//    is a public-listener mount, not admin).
	rawMsg := "From: alice@" + sourceDomain + "\r\n" +
		"To: bob@destination.test\r\n" +
		"Subject: e2e wave 3.1.5\r\n" +
		"Date: Wed, 25 Apr 2026 12:00:00 +0000\r\n" +
		"Message-ID: <e2e-3.1.5@" + sourceDomain + ">\r\n" +
		"\r\n" +
		"hello from the production wiring test.\r\n"
	body, _ := json.Marshal(map[string]any{
		"destinations": []string{"bob@destination.test"},
		"rawMessage":   base64.StdEncoding.EncodeToString([]byte(rawMsg)),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+publicAddr+"/api/v1/mail/send-raw",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKeyPlain)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST send-raw: %v", err)
	}
	rb, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send-raw status=%d body=%s", resp.StatusCode, string(rb))
	}

	// 6. Wait for the smart host to receive the DATA and assert
	//    DKIM-Signature is present.
	deadline := time.Now().Add(15 * time.Second)
	var data string
	for time.Now().Before(deadline) {
		data = rx.LastData()
		if data != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data == "" {
		t.Fatalf("smart host did not receive DATA within deadline")
	}
	if !strings.Contains(strings.ToLower(data), "dkim-signature:") {
		t.Fatalf("DKIM-Signature header missing from delivered message:\n%s", data)
	}
	if !strings.Contains(data, "Subject: e2e wave 3.1.5") {
		t.Fatalf("expected subject in delivered DATA:\n%s", data)
	}
	if !strings.Contains(data, "From: alice@"+sourceDomain) {
		t.Fatalf("expected From in delivered DATA:\n%s", data)
	}
}

// fakeSMTPReceiver is a minimal in-process SMTP server that accepts a
// single connection, plays a tiny EHLO/MAIL/RCPT/DATA/QUIT script in
// plaintext, and records the DATA bytes. It does not implement any
// optional extension; the smart-host path under test runs with
// auth_method=none and tls_mode=none so MAIL/RCPT/DATA suffice.
type fakeSMTPReceiver struct {
	ln    net.Listener
	mu    sync.Mutex
	data  string
	wg    sync.WaitGroup
	close chan struct{}
}

func newFakeSMTPReceiver(t *testing.T) *fakeSMTPReceiver {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen smtp peer: %v", err)
	}
	r := &fakeSMTPReceiver{ln: ln, close: make(chan struct{})}
	r.wg.Add(1)
	go r.acceptLoop(t)
	return r
}

func (r *fakeSMTPReceiver) Addr() string { return r.ln.Addr().String() }

func (r *fakeSMTPReceiver) LastData() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data
}

func (r *fakeSMTPReceiver) Close() {
	select {
	case <-r.close:
		return
	default:
	}
	close(r.close)
	_ = r.ln.Close()
	r.wg.Wait()
}

func (r *fakeSMTPReceiver) acceptLoop(t *testing.T) {
	t.Helper()
	defer r.wg.Done()
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go func(c net.Conn) {
			defer r.wg.Done()
			r.handle(t, c)
		}(conn)
	}
}

func (r *fakeSMTPReceiver) handle(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	writeLine := func(s string) {
		_, _ = bw.WriteString(s + "\r\n")
		_ = bw.Flush()
	}
	readLine := func() (string, error) {
		l, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(l, "\r\n"), nil
	}
	writeLine("220 fake.smarthost ESMTP")
	for {
		line, err := readLine()
		if err != nil {
			return
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeLine("250-fake.smarthost")
			writeLine("250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM"):
			writeLine("250 2.1.0 ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			writeLine("250 2.1.5 ok")
		case strings.HasPrefix(upper, "DATA"):
			writeLine("354 send body")
			var body bytes.Buffer
			for {
				bl, berr := readLine()
				if berr != nil {
					return
				}
				if bl == "." {
					break
				}
				if strings.HasPrefix(bl, "..") {
					bl = bl[1:]
				}
				body.WriteString(bl)
				body.WriteString("\r\n")
			}
			r.mu.Lock()
			r.data = body.String()
			r.mu.Unlock()
			writeLine("250 2.0.0 queued")
		case strings.HasPrefix(upper, "QUIT"):
			writeLine("221 2.0.0 bye")
			return
		case strings.HasPrefix(upper, "RSET"):
			writeLine("250 2.0.0 ok")
		case strings.HasPrefix(upper, "NOOP"):
			writeLine("250 2.0.0 ok")
		default:
			writeLine("502 5.5.1 unknown")
		}
	}
}

// readAllAndClose reads the response body and closes it. Helper for
// concise inline assertions.
func readAllAndClose(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	buf := &bytes.Buffer{}
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}
