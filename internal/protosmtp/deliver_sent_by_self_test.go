package protosmtp_test

// deliver_sent_by_self_test.go: mail a principal sends to itself arrives
// read. The queue's loopback deliverer flags the recipient SentBySelf when
// the submitting principal is the recipient; every mailbox copy of the
// delivery then carries $seen, both when the delivery inserts a new row
// and when it folds onto the sender's own Sent copy (same Message-ID,
// re #496), which is how a JMAP send to oneself arrives.

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_SentBySelf_ArrivesSeen_SQLite(t *testing.T) {
	testDeliverySentBySelfArrivesSeen(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_SentBySelf_ArrivesSeen_Postgres(t *testing.T) {
	testDeliverySentBySelfArrivesSeen(t, newPGStoreFactory)
}

func testDeliverySentBySelfArrivesSeen(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	ctx := context.Background()
	meta := f.ha.Store.Meta()
	inbox, err := meta.GetMailboxByName(ctx, f.principal, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName INBOX: %v", err)
	}

	ingest := func(msgID string, sentBySelf bool) {
		t.Helper()
		body := "From: alice@example.test\r\nTo: alice@example.test\r\n" +
			"Message-ID: <" + msgID + ">\r\nSubject: " + msgID + "\r\n\r\nhi\r\n"
		if err := f.srv.IngestBytes(ctx, protosmtp.IngestRequest{
			Body:         []byte(body),
			MailFrom:     "alice@example.test",
			SourceIP:     "127.0.0.1",
			IngestSource: "loopback",
			Recipients: []protosmtp.IngestRecipient{{
				Addr: "alice@example.test", PrincipalID: f.principal, SentBySelf: sentBySelf,
			}},
		}); err != nil {
			t.Fatalf("IngestBytes %s: %v", msgID, err)
		}
	}
	inboxSeen := func(msgID string) bool {
		t.Helper()
		m, err := meta.GetMessageByMessageIDHeader(ctx, f.principal, msgID)
		if err != nil {
			t.Fatalf("GetMessageByMessageIDHeader %s: %v", msgID, err)
		}
		for _, mm := range m.Mailboxes {
			if mm.MailboxID == inbox.ID {
				return mm.Flags&store.MessageFlagSeen != 0
			}
		}
		t.Fatalf("%s has no INBOX membership: %+v", msgID, m.Mailboxes)
		return false
	}

	// New row.
	ingest("self-new@example.test", true)
	if !inboxSeen("self-new@example.test") {
		t.Errorf("self-sent message arrived unread in INBOX")
	}
	ingest("other-new@example.test", false)
	if inboxSeen("other-new@example.test") {
		t.Errorf("message from another sender arrived read in INBOX")
	}

	// Fold onto the sender's own Sent copy.
	sent, err := meta.GetMailboxByName(ctx, f.principal, "Sent")
	if err != nil {
		t.Fatalf("GetMailboxByName Sent: %v", err)
	}
	raw := "From: alice@example.test\r\nTo: alice@example.test\r\n" +
		"Message-ID: <self-fold@example.test>\r\nSubject: fold\r\n\r\nhi\r\n"
	ref, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := meta.InsertMessage(ctx, store.Message{
		PrincipalID: f.principal, Size: ref.Size, Blob: ref,
		InternalDate: f.ha.Clock.Now(), ReceivedAt: f.ha.Clock.Now(),
		Envelope: store.Envelope{Subject: "fold", From: "alice@example.test", To: "alice@example.test",
			MessageID: "self-fold@example.test"},
	}, []store.MessageMailbox{{MailboxID: sent.ID, Flags: store.MessageFlagSeen}}); err != nil {
		t.Fatalf("InsertMessage Sent copy: %v", err)
	}
	ingest("self-fold@example.test", true)
	if !inboxSeen("self-fold@example.test") {
		t.Errorf("self-sent message folded onto the Sent copy arrived unread in INBOX")
	}
}

// A client on the submission listener that sends to its own address
// delivers locally in the session; that copy arrives read too.
func TestDelivery_SubmissionToSelf_ArrivesSeen_SQLite(t *testing.T) {
	testDeliverySubmissionToSelfArrivesSeen(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_SubmissionToSelf_ArrivesSeen_Postgres(t *testing.T) {
	testDeliverySubmissionToSelfArrivesSeen(t, newPGStoreFactory)
}

func testDeliverySubmissionToSelfArrivesSeen(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS, store: storeFactory(t)})
	ctx := context.Background()
	raw, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, raw, 220)
	raw.send(t, "EHLO client.example.test")
	mustOK(t, raw, 250)
	raw.send(t, "STARTTLS")
	mustOK(t, raw, 220)
	tlsConn := tls.Client(raw.conn, f.tlsClient)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	cli := newSMTPClient(tlsConn)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "AUTH PLAIN "+base64.StdEncoding.EncodeToString([]byte("\x00alice@example.test\x00"+f.password)))
	mustOK(t, cli, 235)
	cli.send(t, "MAIL FROM:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte("From: alice@example.test\r\nTo: alice@example.test\r\n"+
		"Message-ID: <self-submit@example.test>\r\nSubject: note to self\r\n\r\nhi\r\n.\r\n"))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	m, err := f.ha.Store.Meta().GetMessageByMessageIDHeader(ctx, f.principal, "self-submit@example.test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if len(m.Mailboxes) == 0 {
		t.Fatalf("self-submitted message has no mailbox")
	}
	for _, mm := range m.Mailboxes {
		if mm.Flags&store.MessageFlagSeen == 0 {
			t.Errorf("mailbox %d copy of the self-submitted message is unread", mm.MailboxID)
		}
	}
}
