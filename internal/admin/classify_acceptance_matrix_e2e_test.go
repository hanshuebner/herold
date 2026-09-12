package admin

// classify_acceptance_matrix_e2e_test.go is the #304 acceptance matrix
// (Waves 4.3/4.4): a single mail.classify call answers both the spam
// verdict and the category on the SMTP DATA ingest path, driven against
// a real classifierfixture child process (STANDARDS section 8, no mocks
// at the process boundary), on both store backends (SQLite always,
// Postgres when HEROLD_PG_DSN is set).
//
// The IMAP-import leg of the "one call per message" acceptance item
// lives in internal/imapimport (classify_subprocess_e2e_test.go): the
// in-process fake IMAP server that path needs is white-box to that
// package and is not importable from here.

import (
	"bufio"
	"bytes"
	"context"
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
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// classifyMatrixBackends returns the backend names to exercise: "sqlite"
// always, "postgres" only when HEROLD_PG_DSN is set (local dev/CI
// convention shared with spam_llm_e2e_test.go and
// internal/protoadmin/identity_submission_test.go).
func classifyMatrixBackends() []string {
	backends := []string{"sqlite"}
	if os.Getenv("HEROLD_PG_DSN") != "" {
		backends = append(backends, "postgres")
	}
	return backends
}

// classifyMatrixHarness bundles everything one acceptance scenario needs:
// the SMTP and public HTTP addresses to dial, the seeded principal, and a
// way to reopen the store for verification after delivery.
type classifyMatrixHarness struct {
	smtpAddr   string
	publicAddr string
	domain     string
	pid        store.PrincipalID
	logBuf     *syncBuffer
	// openVerifyStore reopens the store backing the running server for
	// read-only verification; it must not truncate (the server is still
	// running against it).
	openVerifyStore func(t *testing.T) store.Store
}

// syncBuffer is a mutex-guarded bytes.Buffer: StartServer logs from many
// goroutines concurrently, so a plain bytes.Buffer would race under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// pluginTOML is a [[plugin]] block to splice into system.toml, or "" for
// "no plugin installed" (acceptance item 4).
//
// startClassifyMatrixServer seeds a local domain and one principal
// ("alice"), boots admin.StartServer with a relay-in SMTP listener plus
// pluginTOML, and returns a harness for delivering + verifying against
// it. classifyTimeout, when non-zero, is written as [spam]
// classify_timeout.
func startClassifyMatrixServer(t *testing.T, backend, pgDSN, pluginTOML string, classifyTimeout time.Duration) *classifyMatrixHarness {
	t.Helper()
	dir := t.TempDir()
	systomlPath := filepath.Join(dir, "system.toml")
	dbPath := filepath.Join(dir, "db.sqlite")
	blobDir := filepath.Join(dir, "blobs")
	clk := clock.NewReal()

	var storageTOML string
	switch backend {
	case "sqlite":
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"sqlite\"\n[server.storage.sqlite]\npath = %q\n", dbPath)
	case "postgres":
		storageTOML = fmt.Sprintf("[server.storage]\nbackend = \"postgres\"\n[server.storage.postgres]\ndsn = %q\nblob_dir = %q\n", pgDSN, blobDir)
	default:
		t.Fatalf("unknown backend %q", backend)
	}

	spamTOML := ""
	if classifyTimeout > 0 {
		spamTOML = fmt.Sprintf("[spam]\nclassify_timeout = %q\n", classifyTimeout.String())
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

%s

%s

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, dir, filepath.Join(dir, "ports.toml"), storageTOML, pluginTOML, spamTOML)
	if err := os.WriteFile(systomlPath, []byte(systomlBody), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("sysconfig.Load: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	openStore := func(truncate bool) store.Store {
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

	const domain = "test.local"
	preSt := openStore(true)
	if err := preSt.Meta().InsertDomain(ctx, store.Domain{Name: domain, IsLocal: true, CreatedAt: clk.Now()}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dirAdapter := directory.New(preSt.Meta(), discardLogger(), clk, nil)
	pid, err := dirAdapter.CreatePrincipal(ctx, "alice@"+domain, "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if err := preSt.Close(); err != nil {
		t.Fatalf("close pre-seed store: %v", err)
	}

	logBuf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           logger,
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
	publicAddr := addrs["public"]
	addrsMu.Unlock()
	if smtpAddr == "" {
		t.Fatalf("smtp listener not bound; addrs=%+v", addrs)
	}
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	return &classifyMatrixHarness{
		smtpAddr:   smtpAddr,
		publicAddr: publicAddr,
		domain:     domain,
		pid:        pid,
		logBuf:     logBuf,
		openVerifyStore: func(t *testing.T) store.Store {
			t.Helper()
			return openStore(false)
		},
	}
}

// deliverClassifyMatrixMessage sends one raw-SMTP message to
// alice@h.domain over a relay-in (unauthenticated) listener, returning
// the elapsed wall-clock time from the first byte of DATA to the 250
// response -- the "budget plus overhead" measurement acceptance item 5
// needs, since the OS/network round trip (not an injected clock) owns
// that wait at this level.
func deliverClassifyMatrixMessage(t *testing.T, h *classifyMatrixHarness, extraHeaders, msgID string) time.Duration {
	t.Helper()
	conn, err := net.DialTimeout("tcp", h.smtpAddr, 5*time.Second)
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
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
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
	send("RCPT TO:<alice@" + h.domain + ">")
	expect(250)
	send("DATA")
	expect(354)
	start := time.Now()
	rawMsg := "From: bob@external.example\r\n" +
		"To: alice@" + h.domain + "\r\n" +
		extraHeaders +
		"Subject: matrix test\r\n" +
		"Message-ID: <" + msgID + "@external.example>\r\n" +
		"\r\n" +
		"Body text.\r\n" +
		".\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, _ = conn.Write([]byte(rawMsg))
	expect(250) // DATA accepted
	elapsed := time.Since(start)
	send("QUIT")
	return elapsed
}

// waitForMessageInMailbox polls until mailboxName owned by h.pid holds at
// least one message, returning it. Fails the test after 15s.
func waitForMessageInMailbox(t *testing.T, h *classifyMatrixHarness, mailboxName string) store.Message {
	t.Helper()
	st := h.openVerifyStore(t)
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mb, err := st.Meta().GetMailboxByName(ctx, h.pid, mailboxName)
		if err == nil {
			msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
			if err == nil && len(msgs) > 0 {
				return msgs[0]
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no message appeared in %s within 15s", mailboxName)
	return store.Message{}
}

// countCallLogLines counts non-empty lines in a classifierfixture
// HEROLD_TEST_CLASSIFY_CALL_LOG file -- one line per real classify RPC
// the subprocess handled.
func countCallLogLines(b []byte) int {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func categoryKeyword(msg store.Message) string {
	for _, kw := range msg.Keywords {
		if strings.HasPrefix(kw, "$category-") {
			return kw
		}
	}
	return ""
}

// classifierPluginTOML builds a [[plugin]] block for classifierfixture.
func classifierPluginTOML(pluginPath, pluginType string) string {
	return fmt.Sprintf(`
[[plugin]]
name = "spam"
type = %q
path = %q
lifecycle = "long-running"
`, pluginType, pluginPath)
}

// -----------------------------------------------------------------------
// Item 1 (SMTP leg): one classify call, verdict + category.
// -----------------------------------------------------------------------

// TestClassifyMatrix_SMTP_OneCallVerdictAndCategory verifies the SMTP
// DATA path invokes the classifier exactly once per message (counted in
// the real classifierfixture child process via its call-log file) and
// the delivered message carries both a spam verdict (the LLM
// transparency record) and a $category-* keyword.
func TestClassifyMatrix_SMTP_OneCallVerdictAndCategory(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	for _, backend := range classifyMatrixBackends() {
		t.Run(backend, func(t *testing.T) {
			callLog := filepath.Join(t.TempDir(), "calls.log")
			t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "ham")
			t.Setenv("HEROLD_TEST_CLASSIFY_CATEGORY", "promotions")
			t.Setenv("HEROLD_TEST_CLASSIFY_CALL_LOG", callLog)

			var pgDSN string
			if backend == "postgres" {
				pgDSN = os.Getenv("HEROLD_PG_DSN")
			}
			h := startClassifyMatrixServer(t, backend, pgDSN, classifierPluginTOML(fixturePath, "classifier"), 0)
			deliverClassifyMatrixMessage(t, h, "", "smtp-one-call")

			msg := waitForMessageInMailbox(t, h, "INBOX")
			if kw := categoryKeyword(msg); kw != "$category-promotions" {
				t.Fatalf("category keyword = %q, want $category-promotions", kw)
			}

			st := h.openVerifyStore(t)
			defer func() { _ = st.Close() }()
			rec, err := st.Meta().GetLLMClassification(context.Background(), msg.ID)
			if err != nil {
				t.Fatalf("GetLLMClassification: %v", err)
			}
			if rec.SpamVerdict == nil || *rec.SpamVerdict != "ham" {
				t.Fatalf("SpamVerdict = %v, want \"ham\"", rec.SpamVerdict)
			}
			if rec.CategoryAssigned == nil || *rec.CategoryAssigned != "promotions" {
				t.Fatalf("CategoryAssigned = %v, want \"promotions\"", rec.CategoryAssigned)
			}

			calls, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatalf("read call log: %v", err)
			}
			if got := countCallLogLines(calls); got != 1 {
				t.Fatalf("classifier invoked %d times, want exactly 1 (call log: %q)", got, string(calls))
			}
		})
	}
}

// -----------------------------------------------------------------------
// Item 2: a spam verdict files into Junk with no category, even though
// the plugin returned one (ADR-0004).
// -----------------------------------------------------------------------

func TestClassifyMatrix_SpamVerdictDropsCategory(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	for _, backend := range classifyMatrixBackends() {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "spam")
			t.Setenv("HEROLD_TEST_CLASSIFY_SCORE", "0.95")
			t.Setenv("HEROLD_TEST_CLASSIFY_CATEGORY", "promotions")

			var pgDSN string
			if backend == "postgres" {
				pgDSN = os.Getenv("HEROLD_PG_DSN")
			}
			h := startClassifyMatrixServer(t, backend, pgDSN, classifierPluginTOML(fixturePath, "classifier"), 0)
			deliverClassifyMatrixMessage(t, h, "", "smtp-spam-drops-category")

			msg := waitForMessageInMailbox(t, h, "Junk")
			if kw := categoryKeyword(msg); kw != "" {
				t.Fatalf("category keyword = %q, want none (ADR-0004: spam drops category)", kw)
			}

			st := h.openVerifyStore(t)
			defer func() { _ = st.Close() }()
			rec, err := st.Meta().GetLLMClassification(context.Background(), msg.ID)
			if err != nil {
				t.Fatalf("GetLLMClassification: %v", err)
			}
			if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
				t.Fatalf("SpamVerdict = %v, want \"spam\"", rec.SpamVerdict)
			}
			if rec.CategoryAssigned != nil {
				t.Fatalf("CategoryAssigned = %v, want nil", *rec.CategoryAssigned)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Item 3: a category outside the principal's set is ignored and logged.
// -----------------------------------------------------------------------

func TestClassifyMatrix_CategoryOutsideSetIgnoredAndLogged(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	for _, backend := range classifyMatrixBackends() {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "ham")
			t.Setenv("HEROLD_TEST_CLASSIFY_CATEGORY", "not-a-real-category")

			var pgDSN string
			if backend == "postgres" {
				pgDSN = os.Getenv("HEROLD_PG_DSN")
			}
			h := startClassifyMatrixServer(t, backend, pgDSN, classifierPluginTOML(fixturePath, "classifier"), 0)
			deliverClassifyMatrixMessage(t, h, "", "smtp-category-outside-set")

			msg := waitForMessageInMailbox(t, h, "INBOX")
			if kw := categoryKeyword(msg); kw != "" {
				t.Fatalf("category keyword = %q, want none (category outside the principal's set is dropped)", kw)
			}

			logs := h.logBuf.String()
			if !strings.Contains(logs, "outside principal's set") || !strings.Contains(logs, "not-a-real-category") {
				t.Fatalf("expected a logged warning naming the rejected category; log tail:\n%s", tailLines(logs, 40))
			}
		})
	}
}

func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// -----------------------------------------------------------------------
// Item 4: with no plugin installed, a list message lands in "forums"
// through the structural fallback (REQ-FILT-214, ADR-0002).
// -----------------------------------------------------------------------

func TestClassifyMatrix_NoPlugin_StructuralFallbackToForums(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	for _, backend := range classifyMatrixBackends() {
		t.Run(backend, func(t *testing.T) {
			var pgDSN string
			if backend == "postgres" {
				pgDSN = os.Getenv("HEROLD_PG_DSN")
			}
			// No [[plugin]] block at all.
			h := startClassifyMatrixServer(t, backend, pgDSN, "", 0)
			deliverClassifyMatrixMessage(t, h, "List-Id: <announce.external.example>\r\n", "smtp-no-plugin-forums")

			msg := waitForMessageInMailbox(t, h, "INBOX")
			if kw := categoryKeyword(msg); kw != "$category-forums" {
				t.Fatalf("category keyword = %q, want $category-forums", kw)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Item 5: a plugin sleeping past [spam] classify_timeout is cut off and
// the mail is delivered within budget plus overhead; the message lands
// unclassified in INBOX. The classify budget itself is enforced against
// admin.StartServer's real (non-injected) clock, so this asserts a
// bounded real-time margin around the SMTP round trip -- the OS/network
// wait is what actually owns the delay at this level (the deterministic,
// FakeClock-driven assertion of the same cutoff mechanism lives in
// internal/imapimport's classify_subprocess_e2e_test.go and
// internal/spam's TestClassify_BudgetCutoff_FakeClock).
// -----------------------------------------------------------------------

func TestClassifyMatrix_TimeoutCutoffWithinBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	const budget = 300 * time.Millisecond
	const overhead = 8 * time.Second // generous margin: process boot, scheduling jitter, CI noise
	for _, backend := range classifyMatrixBackends() {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("HEROLD_TEST_CLASSIFY_SLEEP_MS", "30000") // far past the budget
			t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "ham")

			var pgDSN string
			if backend == "postgres" {
				pgDSN = os.Getenv("HEROLD_PG_DSN")
			}
			h := startClassifyMatrixServer(t, backend, pgDSN, classifierPluginTOML(fixturePath, "classifier"), budget)
			// No structural-fallback headers: this asserts the timeout
			// cutoff, not the (separately tested) fallback categoriser.
			elapsed := deliverClassifyMatrixMessage(t, h, "", "smtp-timeout-cutoff")

			if elapsed > budget+overhead {
				t.Fatalf("delivery took %s, want <= budget(%s)+overhead(%s) = %s", elapsed, budget, overhead, budget+overhead)
			}

			msg := waitForMessageInMailbox(t, h, "INBOX")
			if kw := categoryKeyword(msg); kw != "" {
				t.Errorf("category keyword = %q, want none for a timed-out classify call", kw)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Item 6: a [[plugin]] type = "spam" configuration with the classifier
// fixture declaring the old type still classifies, with no category
// (issue #304 Decision 3, one-release compatibility).
// -----------------------------------------------------------------------

func TestClassifyMatrix_LegacySpamTypeCompat(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	for _, backend := range classifyMatrixBackends() {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("HEROLD_TEST_CLASSIFY_PLUGIN_TYPE", "spam")
			t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "ham")

			var pgDSN string
			if backend == "postgres" {
				pgDSN = os.Getenv("HEROLD_PG_DSN")
			}
			// Operator config says type = "spam"; the fixture's own
			// manifest also declares "spam" (the old contract) here.
			h := startClassifyMatrixServer(t, backend, pgDSN, classifierPluginTOML(fixturePath, "spam"), 0)
			deliverClassifyMatrixMessage(t, h, "", "smtp-legacy-spam-type")

			msg := waitForMessageInMailbox(t, h, "INBOX")
			if kw := categoryKeyword(msg); kw != "" {
				t.Fatalf("category keyword = %q, want none (spam.classify carries no category field)", kw)
			}

			st := h.openVerifyStore(t)
			defer func() { _ = st.Close() }()
			rec, err := st.Meta().GetLLMClassification(context.Background(), msg.ID)
			if err != nil {
				t.Fatalf("GetLLMClassification: %v", err)
			}
			if rec.SpamVerdict == nil || *rec.SpamVerdict != "ham" {
				t.Fatalf("SpamVerdict = %v, want \"ham\"", rec.SpamVerdict)
			}
		})
	}
}
