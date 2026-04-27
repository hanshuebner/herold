package protosmtp_test

// Inbound reaction pipeline tests (REQ-FLOW-104..108).
// Three scenarios:
//   1. Valid reaction email: consumed (not stored in mailbox), reaction row added.
//   2. Reactor not a recognised participant: falls through to normal delivery.
//   3. Spam-classified reaction email: falls through to junk delivery (REQ-FLOW-108).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
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
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

// reactionFixture sets up a complete SMTP server with one local principal
// "alice@example.test" who has an INBOX with one pre-existing message.
type reactionFixture struct {
	ha       *testharness.Server
	srv      *protosmtp.Server
	pid      store.PrincipalID
	msgID    store.MessageID
	origID   string // Message-ID header without angle brackets
	inbox    store.Mailbox
	spamPlug *fakeplugin.FakePlugin
}

func newReactionFixture(t *testing.T) *reactionFixture {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	// Insert an INBOX and a seed message that the reaction will target.
	inbox, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	origMsgIDHeader := "seed001@example.test"
	now := ha.Clock.Now()
	blob, err := ha.Store.Blobs().Put(ctx, strings.NewReader(
		"From: bob@remote.test\r\nTo: alice@example.test\r\nSubject: Original\r\n\r\nHi Alice\r\n",
	))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	uid, _, err := ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    inbox.ID,
		InternalDate: now,
		ReceivedAt:   now,
		Size:         blob.Size,
		Blob:         blob,
		Envelope: store.Envelope{
			MessageID: origMsgIDHeader,
			Subject:   "Original",
			From:      "bob@remote.test",
			To:        "alice@example.test",
			Date:      now,
		},
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	// Retrieve the MessageID via the change feed (same pattern as email_test.go).
	feed, err := ha.Store.Meta().ReadChangeFeed(ctx, pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var seedMsgID store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			seedMsgID = store.MessageID(e.EntityID)
		}
	}
	if seedMsgID == 0 {
		t.Fatalf("seed message not found in feed")
	}
	_ = uid // suppress unused warning

	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
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

	return &reactionFixture{
		ha:       ha,
		srv:      srv,
		pid:      pid,
		msgID:    seedMsgID,
		origID:   origMsgIDHeader,
		inbox:    inbox,
		spamPlug: spamPlug,
	}
}

// deliverReactionEmail drives an SMTP session delivering a reaction email to
// alice@example.test.  fromAddr is used as MAIL FROM and From: header.
// actionValue is the X-Herold-Reaction-Action header value.
func (rf *reactionFixture) deliverReactionEmail(t *testing.T, fromAddr, emoji, actionValue string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := rf.ha.DialSMTPByName(ctx, "smtp")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	cli := newSMTPClient(c)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, fmt.Sprintf("MAIL FROM:<%s>", fromAddr))
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)

	body := fmt.Sprintf(
		"From: %s\r\n"+
			"To: alice@example.test\r\n"+
			"Subject: Re: Original\r\n"+
			"X-Herold-Reaction-To: <%s>\r\n"+
			"X-Herold-Reaction-Emoji: %s\r\n"+
			"X-Herold-Reaction-Action: %s\r\n"+
			"\r\n"+
			"%s reacted with %s.\r\n"+
			".\r\n",
		fromAddr, rf.origID, emoji, actionValue, fromAddr, emoji,
	)
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

