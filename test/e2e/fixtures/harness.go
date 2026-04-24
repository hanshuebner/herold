// Package fixtures assembles the shared wiring that the Phase 1 e2e
// tests share: a testharness.Server with bound SMTP + IMAP listeners, a
// configured protosmtp.Server + protoimap.Server, an "alice@example.test"
// principal, a fake spam plugin, and a self-signed TLS cert pool.
//
// Every e2e scenario expects to be parameterised over the
// store.Store-producing backend (SQLite + Postgres at minimum) and
// therefore constructs the harness through Build(t, opts), with the
// concrete store.Store handed in as opts.Store. See
// test/e2e/backends_test.go for how the Postgres leg skips when
// HEROLD_PG_DSN is unset.
//
// This package lives under test/e2e/fixtures because it is used only by
// the e2e suite; no production code imports it.
package fixtures

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// Opts configures a Fixture. The zero value is valid (everything fills
// in with sensible defaults); tests override what they need.
type Opts struct {
	// Store is the backing store.Store. When nil the harness defaults to
	// fakestore.New (in-memory). Backend-matrix tests inject a concrete
	// SQLite or Postgres handle here.
	Store store.Store
	// Principal is the primary e-mail address created in the directory.
	// Defaults to "alice@example.test".
	Principal string
	// Password is the primary principal's password. Defaults to a fixed
	// test string.
	Password string
	// HamVerdict, when true, wires the fake spam plugin to always return
	// a ham verdict. The default is ham; tests that need a spam verdict
	// rewire via Fixture.SpamPlugin.Handle.
	HamVerdict bool
}

// Fixture is one fully-wired e2e harness. Tests dial the SMTP / IMAP
// listeners through the testharness helpers; after the test returns the
// cleanup registered via t.Cleanup tears everything down.
type Fixture struct {
	HA           *testharness.Server
	SMTPServer   *protosmtp.Server
	IMAPServer   *protoimap.Server
	SMTPListener string
	IMAPListener string
	Principal    store.PrincipalID
	Email        string
	Password     string
	TLSClient    *tls.Config
	SpamPlugin   *fakeplugin.FakePlugin
}

