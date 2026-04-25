package protosmtp_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// --- test fixtures ---------------------------------------------------

// fixture bundles the harness + configured SMTP server for one test.
type fixture struct {
	ha        *testharness.Server
	srv       *protosmtp.Server
	listener  string
	mode      protosmtp.ListenerMode
	principal store.PrincipalID
	password  string
	tlsClient *tls.Config
	spamPlug  *fakeplugin.FakePlugin
}

type fixtureOpts struct {
	mode protosmtp.ListenerMode
}

func newFixture(t *testing.T, fo fixtureOpts) *fixture {
	t.Helper()
	proto, name := protoNameFor(fo.mode)
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
	tlsStore, clientCfg := newTestTLSStore(t)

	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &fakePluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := newResolverAdapter(ha.DNS)
	dkimV := maildkim.New(resolver, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolver, ha.Clock)
	dmarcV := maildmarc.New(resolver)
	arcV := mailarc.New(resolver)
	interp := sieve.NewInterpreter()

	scramLk := &scramLookup{pid: pid, email: "alice@example.test", password: password}

	srv, err := protosmtp.New(protosmtp.Config{
		Store:       ha.Store,
		Directory:   dir,
		DKIM:        dkimV,
		SPF:         spfV,
		DMARC:       dmarcV,
		ARC:         arcV,
		Spam:        spamCls,
		Sieve:       interp,
		TLS:         tlsStore,
		Resolver:    resolver,
		Clock:       ha.Clock,
		Logger:      ha.Logger,
		SCRAMLookup: scramLk,
		Options: protosmtp.Options{
			Hostname:                 "mx.example.test",
			AuthservID:               "mx.example.test",
			MaxMessageSize:           65536,
			ReadTimeout:              5 * time.Second,
			WriteTimeout:             5 * time.Second,
			DataTimeout:              10 * time.Second,
			ShutdownGrace:            2 * time.Second,
			MaxRecipientsPerMessage:  5,
			MaxCommandsPerSession:    200,
			MaxConcurrentConnections: 32,
			MaxConcurrentPerIP:       16,
		},
	})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP(name, srv, fo.mode)
	return &fixture{
		ha:        ha,
		srv:       srv,
		listener:  name,
		mode:      fo.mode,
		principal: pid,
		password:  password,
		tlsClient: clientCfg,
		spamPlug:  spamPlug,
	}
}

func protoNameFor(mode protosmtp.ListenerMode) (proto, name string) {
	switch mode {
	case protosmtp.SubmissionSTARTTLS:
		return "smtp-submission", "sub"
	case protosmtp.SubmissionImplicitTLS:
		return "smtps", "smtps"
	default:
		return "smtp", "smtp"
	}
}

// dial opens a conn to the listener and returns a smtpClient + cleanup.
func (f *fixture) dial(t *testing.T) (*smtpClient, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if f.mode == protosmtp.SubmissionImplicitTLS {
		c, err := f.ha.DialSMTPSByName(ctx, f.listener, f.tlsClient)
		if err != nil {
			t.Fatalf("dial smtps: %v", err)
		}
		return newSMTPClient(c), func() { _ = c.Close() }
	}
	c, err := f.ha.DialSMTPByName(ctx, f.listener)
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	return newSMTPClient(c), func() { _ = c.Close() }
}

// --- tiny SMTP client -------------------------------------------------

type smtpClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func newSMTPClient(c net.Conn) *smtpClient {
	return &smtpClient{conn: c, r: bufio.NewReader(c)}
}

func (c *smtpClient) readReply(t *testing.T) (int, string) {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})
	var lines []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			t.Fatalf("read reply: %v (so-far %q)", err, strings.Join(lines, ""))
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			t.Fatalf("short line: %q", line)
		}
		lines = append(lines, line)
		if line[3] == ' ' {
			var code int
			fmt.Sscanf(line[:3], "%d", &code)
			return code, strings.Join(lines, "\n")
		}
		if line[3] != '-' {
			t.Fatalf("bad continuation marker: %q", line)
		}
	}
}

func (c *smtpClient) send(t *testing.T, line string) {
	t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetWriteDeadline(time.Time{})
	if _, err := c.conn.Write([]byte(line + "\r\n")); err != nil {
		t.Fatalf("send %q: %v", line, err)
	}
}

