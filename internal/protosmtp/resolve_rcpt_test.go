package protosmtp_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// scriptedRcptInvoker is a directory.ResolveRcptInvoker driven by a
// per-call function. Tests register handlers per recipient.
type scriptedRcptInvoker struct {
	calls atomic.Int64
	fn    func(context.Context, string, directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error)
}

func (s *scriptedRcptInvoker) InvokeResolveRcpt(ctx context.Context, plugin string, req directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
	s.calls.Add(1)
	return s.fn(ctx, plugin, req)
}

// rcptFixture bundles an inbound SMTP server wired with a scripted
// resolve_rcpt invoker plus the supporting mail-auth verifiers and a
// principal "alice@example.test" so the tests can exercise the
// REQ-DIR-RCPT-03 resolution order against a realistic listener.
type rcptFixture struct {
	ha        *testharness.Server
	srv       *protosmtp.Server
	listener  string
	tlsClient *tls.Config
	inv       *scriptedRcptInvoker
	pid       store.PrincipalID
}

func newRcptFixture(t *testing.T, pluginFirst []string) *rcptFixture {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "app.example.com", IsLocal: true}); err != nil {
		t.Fatalf("insert app domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tlsStore, clientCfg := newTestTLSStore(t)

	inv := &scriptedRcptInvoker{
		fn: func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
			return directory.ResolveRcptResponse{Action: "fallthrough"}, nil
		},
	}
	resolver, err := directory.NewRcptResolver(directory.RcptResolverConfig{
		Invoker:  inv,
		Clock:    ha.Clock,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metadata: ha.Store.Meta(),
	})
	if err != nil {
		t.Fatalf("NewRcptResolver: %v", err)
	}

	resolverDNS := newResolverAdapter(ha.DNS)
	dkimV := maildkim.New(resolverDNS, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolverDNS, ha.Clock)
	dmarcV := maildmarc.New(resolverDNS)
	arcV := mailarc.New(resolverDNS)
	interp := sieve.NewInterpreter()
	spamCls := spam.New(nil, ha.Logger, ha.Clock)

	srv, err := protosmtp.New(protosmtp.Config{
		Store:                  ha.Store,
		Directory:              dir,
		DKIM:                   dkimV,
		SPF:                    spfV,
		DMARC:                  dmarcV,
		ARC:                    arcV,
		Spam:                   spamCls,
		Sieve:                  interp,
		TLS:                    tlsStore,
		Resolver:               resolverDNS,
		Clock:                  ha.Clock,
		Logger:                 ha.Logger,
		RcptResolver:           resolver,
		RcptPluginName:         "app-rcpt",
		RcptPluginFirstDomains: pluginFirst,
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
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)
	return &rcptFixture{
		ha:        ha,
		srv:       srv,
		listener:  "smtp",
		tlsClient: clientCfg,
		inv:       inv,
		pid:       pid,
	}
}

func (f *rcptFixture) dial(t *testing.T) (*smtpClient, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := f.ha.DialSMTPByName(ctx, f.listener)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return newSMTPClient(c), func() { _ = c.Close() }
}

func startRelayInSession(t *testing.T, f *rcptFixture) *smtpClient {
	t.Helper()
	cli, _ := f.dial(t)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	return cli
}

func TestRCPT_LocalDirectoryShortCircuitsBeforePlugin(t *testing.T) {
	f := newRcptFixture(t, nil)
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<alice@example.test>")
	if code, text := cli.readReply(t); code != 250 {
		t.Fatalf("local recipient: got %d %s", code, text)
	}
	if got := f.inv.calls.Load(); got != 0 {
		t.Fatalf("plugin should NOT be invoked for local recipient (calls=%d)", got)
	}
}

func TestRCPT_PluginAcceptSynthetic_RoundTripsThroughDATA(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, req directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "accept", RouteTag: "ticket:42"}, nil
	}
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+42@app.example.com>")
	if code, _ := cli.readReply(t); code != 250 {
		t.Fatalf("synthetic accept: got %d", code)
	}
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: reply+42@app.example.com\r\nSubject: hi\r\n\r\nbody.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	if code, text := cli.readReply(t); code != 250 {
		t.Fatalf("DATA accept: got %d %s", code, text)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

func TestRCPT_PluginRejectMapsTo5xx(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "reject", Code: "5.7.1", Reason: "ticket closed"}, nil
	}
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+42@app.example.com>")
	code, text := cli.readReply(t)
	if code != 550 {
		t.Fatalf("expected 550, got %d %s", code, text)
	}
}

func TestRCPT_PluginDeferMapsTo4xx(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "defer", Code: "4.5.1", Reason: "later"}, nil
	}
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+42@app.example.com>")
	code, _ := cli.readReply(t)
	if code != 450 {
		t.Fatalf("expected 450, got %d", code)
	}
}

func TestRCPT_PluginTimeoutMapsToDefer443(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{}, fmt.Errorf("%w: deadline", directory.ErrResolveRcptTimeout)
	}
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+42@app.example.com>")
	code, text := cli.readReply(t)
	if code != 450 {
		t.Fatalf("expected 450, got %d %s", code, text)
	}
}

func TestRCPT_PluginFallthroughHitsCatchallAndUnknownIs550(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "fallthrough"}, nil
	}
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<unknown@app.example.com>")
	code, _ := cli.readReply(t)
	if code != 550 {
		t.Fatalf("expected 550 after fallthrough, got %d", code)
	}
}

func TestRCPT_PluginFirstForDomain_BypassesInternalLookup(t *testing.T) {
	// alice@example.test is local, but with plugin_first_for_domains
	// covering "example.test" the plugin must own RCPT for that domain.
	// We verify by scripting reject and asserting we see 5.x.x even
	// though the principal exists.
	f := newRcptFixture(t, []string{"example.test"})
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "reject", Code: "5.7.1", Reason: "plugin owns this domain"}, nil
	}
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<alice@example.test>")
	code, _ := cli.readReply(t)
	if code != 550 {
		t.Fatalf("plugin-first reject: got %d", code)
	}
	if f.inv.calls.Load() == 0 {
		t.Fatalf("plugin must be invoked plugin-first for example.test")
	}
}

func TestRCPT_BreakerOpenSkipsPluginCall(t *testing.T) {
	// Drive 25 errors to open the breaker, then send a 26th and assert
	// the plugin is NOT called.
	f := newRcptFixture(t, nil)
	var failing atomic.Bool
	failing.Store(true)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		if failing.Load() {
			return directory.ResolveRcptResponse{}, fmt.Errorf("%w: down", directory.ErrResolveRcptUnavailable)
		}
		return directory.ResolveRcptResponse{Action: "accept"}, nil
	}
	for i := 0; i < 25; i++ {
		cli := startRelayInSession(t, f)
		cli.send(t, "RCPT TO:<reply+x@app.example.com>")
		_, _ = cli.readReply(t)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	}
	beforeCalls := f.inv.calls.Load()
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+x@app.example.com>")
	code, _ := cli.readReply(t)
	if code != 450 {
		t.Fatalf("breaker-open should defer, got %d", code)
	}
	if f.inv.calls.Load() != beforeCalls {
		t.Fatalf("breaker-open path should NOT call the plugin")
	}
}

func TestRCPT_AcceptUnknownPrincipalDowngradesToDefer43(t *testing.T) {
	f := newRcptFixture(t, nil)
	bogusPID := uint64(99999)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "accept", PrincipalID: &bogusPID}, nil
	}
	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+x@app.example.com>")
	code, _ := cli.readReply(t)
	if code != 450 {
		t.Fatalf("unknown principal_id must defer 4.3.0, got %d", code)
	}
}
