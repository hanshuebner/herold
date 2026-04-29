package protomanagesieve_test

// Wire-level tests for the ManageSieve listener (RFC 5804).
//
// The fixture mirrors protoimap's: a per-test harness with a fresh
// principal, a self-signed TLS leaf for STARTTLS, and a *client
// helper that wraps reads/writes around a bufio.Reader. These tests
// drive STARTTLS + AUTHENTICATE and exercise every command the
// listener implements.

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
	"io"
	"log/slog"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protomanagesieve"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

type fixture struct {
	ha       *testharness.Server
	srv      *protomanagesieve.Server
	name     string
	pid      store.PrincipalID
	password string
	dir      *directory.Directory
	tlsCfg   *tls.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	name := "managesieve"
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: name, Protocol: "managesieve"}},
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

	tlsStore, clientCfg := newTestTLSStore(t)
	srv := protomanagesieve.NewServer(
		ha.Store, dir, tlsStore, ha.Clock, ha.Logger, nil, nil,
		protomanagesieve.Options{
			ServerName:  "herold",
			IdleTimeout: time.Minute,
		},
	)
	ha.AttachManageSieve(name, srv)
	t.Cleanup(func() { _ = srv.Close() })
	return &fixture{
		ha: ha, srv: srv, name: name,
		pid: pid, password: password,
		dir: dir, tlsCfg: clientCfg,
	}
}

// sharedTestCert is the package-wide self-signed leaf used by every
// test fixture. ECDSA P-256 keypair generation + x509.CreateCertificate
// is small in absolute terms (tens of milliseconds in good times) but
// becomes a multi-second outlier under `go test -race ./...`, where
// dozens of test binaries compete for CPU and the runtime cannot
// schedule the cert work fast enough. Generating the cert once and
// reusing it across every fixture removes the per-test setup cost as
// a source of deadline pressure on the wire-protocol round trips.
var (
	sharedTestCertOnce sync.Once
	sharedTestCert     *tls.Certificate
	sharedTestLeaf     *x509.Certificate
	sharedTestCertErr  error
)

func ensureSharedTestCert() error {
	sharedTestCertOnce.Do(func() {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			sharedTestCertErr = fmt.Errorf("gen key: %w", err)
			return
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
			sharedTestCertErr = fmt.Errorf("cert: %w", err)
			return
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			sharedTestCertErr = fmt.Errorf("parse cert: %w", err)
			return
		}
		sharedTestCert = &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}
		sharedTestLeaf = leaf
	})
	return sharedTestCertErr
}

func newTestTLSStore(t *testing.T) (*heroldtls.Store, *tls.Config) {
	t.Helper()
	if err := ensureSharedTestCert(); err != nil {
		t.Fatalf("shared test cert: %v", err)
	}
	st := heroldtls.NewStore()
	st.SetDefault(sharedTestCert)
	pool := x509.NewCertPool()
	pool.AddCert(sharedTestLeaf)
	return st, &tls.Config{RootCAs: pool, ServerName: "mail.example.test"}
}

// -----------------------------------------------------------------------------
// client helper
// -----------------------------------------------------------------------------