func (c *smtpClient) sendRaw(t *testing.T, b []byte) {
	t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetWriteDeadline(time.Time{})
	if _, err := c.conn.Write(b); err != nil {
		t.Fatalf("sendRaw: %v", err)
	}
}

func mustOK(t *testing.T, c *smtpClient, want int) string {
	t.Helper()
	code, text := c.readReply(t)
	if code != want {
		t.Fatalf("expected %d, got %d: %s", want, code, text)
	}
	return text
}

// --- TLS / plugin / DNS glue -----------------------------------------

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
		DNSNames:              []string{"mx.example.test"},
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
	cfg := &tls.Config{RootCAs: pool, ServerName: "mx.example.test"}
	return st, cfg
}

type fakePluginInvoker struct{ reg *fakeplugin.Registry }

func (f *fakePluginInvoker) Call(ctx context.Context, plugin, method string, params any, result any) error {
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

// resolverAdapter wraps fakedns.Resolver into mailauth.Resolver.
type resolverAdapter struct{ d *fakedns.Resolver }

func newResolverAdapter(d *fakedns.Resolver) mailauth.Resolver {
	return resolverAdapter{d: d}
}

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

type scramLookup struct {
	pid      store.PrincipalID
	email    string
	password string
}

func (s *scramLookup) LookupSCRAMCredentials(_ context.Context, authcid string) (sasl.SCRAMCredentials, sasl.PrincipalID, error) {
	if authcid != s.email {
		return sasl.SCRAMCredentials{}, 0, errors.New("no such user")
	}
	salt := []byte("0123456789abcdef")
	return sasl.DeriveSCRAMCredentials(s.password, salt, 4096), sasl.PrincipalID(s.pid), nil
}

// --- tests -----------------------------------------------------------

func TestEHLO_AdvertisesAllImplementedExtensions(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("EHLO: %d %s", code, text)
	}
	for _, must := range []string{
		"STARTTLS",
		"SIZE ", "PIPELINING", "8BITMIME", "SMTPUTF8",
		"CHUNKING", "DSN", "ENHANCEDSTATUSCODES", "REQUIRETLS",
	} {
		if !strings.Contains(text, must) {
			t.Errorf("EHLO reply missing %q:\n%s", must, text)
		}
	}
	if strings.Contains(text, "AUTH ") {
		t.Errorf("relay-in must not advertise AUTH:\n%s", text)
	}
}

func TestSTARTTLS_Upgrades_ResetsState(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("EHLO: %d %s", code, text)
	}
	if strings.Contains(text, "PLAIN") {
		t.Errorf("submission before TLS must not offer PLAIN:\n%s", text)
	}
	cli.send(t, "STARTTLS")
	mustOK(t, cli, 220)
	tlsConn := tls.Client(cli.conn, f.tlsClient)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		t.Fatalf("tls: %v", err)
	}
	cli2 := newSMTPClient(tlsConn)
	cli2.send(t, "EHLO client.example.test")
	code, text = cli2.readReply(t)
	if code != 250 {
		t.Fatalf("EHLO2: %d %s", code, text)
	}
	if !strings.Contains(text, "PLAIN") {
		t.Errorf("submission over TLS must advertise PLAIN:\n%s", text)
	}
}

func TestMAILFROM_Size_Rejection_BeforeData(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test> SIZE=999999999")
	code, text := cli.readReply(t)
	if code != 552 {
		t.Fatalf("expected 552 over-size, got %d %s", code, text)
	}
}

func TestRCPTTO_NonLocalRefused_RelayIn(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<unknown@unknown.test>")
	code, _ := cli.readReply(t)
	if code != 550 {
		t.Fatalf("expected 550, got %d", code)
	}
}

func TestDATA_ParsesAndDelivers(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: hello\r\n\r\nHi there.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
	assertMessageInMailbox(t, f, f.principal, "INBOX", "hello", "Hi there.")
}

// TestSMTPMetrics_AcceptedIncrementsCounter drives one full DATA accept
// and asserts the herold_smtp_messages_accepted_total counter advanced
// by at least one for the relay_in listener label. Proves the metric
// wiring at the deliver-success path is live.
func TestSMTPMetrics_AcceptedIncrementsCounter(t *testing.T) {
	observe.RegisterSMTPMetrics()
	before := testutil.ToFloat64(observe.SMTPMessagesAcceptedTotal.WithLabelValues("relay_in"))

	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: m\r\n\r\nbody\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	after := testutil.ToFloat64(observe.SMTPMessagesAcceptedTotal.WithLabelValues("relay_in"))
	if after <= before {
		t.Fatalf("herold_smtp_messages_accepted_total{listener=relay_in}: before=%v after=%v; want strict increase", before, after)
	}
}