// Build spins a harness according to opts. The returned *Fixture's
// listeners are live (SMTP on RelayIn mode; IMAP on implicit 993). All
// goroutines unwind via t.Cleanup.
func Build(t *testing.T, opts Opts) *Fixture {
	t.Helper()
	email := opts.Principal
	if email == "" {
		email = "alice@example.test"
	}
	password := opts.Password
	if password == "" {
		password = "correct-horse-staple-battery"
	}

	harnessOpts := testharness.Options{
		Listeners: []testharness.ListenerSpec{
			{Name: "smtp", Protocol: "smtp"},
			{Name: "imaps", Protocol: "imaps"},
		},
	}
	if opts.Store != nil {
		harnessOpts.Store = opts.Store
	}
	ha, _ := testharness.Start(t, harnessOpts)

	ctx := context.Background()
	domain := domainOf(email)
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: domain, IsLocal: true}); err != nil {
		// Ignore conflict: the Postgres leg may reuse a database across
		// test runs; the domain row may already exist. Tests running
		// against fresh SQLite never hit this.
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("insert domain: %v", err)
		}
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, email, password)
	if err != nil {
		t.Fatalf("create principal %q: %v", email, err)
	}
	// Seed an INBOX so IMAP SELECT works without waiting for auto-
	// creation on first delivery. SMTP delivery paths that ensureMailbox
	// will no-op when an INBOX already exists.
	if _, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox | store.MailboxAttrSubscribed,
	}); err != nil && !errors.Is(err, store.ErrConflict) {
		t.Fatalf("insert inbox: %v", err)
	}

	tlsStore, clientCfg := newTestTLSStore(t)

	// Spam plugin. Default verdict is ham with a low score. Individual
	// tests replace the handler with their own via SpamPlugin.Handle to
	// exercise the spam path without touching the fixture code.
	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &pluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := newResolverAdapter(ha.DNS)
	dkimV := maildkim.New(resolver, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolver, ha.Clock)
	dmarcV := maildmarc.New(resolver)
	arcV := mailarc.New(resolver)
	interp := sieve.NewInterpreter()

	smtpSrv, err := protosmtp.New(protosmtp.Config{
		Store:     ha.Store,
		Directory: dir,
		DKIM:      dkimV,
		SPF:       spfV,
		DMARC:     dmarcV,
		ARC:       arcV,
		Spam:      spamCls,
		Sieve:     interp,
		TLS:       tlsStore,
		Resolver:  resolver,
		Clock:     ha.Clock,
		Logger:    ha.Logger,
		Options: protosmtp.Options{
			Hostname:                 "mx." + domain,
			AuthservID:               "mx." + domain,
			MaxMessageSize:           128 * 1024,
			ReadTimeout:              5 * time.Second,
			WriteTimeout:             5 * time.Second,
			DataTimeout:              10 * time.Second,
			ShutdownGrace:            2 * time.Second,
			MaxRecipientsPerMessage:  8,
			MaxCommandsPerSession:    200,
			MaxConcurrentConnections: 32,
			MaxConcurrentPerIP:       16,
		},
	})
	if err != nil {
		t.Fatalf("protosmtp.New: %v", err)
	}
	t.Cleanup(func() { _ = smtpSrv.Close(context.Background()) })
	ha.AttachSMTP("smtp", smtpSrv, protosmtp.RelayIn)

	imapSrv := protoimap.NewServer(
		ha.Store, dir, tlsStore, ha.Clock, ha.Logger, nil, nil,
		protoimap.Options{
			MaxConnections:        16,
			MaxCommandsPerSession: 1000,
			IdleMaxDuration:       30 * time.Minute,
			ServerName:            "herold",
		},
	)
	t.Cleanup(func() { _ = imapSrv.Close() })
	ha.AttachIMAP("imaps", imapSrv, protoimap.ListenerModeImplicit993)

	return &Fixture{
		HA:           ha,
		SMTPServer:   smtpSrv,
		IMAPServer:   imapSrv,
		SMTPListener: "smtp",
		IMAPListener: "imaps",
		Principal:    pid,
		Email:        email,
		Password:     password,
		TLSClient:    clientCfg,
		SpamPlugin:   spamPlug,
	}
}

// domainOf returns the domain component of an e-mail address, or
// "example.test" if the address has no @.
func domainOf(email string) string {
	if i := strings.LastIndexByte(email, '@'); i > 0 {
		return email[i+1:]
	}
	return "example.test"
}

// --- SMTP dialog helper ------------------------------------------------

// SMTPClient is a no-frills line-oriented SMTP client used by the
// e2e scenarios. It is deliberately hand-rolled (rather than using
// net/smtp.Client) to keep full control over the wire bytes and to
// match the style of internal/protosmtp's own tests.
type SMTPClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// DialSMTP opens a plaintext SMTP connection against the fixture's SMTP
// listener and reads the initial 220 greeting.
func (f *Fixture) DialSMTP(t *testing.T) *SMTPClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := f.HA.DialSMTPByName(ctx, f.SMTPListener)
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	c := &SMTPClient{t: t, conn: conn, r: bufio.NewReader(conn)}
	c.Expect(220)
	return c
}

// Send writes a single CRLF-terminated command. Use SendRaw when you
// need to transmit bytes without the trailing CRLF.
func (c *SMTPClient) Send(line string) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetWriteDeadline(time.Time{})
	if _, err := c.conn.Write([]byte(line + "\r\n")); err != nil {
		c.t.Fatalf("send %q: %v", line, err)
	}
}

// SendRaw writes b verbatim.
func (c *SMTPClient) SendRaw(b []byte) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetWriteDeadline(time.Time{})
	if _, err := c.conn.Write(b); err != nil {
		c.t.Fatalf("sendRaw: %v", err)
	}
}