type client struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func (f *fixture) dial(t *testing.T) *client {
	t.Helper()
	conn, err := f.ha.DialManageSieveByName(context.Background(), f.name)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &client{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.consumeUntilStatus()
	return c
}

func (c *client) write(s string) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.conn.Write([]byte(s)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *client) readLine() string {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := c.br.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v (partial=%q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

// consumeUntilStatus reads lines until it sees an OK / NO / BYE
// terminator and returns the slice (including the terminator). Used
// to drain the unsolicited capability list at greeting / post-STARTTLS.
func (c *client) consumeUntilStatus() []string {
	var out []string
	for {
		line := c.readLine()
		out = append(out, line)
		up := strings.ToUpper(line)
		if strings.HasPrefix(up, "OK") || strings.HasPrefix(up, "NO") || strings.HasPrefix(up, "BYE") {
			return out
		}
	}
}

// readN reads exactly n bytes.
func (c *client) readN(n int) []byte {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		c.t.Fatalf("read n=%d: %v", n, err)
	}
	return buf
}

// upgradeTLS wraps the connection in TLS using the fixture's client
// config. Mirrors what a real ManageSieve client does after the
// server's "OK" response to STARTTLS.
func (c *client) upgradeTLS(t *testing.T, cfg *tls.Config) {
	t.Helper()
	tc := tls.Client(c.conn, cfg)
	if err := tc.HandshakeContext(context.Background()); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	c.conn = tc
	c.br = bufio.NewReader(tc)
	// After the handshake the server re-emits the capability list
	// terminated with OK. Drain it.
	c.consumeUntilStatus()
}

// authenticatePLAIN drives a PLAIN AUTHENTICATE handshake against the
// fixture's principal. The TLS upgrade must already have run.
//
// Per RFC 5804 §2.1 SASL data on the wire is base64-encoded.
func (c *client) authenticatePLAIN(t *testing.T, user, pass string) {
	t.Helper()
	ir := "\x00" + user + "\x00" + pass
	encoded := base64.StdEncoding.EncodeToString([]byte(ir))
	c.write(fmt.Sprintf("AUTHENTICATE \"PLAIN\" \"%s\"\r\n", encoded))
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
		t.Fatalf("AUTHENTICATE failed: %v", resp)
	}
}

// -----------------------------------------------------------------------------
// CAPABILITY
// -----------------------------------------------------------------------------

func TestCAPABILITY_AdvertisesSieveExtensions_FromInterpreter(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t)
	defer c.conn.Close()
	c.write("CAPABILITY\r\n")
	resp := c.consumeUntilStatus()
	joined := strings.Join(resp, "\n")
	for _, ext := range []string{"fileinto", "vacation", "imap4flags", "envelope"} {
		if !strings.Contains(joined, ext) {
			t.Fatalf("missing %q in SIEVE caps: %v", ext, resp)
		}
	}
	if !strings.Contains(joined, "STARTTLS") {
		t.Fatalf("expected STARTTLS in pre-TLS caps: %v", resp)
	}
}

// -----------------------------------------------------------------------------
// STARTTLS gating
// -----------------------------------------------------------------------------

func TestSTARTTLS_RequiredBeforeAUTHENTICATE(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t)
	defer c.conn.Close()
	// Without STARTTLS, AUTHENTICATE must be refused with
	// ENCRYPT-NEEDED.
	c.write(`AUTHENTICATE "PLAIN" ""` + "\r\n")
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "NO") {
		t.Fatalf("expected NO, got %q", resp)
	}
	if !strings.Contains(strings.ToUpper(resp), "ENCRYPT-NEEDED") {
		t.Fatalf("expected ENCRYPT-NEEDED code: %v", resp)
	}
}

// -----------------------------------------------------------------------------
// PUTSCRIPT / CHECKSCRIPT / GETSCRIPT / DELETESCRIPT
// -----------------------------------------------------------------------------

const validSieveScript = `require ["fileinto"];
if header :contains "Subject" "spam" {
    fileinto "Junk";
}
`

// invalidSieveScript is rejected by the validator: it requires the
// "fileinto" extension but never declares it via require.
const invalidSieveScript = `if header :contains "Subject" "spam" {
    fileinto "Junk";
}
`

// authedClient produces a logged-in *client over the TLS-upgraded
// connection. It runs the standard STARTTLS + PLAIN dance.
func authedClient(t *testing.T, f *fixture) *client {
	t.Helper()
	c := f.dial(t)
	c.write("STARTTLS\r\n")
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
		t.Fatalf("STARTTLS: %v", resp)
	}
	c.upgradeTLS(t, f.tlsCfg)
	c.authenticatePLAIN(t, "alice@example.test", f.password)
	return c
}