func TestBDAT_DATAEquivalence(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: bdat\r\n\r\nOne two three.\r\n"
	cli.send(t, fmt.Sprintf("BDAT %d LAST", len(body)))
	cli.sendRaw(t, []byte(body))
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("BDAT LAST: %d %s", code, text)
	}
	assertMessageInMailbox(t, f, f.principal, "INBOX", "bdat", "One two three.")
}

func TestSMTPUTF8_NonASCIILocalPart(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	ctx := context.Background()
	dir := directory.New(f.ha.Store.Meta(), f.ha.Logger, f.ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "užívateľ@example.test", "lorem-ipsum-bar-baz-quux")
	if err != nil {
		t.Fatalf("utf8 principal: %v", err)
	}
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test> SMTPUTF8")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<užívateľ@example.test>")
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("RCPT utf8: %d %s", code, text)
	}
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: užívateľ@example.test\r\nSubject: čau\r\n\r\nAhoj.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)

	raw := loadFirstMessageBytes(t, f, pid)
	if !bytes.Contains(raw, []byte("užívateľ@example.test")) {
		t.Errorf("non-ASCII local-part not preserved")
	}
	if !bytes.Contains(raw, []byte("Ahoj.")) {
		t.Errorf("non-ASCII text body not preserved")
	}
}

func TestPipelining_BatchedCommands(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	ctx := context.Background()
	dir := directory.New(f.ha.Store.Meta(), f.ha.Logger, f.ha.Clock, rand.Reader)
	if _, err := dir.CreatePrincipal(ctx, "bob@example.test", "correct-horse-staple-battery"); err != nil {
		t.Fatalf("principal: %v", err)
	}
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.sendRaw(t, []byte("MAIL FROM:<ext@sender.test>\r\nRCPT TO:<alice@example.test>\r\nRCPT TO:<bob@example.test>\r\nDATA\r\n"))
	mustOK(t, cli, 250)
	mustOK(t, cli, 250)
	mustOK(t, cli, 250)
	mustOK(t, cli, 354)
	body := "From: ext@sender.test\r\nTo: alice@example.test, bob@example.test\r\nSubject: pipeline\r\n\r\nBody.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
}

func TestAuth_PLAIN_RejectedWithoutTLS(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	ir := base64.StdEncoding.EncodeToString([]byte("\x00alice@example.test\x00" + f.password))
	cli.send(t, "AUTH PLAIN "+ir)
	code, _ := cli.readReply(t)
	if code == 235 {
		t.Fatalf("PLAIN without TLS must not succeed")
	}
}

func TestAuth_PLAIN_OverTLS_Succeeds(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "STARTTLS")
	mustOK(t, cli, 220)
	tlsConn := tls.Client(cli.conn, f.tlsClient)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	cli2 := newSMTPClient(tlsConn)
	cli2.send(t, "EHLO client.example.test")
	mustOK(t, cli2, 250)
	ir := base64.StdEncoding.EncodeToString([]byte("\x00alice@example.test\x00" + f.password))
	cli2.send(t, "AUTH PLAIN "+ir)
	mustOK(t, cli2, 235)
}

func TestAuth_SCRAM_SHA_256(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "STARTTLS")
	mustOK(t, cli, 220)
	tlsConn := tls.Client(cli.conn, f.tlsClient)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	cli2 := newSMTPClient(tlsConn)
	cli2.send(t, "EHLO client.example.test")
	mustOK(t, cli2, 250)

	user := "alice@example.test"
	cNonce := "rOprNGfwEbeRWgbNEkqO"
	clientFirst := "n,,n=" + user + ",r=" + cNonce
	cli2.send(t, "AUTH SCRAM-SHA-256 "+base64.StdEncoding.EncodeToString([]byte(clientFirst)))
	code, text := cli2.readReply(t)
	if code != 334 {
		t.Fatalf("SCRAM start: %d %s", code, text)
	}
	sfB64 := strings.TrimSpace(strings.TrimPrefix(text, "334 "))
	sfBytes, _ := base64.StdEncoding.DecodeString(sfB64)
	sf := string(sfBytes)
	attrs := parseSCRAMAttrs(sf)
	combinedNonce := attrs["r"]
	salt, _ := base64.StdEncoding.DecodeString(attrs["s"])
	cbind := base64.StdEncoding.EncodeToString([]byte("n,,"))
	cfWithoutProof := fmt.Sprintf("c=%s,r=%s", cbind, combinedNonce)
	authMessage := "n=" + user + ",r=" + cNonce + "," + sf + "," + cfWithoutProof
	clientKey := hmacSHA256(pbkdf2HMAC(f.password, salt, 4096, sha256.Size), []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientSig := hmacSHA256(storedKey[:], []byte(authMessage))
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ clientSig[i]
	}
	clientFinal := cfWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	cli2.send(t, base64.StdEncoding.EncodeToString([]byte(clientFinal)))
	code, text = cli2.readReply(t)
	if code != 334 {
		t.Fatalf("SCRAM server-final: %d %s", code, text)
	}
	cli2.send(t, "")
	mustOK(t, cli2, 235)
}

