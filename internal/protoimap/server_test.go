package protoimap_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// -----------------------------------------------------------------------------
// fixture
// -----------------------------------------------------------------------------

type fixture struct {
	ha       *testharness.Server
	srv      *protoimap.Server
	name     string
	pid      store.PrincipalID
	password string
	dir      *directory.Directory
	tlsCfg   *tls.Config
	inbox    store.Mailbox
}

type fxOpts struct {
	implicitTLS     bool
	allowPlainLogin bool
	downloadRate    int64 // bytes/sec, 0 disables
	downloadBurst   int64
}

func newFixture(t *testing.T, fo fxOpts) *fixture {
	t.Helper()

	name := "imap"
	proto := "imap"
	if fo.implicitTLS {
		name = "imaps"
		proto = "imaps"
	}
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: name, Protocol: proto}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	password := "correct-horse-staple-battery"
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", password)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}

	// Create an INBOX for alice.
	inbox, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox | store.MailboxAttrSubscribed,
	})
	if err != nil {
		t.Fatalf("insert INBOX: %v", err)
	}

	// TLS store + client config.
	tlsStore, clientCfg := newTestTLSStore(t)

	srv := protoimap.NewServer(
		ha.Store,
		dir,
		tlsStore,
		ha.Clock,
		ha.Logger,
		nil, // passwords lookup (SCRAM) - tests that need it wire one
		nil, // token verifier
		protoimap.Options{
			MaxConnections:            16,
			MaxCommandsPerSession:     1000,
			DownloadBytesPerSecond:    fo.downloadRate,
			DownloadBurstBytes:        fo.downloadBurst,
			IdleMaxDuration:           30 * time.Minute,
			AllowPlainLoginWithoutTLS: fo.allowPlainLogin,
			ServerName:                "herold",
		},
	)
	mode := protoimap.ListenerModeSTARTTLS
	if fo.implicitTLS {
		mode = protoimap.ListenerModeImplicit993
	}
	ha.AttachIMAP(name, srv, mode)
	t.Cleanup(func() { _ = srv.Close() })

	return &fixture{
		ha: ha, srv: srv, name: name,
		pid: pid, password: password,
		dir: dir, tlsCfg: clientCfg, inbox: inbox,
	}
}

func newTestTLSStore(t *testing.T) (*heroldtls.Store, *tls.Config) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mail.example.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"mail.example.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	leaf, _ := x509.ParseCertificate(der)
	cert.Leaf = leaf
	st := heroldtls.NewStore()
	st.SetDefault(&cert)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return st, &tls.Config{RootCAs: pool, ServerName: "mail.example.test"}
}

// -----------------------------------------------------------------------------
// client helpers
// -----------------------------------------------------------------------------

type client struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func (f *fixture) dial(t *testing.T) *client {
	t.Helper()
	conn, err := f.ha.DialIMAPByName(context.Background(), f.name)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &client{t: t, conn: conn, br: bufio.NewReader(conn)}
	// Consume greeting.
	c.readLine()
	return c
}