func TestPUTSCRIPT_ValidScript_Persists(t *testing.T) {
	f := newFixture(t)
	c := authedClient(t, f)
	defer c.conn.Close()

	body := validSieveScript
	c.write(fmt.Sprintf("PUTSCRIPT \"active\" {%d+}\r\n%s\r\n", len(body), body))
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
		t.Fatalf("PUTSCRIPT: %v", resp)
	}
	persisted, err := f.ha.Store.Meta().GetSieveScript(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if persisted != body {
		t.Fatalf("persisted script mismatch:\nwant=%q\ngot =%q", body, persisted)
	}
}

func TestPUTSCRIPT_InvalidScript_NOWithDiagnostic(t *testing.T) {
	f := newFixture(t)
	c := authedClient(t, f)
	defer c.conn.Close()

	body := invalidSieveScript
	c.write(fmt.Sprintf("PUTSCRIPT \"active\" {%d+}\r\n%s\r\n", len(body), body))
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "NO") {
		t.Fatalf("expected NO, got: %v", resp)
	}
	// The NO line should carry the script name in parentheses and
	// some non-empty diagnostic.
	if !strings.Contains(resp, "active") {
		t.Fatalf("NO should echo script name: %v", resp)
	}
	// Confirm nothing was persisted.
	persisted, err := f.ha.Store.Meta().GetSieveScript(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if persisted != "" {
		t.Fatalf("script should NOT be persisted on parse error: %q", persisted)
	}
}

func TestCHECKSCRIPT_ParseOnly_NoPersist(t *testing.T) {
	f := newFixture(t)
	c := authedClient(t, f)
	defer c.conn.Close()

	body := validSieveScript
	c.write(fmt.Sprintf("CHECKSCRIPT {%d+}\r\n%s\r\n", len(body), body))
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
		t.Fatalf("CHECKSCRIPT: %v", resp)
	}
	persisted, err := f.ha.Store.Meta().GetSieveScript(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if persisted != "" {
		t.Fatalf("CHECKSCRIPT must not persist: %q", persisted)
	}
}

func TestGETSCRIPT_ReturnsLiteral(t *testing.T) {
	f := newFixture(t)
	body := validSieveScript
	if err := f.ha.Store.Meta().SetSieveScript(context.Background(), f.pid, body); err != nil {
		t.Fatalf("seed script: %v", err)
	}
	c := authedClient(t, f)
	defer c.conn.Close()

	c.write("GETSCRIPT \"active\"\r\n")
	first := c.readLine()
	// Expect "{N}" literal-prefix line.
	if !strings.HasPrefix(first, "{") || !strings.HasSuffix(first, "}") {
		t.Fatalf("expected literal prefix, got %q", first)
	}
	n, perr := strconv.Atoi(first[1 : len(first)-1])
	if perr != nil {
		t.Fatalf("bad literal prefix %q: %v", first, perr)
	}
	bodyOut := string(c.readN(n))
	if bodyOut != body {
		t.Fatalf("body mismatch:\nwant=%q\ngot =%q", body, bodyOut)
	}
	// Trailing CRLF, then OK.
	_ = c.readLine()
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
		t.Fatalf("expected OK, got %q", resp)
	}
}

func TestDELETESCRIPT_RemovesActive(t *testing.T) {
	f := newFixture(t)
	if err := f.ha.Store.Meta().SetSieveScript(context.Background(), f.pid, validSieveScript); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c := authedClient(t, f)
	defer c.conn.Close()
	c.write("DELETESCRIPT \"active\"\r\n")
	resp := c.readLine()
	if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
		t.Fatalf("DELETESCRIPT: %v", resp)
	}
	persisted, err := f.ha.Store.Meta().GetSieveScript(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if persisted != "" {
		t.Fatalf("expected empty after DELETESCRIPT, got %q", persisted)
	}
}

// -----------------------------------------------------------------------------
// Activity-tagging tests (REQ-OPS-86a)
// -----------------------------------------------------------------------------