// TestAuth_SCRAM_SHA_256_PLUS exercises the tls-server-end-point
// channel binding round trip on a real TLS connection. Before the fix
// to Wave 4 finding 8 the server's endpointBinding stub returned nil so
// every -PLUS attempt failed with ErrChannelBindingMismatch and the
// mechanism was actually advertised in EHLO. This test fails on the
// pre-fix code in two places: AUTH=SCRAM-SHA-256-PLUS missing from the
// EHLO advertisement, and the proof exchange returning a non-235 reply.
func TestAuth_SCRAM_SHA_256_PLUS(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionImplicitTLS})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	_, ehlo := cli.readReply(t)
	if !strings.Contains(ehlo, "SCRAM-SHA-256-PLUS") {
		t.Fatalf("EHLO did not advertise SCRAM-SHA-256-PLUS: %s", ehlo)
	}
	tlsConn := cli.conn.(*tls.Conn)
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatalf("no peer certs in TLS state")
	}
	cb, err := sasl.TLSServerEndpoint(state.PeerCertificates[0])
	if err != nil {
		t.Fatalf("compute cb: %v", err)
	}
	user := "alice@example.test"
	cNonce := "rOprNGfwEbeRWgbNEkqO"
	gs2 := "p=tls-server-end-point,,"
	clientFirst := gs2 + "n=" + user + ",r=" + cNonce
	cli.send(t, "AUTH SCRAM-SHA-256-PLUS "+base64.StdEncoding.EncodeToString([]byte(clientFirst)))
	code, text := cli.readReply(t)
	if code != 334 {
		t.Fatalf("SCRAM-PLUS start: %d %s", code, text)
	}
	sfB64 := strings.TrimSpace(strings.TrimPrefix(text, "334 "))
	sfBytes, _ := base64.StdEncoding.DecodeString(sfB64)
	sf := string(sfBytes)
	attrs := parseSCRAMAttrs(sf)
	combinedNonce := attrs["r"]
	salt, _ := base64.StdEncoding.DecodeString(attrs["s"])
	cbInput := append([]byte(gs2), cb...)
	cbind := base64.StdEncoding.EncodeToString(cbInput)
	cfWithoutProof := fmt.Sprintf("c=%s,r=%s", cbind, combinedNonce)
	authMessage := "n=" + user + ",r=" + cNonce + "," + sf + "," + cfWithoutProof
	clientKey := hmacSHA256(pbkdf2HMAC(f.password, salt, 4096, sha256.Size), []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientSig := hmacSHA256(storedKey[:], []byte(authMessage))
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ clientSig[i]
	}
	clientFinal := cfWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	cli.send(t, base64.StdEncoding.EncodeToString([]byte(clientFinal)))
	code, text = cli.readReply(t)
	if code != 334 {
		t.Fatalf("SCRAM-PLUS server-final: %d %s", code, text)
	}
	cli.send(t, "")
	mustOK(t, cli, 235)
}

// TestAuth_SCRAM_PLUS_NotAdvertisedWithoutTLS guards the RFC 5802 §6
// rule that the -PLUS variant is TLS-only. STANDARDS rule 10 also
// forbids advertising a wire extension we cannot honour, so the EHLO
// list on a submission STARTTLS port pre-STARTTLS must omit -PLUS even
// though the same listener offers it once TLS is up.
func TestAuth_SCRAM_PLUS_NotAdvertisedWithoutTLS(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	_, ehlo := cli.readReply(t)
	if strings.Contains(ehlo, "SCRAM-SHA-256-PLUS") {
		t.Fatalf("SCRAM-SHA-256-PLUS must not be advertised over cleartext: %s", ehlo)
	}
	if !strings.Contains(ehlo, "SCRAM-SHA-256") {
		t.Fatalf("SCRAM-SHA-256 should still be advertised over cleartext: %s", ehlo)
	}
}