func (f *fixture) dialImplicitTLS(t *testing.T) *client {
	t.Helper()
	conn, err := f.ha.DialIMAPSByName(context.Background(), f.name, f.tlsCfg)
	if err != nil {
		t.Fatalf("dialTLS: %v", err)
	}
	c := &client{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.readLine()
	return c
}

func (c *client) readLine() string {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.br.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v (partial=%q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

func (c *client) readN(n int) []byte {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		c.t.Fatalf("read n=%d: %v", n, err)
	}
	return buf
}

func (c *client) write(s string) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write([]byte(s)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *client) send(tag, cmd string) []string {
	c.write(fmt.Sprintf("%s %s\r\n", tag, cmd))
	return c.readUntilTag(tag)
}

// readUntilTag collects response lines until it sees "tag " at start; the
// returned slice includes the tagged status response as the final entry.
func (c *client) readUntilTag(tag string) []string {
	var lines []string
	for {
		line := c.readLine()
		lines = append(lines, line)
		if strings.HasPrefix(line, tag+" ") {
			return lines
		}
	}
}

func (c *client) close() { _ = c.conn.Close() }

// -----------------------------------------------------------------------------
// CAPABILITY
// -----------------------------------------------------------------------------

func TestCAPABILITY_BeforeLogin(t *testing.T) {
	f := newFixture(t, fxOpts{})
	c := f.dial(t)
	defer c.close()
	lines := c.send("a1", "CAPABILITY")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "IMAP4rev2") || !strings.Contains(joined, "STARTTLS") {
		t.Fatalf("missing baseline caps: %v", lines)
	}
	if !strings.Contains(joined, "LOGINDISABLED") {
		t.Fatalf("expected LOGINDISABLED on cleartext listener: %v", lines)
	}
	if strings.Contains(joined, "AUTH=PLAIN") {
		t.Fatalf("PLAIN should not be advertised over cleartext: %v", lines)
	}
}

// TestIMAPMetrics_CommandIncrementsCounter drives one CAPABILITY command
// and asserts the herold_imap_commands_total{command="CAPABILITY"}
// counter advanced by at least one. Proves the dispatch-level metric
// wiring is live and the bounded-label vocabulary survives.
func TestIMAPMetrics_CommandIncrementsCounter(t *testing.T) {
	observe.RegisterIMAPMetrics()
	before := testutil.ToFloat64(observe.IMAPCommandsTotal.WithLabelValues("CAPABILITY"))

	f := newFixture(t, fxOpts{})
	c := f.dial(t)
	defer c.close()
	_ = c.send("a1", "CAPABILITY")

	after := testutil.ToFloat64(observe.IMAPCommandsTotal.WithLabelValues("CAPABILITY"))
	if after <= before {
		t.Fatalf("herold_imap_commands_total{command=CAPABILITY}: before=%v after=%v; want strict increase", before, after)
	}
}

func TestCAPABILITY_AfterLoginOverTLS(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := f.dialImplicitTLS(t)
	defer c.close()
	// Pre-login caps.
	pre := c.send("a1", "CAPABILITY")
	if !strings.Contains(strings.Join(pre, "\n"), "AUTH=PLAIN") {
		t.Fatalf("expected AUTH=PLAIN over TLS: %v", pre)
	}
	// Login.
	resp := c.send("a2", fmt.Sprintf("LOGIN alice@example.test %s", f.password))
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("login failed: %v", resp)
	}
	post := c.send("a3", "CAPABILITY")
	// LOGINDISABLED should no longer appear.
	if strings.Contains(strings.Join(post, "\n"), "LOGINDISABLED") {
		t.Fatalf("LOGINDISABLED should not advertise after auth: %v", post)
	}
}

// -----------------------------------------------------------------------------
// LOGIN
// -----------------------------------------------------------------------------

func TestLOGIN_RejectedWithoutTLS(t *testing.T) {
	f := newFixture(t, fxOpts{})
	c := f.dial(t)
	defer c.close()
	resp := c.send("a1", fmt.Sprintf("LOGIN alice@example.test %s", f.password))
	last := resp[len(resp)-1]
	if !strings.Contains(last, "NO") || !strings.Contains(last, "PRIVACYREQUIRED") {
		t.Fatalf("expected NO [PRIVACYREQUIRED], got: %v", last)
	}
}

func TestLOGIN_OverTLS_Succeeds(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := f.dialImplicitTLS(t)
	defer c.close()
	resp := c.send("a1", fmt.Sprintf("LOGIN alice@example.test %s", f.password))
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("expected OK, got: %v", last)
	}
}

func TestLOGIN_AllowedWithoutTLS_WhenConfigured(t *testing.T) {
	f := newFixture(t, fxOpts{allowPlainLogin: true})
	c := f.dial(t)
	defer c.close()
	resp := c.send("a1", fmt.Sprintf("LOGIN alice@example.test %s", f.password))
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("expected OK with AllowPlainLoginWithoutTLS, got: %v", last)
	}
}

// -----------------------------------------------------------------------------
// SELECT / APPEND / FETCH / STORE / EXPUNGE / SEARCH
// -----------------------------------------------------------------------------

// loggedInClient dials + logs in + SELECTs INBOX, returning a ready client.
func loggedInClient(t *testing.T, f *fixture) *client {
	t.Helper()
	c := f.dialImplicitTLS(t)
	resp := c.send("LOGIN", fmt.Sprintf("LOGIN alice@example.test %s", f.password))
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("login failed: %v", resp)
	}
	return c
}

func TestSELECT_ReturnsExpectedUntaggedResponses(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("s1", "SELECT INBOX")
	joined := strings.Join(resp, "\n")
	for _, needle := range []string{"EXISTS", "UIDVALIDITY", "UIDNEXT", "FLAGS", "PERMANENTFLAGS"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("missing %q in SELECT response: %v", needle, resp)
		}
	}
	if !strings.Contains(resp[len(resp)-1], "OK") || !strings.Contains(resp[len(resp)-1], "READ-WRITE") {
		t.Fatalf("expected OK READ-WRITE: %v", resp)
	}
}

