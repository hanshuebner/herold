package protosmtp_test

// Tests that all log records emitted by the protosmtp package carry a
// valid "activity" attribute (REQ-OPS-86 / REQ-OPS-86a).
//
// TestActivityTagged_AuthSuccess asserts that AUTH success logs the
// "smtp auth success" record with activity=audit.
//
// TestActivityTagged_DataAcceptedSubmission asserts that a successful
// DATA transaction on a submission listener logs "smtp data accepted"
// with activity=user.
//
// TestActivityTagged_DataAcceptedRelayIn asserts the same transaction
// on a relay-in listener produces activity=system for the data-accept
// record.
//
// TestActivityTagged_FullSession drives a complete session through
// observe.AssertActivityTagged, verifying that every record emitted
// during the session carries a valid activity value.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

// buildActivityFixture creates a protosmtp.Server whose logger is the
// supplied log. All other dependencies are spun up from a fresh
// testharness. The returned fixture, its closer, and the harness are
// returned so the caller can dial and drive SMTP.
func buildActivityFixture(
	t *testing.T,
	log *slog.Logger,
	mode protosmtp.ListenerMode,
) (*fixture, func()) {
	t.Helper()
	proto, name := protoNameFor(mode)
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
	spamPlug.Handle("spam.classify", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
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
		Logger:      log, // recording logger from AssertActivityTagged
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
		t.Fatalf("protosmtp.New: %v", err)
	}
	ha.AttachSMTP(name, srv, mode)
	f := &fixture{
		ha:        ha,
		srv:       srv,
		listener:  name,
		mode:      mode,
		principal: pid,
		password:  password,
		tlsClient: clientCfg,
		spamPlug:  spamPlug,
	}
	closer := func() { _ = srv.Close(context.Background()) }
	return f, closer
}

// dialAndUpgradeTLS opens a TLS-upgraded connection on a STARTTLS
// submission listener. The plain and upgraded clients are both
// returned; the caller must close the upgraded conn.
func dialAndUpgradeTLS(t *testing.T, f *fixture) (plain *smtpClient, upgraded *smtpClient, closeConn func()) {
	t.Helper()
	cli, closeFn := f.dial(t)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "STARTTLS")
	mustOK(t, cli, 220)
	tlsConn := tls.Client(cli.conn, f.tlsClient)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		closeFn()
		t.Fatalf("tls handshake: %v", err)
	}
	cli2 := newSMTPClient(tlsConn)
	cli2.send(t, "EHLO client.example.test")
	mustOK(t, cli2, 250)
	return cli, cli2, func() { _ = tlsConn.Close(); closeFn() }
}

// TestActivityTagged_FullSession drives an entire relay-in session
// (EHLO → MAIL → RCPT → DATA → QUIT) through observe.AssertActivityTagged
// and asserts that every log record emitted by the protosmtp package
// carries a valid "activity" attribute (REQ-OPS-86a).
func TestActivityTagged_FullSession(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		f, closer := buildActivityFixture(t, log, protosmtp.RelayIn)
		defer closer()

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
		msg := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: tagged\r\n\r\nBody.\r\n.\r\n"
		cli.sendRaw(t, []byte(msg))
		mustOK(t, cli, 250)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	})
}

// TestActivityTagged_AuthSuccess verifies that a successful AUTH
// interaction on a submission STARTTLS listener emits exactly one
// "smtp auth success" record with activity=audit.
func TestActivityTagged_AuthSuccess(t *testing.T) {
	var auditRecords []string
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		// Intercept records so we can inspect the "smtp auth success" one.
		interceptLog := slog.New(&activityCapture{
			base:    log.Handler(),
			capture: &auditRecords,
			target:  "smtp auth success",
			wantAct: observe.ActivityAudit,
			t:       t,
		})

		f, closer := buildActivityFixture(t, interceptLog, protosmtp.SubmissionSTARTTLS)
		defer closer()

		_, cli2, closeFn := dialAndUpgradeTLS(t, f)
		defer closeFn()

		ir := base64.StdEncoding.EncodeToString([]byte("\x00alice@example.test\x00" + f.password))
		cli2.send(t, "AUTH PLAIN "+ir)
		mustOK(t, cli2, 235)
		cli2.send(t, "QUIT")
		mustOK(t, cli2, 221)
	})

	if len(auditRecords) == 0 {
		t.Errorf("expected at least one %q record with activity=audit, got none", "smtp auth success")
	}
}

// TestActivityTagged_DataAcceptedSubmission verifies that a successful
// DATA transaction on a submission listener logs "smtp data accepted"
// with activity=user (REQ-OPS-86d).
func TestActivityTagged_DataAcceptedSubmission(t *testing.T) {
	var userRecords []string
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		interceptLog := slog.New(&activityCapture{
			base:    log.Handler(),
			capture: &userRecords,
			target:  "smtp data accepted",
			wantAct: observe.ActivityUser,
			t:       t,
		})

		f, closer := buildActivityFixture(t, interceptLog, protosmtp.SubmissionSTARTTLS)
		defer closer()

		_, cli2, closeFn := dialAndUpgradeTLS(t, f)
		defer closeFn()

		ir := base64.StdEncoding.EncodeToString([]byte("\x00alice@example.test\x00" + f.password))
		cli2.send(t, "AUTH PLAIN "+ir)
		mustOK(t, cli2, 235)
		// Submission: send a message to a local recipient.
		cli2.send(t, "MAIL FROM:<alice@example.test>")
		mustOK(t, cli2, 250)
		cli2.send(t, "RCPT TO:<alice@example.test>")
		mustOK(t, cli2, 250)
		cli2.send(t, "DATA")
		mustOK(t, cli2, 354)
		msg := "From: alice@example.test\r\nTo: alice@example.test\r\nSubject: sub\r\n\r\nHello.\r\n.\r\n"
		cli2.sendRaw(t, []byte(msg))
		mustOK(t, cli2, 250)
		cli2.send(t, "QUIT")
		mustOK(t, cli2, 221)
	})

	if len(userRecords) == 0 {
		t.Errorf("expected at least one %q record with activity=user, got none", "smtp data accepted")
	}
}

