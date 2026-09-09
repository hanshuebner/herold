package admin

// spam_llm_e2e_test.go verifies that herold-spam-llm can be configured
// entirely through a [[plugin]] system.toml block -- the documented
// operator path -- and that a real, spawned instance of the plugin
// classifies an inbound message end to end (re #302).
//
// Before the fix, every numeric/boolean option (timeout_sec,
// spam_threshold, max_body_chars, log_samples) failed OnConfigure because
// system.toml [[plugin]] options are always strings and the plugin only
// accepted native JSON types; api_key_env could only ever receive an
// already-resolved secret value (its key matches sysconfig's secret-key
// heuristic), which OnConfigure then rejected as an empty environment
// variable. Together these gaps made it impossible to bring up a cloud
// classifier endpoint through system.toml.
//
// This test boots a real admin.StartServer with a [[plugin]] block
// pointing at a freshly built herold-spam-llm binary and a fake
// OpenAI-compatible chat-completions endpoint (an httptest.Server), using
// only the string-typed options the server actually sends plus the new
// api_key option carrying a resolved secret. It then delivers one message
// over raw SMTP and reads back the persisted spam verdict from the LLM
// transparency record (REQ-FILT-66).
//
// Runs on SQLite always and on Postgres when HEROLD_PG_DSN is set.

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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// spamLLME2EAPIKeyEnv is the env var the test's system.toml references as
// "$HEROLD_SPAM_LLM_E2E_API_KEY" for the plugin's api_key option; the
// server resolves it before the plugin ever sees it (STANDARDS section 9).
const spamLLME2EAPIKeyEnv = "HEROLD_SPAM_LLM_E2E_API_KEY"

// buildSpamLLMPluginBinary compiles the real herold-spam-llm plugin once
// for the test. Herold's plugin architecture is out-of-process JSON-RPC
// on stdio (docs/design/server/architecture/07-plugin-architecture.md);
// the point of this test is that the server spawns and configures an
// actual child process, not a mock standing in for one.
func buildSpamLLMPluginBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herold-spam-llm")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "github.com/hanshuebner/herold/plugins/herold-spam-llm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build herold-spam-llm: %v\n%s", err, out)
	}
	return bin
}

// TestSpamLLM_E2E_ConfiguredThroughSystemTOML is the acceptance test for
// re #302.
func TestSpamLLM_E2E_ConfiguredThroughSystemTOML(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	bin := buildSpamLLMPluginBinary(t)
	t.Run("sqlite", func(t *testing.T) { runSpamLLME2E(t, bin, "sqlite", "") })
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) { runSpamLLME2E(t, bin, "postgres", dsn) })
	}
}

func runSpamLLME2E(t *testing.T, pluginBin, backend, pgDSN string) {
	// Fake OpenAI-compatible endpoint: always returns a fixed "spam"
	// verdict with a high score, and records the Authorization header so
	// the test can assert the resolved api_key reached the plugin.
	var gotAuth string
	var authMu sync.Mutex
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			authMu.Lock()
			gotAuth = r.Header.Get("Authorization")
			authMu.Unlock()
			resp := map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{
						"role":    "assistant",
						"content": `{"verdict":"spam","score":0.97,"reason":"looks like spam"}`,
					}},
				},
			}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)
			return
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(llm.Close)

	const apiKeySecret = "sk-e2e-test-secret-value"
	t.Setenv(spamLLME2EAPIKeyEnv, apiKeySecret)

	dir := t.TempDir()
	systomlPath := filepath.Join(dir, "system.toml")
	dbPath := filepath.Join(dir, "db.sqlite")
	blobDir := filepath.Join(dir, "blobs")

	var storageTOML string
	clk := clock.NewReal()
	switch backend {
	case "sqlite":
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"sqlite\"\n[server.storage.sqlite]\npath = %q\n", dbPath)
	case "postgres":
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"postgres\"\n[server.storage.postgres]\ndsn = %q\nblob_dir = %q\n", pgDSN, blobDir)
	default:
		t.Fatalf("unknown backend %q", backend)
	}

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

%s

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

