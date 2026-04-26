package admin

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestSubmissionListener_E2E_NonLocalRecipient_RoutesThroughQueue is
// the Phase 3 Wave 3.1.6 production-wiring test: an authenticated
// MUA-client connects to the SMTP submission listener (port 587 with
// STARTTLS), sends a message with MAIL FROM = own domain and RCPT TO =
// remote address, and the queue worker delivers the bytes to a fake
// smart host.
//
// This regression-guards the wave brief gap: prior to 3.1.6 the SMTP
// submission listener returned 550 5.7.1 on RCPT TO of any non-local
// recipient with the comment "Phase 2 not enabled" — meaning MUA
// clients (Thunderbird, mutt, etc.) could not relay through Herold
// even though JMAP EmailSubmission and the HTTP send API both worked.
func TestSubmissionListener_E2E_NonLocalRecipient_RoutesThroughQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}

	// 1. Spin up a fake SMTP receiver representing the smart host.
	rx := newFakeSMTPReceiver(t)
	t.Cleanup(rx.Close)

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
name = "smtp-submission"
address = "127.0.0.1:0"
protocol = "smtp-submission"
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
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
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

	// Seed: domain, DKIM key, principal alice with a password.
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
	dirAdapter := directory.New(st.Meta(), discardLogger(), clk, nil)
	const password = "correct-horse-staple-battery"
	pid, err := dirAdapter.CreatePrincipal(ctx, "alice@"+sourceDomain, password)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_ = pid
	if err := st.Close(); err != nil {
		t.Fatalf("store close: %v", err)
	}

	// Boot the production lifecycle.
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
	subAddr := addrs["smtp-submission"]
	addrsMu.Unlock()
	if subAddr == "" {
		t.Fatalf("submission listener not bound; addrs=%+v", addrs)
	}

	// SMTP submission session: EHLO -> STARTTLS -> AUTH PLAIN -> MAIL/RCPT/DATA.
	conn, err := net.DialTimeout("tcp", subAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial submission: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	send := func(line string) {
		_, _ = conn.Write([]byte(line + "\r\n"))
	}
	expect := func(want int) string {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var lines []string
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read reply: %v (so-far %s)", err, strings.Join(lines, ""))
			}
			l = strings.TrimRight(l, "\r\n")
			lines = append(lines, l)
			if len(l) < 4 {
				t.Fatalf("short SMTP line: %q", l)
			}
			if l[3] == ' ' {
				var code int
				fmt.Sscanf(l[:3], "%d", &code)
				if code != want {
					t.Fatalf("expected %d, got %d: %s", want, code, strings.Join(lines, "\n"))
				}
				return strings.Join(lines, "\n")
			}
		}
	}
	expect(220)
	send("EHLO client.test")
	expect(250)
	send("STARTTLS")
	expect(220)
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "test.local"})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		t.Fatalf("starttls handshake: %v", err)
	}
	conn = tlsConn
	br = bufio.NewReader(tlsConn)
	send2 := func(line string) {
		_, _ = tlsConn.Write([]byte(line + "\r\n"))
	}
	send2("EHLO client.test")
	expect(250)
	plainIR := base64.StdEncoding.EncodeToString([]byte("\x00alice@" + sourceDomain + "\x00" + password))
	send2("AUTH PLAIN " + plainIR)
	expect(235)
	send2("MAIL FROM:<alice@" + sourceDomain + ">")
	expect(250)
	send2("RCPT TO:<bob@destination.test>")
	expect(250)
	send2("DATA")
	expect(354)
	rawMsg := "From: alice@" + sourceDomain + "\r\n" +
		"To: bob@destination.test\r\n" +
		"Subject: e2e wave 3.1.6 submission\r\n" +
		"Date: Wed, 25 Apr 2026 12:00:00 +0000\r\n" +
		"Message-ID: <e2e-submission@" + sourceDomain + ">\r\n" +
		"\r\n" +
		"hello from MUA via SMTP submission.\r\n" +
		".\r\n"
	_, _ = tlsConn.Write([]byte(rawMsg))
	expect(250)
	send2("QUIT")

	// Wait for the smart host to receive the DATA.
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
	if !strings.Contains(data, "Subject: e2e wave 3.1.6 submission") {
		t.Fatalf("expected subject in delivered DATA:\n%s", data)
	}
	if !strings.Contains(data, "hello from MUA via SMTP submission") {
		t.Fatalf("expected body in delivered DATA:\n%s", data)
	}
}