// Expect reads a complete SMTP reply and fails the test if its code
// does not equal want.
func (c *SMTPClient) Expect(want int) string {
	c.t.Helper()
	code, text := c.ReadReply()
	if code != want {
		c.t.Fatalf("expected %d, got %d: %s", want, code, text)
	}
	return text
}

// ReadReply reads a (possibly multi-line) SMTP reply and returns the
// numeric code plus the full textual body joined with '\n'.
func (c *SMTPClient) ReadReply() (int, string) {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})
	var lines []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("read reply: %v (so-far %q)", err, strings.Join(lines, ""))
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			c.t.Fatalf("short reply line: %q", line)
		}
		lines = append(lines, line)
		if line[3] == ' ' {
			var code int
			_, _ = fmt.Sscanf(line[:3], "%d", &code)
			return code, strings.Join(lines, "\n")
		}
		if line[3] != '-' {
			c.t.Fatalf("bad continuation marker: %q", line)
		}
	}
}

// Close tears the connection down. Safe to call twice.
func (c *SMTPClient) Close() { _ = c.conn.Close() }

// SendMessage drives a full EHLO/MAIL/RCPT/DATA/. dialogue with the
// fixture. Fails the test on any 4xx/5xx response. Returns on clean
// 250 OK after the terminating dot. quit=true sends QUIT (221) after.
func (f *Fixture) SendMessage(t *testing.T, from string, to []string, body string, quit bool) {
	t.Helper()
	c := f.DialSMTP(t)
	defer c.Close()
	c.Send("EHLO client.external.test")
	c.Expect(250)
	c.Send("MAIL FROM:<" + from + ">")
	c.Expect(250)
	for _, rc := range to {
		c.Send("RCPT TO:<" + rc + ">")
		c.Expect(250)
	}
	c.Send("DATA")
	c.Expect(354)
	// Ensure CRLF termination and the dot terminator.
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	c.SendRaw([]byte(body + ".\r\n"))
	c.Expect(250)
	if quit {
		c.Send("QUIT")
		c.Expect(221)
	}
}

// --- IMAP dialog helper ------------------------------------------------

// IMAPClient is the minimal tagged-line IMAP client used by e2e tests.
type IMAPClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// DialIMAP opens an implicit-TLS IMAPS connection, consumes the initial
// greeting, and returns the client ready for CAPABILITY / LOGIN /...
func (f *Fixture) DialIMAP(t *testing.T) *IMAPClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := f.HA.DialIMAPSByName(ctx, f.IMAPListener, f.TLSClient)
	if err != nil {
		t.Fatalf("dial imaps: %v", err)
	}
	c := &IMAPClient{t: t, conn: conn, r: bufio.NewReader(conn)}
	// Drain greeting.
	_ = c.ReadLine()
	return c
}

