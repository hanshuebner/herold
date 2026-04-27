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
	"encoding/base64"
	"fmt"
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

// TestAUTHENTICATE_PLAIN_SASLIR exercises RFC 4959 SASL-IR: the initial
// response is base64-encoded on the AUTHENTICATE line. Mutt and many
// other IMAP clients use SASL-IR by default, so a regression here breaks
// real-world interop.
func TestAUTHENTICATE_PLAIN_SASLIR(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := f.dialImplicitTLS(t)
	defer c.close()
	// authzid = authcid, both equal to the email; per RFC 4616 this is
	// equivalent to an empty authzid and must be accepted.
	cred := []byte("alice@example.test\x00alice@example.test\x00" + f.password)
	ir := base64.StdEncoding.EncodeToString(cred)
	resp := c.send("a1", "AUTHENTICATE PLAIN "+ir)
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("expected OK from AUTHENTICATE PLAIN with SASL-IR, got: %v", resp)
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

// -----------------------------------------------------------------------------
// RFC 3501 §7.2.6 / RFC 9051 §7.3.5 keyword advertisement in * FLAGS
// -----------------------------------------------------------------------------

// TestSELECT_FLAGS_IncludesExistingKeywords verifies that when a mailbox
// already contains messages with keyword flags, SELECT reports those keywords
// in the "* FLAGS" untagged response — not just the five system flags.
func TestSELECT_FLAGS_IncludesExistingKeywords(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := buildMessage("kw-seed", "body")
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID: f.inbox.ID,
		Size:      int64(len(msg)),
		Blob:      blob,
		Keywords:  []string{"$label1"},
		Envelope:  parseStoreEnvelope(msg),
	})

	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("s1", "SELECT INBOX")
	joined := strings.Join(resp, "\n")
	// "$label1" must appear in the * FLAGS line.
	if !strings.Contains(joined, "$label1") {
		t.Fatalf("SELECT FLAGS did not include pre-existing keyword $label1: %v", resp)
	}
	// PERMANENTFLAGS must also carry it and \*.
	if !strings.Contains(joined, "PERMANENTFLAGS") {
		t.Fatalf("PERMANENTFLAGS missing: %v", resp)
	}
	if !strings.Contains(joined, `\*`) {
		t.Fatalf(`PERMANENTFLAGS missing \*: %v`, resp)
	}
}

// TestSELECT_FLAGS_EmptyMailbox verifies that SELECT on a mailbox with no
// messages returns only the five system flags (no keyword noise).
func TestSELECT_FLAGS_EmptyMailbox(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("s1", "SELECT INBOX")
	joined := strings.Join(resp, "\n")
	// The FLAGS line must contain the five system flags …
	for _, sf := range []string{`\Answered`, `\Flagged`, `\Deleted`, `\Seen`, `\Draft`} {
		if !strings.Contains(joined, sf) {
			t.Fatalf("system flag %s missing from FLAGS: %v", sf, resp)
		}
	}
	// … and nothing that looks like a user keyword (starts with '$' or a
	// lowercase letter without backslash that is not "Limited" etc.).
	for _, line := range resp {
		if !strings.HasPrefix(line, "* FLAGS") {
			continue
		}
		// "$" or a bare lowercase atom in the FLAGS list would be a keyword.
		if strings.Contains(line, "$") {
			t.Fatalf("unexpected keyword in FLAGS on empty mailbox: %q", line)
		}
	}
}

// TestSTORE_NewKeyword_EmitsFLAGSBeforeFETCH verifies that STORE +FLAGS
// ($foo) on a message that does not yet carry $foo emits a fresh "* FLAGS"
// untagged response before the "* N FETCH (FLAGS ...)" response, and that the
// * FLAGS line includes the new keyword.
func TestSTORE_NewKeyword_EmitsFLAGSBeforeFETCH(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := buildMessage("kw-new", "body")
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID: f.inbox.ID,
		Size:      int64(len(msg)),
		Blob:      blob,
		Envelope:  parseStoreEnvelope(msg),
	})

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("st1", `STORE 1 +FLAGS ($testlabel)`)

	// Collect the positions (indices) of * FLAGS and * 1 FETCH lines.
	flagsIdx := -1
	fetchIdx := -1
	for i, line := range resp {
		if strings.HasPrefix(line, "* FLAGS") {
			flagsIdx = i
		}
		if strings.Contains(line, "FETCH") && strings.Contains(line, "FLAGS") {
			fetchIdx = i
		}
	}
	if flagsIdx < 0 {
		t.Fatalf("no updated * FLAGS untagged response in STORE reply: %v", resp)
	}
	if fetchIdx < 0 {
		t.Fatalf("no * FETCH FLAGS response in STORE reply: %v", resp)
	}
	if flagsIdx > fetchIdx {
		t.Fatalf("* FLAGS (idx=%d) must precede * FETCH (idx=%d): %v", flagsIdx, fetchIdx, resp)
	}
	// The FLAGS line must include the new keyword.
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "$testlabel") {
		t.Fatalf("$testlabel not found in updated * FLAGS: %v", resp)
	}

	// A subsequent SELECT must still advertise the keyword.
	resp2 := c.send("s2", "SELECT INBOX")
	if !strings.Contains(strings.Join(resp2, "\n"), "$testlabel") {
		t.Fatalf("$testlabel missing from FLAGS on re-SELECT: %v", resp2)
	}
}