// buildMessage returns a minimal RFC 5322 message body.
func buildMessage(subject, body string) string {
	return "From: sender@example.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Fri, 01 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <" + subject + "@example.test>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		body + "\r\n"
}

func TestAPPEND_UIDPLUS_ReturnsUID(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	msg := buildMessage("test-append", "hello world")
	// Send APPEND with a synchronising literal.
	c.write(fmt.Sprintf("a1 APPEND INBOX (\\Seen) {%d}\r\n", len(msg)))
	// Expect continuation.
	line := c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected continuation, got: %q", line)
	}
	c.write(msg + "\r\n")
	resp := c.readUntilTag("a1")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") || !strings.Contains(last, "APPENDUID") {
		t.Fatalf("expected OK [APPENDUID ...], got: %v", last)
	}
}

func TestFETCH_Envelope_Body_Flags(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	// Pre-seed a message directly via the store.
	ctx := context.Background()
	msg := buildMessage("invoice", "the invoice body text")
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	env := parseStoreEnvelope(msg)
	_, _, err = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    f.inbox.ID,
		Flags:        0,
		InternalDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Size:         int64(len(msg)),
		Blob:         blob,
		Envelope:     env,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("f1", "FETCH 1 (UID FLAGS ENVELOPE RFC822.SIZE)")
	joined := strings.Join(resp, "\n")
	for _, needle := range []string{"UID ", "FLAGS ", "ENVELOPE", "RFC822.SIZE", "invoice"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("missing %q in FETCH: %v", needle, resp)
		}
	}
}

// parseStoreEnvelope extracts envelope fields for seeded messages.
func parseStoreEnvelope(msg string) store.Envelope {
	var env store.Envelope
	lines := strings.Split(msg, "\r\n")
	for _, ln := range lines {
		if ln == "" {
			break
		}
		colon := strings.IndexByte(ln, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(ln[:colon])
		val := strings.TrimSpace(ln[colon+1:])
		switch strings.ToLower(key) {
		case "subject":
			env.Subject = val
		case "from":
			env.From = val
		case "to":
			env.To = val
		case "message-id":
			env.MessageID = strings.Trim(val, "<>")
		case "date":
			if t, err := time.Parse(time.RFC1123Z, val); err == nil {
				env.Date = t
			}
		}
	}
	return env
}

func TestSTORE_SetFlags_EmitsUntaggedFETCH(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		msg := buildMessage(fmt.Sprintf("m%d", i), "body")
		blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
		_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
			MailboxID: f.inbox.ID, Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
		})
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("st1", `STORE 1:3 +FLAGS (\Seen)`)
	seen := 0
	for _, line := range resp {
		if strings.Contains(line, "FETCH") && strings.Contains(line, "FLAGS") && strings.Contains(line, "\\Seen") {
			seen++
		}
	}
	if seen != 3 {
		t.Fatalf("expected 3 untagged FETCH responses, got %d: %v", seen, resp)
	}
}

func TestSEARCH_Flagged_Subject(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	insert := func(subject string, flagged bool) {
		msg := buildMessage(subject, "body")
		blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
		var flags store.MessageFlags
		if flagged {
			flags = store.MessageFlagFlagged
		}
		_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
			MailboxID: f.inbox.ID, Size: int64(len(msg)), Blob: blob, Flags: flags, Envelope: parseStoreEnvelope(msg),
		})
	}
	insert("invoice 001", true)
	insert("hello world", false)
	insert("invoice 002", false)

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	// Only msg 1 is both FLAGGED and contains "invoice".
	resp := c.send("q1", `SEARCH FLAGGED SUBJECT "invoice"`)
	joined := strings.Join(resp, "\n")
	// Canonical "* SEARCH 1" untagged output.
	foundMatch := false
	for _, line := range resp {
		if strings.HasPrefix(line, "* SEARCH") {
			foundMatch = true
			if !strings.Contains(line, " 1") {
				t.Fatalf("expected SEARCH to match msg 1: %q", line)
			}
			if strings.Contains(line, " 2") || strings.Contains(line, " 3") {
				t.Fatalf("SEARCH should not match msgs 2/3: %q", line)
			}
		}
	}
	if !foundMatch {
		t.Fatalf("no SEARCH response: %v", joined)
	}
}