// TestActivityTagged_DataAcceptedRelayIn verifies that a DATA accept on
// a relay-in listener logs "smtp data accepted" with activity=system
// (REQ-OPS-86d).
func TestActivityTagged_DataAcceptedRelayIn(t *testing.T) {
	var sysRecords []string
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		interceptLog := slog.New(&activityCapture{
			base:    log.Handler(),
			capture: &sysRecords,
			target:  "smtp data accepted",
			wantAct: observe.ActivitySystem,
			t:       t,
		})

		f, closer := buildActivityFixture(t, interceptLog, protosmtp.RelayIn)
		defer closer()

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
		msg := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: relay\r\n\r\nBody.\r\n.\r\n"
		cli.sendRaw(t, []byte(msg))
		mustOK(t, cli, 250)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	})

	if len(sysRecords) == 0 {
		t.Errorf("expected at least one %q record with activity=system, got none", "smtp data accepted")
	}
}

// activityCapture is an slog.Handler that wraps a base handler and
// captures messages matching target, asserting they carry wantAct as
// the "activity" attribute value.
type activityCapture struct {
	base    slog.Handler
	capture *[]string
	target  string
	wantAct string
	t       *testing.T
}

func (a *activityCapture) Enabled(ctx context.Context, lvl slog.Level) bool {
	return a.base.Enabled(ctx, lvl)
}

func (a *activityCapture) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == a.target {
		var gotAct string
		r.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "activity" {
				gotAct = attr.Value.String()
				return false
			}
			return true
		})
		// Also check pre-scoped attrs via a clone with the handler's attrs.
		if gotAct == "" {
			// activity may be pre-scoped; the base handler carries it.
			// We can't directly access pre-scoped attrs here, but our
			// WithAttrs override merges them into a child.
		}
		if gotAct != a.wantAct && gotAct != "" {
			a.t.Errorf("record %q: activity=%q want %q", r.Message, gotAct, a.wantAct)
		}
		if gotAct == a.wantAct {
			*a.capture = append(*a.capture, r.Message)
		}
	}
	return a.base.Handle(ctx, r)
}

func (a *activityCapture) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Propagate activity pre-scoped attrs into the capture logic.
	var preAct string
	for _, attr := range attrs {
		if attr.Key == "activity" {
			preAct = attr.Value.String()
		}
	}
	child := &activityCaptureChild{
		parent:  a,
		base:    a.base.WithAttrs(attrs),
		preAct:  preAct,
		capture: a.capture,
		target:  a.target,
		wantAct: a.wantAct,
		t:       a.t,
	}
	return child
}

func (a *activityCapture) WithGroup(name string) slog.Handler {
	return &activityCapture{
		base:    a.base.WithGroup(name),
		capture: a.capture,
		target:  a.target,
		wantAct: a.wantAct,
		t:       a.t,
	}
}

// activityCaptureChild handles the WithAttrs case where activity is
// pre-scoped.
type activityCaptureChild struct {
	parent  *activityCapture
	base    slog.Handler
	preAct  string
	capture *[]string
	target  string
	wantAct string
	t       *testing.T
}

func (c *activityCaptureChild) Enabled(ctx context.Context, lvl slog.Level) bool {
	return c.base.Enabled(ctx, lvl)
}

func (c *activityCaptureChild) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == c.target {
		gotAct := c.preAct
		r.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "activity" {
				gotAct = attr.Value.String()
				return false
			}
			return true
		})
		if gotAct != c.wantAct && gotAct != "" {
			c.t.Errorf("record %q: activity=%q want %q", r.Message, gotAct, c.wantAct)
		}
		if gotAct == c.wantAct {
			*c.capture = append(*c.capture, r.Message)
		}
	}
	return c.base.Handle(ctx, r)
}

func (c *activityCaptureChild) WithAttrs(attrs []slog.Attr) slog.Handler {
	preAct := c.preAct
	for _, attr := range attrs {
		if attr.Key == "activity" {
			preAct = attr.Value.String()
		}
	}
	return &activityCaptureChild{
		parent:  c.parent,
		base:    c.base.WithAttrs(attrs),
		preAct:  preAct,
		capture: c.capture,
		target:  c.target,
		wantAct: c.wantAct,
		t:       c.t,
	}
}

func (c *activityCaptureChild) WithGroup(name string) slog.Handler {
	return &activityCaptureChild{
		parent:  c.parent,
		base:    c.base.WithGroup(name),
		preAct:  c.preAct,
		capture: c.capture,
		target:  c.target,
		wantAct: c.wantAct,
		t:       c.t,
	}
}
