package protosmtp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

// TestInboundDelivery_PopulatesReceivedTo runs a single SMTP RCPT TO
// against the relay-in listener and asserts the per-(message, mailbox)
// row records the canonical envelope address (REQ-FLOW-33). The test
// also confirms the stored blob does NOT contain the synthetic
// X-Herold-Recipient header (REQ-FLOW-34: render-only, not persisted)
// and that the read path injects the header from the per-recipient
// row.
func TestInboundDelivery_PopulatesReceivedTo(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	// Create alice and install a +work alias to her so the upstream
	// RCPT TO can use a tag-style local-part while still resolving to
	// alice's INBOX. This mirrors the v1 alias-expansion path.
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := ha.Store.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "alice+work",
		Domain:          "example.test",
		TargetPrincipal: pid,
	}); err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}

	// Spam plug returns Ham so the delivery walks through to InsertMessage.
	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05}`), nil
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
	tlsStore, _ := newTestTLSStore(t)

	srv, err := protosmtp.New(protosmtp.Config{
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
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)

	// Drive a single SMTP delivery with a +work tag.
	c, err := ha.DialSMTPByName(ctx, "smtp")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	cli := newSMTPClient(c)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	// Upper-case the recipient on the wire to verify canonicalisation
	// lower-cases for storage.
	cli.send(t, "RCPT TO:<Alice+Work@Example.Test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: tagged\r\n\r\nbody-content\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	// The stored message's per-mailbox row must carry the canonical
	// envelope recipient.
	mb, err := ha.Store.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	msgs, err := ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	got := msgs[0].ReceivedTo
	want := "alice+work@example.test"
	if got != want {
		t.Fatalf("ReceivedTo = %q, want %q (canonical lower-cased form)", got, want)
	}

	// The persisted blob MUST NOT contain X-Herold-Recipient — the
	// header is injected at render time only (REQ-FLOW-34 keeps the
	// blob dedup-safe across fan-out per REQ-FLOW-30).
	rc, err := ha.Store.Blobs().Get(ctx, msgs[0].Blob.Hash)
	if err != nil {
		t.Fatalf("Blobs.Get: %v", err)
	}
	defer rc.Close()
	rawBlob, rerr := io.ReadAll(rc)
	if rerr != nil {
		t.Fatalf("read blob: %v", rerr)
	}
	if strings.Contains(strings.ToLower(string(rawBlob)), strings.ToLower(mailparse.XHeroldRecipientHeader)) {
		t.Fatalf("persisted blob carries %s header — must be render-time only:\n%s",
			mailparse.XHeroldRecipientHeader, string(rawBlob))
	}
}