[[plugin]]
name = "spam-llm-e2e"
path = %q
type = "spam"
lifecycle = "long-running"
options.endpoint = %q
options.model = "fake-e2e-model"
options.api_key = "$%s"
options.timeout_sec = "5"
options.spam_threshold = "0.5"
options.max_body_chars = "2000"
options.log_samples = "false"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, dir, filepath.Join(dir, "ports.toml"), storageTOML, pluginBin, llm.URL+"/v1", spamLLME2EAPIKeyEnv)
	if err := os.WriteFile(systomlPath, []byte(systomlBody), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("sysconfig.Load: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Seed a local domain + principal before the server opens the store.
	// truncate wipes every application table first; it must run only on
	// the pre-seed open, never on the post-delivery verification open --
	// truncating a live Postgres DB out from under StartServer's own
	// connection pool wipes the just-delivered message before the test
	// can read it back and disrupts the store's change-feed connection.
	const domain = "test.local"
	openStoreForBackend := func(truncate bool) store.Store {
		switch backend {
		case "sqlite":
			st, err := storesqlite.Open(ctx, dbPath, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			return st
		case "postgres":
			st, err := storepg.Open(ctx, pgDSN, blobDir, discardLogger(), clk)
			if err != nil {
				t.Fatalf("storepg.Open: %v", err)
			}
			if truncate {
				// Shared throwaway DB: clear rows so seeds do not collide
				// with a prior test run.
				if tr, ok := st.(interface {
					TruncateAll(context.Context) error
				}); ok {
					if err := tr.TruncateAll(ctx); err != nil {
						_ = st.Close()
						t.Fatalf("TruncateAll: %v", err)
					}
				}
			}
			return st
		default:
			t.Fatalf("unknown backend %q", backend)
			return nil
		}
	}

	preSt := openStoreForBackend(true)
	pid := seedSpamLLME2EStore(t, ctx, preSt, clk, domain)
	if err := preSt.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Boot StartServer against the seeded store; the [[plugin]] block
	// above makes it spawn the real herold-spam-llm binary.
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

	deliverSpamLLME2EMessage(t, smtpAddr, domain)

	// Reopen the store and read back the spam classification record. No
	// truncate here: this must observe the message StartServer just
	// delivered, not wipe it.
	verifySt := openStoreForBackend(false)
	defer func() { _ = verifySt.Close() }()

	deadline := time.Now().Add(15 * time.Second)
	var rec store.LLMClassificationRecord
	var found bool
	var lastErr string
	for time.Now().Before(deadline) {
		// A spam verdict files into Junk by default (REQ-FILT-02).
		mb, err := verifySt.Meta().GetMailboxByName(ctx, pid, "Junk")
		if err != nil {
			lastErr = fmt.Sprintf("GetMailboxByName: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		msgs, err := verifySt.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
		if err != nil || len(msgs) == 0 {
			lastErr = fmt.Sprintf("ListMessages: err=%v len=%d", err, len(msgs))
			time.Sleep(50 * time.Millisecond)
			continue
		}
		rec, err = verifySt.Meta().GetLLMClassification(ctx, msgs[0].ID)
		if err != nil || rec.SpamVerdict == nil {
			lastErr = fmt.Sprintf("GetLLMClassification(msgID=%v): err=%v rec=%+v", msgs[0].ID, err, rec)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("no spam classification record found for delivered message; last: %s", lastErr)
	}
	if *rec.SpamVerdict != "spam" {
		t.Fatalf("SpamVerdict = %q, want %q", *rec.SpamVerdict, "spam")
	}
	if rec.SpamConfidence == nil || *rec.SpamConfidence < 0.9 {
		t.Fatalf("SpamConfidence = %v, want >= 0.9", rec.SpamConfidence)
	}

	authMu.Lock()
	auth := gotAuth
	authMu.Unlock()
	if auth != "Bearer "+apiKeySecret {
		t.Fatalf("Authorization header seen by fake LLM = %q, want the resolved api_key as bearer token", auth)
	}
}

// seedSpamLLME2EStore inserts a local domain and a principal ("alice")
// before the server opens the store, returning alice's principal id.
func seedSpamLLME2EStore(t *testing.T, ctx context.Context, st store.Store, clk clock.Clock, domain string) store.PrincipalID {
	t.Helper()
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
	return pid
}

// deliverSpamLLME2EMessage sends one message to alice@domain over raw SMTP
// on a relay-in (unauthenticated) listener.
func deliverSpamLLME2EMessage(t *testing.T, smtpAddr, domain string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", smtpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	send := func(line string) {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte(line + "\r\n"))
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
	send("RCPT TO:<alice@" + domain + ">")
	expect(250)
	send("DATA")
	expect(354)
	rawMsg := "From: bob@external.example\r\n" +
		"To: alice@" + domain + "\r\n" +
		"Subject: Great deal inside!\r\n" +
		"Message-ID: <spam-llm-e2e-test@external.example>\r\n" +
		"\r\n" +
		"Click here to claim your prize.\r\n" +
		".\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = conn.Write([]byte(rawMsg))
	expect(250) // DATA accepted
	send("QUIT")
}
