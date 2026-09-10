package protoimap_test

// trace_test.go verifies the trace-level IMAP wire log (REQ-OPS-82,
// issue #320): LOGIN + SELECT + FETCH BODY.PEEK against a trace-enabled
// logger must show the command lines (password redacted) and the response
// lines (a body literal truncated to 256 bytes with its full length
// noted); the same flow against an info-level logger must show none of it.

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// lockedBuffer is a bytes.Buffer safe for concurrent use: the IMAP session
// logs from its own goroutine while the test reads the captured output
// (mirrors internal/plugin's supervisor_integration_test.go pattern).
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// buildTraceServer wires a protoimap server to the given logger, seeds one
// principal, and returns a ready-to-dial fixture.
func buildTraceServer(t *testing.T, log *slog.Logger) *fixture {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "imaps", Protocol: "imap"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "trace.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	password := "correct-horse-battery-staple"
	pid, err := dir.CreatePrincipal(ctx, "trace-alice@trace.test", password)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	inbox, err := ha.Store.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		t.Fatalf("get INBOX: %v", err)
	}
	if err := ha.Store.Meta().SetMailboxSubscribed(ctx, inbox.ID, true); err != nil {
		t.Fatalf("subscribe INBOX: %v", err)
	}
	tlsStore, clientCfg := newTestTLSStore(t)
	srv := protoimap.NewServer(
		ha.Store, dir, tlsStore, ha.Clock, log, nil, nil,
		protoimap.Options{
			MaxConnections:         16,
			MaxCommandsPerSession:  1000,
			IdleMaxDuration:        30 * time.Minute,
			ServerName:             "herold-test",
			DefaultCommandDeadline: 30 * time.Second,
		},
	)
	ha.AttachIMAP("imaps", srv, protoimap.ListenerModeImplicit993)
	t.Cleanup(func() { _ = srv.Close() })
	return &fixture{
		ha: ha, srv: srv, name: "imaps",
		pid: pid, password: password,
		dir: dir, tlsCfg: clientCfg, inbox: inbox,
	}
}

// TestTrace_LoginSelectFetch_TraceLevel drives LOGIN + SELECT + FETCH
// BODY.PEEK against a trace-enabled logger and asserts the command and
// response lines it produces (REQ-OPS-82, issue #320).
func TestTrace_LoginSelectFetch_TraceLevel(t *testing.T) {
	var logBuf lockedBuffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: observe.LevelTrace}))

	f := buildTraceServer(t, log)

	// Seed a message whose body is well over the 256-byte trace truncation
	// threshold so the test can assert both the truncation marker and the
	// full length.
	body := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 10) // 450 bytes
	msg := buildMessage("trace-fetch", body)
	blob, err := f.ha.Store.Blobs().Put(context.Background(), strings.NewReader(msg))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	_, _, err = f.ha.Store.Meta().InsertMessage(context.Background(), store.Message{
		PrincipalID:  f.pid,
		InternalDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Size:         int64(len(msg)),
		Blob:         blob,
		Envelope:     parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{MailboxID: f.inbox.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	c := f.dialImplicitTLS(t)
	defer c.close()

	loginResp := c.send("a1", fmt.Sprintf("LOGIN trace-alice@trace.test %s", f.password))
	if !strings.Contains(loginResp[len(loginResp)-1], "OK") {
		t.Fatalf("login failed: %v", loginResp)
	}
	selResp := c.send("a2", "SELECT INBOX")
	if !strings.Contains(selResp[len(selResp)-1], "OK") {
		t.Fatalf("select failed: %v", selResp)
	}
	fetchResp := c.send("a3", "FETCH 1 (BODY.PEEK[])")
	if !strings.Contains(fetchResp[len(fetchResp)-1], "OK") {
		t.Fatalf("fetch failed: %v", fetchResp)
	}

	logs := logBuf.String()

	// The LOGIN command line must be present with the username visible
	// and the password absent.
	if !strings.Contains(logs, "a1 LOGIN trace-alice@trace.test REDACTED") {
		t.Fatalf("expected redacted LOGIN trace line; got:\n%s", logs)
	}
	if strings.Contains(logs, f.password) {
		t.Fatalf("password leaked into trace log:\n%s", logs)
	}

	// The FETCH command line must be present.
	if !strings.Contains(logs, "a3 FETCH 1 (BODY.PEEK[])") {
		t.Fatalf("expected FETCH command trace line; got:\n%s", logs)
	}

	// The response side: at least one untagged and one tagged response
	// line must have been traced.
	if !strings.Contains(logs, `"kind":"untagged"`) {
		t.Fatalf("expected an untagged response trace line; got:\n%s", logs)
	}
	if !strings.Contains(logs, `"kind":"tagged"`) {
		t.Fatalf("expected a tagged response trace line; got:\n%s", logs)
	}

	// BODY.PEEK[] fetches the entire message (headers + body); the trace
	// line must truncate it to 256 bytes and note the full length.
	fullLen := len(msg)
	if !strings.Contains(logs, fmt.Sprintf("{%d bytes, showing first 256}", fullLen)) {
		t.Fatalf("expected literal truncation marker for %d bytes; got:\n%s", fullLen, logs)
	}
	// The body repeats a 46-byte phrase 10 times; only the first 256
	// bytes of the 647-byte message (header + body) are shown, which
	// covers at most one full repeat of the phrase. Ten repeats present
	// in the log would mean truncation did not happen.
	if strings.Count(logs, "the quick brown fox jumps over the lazy dog. ") > 2 {
		t.Fatalf("expected the body literal to be truncated, but repeated body text is present:\n%s", logs)
	}

	// Every trace record carries the session id and (once authenticated)
	// the principal.
	if !strings.Contains(logs, `"session_id"`) {
		t.Fatalf("expected session_id on trace records; got:\n%s", logs)
	}
	if !strings.Contains(logs, fmt.Sprintf(`"principal_id":%d`, f.pid)) {
		t.Fatalf("expected principal_id on trace records after LOGIN; got:\n%s", logs)
	}
}

// TestTrace_LoginSelectFetch_InfoLevel drives the same flow against an
// info-level logger and asserts no trace-level wire lines are emitted.
func TestTrace_LoginSelectFetch_InfoLevel(t *testing.T) {
	var logBuf lockedBuffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	f := buildTraceServer(t, log)
	c := f.dialImplicitTLS(t)
	defer c.close()

	loginResp := c.send("a1", fmt.Sprintf("LOGIN trace-alice@trace.test %s", f.password))
	if !strings.Contains(loginResp[len(loginResp)-1], "OK") {
		t.Fatalf("login failed: %v", loginResp)
	}
	c.send("a2", "SELECT INBOX")

	logs := logBuf.String()
	if strings.Contains(logs, "protoimap: C: ") || strings.Contains(logs, "protoimap: S: ") {
		t.Fatalf("did not expect any trace-level wire log lines at info level; got:\n%s", logs)
	}
}