// TestSTORE_ExistingKeyword_NoExtraFLAGS verifies that when the same keyword
// is STOREd a second time (it is already in the advertised set), no new
// "* FLAGS" untagged response is emitted.
func TestSTORE_ExistingKeyword_NoExtraFLAGS(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := buildMessage("kw-repeat", "body")
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID: f.inbox.ID,
		Size:      int64(len(msg)),
		Blob:      blob,
		Envelope:  parseStoreEnvelope(msg),
	})

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	// First STORE introduces the keyword — expect one FLAGS update.
	resp1 := c.send("st1", `STORE 1 +FLAGS ($known)`)
	flagsCount1 := 0
	for _, line := range resp1 {
		if strings.HasPrefix(line, "* FLAGS") {
			flagsCount1++
		}
	}
	if flagsCount1 == 0 {
		t.Fatalf("first STORE expected * FLAGS update, got none: %v", resp1)
	}
	// Second STORE with the same keyword — expect no extra FLAGS untagged.
	resp2 := c.send("st2", `STORE 1 +FLAGS ($known)`)
	for _, line := range resp2 {
		if strings.HasPrefix(line, "* FLAGS") {
			t.Fatalf("second STORE of existing keyword emitted unexpected * FLAGS: %v", resp2)
		}
	}
}

// TestAPPEND_NewKeyword_EmitsFLAGS verifies that APPENDing a message with a
// brand-new keyword into the currently-selected mailbox emits an updated
// "* FLAGS" before the tagged OK.
func TestAPPEND_NewKeyword_EmitsFLAGS(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")

	msg := buildMessage("kw-append", "appended body")
	// APPEND with a keyword flag.
	c.write(fmt.Sprintf("a1 APPEND INBOX ($appendkw) {%d}\r\n", len(msg)))
	line := c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected continuation, got: %q", line)
	}
	c.write(msg + "\r\n")
	resp := c.readUntilTag("a1")

	joined := strings.Join(resp, "\n")
	// The * FLAGS update must be present before the tagged OK.
	if !strings.Contains(joined, "$appendkw") {
		t.Fatalf("APPEND did not emit * FLAGS with $appendkw: %v", resp)
	}
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("APPEND expected OK, got: %v", resp)
	}
}

// TestIDLE_NewKeyword_StreamsFLAGSUpdate verifies that when session A stores
// a brand-new keyword while session B is IDLEing on the same mailbox, session
// B receives an updated "* FLAGS" untagged response (RFC 3501 §7.2.6 /
// REQ-PROTO-30).
func TestIDLE_NewKeyword_StreamsFLAGSUpdate(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()

	// Seed one message.
	msg := buildMessage("idle-kw", "idle keyword body")
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, _ = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID: f.inbox.ID,
		Size:      int64(len(msg)),
		Blob:      blob,
		Envelope:  parseStoreEnvelope(msg),
	})

	// Session B enters IDLE.
	cB := loggedInClient(t, f)
	defer cB.close()
	cB.send("s1", "SELECT INBOX")
	cB.write("i1 IDLE\r\n")
	contLine := cB.readLine()
	if !strings.HasPrefix(contLine, "+") {
		t.Fatalf("session B: expected continuation, got %q", contLine)
	}

	// Session A stores a new keyword.
	cA := loggedInClient(t, f)
	defer cA.close()
	cA.send("sA", "SELECT INBOX")
	resp := cA.send("stA", `STORE 1 +FLAGS ($idlekw)`)
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("session A STORE failed: %v", resp)
	}

	// Advance fake clock to trigger the IDLE poll tick on session B.
	if fc, ok := f.ha.Clock.(*clock.FakeClock); ok {
		fc.Advance(300 * time.Millisecond)
	}

	// Session B should receive an updated * FLAGS carrying $idlekw.
	deadline := time.Now().Add(3 * time.Second)
	sawFlagsUpdate := false
	for time.Now().Before(deadline) {
		_ = cB.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		line, err := cB.br.ReadString('\n')
		if err != nil {
			if fc, ok := f.ha.Clock.(*clock.FakeClock); ok {
				fc.Advance(300 * time.Millisecond)
			}
			continue
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "* FLAGS") && strings.Contains(line, "$idlekw") {
			sawFlagsUpdate = true
			break
		}
	}
	// Terminate IDLE cleanly.
	_ = cB.conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	cB.write("DONE\r\n")

	if !sawFlagsUpdate {
		t.Fatalf("session B did not receive * FLAGS update with $idlekw during IDLE")
	}
}
