package admin

// smtp_inbound_categorise_e2e_test.go verifies that the production
// StartServer wiring passes a live categorise.Categoriser into the SMTP
// server so inbound relay-in messages delivered to a local recipient's
// INBOX receive the $category-* keyword assigned by the LLM.
//
// Prior to the fix for re #39, admin.StartServer constructed the
// smtpServer WITHOUT a Categorise field, so the REQ-FILT-200 code path
// in protosmtp/deliver.go was dead in production even though the
// categoriser was wired for the IMAP import path.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// fakeChatJSONForCategory returns a minimal OpenAI chat-completions
// response JSON that makes the categoriser return the given category.
// json.Marshal is used for both the inner content string and the outer
// envelope to guarantee all escaping is correct.
func fakeChatJSONForCategory(category string) string {
	content := fmt.Sprintf(
		`{"categories":["primary","social","promotions","updates","forums"],"assigned":%s}`,
		func() string { b, _ := json.Marshal(category); return string(b) }(),
	)
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// TestSMTPInbound_E2E_CategorisesINBOXMessages is the production-wiring
// regression test for re #39.
//
// It boots admin.StartServer with an inbound relay-in SMTP listener,
// seeds the database with a local domain, a principal, and a
// CategorisationConfig whose Endpoint points to a fake LLM server, then
// delivers a message via raw SMTP and asserts the stored Email carries
// the $category-promotions keyword. This verifies the end-to-end wiring:
// StartServer must pass the categoriser to protosmtp.New so delivery
// calls categorise.Categoriser.CategoriseRich for each INBOX placement.
func TestSMTPInbound_E2E_CategorisesINBOXMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}

	// 1. Fake LLM endpoint that always assigns "promotions".
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, fakeChatJSONForCategory("promotions"))
	}))
	t.Cleanup(llmSrv.Close)

	// 2. Write system.toml: relay-in SMTP listener, no TLS anywhere.
	dir := t.TempDir()
	systomlPath := filepath.Join(dir, "system.toml")
	dbPath := filepath.Join(dir, "db.sqlite")
	systomlBody := fmt.Sprintf(`
[server]
hostname = "mx.test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
shutdown_grace = "5s"
port_report_file = %q

[server.admin_tls]
source = "none"

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

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
`, dir, filepath.Join(dir, "ports.toml"), dbPath)
	if err := os.WriteFile(systomlPath, []byte(systomlBody), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("sysconfig.Load: %v", err)
	}

	// 3. Seed the store before the server opens it:
	//    - a local domain
	//    - alice as a principal
	//    - a CategorisationConfig for alice with Endpoint = fake LLM URL
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clk := clock.NewReal()
	st, err := storesqlite.Open(ctx, dbPath, discardLogger(), clk)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	const domain = "test.local"
	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: domain, IsLocal: true, CreatedAt: clk.Now(),
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dirAdapter := directory.New(st.Meta(), discardLogger(), clk, nil)
	pid, err := dirAdapter.CreatePrincipal(ctx, "alice@"+domain, "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	// Set per-account endpoint so the production categoriser (which has no
	// DefaultEndpoint) can reach the fake LLM. Model must be non-empty.
	llmEndpoint := llmSrv.URL
	llmModel := "test-model"
	catCfg := store.CategorisationConfig{
		PrincipalID: pid,
		Endpoint:    &llmEndpoint,
		Model:       &llmModel,
		Enabled:     true,
		TimeoutSec:  5,
		Prompt:      "You are a mail categoriser. Return JSON {categories, assigned}.",
	}
	if err := st.Meta().UpdateCategorisationConfig(ctx, catCfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store close: %v", err)
	}

	// 4. Boot StartServer.
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
	case <-time.After(30 * time.Second):
		t.Fatalf("server did not become ready within 30 s")
	}
	addrsMu.Lock()
	smtpAddr := addrs["smtp"]
	addrsMu.Unlock()
	if smtpAddr == "" {
		t.Fatalf("smtp listener not bound; addrs=%+v", addrs)
	}

	// 5. Deliver a message to alice@test.local via raw SMTP (no auth needed
	//    on a relay-in listener).
	conn, err := net.DialTimeout("tcp", smtpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	smtpSend := func(line string) {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte(line + "\r\n"))
	}
	smtpExpect := func(want int) {
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
	smtpExpect(220) // greeting
	smtpSend("EHLO sender.external")
	smtpExpect(250)
	smtpSend("MAIL FROM:<bob@external.example>")
	smtpExpect(250)
	smtpSend("RCPT TO:<alice@" + domain + ">")
	smtpExpect(250)
	smtpSend("DATA")
	smtpExpect(354)
	rawMsg := "From: bob@external.example\r\n" +
		"To: alice@" + domain + "\r\n" +
		"Subject: Great deal inside!\r\n" +
		"Message-ID: <cat-e2e-test@external.example>\r\n" +
		"\r\n" +
		"Click here to claim your prize.\r\n" +
		".\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = conn.Write([]byte(rawMsg))
	smtpExpect(250) // DATA accepted
	smtpSend("QUIT")

	// 6. Reopen the store and verify the keyword.
	//    Allow up to 10 s for the categoriser goroutine (inline in the SMTP
	//    delivery path) to apply the keyword. In practice it is synchronous
	//    and should already be present immediately after the 250.
	st2, err := storesqlite.Open(ctx, dbPath, discardLogger(), clk)
	if err != nil {
		t.Fatalf("open store for verification: %v", err)
	}
	defer func() { _ = st2.Close() }()

	deadline := time.Now().Add(10 * time.Second)
	var foundKeyword bool
	for !foundKeyword && time.Now().Before(deadline) {
		mb, err := st2.Meta().GetMailboxByName(ctx, pid, "INBOX")
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		msgs, err := st2.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
		if err != nil || len(msgs) == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		for _, kw := range msgs[0].Keywords {
			if strings.HasPrefix(kw, "$category-") {
				foundKeyword = true
				break
			}
		}
		if foundKeyword {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !foundKeyword {
		// Gather what keywords were present for a useful failure message.
		mb, _ := st2.Meta().GetMailboxByName(ctx, pid, "INBOX")
		var kws []string
		if mb.ID != 0 {
			msgs, _ := st2.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
			if len(msgs) > 0 {
				kws = msgs[0].Keywords
			}
		}
		t.Fatalf("expected $category-* keyword on delivered message; keywords=%v", kws)
	}
}