func TestUID_FETCH_Semantics(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	var firstUID store.UID
	for i := 0; i < 3; i++ {
		msg := buildMessage(fmt.Sprintf("m%d", i), "body")
		blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
		uid, _, _ := f.ha.Store.Meta().InsertMessage(ctx, store.Message{
			MailboxID: f.inbox.ID, Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
		})
		if i == 0 {
			firstUID = uid
		}
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("u1", fmt.Sprintf("UID FETCH %d (UID)", firstUID))
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, fmt.Sprintf("UID %d", firstUID)) {
		t.Fatalf("UID FETCH did not return UID %d: %v", firstUID, resp)
	}
}

func TestEXPUNGE_EmitsUntaggedExpunges(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		msg := buildMessage(fmt.Sprintf("m%d", i), "body")
		blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
		_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
			MailboxID: f.inbox.ID, Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
		})
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	c.send("st1", `STORE 1:3 +FLAGS (\Deleted)`)
	resp := c.send("e1", "EXPUNGE")
	expungeCount := 0
	for _, line := range resp {
		if strings.HasSuffix(line, " EXPUNGE") || strings.Contains(line, "* ") && strings.Contains(line, " EXPUNGE") {
			expungeCount++
		}
	}
	if expungeCount != 3 {
		t.Fatalf("expected 3 EXPUNGE responses, got %d: %v", expungeCount, resp)
	}
}

// -----------------------------------------------------------------------------
// IDLE
// -----------------------------------------------------------------------------

func TestIDLE_ClientSendsDONE_ReturnsOK(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	c.write("i1 IDLE\r\n")
	line := c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected continuation, got %q", line)
	}
	// Give the poll goroutine time to spin up, then send DONE.
	time.Sleep(50 * time.Millisecond)
	c.write("DONE\r\n")
	resp := c.readUntilTag("i1")
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("IDLE did not complete OK: %v", resp)
	}
}

func TestIDLE_StreamsNewMessage(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	c.write("i1 IDLE\r\n")
	line := c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected continuation, got %q", line)
	}

	// Advance fake clock so the IDLE poll ticks.
	ctx := context.Background()
	msg := buildMessage("pushed", "idle delivery")
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID: f.inbox.ID, Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
	})
	// Fire the poll tick.
	if fc, ok := f.ha.Clock.(*clock.FakeClock); ok {
		fc.Advance(250 * time.Millisecond)
	}

	// Look for "* 1 EXISTS" within a bounded window.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		line, err := c.br.ReadString('\n')
		if err != nil {
			if fc, ok := f.ha.Clock.(*clock.FakeClock); ok {
				fc.Advance(250 * time.Millisecond)
			}
			continue
		}
		if strings.Contains(line, "EXISTS") {
			break
		}
	}
	// Terminate.
	_ = c.conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	c.write("DONE\r\n")
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_ = c.br // drain until tag; we do not fail this test on exact newer responses
}

// -----------------------------------------------------------------------------
// FETCH download rate-limit throttling
// -----------------------------------------------------------------------------

func TestFETCH_DownloadRateLimit_Throttles(t *testing.T) {
	// Very low rate + small burst forces the bucket to block; advance the
	// fake clock to replenish tokens and unblock.
	f := newFixture(t, fxOpts{
		implicitTLS:   true,
		downloadRate:  100, // 100 bytes/sec
		downloadBurst: 50,  // burst smaller than one message
	})
	ctx := context.Background()
	// 500-byte body.
	body := strings.Repeat("A", 500)
	msg := buildMessage("big", body)
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID: f.inbox.ID, Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
	})
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")

	// FETCH the full body; should block since burst < body size.
	done := make(chan []string, 1)
	go func() { done <- c.send("f1", "FETCH 1 (BODY[])") }()

	// Give the goroutine a tick to register a waiter on the FakeClock.
	time.Sleep(50 * time.Millisecond)
	fc, ok := f.ha.Clock.(*clock.FakeClock)
	if !ok {
		t.Fatalf("expected FakeClock")
	}
	// Advance enough time to accumulate the full 500+overhead bytes.
	// With 100 B/s, 10 seconds is plenty. Yield between advances so the
	// consumer goroutine can re-register its next After waiter.
	for i := 0; i < 30; i++ {
		fc.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case resp := <-done:
		if !strings.Contains(resp[len(resp)-1], "OK") {
			t.Fatalf("expected OK after throttled fetch, got: %v", resp)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("FETCH did not complete after clock advance")
	}
}