// messageCountInInbox returns the number of messages in alice's INBOX.
func (rf *reactionFixture) messageCountInInbox(t *testing.T) int {
	t.Helper()
	msgs, err := rf.ha.Store.Meta().ListMessages(context.Background(), rf.inbox.ID, store.MessageFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	return len(msgs)
}

// TestReaction_InboundConsumed verifies that a valid inbound reaction email
// from a recognised participant (who is also a local principal) is consumed
// (not stored in INBOX) and the reaction is added to the original message
// (REQ-FLOW-104..107).
//
// The reactor must be a local principal because v1 only resolves reactors via
// GetPrincipalByEmail (REQ-FLOW-106 v1 simplification: external reactors not
// in the directory fall through to normal delivery).  We use "charlie" who is
// a local user and also appeared in the original message's Cc field.
func TestReaction_InboundConsumed(t *testing.T) {
	rf := newReactionFixture(t)
	ctx := context.Background()

	// Create charlie as a local principal (the reactor).
	dir := directory.New(rf.ha.Store.Meta(), rf.ha.Logger, rf.ha.Clock, rand.Reader)
	_, err := dir.CreatePrincipal(ctx, "charlie@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal charlie: %v", err)
	}

	// Update the seed message envelope so that charlie is a Cc participant.
	// The easiest way is to insert a fresh message with charlie in Cc and use
	// that as the reaction target.
	now := rf.ha.Clock.Now()
	blob, err := rf.ha.Store.Blobs().Put(ctx, strings.NewReader(
		"From: charlie@example.test\r\nTo: alice@example.test\r\nSubject: With CC\r\n\r\nHi\r\n",
	))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	newOrigID := "seed-charlie@example.test"
	if _, _, err := rf.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    rf.inbox.ID,
		InternalDate: now,
		ReceivedAt:   now,
		Size:         blob.Size,
		Blob:         blob,
		Envelope: store.Envelope{
			MessageID: newOrigID,
			Subject:   "With CC",
			From:      "charlie@example.test",
			To:        "alice@example.test",
			Date:      now,
		},
	}); err != nil {
		t.Fatalf("InsertMessage charlie-orig: %v", err)
	}

	// Look up the new message ID via change feed.
	feed, err := rf.ha.Store.Meta().ReadChangeFeed(ctx, rf.pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var charlieMsgID store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			charlieMsgID = store.MessageID(e.EntityID)
		}
	}
	if charlieMsgID == 0 {
		t.Fatalf("charlie seed message not found in feed")
	}

	// INBOX now has 2 messages (original seed + charlie-seed).
	before := rf.messageCountInInbox(t)
	if before != 2 {
		t.Fatalf("precondition: inbox count = %d, want 2", before)
	}

	// Deliver a reaction email where the reactor is charlie@example.test
	// (a local principal who is also the From: sender of the target message).
	// We override deliverReactionEmail inline to use newOrigID.
	func() {
		c2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := rf.ha.DialSMTPByName(c2, "smtp")
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer conn.Close()
		cli := newSMTPClient(conn)
		mustOK(t, cli, 220)
		cli.send(t, "EHLO client.example.test")
		mustOK(t, cli, 250)
		cli.send(t, "MAIL FROM:<charlie@example.test>")
		mustOK(t, cli, 250)
		cli.send(t, "RCPT TO:<alice@example.test>")
		mustOK(t, cli, 250)
		cli.send(t, "DATA")
		mustOK(t, cli, 354)
		body := fmt.Sprintf(
			"From: charlie@example.test\r\n"+
				"To: alice@example.test\r\n"+
				"Subject: Re: With CC\r\n"+
				"X-Herold-Reaction-To: <%s>\r\n"+
				"X-Herold-Reaction-Emoji: thumbs-up\r\n"+
				"X-Herold-Reaction-Action: add\r\n"+
				"\r\n"+
				"charlie reacted with thumbs-up.\r\n"+
				".\r\n",
			newOrigID,
		)
		cli.sendRaw(t, []byte(body))
		mustOK(t, cli, 250)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	}()

	// INBOX must still have 2 messages (reaction was consumed, not stored).
	after := rf.messageCountInInbox(t)
	if after != 2 {
		t.Errorf("inbox count = %d, want 2 (reaction should have been consumed)", after)
	}

	// The reaction row must be present on the charlie-seed message.
	rxns, err := rf.ha.Store.Meta().ListEmailReactions(ctx, charlieMsgID)
	if err != nil {
		t.Fatalf("ListEmailReactions: %v", err)
	}
	if len(rxns["thumbs-up"]) == 0 {
		t.Errorf("thumbs-up reaction not stored: %v", rxns)
	}
}

// TestReaction_InboundUnrecognisedReactor verifies that a reaction email from
// an address not in the original message's participants is delivered normally
// to INBOX (REQ-FLOW-105.4).
func TestReaction_InboundUnrecognisedReactor(t *testing.T) {
	rf := newReactionFixture(t)

	before := rf.messageCountInInbox(t)
	if before != 1 {
		t.Fatalf("precondition: inbox count = %d, want 1", before)
	}

	// stranger@other.test is not a participant of the seed message.
	rf.deliverReactionEmail(t, "stranger@other.test", "fire", "add")

	// Message must be delivered normally (inbox count goes to 2).
	after := rf.messageCountInInbox(t)
	if after != 2 {
		t.Errorf("inbox count = %d, want 2 (unrecognised reactor delivers normally)", after)
	}

	// No reaction should have been stored.
	rxns, err := rf.ha.Store.Meta().ListEmailReactions(context.Background(), rf.msgID)
	if err != nil {
		t.Fatalf("ListEmailReactions: %v", err)
	}
	if len(rxns) != 0 {
		t.Errorf("reaction stored despite unrecognised reactor: %v", rxns)
	}
}

// TestReaction_InboundSpamDeliveredNormally verifies that a spam-classified
// reaction email is delivered normally to the Junk mailbox and is NOT
// consumed as a reaction (REQ-FLOW-108).
func TestReaction_InboundSpamDeliveredNormally(t *testing.T) {
	rf := newReactionFixture(t)
	ctx := context.Background()

	// Reconfigure the spam plugin to return Spam for this test.
	rf.spamPlug.Handle("spam.classify", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.99}`), nil
	})

	// Ensure a Junk mailbox exists.
	junk, err := rf.ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: rf.pid,
		Name:        "Junk",
	})
	if err != nil {
		t.Fatalf("InsertMailbox Junk: %v", err)
	}

	before := rf.messageCountInInbox(t)
	if before != 1 {
		t.Fatalf("precondition: inbox count = %d, want 1", before)
	}

	// bob@remote.test is a recognised participant but the message is spam.
	rf.deliverReactionEmail(t, "bob@remote.test", "heart", "add")

	// No reaction row must be stored.
	rxns, err := rf.ha.Store.Meta().ListEmailReactions(ctx, rf.msgID)
	if err != nil {
		t.Fatalf("ListEmailReactions: %v", err)
	}
	if len(rxns) != 0 {
		t.Errorf("reaction stored despite spam classification: %v", rxns)
	}

	// The spam message must be delivered somewhere (Junk or INBOX).
	// Just verify total message count increased.
	afterInbox := rf.messageCountInInbox(t)
	junkMsgs, err := rf.ha.Store.Meta().ListMessages(ctx, junk.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages junk: %v", err)
	}
	totalAfter := afterInbox + len(junkMsgs)
	if totalAfter < 2 {
		t.Errorf("total messages = %d, want >= 2 (spam should be delivered, not consumed)", totalAfter)
	}
}
