package protosmtp_test

// classify_structural_fallback_test.go is the regression test for a
// defect found while writing the #304 acceptance matrix: REQ-FILT-214 /
// ADR-0002 promise that the server's structural fallback categoriser
// ("List-Id present -> forums", etc.) runs "where the classifier
// plugin's category is empty ... or no classifier plugin is installed".
// Before this fix, classifyMessage (internal/protosmtp/deliver.go)
// returned Classification{Verdict: Unclassified} immediately whenever
// spam.Classifier.Classify returned an error -- which is exactly what
// happens when no plugin is configured at all (the plugin-registry
// lookup for the (defaulted) plugin name fails) -- bypassing the switch
// that applies the structural fallback. A herold with no classifier
// plugin therefore never got a $category-* keyword on any message, list
// mail included, even though categorisation was enabled for the
// principal.
//
// This test constructs a protosmtp.Server with a real, non-nil
// spam.Classifier but NO plugin registered under any name (the shape
// admin.StartServer produces when system.toml carries no [[plugin]]
// block of type "spam"/"classifier"), delivers a List-Id-bearing
// message, and asserts the message still lands with $category-forums.

import (
	"context"
	"crypto/rand"
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

func TestDelivery_NoPluginConfigured_StructuralFallbackStillApplies(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
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
	tlsStore, _ := newTestTLSStore(t)

	// The invoker's registry is deliberately empty: no plugin registered
	// under any name, matching a system.toml with no [[plugin]] block of
	// type "spam"/"classifier". The Classifier itself is still wired
	// (spamClassifier is always constructed in admin.StartServer,
	// regardless of whether any plugin is configured), so this exercises
	// classifyMessage's Classify-returns-an-error path, not its
	// srv.spam == nil path.
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
	ha.AttachSMTPWithOptions("smtp", srv, protosmtp.ListenerOptions{Mode: protosmtp.RelayIn})

	dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := ha.DialSMTPByName(dctx, "smtp")
	if err != nil {
		t.Fatalf("dial smtp: %v", err)
	}
	defer c.Close()
	cli := newSMTPClient(c)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<sender@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: sender@sender.test\r\nTo: alice@example.test\r\n" +
		"List-Id: <announce.example.test>\r\n" +
		"Message-ID: <no-plugin-fallback@sender.test>\r\n" +
		"Subject: list mail with no classifier configured\r\n\r\nBody.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	mb, err := ha.Store.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	msgs, err := ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in INBOX = %d, want 1", len(msgs))
	}
	var gotCategory bool
	for _, kw := range msgs[0].Keywords {
		if kw == "$category-forums" {
			gotCategory = true
		}
	}
	if !gotCategory {
		t.Fatalf("keywords = %v, want $category-forums present (structural fallback, REQ-FILT-214) even with no classifier plugin configured", msgs[0].Keywords)
	}
}
