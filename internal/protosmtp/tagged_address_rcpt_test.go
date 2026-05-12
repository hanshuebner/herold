package protosmtp_test

// tagged_address_rcpt_test.go covers the RFC 5233 sub-addressing
// fallback added at RCPT-resolution time (REQ-TAG-01, REQ-TAG-20 and
// the corresponding fix to session.go:runRcptResolutionChain).
//
// Background: the directory has only `alice@example.local` registered;
// before the fix the literal lookup of `alice+test` returned ErrNotFound
// and the RCPT was rejected with 550 even though the inbound pipeline
// would otherwise handle the +suffix at deliver time (REQ-TAG-20).
// The fix strips the suffix and retries the directory lookup with the
// base local-part, gated on [server.tagged_addresses] enabled = true.

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
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

// startTagAddrServer constructs a relay-in SMTP server with ONLY
// `alice@example.test` registered in the directory. The
// taggedAddressesEnabled flag is configurable so tests can assert both
// the enabled (fallback applies) and disabled (pre-fix 550) branches.
//
// We intentionally do NOT wire a resolve_rcpt plugin: the fallback we
// are testing lives in the internal-only chain (step 2b in
// runRcptResolutionChain). The plugin step is exercised by the
// resolve_rcpt_test.go suite.
func startTagAddrServer(t *testing.T, tagEnabled bool) (*testharness.Server, store.PrincipalID) {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	resolverDNS := newResolverAdapter(ha.DNS)
	tlsStore, _ := newTestTLSStore(t)
	spamCls := spam.New(nil, ha.Logger, ha.Clock)
	cfg := protosmtp.Config{
		Store:        ha.Store,
		Directory:    dir,
		DKIM:         maildkim.New(resolverDNS, slog.New(slog.NewTextHandler(io.Discard, nil)), ha.Clock),
		SPF:          mailspf.New(resolverDNS, ha.Clock),
		DMARC:        maildmarc.New(resolverDNS),
		ARC:          mailarc.New(resolverDNS),
		Spam:         spamCls,
		Sieve:        sieve.NewInterpreter(),
		TLS:          tlsStore,
		Resolver:     resolverDNS,
		Clock:        ha.Clock,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RcptResolver: nil,
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
	}
	cfg.TaggedAddressesEnabled = &tagEnabled
	srv, err := protosmtp.New(cfg)
	if err != nil {
		t.Fatalf("protosmtp.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)
	return ha, pid
}

func dialRcptSession(t *testing.T, ha *testharness.Server) *smtpClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := ha.DialSMTPByName(ctx, "smtp")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cli := newSMTPClient(conn)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	return cli
}

// TestRCPT_TaggedAddress_FallbackAccepts is the headline Gap 2 test:
// the directory has only `alice` registered; a RCPT to
// `alice+test@example.test` must be accepted (250) when the tagged-
// addresses feature is on. The base principal id is wired into the
// envelope so the inbound pipeline can resolve the +test suffix at
// delivery time (REQ-TAG-20 step 1).
func TestRCPT_TaggedAddress_FallbackAccepts(t *testing.T) {
	ha, _ := startTagAddrServer(t, true)
	cli := dialRcptSession(t, ha)
	cli.send(t, "RCPT TO:<alice+test@example.test>")
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("alice+test: got %d %s; want 250", code, text)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

// TestRCPT_TaggedAddress_DeliversWithSuffix carries the fallback
// through DATA so we know the message lands. The downstream tagged-
// address machinery is exercised independently in deliver_tagged_test;
// here we only need to confirm the RCPT/DATA sequence completes with
// 250 (the message body's Sieve / spam / mailbox-membership wiring is
// taken care of by the shared fixture).
func TestRCPT_TaggedAddress_DeliversWithSuffix(t *testing.T) {
	ha, _ := startTagAddrServer(t, true)
	cli := dialRcptSession(t, ha)
	cli.send(t, "RCPT TO:<alice+test@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice+test@example.test\r\nSubject: tagged\r\n\r\nbody\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("DATA: got %d %s", code, text)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

// TestRCPT_TaggedAddress_FallbackDisabled asserts the pre-fix behaviour
// is preserved when [server.tagged_addresses] enabled = false. With
// the feature off the fallback must NOT apply; a +suffixed recipient
// that has no directory entry falls through to the 550 reply.
func TestRCPT_TaggedAddress_FallbackDisabled(t *testing.T) {
	ha, _ := startTagAddrServer(t, false)
	cli := dialRcptSession(t, ha)
	cli.send(t, "RCPT TO:<alice+test@example.test>")
	code, text := cli.readReply(t)
	if code != 550 {
		t.Fatalf("disabled feature: got %d %s; want 550", code, text)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

// TestRCPT_TaggedAddress_EmptyBaseLocalPart asserts that a recipient
// whose local-part starts with `+` (no characters before the
// delimiter) is rejected. RFC 5233 §3.1 reserves a non-empty base
// local-part; `+something` is not a valid sub-address.
func TestRCPT_TaggedAddress_EmptyBaseLocalPart(t *testing.T) {
	ha, _ := startTagAddrServer(t, true)
	cli := dialRcptSession(t, ha)
	cli.send(t, "RCPT TO:<+test@example.test>")
	code, text := cli.readReply(t)
	if code != 550 {
		t.Fatalf("+test@: got %d %s; want 550", code, text)
	}
}

// TestRCPT_TaggedAddress_MultiplePluses asserts that local-parts with
// more than one `+` accept on the first `+` as the separator
// (REQ-TAG-01 last sentence: bytes after the first `+` are part of
// the suffix verbatim). `alice+a+b@example.test` must be accepted
// because the base local-part is `alice` and the suffix is `a+b`.
func TestRCPT_TaggedAddress_MultiplePluses(t *testing.T) {
	ha, _ := startTagAddrServer(t, true)
	cli := dialRcptSession(t, ha)
	cli.send(t, "RCPT TO:<alice+a+b@example.test>")
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("alice+a+b: got %d %s; want 250", code, text)
	}
}

// TestRCPT_TaggedAddress_CaseInsensitiveBaseLookup confirms the
// fallback inherits the directory's case-folding semantics: a RCPT
// to `Alice+Test@Example.test` resolves through the lowercase base
// principal `alice@example.test`.
func TestRCPT_TaggedAddress_CaseInsensitiveBaseLookup(t *testing.T) {
	ha, _ := startTagAddrServer(t, true)
	cli := dialRcptSession(t, ha)
	cli.send(t, "RCPT TO:<Alice+Test@Example.test>")
	code, text := cli.readReply(t)
	if code != 250 {
		t.Fatalf("mixed-case sub-address: got %d %s; want 250", code, text)
	}
}

// TestRCPT_TaggedAddress_UnknownBaseStillRejected ensures the
// fallback only accepts when the BASE principal exists. A RCPT to
// `nobody+test@example.test` (no `nobody` principal) must reject
// 550 even with the feature on; the fallback is not a bypass for
// arbitrary local-parts.
func TestRCPT_TaggedAddress_UnknownBaseStillRejected(t *testing.T) {
	ha, _ := startTagAddrServer(t, true)
	cli := dialRcptSession(t, ha)
	cli.send(t, "RCPT TO:<nobody+test@example.test>")
	code, text := cli.readReply(t)
	if code != 550 {
		t.Fatalf("nobody+test: got %d %s; want 550 (no base principal)", code, text)
	}
}