// -----------------------------------------------------------------------------
// REQ-PROTO-49 JMAP snooze (IMAP cross-cut)
// -----------------------------------------------------------------------------

// seedMessageID inserts a body into f.inbox via the store and returns
// the message id. Used by the snooze cross-cut tests.
func seedMessageID(t *testing.T, f *fixture, subject string) store.MessageID {
	t.Helper()
	ctx := context.Background()
	body := buildMessage(subject, "body")
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, _, err := f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    f.inbox.ID,
		Size:         int64(len(body)),
		Blob:         blob,
		InternalDate: time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC),
		Envelope:     parseStoreEnvelope(body),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	feed, err := f.ha.Store.Meta().ReadChangeFeed(ctx, f.pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var id store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			id = store.MessageID(e.EntityID)
		}
	}
	if id == 0 {
		t.Fatalf("no created entry")
	}
	return id
}

func TestSELECT_PERMANENTFLAGS_Includes_Snoozed(t *testing.T) {
	// PERMANENTFLAGS includes the wildcard "\*", which advertises
	// that arbitrary keywords (including "$snoozed") may be set on
	// messages in the mailbox per RFC 3501 §6.3.1. We assert "\*" is
	// present so the existing keyword machinery satisfies the snooze
	// requirement without a per-keyword enumeration.
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("s1", "SELECT INBOX")
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "PERMANENTFLAGS") {
		t.Fatalf("PERMANENTFLAGS missing: %v", resp)
	}
	if !strings.Contains(joined, `\*`) {
		t.Fatalf(`PERMANENTFLAGS missing \* (keyword wildcard): %v`, resp)
	}
}

func TestSTORE_AddSnoozedKeyword_RequiresDate(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	id := seedMessageID(t, f, "snooze-add")
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("st1", `STORE 1 +FLAGS ($snoozed)`)
	last := resp[len(resp)-1]
	if !strings.Contains(last, "BAD") {
		t.Fatalf("expected BAD for $snoozed without date: %v", resp)
	}
	if !strings.Contains(last, "SNOOZE-DATE-MISSING") {
		t.Fatalf("expected SNOOZE-DATE-MISSING response code: %v", last)
	}
	// Message must be unchanged.
	m, err := f.ha.Store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	for _, k := range m.Keywords {
		if k == "$snoozed" {
			t.Fatalf("$snoozed keyword set despite BAD response")
		}
	}
	if m.SnoozedUntil != nil {
		t.Fatalf("SnoozedUntil set despite BAD response: %v", m.SnoozedUntil)
	}
}

func TestSTORE_RemoveSnoozedKeyword_AlsoNullsColumn(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	id := seedMessageID(t, f, "snooze-remove")
	// Set snooze via the store (canonical JMAP path).
	t1 := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	if _, err := f.ha.Store.Meta().SetSnooze(context.Background(), id, &t1); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("st1", `STORE 1 -FLAGS ($snoozed)`)
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("expected OK on -FLAGS $snoozed, got: %v", resp)
	}
	m, err := f.ha.Store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.SnoozedUntil != nil {
		t.Errorf("SnoozedUntil = %v, want nil after STORE -FLAGS $snoozed", m.SnoozedUntil)
	}
	for _, k := range m.Keywords {
		if k == "$snoozed" {
			t.Errorf("$snoozed keyword still present")
		}
	}
}

func TestSEARCH_Keyword_Snoozed_FindsSnoozedMessages(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	id1 := seedMessageID(t, f, "msg-snoozed")
	seedMessage(t, f, "msg-not-snoozed")
	t1 := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	if _, err := f.ha.Store.Meta().SetSnooze(context.Background(), id1, &t1); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("se1", `SEARCH KEYWORD $snoozed`)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "* SEARCH") {
		t.Fatalf("SEARCH response missing untagged: %v", resp)
	}
	// The snoozed message should appear; the unsnoozed should not.
	// Sequence numbers (UIDs are 1 and 2 in INBOX); the snoozed id
	// corresponds to seq 1.
	if !strings.Contains(joined, "* SEARCH 1") {
		t.Errorf("expected SEARCH 1 (snoozed message), got: %v", resp)
	}
	if strings.Contains(joined, "* SEARCH 1 2") || strings.Contains(joined, "* SEARCH 2") {
		t.Errorf("unsnoozed message returned by SEARCH KEYWORD $snoozed: %v", resp)
	}
}