// ReadLine reads one CRLF-terminated response line.
func (c *IMAPClient) ReadLine() string {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("imap read: %v (partial=%q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

// Send writes "tag cmd\r\n" and returns all lines up to and including
// the tagged status response.
func (c *IMAPClient) Send(tag, cmd string) []string {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetWriteDeadline(time.Time{})
	if _, err := c.conn.Write([]byte(tag + " " + cmd + "\r\n")); err != nil {
		c.t.Fatalf("imap write: %v", err)
	}
	var out []string
	for {
		line := c.ReadLine()
		out = append(out, line)
		if strings.HasPrefix(line, tag+" ") {
			return out
		}
	}
}

// Login issues LOGIN for the fixture's primary principal and fails the
// test if the response is not OK.
func (c *IMAPClient) Login(email, password string) {
	c.t.Helper()
	resp := c.Send("lg", fmt.Sprintf("LOGIN %s %s", email, password))
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		c.t.Fatalf("LOGIN failed: %v", resp)
	}
}

// Close tears the conn down.
func (c *IMAPClient) Close() { _ = c.conn.Close() }

// --- store introspection helpers --------------------------------------

// LoadMessagesIn returns every message row stored under the named
// mailbox for the given principal, along with the bytes of its blob.
// Results are ordered by MessageID ascending.
func LoadMessagesIn(t *testing.T, f *Fixture, pid store.PrincipalID, mailboxName string) []StoredMessage {
	t.Helper()
	ctx := context.Background()
	mbs, err := f.HA.Store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		t.Fatalf("list mailboxes: %v", err)
	}
	var target store.Mailbox
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, mailboxName) {
			target = mb
			break
		}
	}
	if target.ID == 0 {
		return nil
	}
	var out []StoredMessage
	// Message IDs are monotone across the test. We scan 1..500 which
	// covers every e2e test by a comfortable margin.
	for mid := store.MessageID(1); mid < 500; mid++ {
		m, err := f.HA.Store.Meta().GetMessage(ctx, mid)
		if err != nil {
			continue
		}
		if m.MailboxID != target.ID {
			continue
		}
		rc, err := f.HA.Store.Blobs().Get(ctx, m.Blob.Hash)
		if err != nil {
			t.Fatalf("load blob %s: %v", m.Blob.Hash, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out = append(out, StoredMessage{Message: m, Bytes: b})
	}
	return out
}

// StoredMessage is the metadata row + raw blob bytes, returned together
// so tests can assert both envelope fields and header presence in one
// call.
type StoredMessage struct {
	Message store.Message
	Bytes   []byte
}

// --- TLS / plugin / DNS glue ------------------------------------------

// newTestTLSStore builds an ephemeral self-signed cert suitable for
// implicit-TLS IMAPS + SMTPS. The returned client config trusts the
// generated CA and pins ServerName to "mx.example.test".
func newTestTLSStore(t *testing.T) (*heroldtls.Store, *tls.Config) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mx.example.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"mx.example.test", "mail.example.test"},
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
	st.Add("mx.example.test", &cert)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return st, &tls.Config{RootCAs: pool, ServerName: "mx.example.test"}
}

// pluginInvoker adapts the harness's fakeplugin.Registry to the
// spam.Classifier's invoker interface.
type pluginInvoker struct{ reg *fakeplugin.Registry }

func (f *pluginInvoker) Call(ctx context.Context, plugin, method string, params any, result any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := f.reg.Call(ctx, plugin, method, paramsJSON)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}

// resolverAdapter wires fakedns into mailauth.Resolver.
type resolverAdapter struct{ d *fakedns.Resolver }

func newResolverAdapter(d *fakedns.Resolver) mailauth.Resolver { return resolverAdapter{d: d} }

func (a resolverAdapter) TXTLookup(ctx context.Context, name string) ([]string, error) {
	out, err := a.d.LookupTXT(ctx, name)
	if err != nil {
		if errors.Is(err, fakedns.ErrNoRecords) {
			return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, name)
		}
		return nil, err
	}
	return out, nil
}

func (a resolverAdapter) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	mxs, err := a.d.LookupMX(ctx, domain)
	if err != nil {
		if errors.Is(err, fakedns.ErrNoRecords) {
			return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, domain)
		}
		return nil, err
	}
	out := make([]*net.MX, 0, len(mxs))
	for _, m := range mxs {
		out = append(out, &net.MX{Host: m.Host, Pref: m.Preference})
	}
	return out, nil
}

func (a resolverAdapter) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	v4, err4 := a.d.LookupA(ctx, host)
	v6, err6 := a.d.LookupAAAA(ctx, host)
	if err4 != nil && err6 != nil {
		return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, host)
	}
	return append(append([]net.IP{}, v4...), v6...), nil
}

// NewFakeClock returns a deterministic clock anchored at the e2e
// suite's canonical start time, matching testharness' default. Exposed
// so tests that need a clock outside the harness (e.g. directoryoidc's
// RP) pass the same instance.
func NewFakeClock() *clock.FakeClock {
	return clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}