func TestDelivery_Pipeline_CallsSpamAndSieve(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	f.spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.95}`), nil
	})
	script := `require ["fileinto", "spamtest", "relational"];
if spamtest :value "ge" :comparator "i;ascii-numeric" "5" {
  fileinto "Junk";
  stop;
}
`
	if err := f.ha.Store.Meta().SetSieveScript(context.Background(), f.principal, script); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<spammer@phish.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: spammer@phish.test\r\nTo: alice@example.test\r\nSubject: buy viagra\r\n\r\nCheap deals.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)

	assertMessageInMailbox(t, f, f.principal, "Junk", "buy viagra", "Cheap deals.")
}

func TestDelivery_AuthenticationResults_Header_Prepended(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	f.ha.AddDNSRecord("_dmarc.sender.test", "TXT", "v=DMARC1; p=none;")
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: auth\r\n\r\nHi.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)

	raw := loadFirstMessageBytes(t, f, f.principal)
	arIdx := bytes.Index(raw, []byte("Authentication-Results:"))
	fromIdx := bytes.Index(raw, []byte("From: bob@sender.test"))
	if arIdx < 0 {
		t.Fatalf("no Authentication-Results in stored body:\n%s", raw)
	}
	if fromIdx < 0 || arIdx > fromIdx {
		t.Fatalf("Authentication-Results not prepended:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("dmarc=")) {
		t.Errorf("AR missing dmarc= verdict:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("spf=")) {
		t.Errorf("AR missing spf= verdict:\n%s", raw)
	}
}

// TestDeliver_PreservesTextCalendarMIMEPart proves that an inbound
// multipart/alternative message carrying a text/calendar part lands in
// the recipient's mailbox with the calendar bytes byte-for-byte
// preserved (REQ-PROTO-59). The delivery path only prepends Received +
// Authentication-Results headers; nothing rewrites or strips the MIME
// tree.
func TestDeliver_PreservesTextCalendarMIMEPart(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<organizer@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	const calendarPart = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:invite-1@sender.test\r\nDTSTART:20260601T100000Z\r\nDTEND:20260601T110000Z\r\nSUMMARY:Phase 2 Wave 2.5 sync\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	body := "From: organizer@sender.test\r\nTo: alice@example.test\r\nSubject: Invitation: Phase 2 Wave 2.5 sync\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=\"BOUND\"\r\n\r\n--BOUND\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nYou are invited.\r\n--BOUND\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\n" + calendarPart + "--BOUND--\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	raw := loadFirstMessageBytes(t, f, f.principal)
	if !bytes.Contains(raw, []byte(calendarPart)) {
		t.Fatalf("text/calendar body not preserved verbatim:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("Content-Type: text/calendar; method=REQUEST")) {
		t.Fatalf("text/calendar Content-Type header was rewritten:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("BEGIN:VCALENDAR")) || !bytes.Contains(raw, []byte("END:VCALENDAR")) {
		t.Fatalf("VCALENDAR envelope missing or rewritten:\n%s", raw)
	}
}

func TestRateLimiting_Per_IP_ConnectionCap(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	_ = ha.Store.Meta().InsertDomain(context.Background(), store.Domain{Name: "example.test", IsLocal: true})
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	_, _ = dir.CreatePrincipal(context.Background(), "alice@example.test", "correct-horse-staple-battery")
	tlsStore, _ := newTestTLSStore(t)
	resolver := newResolverAdapter(ha.DNS)
	srv, err := protosmtp.New(protosmtp.Config{
		Store:     ha.Store,
		Directory: dir,
		DKIM:      maildkim.New(resolver, ha.Logger, ha.Clock),
		SPF:       mailspf.New(resolver, ha.Clock),
		DMARC:     maildmarc.New(resolver),
		ARC:       mailarc.New(resolver),
		TLS:       tlsStore,
		Resolver:  resolver,
		Clock:     ha.Clock,
		Logger:    ha.Logger,
		Options: protosmtp.Options{
			Hostname:                 "mx.example.test",
			MaxConcurrentPerIP:       2,
			MaxConcurrentConnections: 8,
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)

	ctx := context.Background()
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		c, err := ha.DialSMTPByName(ctx, "smtp")
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		r := bufio.NewReader(c)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("greeting %d: %v", i, err)
		}
		if !strings.HasPrefix(line, "220") {
			t.Fatalf("greeting %d: %q", i, line)
		}
		conns = append(conns, c)
	}
	c, err := ha.DialSMTPByName(ctx, "smtp")
	if err != nil {
		t.Fatalf("dial N+1: %v", err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read N+1: %v", err)
	}
	if !strings.HasPrefix(line, "421 ") {
		t.Fatalf("expected 421 on N+1, got %q", line)
	}
}

func TestPanic_InHandler_Does_Not_Kill_Server(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	f.spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		panic("boom in spam plugin")
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte("From: bob@sender.test\r\nSubject: panic\r\n\r\nbody\r\n.\r\n"))
	code, _ := cli.readReply(t)
	if code != 421 {
		t.Fatalf("expected 421 after panic, got %d", code)
	}

	// Server must still accept a second connection.
	cli2, close2 := f.dial(t)
	defer close2()
	mustOK(t, cli2, 220)
	cli2.send(t, "QUIT")
	mustOK(t, cli2, 221)
}

// --- verifiers -------------------------------------------------------

func assertMessageInMailbox(t *testing.T, f *fixture, pid store.PrincipalID, mbName, subjectSubstr, bodySubstr string) {
	t.Helper()
	ctx := context.Background()
	mbs, err := f.ha.Store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		t.Fatalf("list mailboxes: %v", err)
	}
	var target store.Mailbox
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, mbName) {
			target = mb
			break
		}
	}
	if target.ID == 0 {
		t.Fatalf("mailbox %q not found (have %v)", mbName, mbNames(mbs))
	}
	var found bool
	for mid := store.MessageID(1); mid < 200; mid++ {
		m, err := f.ha.Store.Meta().GetMessage(ctx, mid)
		if err != nil {
			continue
		}
		if m.MailboxID != target.ID {
			continue
		}
		rc, _ := f.ha.Store.Blobs().Get(ctx, m.Blob.Hash)
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if !bytes.Contains(b, []byte(subjectSubstr)) {
			t.Errorf("missing %q in stored body of %q", subjectSubstr, mbName)
		}
		if !bytes.Contains(b, []byte(bodySubstr)) {
			t.Errorf("missing body text %q in %q", bodySubstr, mbName)
		}
		found = true
	}
	if !found {
		t.Fatalf("no message in %q", mbName)
	}
}

func loadFirstMessageBytes(t *testing.T, f *fixture, pid store.PrincipalID) []byte {
	t.Helper()
	ctx := context.Background()
	mbs, _ := f.ha.Store.Meta().ListMailboxes(ctx, pid)
	idset := map[store.MailboxID]bool{}
	for _, mb := range mbs {
		idset[mb.ID] = true
	}
	for mid := store.MessageID(1); mid < 200; mid++ {
		m, err := f.ha.Store.Meta().GetMessage(ctx, mid)
		if err != nil {
			continue
		}
		if !idset[m.MailboxID] {
			continue
		}
		rc, err := f.ha.Store.Blobs().Get(ctx, m.Blob.Hash)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		return b
	}
	t.Fatalf("no message found for principal %d", pid)
	return nil
}

func mbNames(mbs []store.Mailbox) []string {
	out := make([]string, 0, len(mbs))
	for _, m := range mbs {
		out = append(out, m.Name)
	}
	return out
}

// --- SCRAM client math ------------------------------------------------

func parseSCRAMAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if len(kv) < 2 || kv[1] != '=' {
			continue
		}
		out[kv[:1]] = kv[2:]
	}
	return out
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func pbkdf2HMAC(password string, salt []byte, iter, keylen int) []byte {
	prf := hmac.New(sha256.New, []byte(password))
	return pbkdf2F(prf, salt, iter, keylen)
}

func pbkdf2F(prf hash.Hash, salt []byte, iter, keylen int) []byte {
	hLen := prf.Size()
	l := (keylen + hLen - 1) / hLen
	out := make([]byte, 0, l*hLen)
	for block := 1; block <= l; block++ {
		prf.Reset()
		prf.Write(salt)
		var buf [4]byte
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 2; i <= iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keylen]
}