// newFixtureWithLogger returns a fixture whose server uses the provided logger.
// It replicates newFixture but accepts a caller-supplied logger so
// AssertActivityTagged can capture all records.
func newFixtureWithLogger(t *testing.T, logger *slog.Logger) *fixture {
	t.Helper()
	name := "managesieve"
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: name, Protocol: "managesieve"}},
		Logger:    logger,
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), logger, ha.Clock, rand.Reader)
	password := "correct-horse-staple-battery"
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", password)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tlsStore, clientCfg := newTestTLSStore(t)
	srv := protomanagesieve.NewServer(
		ha.Store, dir, tlsStore, ha.Clock, logger, nil, nil,
		protomanagesieve.Options{
			ServerName:  "herold",
			IdleTimeout: time.Minute,
		},
	)
	ha.AttachManageSieve(name, srv)
	t.Cleanup(func() { _ = srv.Close() })
	return &fixture{
		ha: ha, srv: srv, name: name,
		pid: pid, password: password,
		dir: dir, tlsCfg: clientCfg,
	}
}

// TestActivityTagging_PUTSCRIPT asserts PUTSCRIPT emits a user/info record
// (REQ-OPS-86a / REQ-OPS-86d).
func TestActivityTagging_PUTSCRIPT(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		f := newFixtureWithLogger(t, log)
		c := authedClient(t, f)
		defer c.conn.Close()

		body := validSieveScript
		c.write(fmt.Sprintf("PUTSCRIPT \"active\" {%d+}\r\n%s\r\n", len(body), body))
		resp := c.readLine()
		if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
			t.Fatalf("PUTSCRIPT: %v", resp)
		}
	})
}

// TestActivityTagging_AUTHSuccess asserts a successful AUTHENTICATE emits
// an audit/info record (REQ-OPS-86a).
func TestActivityTagging_AUTHSuccess(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		f := newFixtureWithLogger(t, log)
		// authedClient performs STARTTLS + AUTHENTICATE; both must be tagged.
		c := authedClient(t, f)
		defer c.conn.Close()
	})
}

// TestActivityTagging_AUTHFailure asserts a failed AUTHENTICATE emits an
// audit/warn record (REQ-OPS-86a).
func TestActivityTagging_AUTHFailure(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		f := newFixtureWithLogger(t, log)
		c := f.dial(t)
		defer c.conn.Close()
		c.write("STARTTLS\r\n")
		resp := c.readLine()
		if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
			t.Fatalf("STARTTLS: %v", resp)
		}
		c.upgradeTLS(t, f.tlsCfg)
		// Send PLAIN with a wrong password.
		ir := "\x00alice@example.test\x00wrongpassword"
		encoded := base64.StdEncoding.EncodeToString([]byte(ir))
		c.write(fmt.Sprintf("AUTHENTICATE \"PLAIN\" \"%s\"\r\n", encoded))
		resp = c.readLine()
		if !strings.HasPrefix(strings.ToUpper(resp), "NO") {
			t.Fatalf("expected NO for bad auth, got %q", resp)
		}
	})
}

// TestActivityTagging_CHECKSCRIPT asserts CHECKSCRIPT emits a user/info
// record (REQ-OPS-86a).
func TestActivityTagging_CHECKSCRIPT(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		f := newFixtureWithLogger(t, log)
		c := authedClient(t, f)
		defer c.conn.Close()

		body := validSieveScript
		c.write(fmt.Sprintf("CHECKSCRIPT {%d+}\r\n%s\r\n", len(body), body))
		resp := c.readLine()
		if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
			t.Fatalf("CHECKSCRIPT: %v", resp)
		}
	})
}

// TestActivityTagging_DELETESCRIPT asserts DELETESCRIPT emits a user/info
// record (REQ-OPS-86a).
func TestActivityTagging_DELETESCRIPT(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		f := newFixtureWithLogger(t, log)
		if err := f.ha.Store.Meta().SetSieveScript(context.Background(), f.pid, validSieveScript); err != nil {
			t.Fatalf("seed: %v", err)
		}
		c := authedClient(t, f)
		defer c.conn.Close()
		c.write("DELETESCRIPT \"active\"\r\n")
		resp := c.readLine()
		if !strings.HasPrefix(strings.ToUpper(resp), "OK") {
			t.Fatalf("DELETESCRIPT: %v", resp)
		}
	})
}
