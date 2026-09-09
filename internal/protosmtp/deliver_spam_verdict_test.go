package protosmtp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

// TestDelivery_SpamVerdict_DefaultMapping exercises REQ-FILT-02's default
// verdict-to-mailbox mapping when the recipient has no Sieve script:
// ham -> INBOX / DeliveryDispositionInbox, suspect -> INBOX with the
// "$Junk" keyword, spam -> the Junk mailbox / DeliveryDispositionJunk
// (re #297).
func TestDelivery_SpamVerdict_DefaultMapping(t *testing.T) {
	cases := []struct {
		name           string
		verdictJSON    string
		wantMailbox    string
		wantDisp       store.MessageDeliveryDisposition
		wantKeyword    string
		unwantKeywords []string
	}{
		{
			name:           "ham",
			verdictJSON:    `{"verdict":"ham","score":0.05}`,
			wantMailbox:    "INBOX",
			wantDisp:       store.DeliveryDispositionInbox,
			unwantKeywords: []string{"$Junk"},
		},
		{
			name:        "suspect",
			verdictJSON: `{"verdict":"suspect","score":0.5}`,
			wantMailbox: "INBOX",
			wantDisp:    store.DeliveryDispositionInbox,
			wantKeyword: "$Junk",
		},
		{
			name:        "spam",
			verdictJSON: `{"verdict":"spam","score":0.95}`,
			wantMailbox: "Junk",
			wantDisp:    store.DeliveryDispositionJunk,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
			f.spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(tc.verdictJSON), nil
			})

			cli, closeFn := f.dial(t)
			defer closeFn()
			mustOK(t, cli, 220)
			cli.send(t, "EHLO client.example.test")
			mustOK(t, cli, 250)
			cli.send(t, "MAIL FROM:<sender@sender.test>")
			mustOK(t, cli, 250)
			cli.send(t, "RCPT TO:<alice@example.test>")
			mustOK(t, cli, 250)
			cli.send(t, "DATA")
			mustOK(t, cli, 354)
			msgID := "verdict-" + tc.name + "@sender.test"
			body := "From: sender@sender.test\r\nTo: alice@example.test\r\n" +
				"Message-ID: <" + msgID + ">\r\n" +
				"Subject: verdict test " + tc.name + "\r\n\r\nBody text.\r\n.\r\n"
			cli.sendRaw(t, []byte(body))
			mustOK(t, cli, 250)
			cli.send(t, "QUIT")
			mustOK(t, cli, 221)

			ctx := context.Background()
			mb, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, tc.wantMailbox)
			if err != nil {
				t.Fatalf("GetMailboxByName(%q): %v", tc.wantMailbox, err)
			}
			msgs, err := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}
			if len(msgs) != 1 {
				t.Fatalf("messages in %s = %d, want 1", tc.wantMailbox, len(msgs))
			}
			msg := msgs[0]

			// DeliveryDisposition is recorded at ingest but only surfaced
			// via the admin message-research read path (re #143); assert
			// it there, keyed on the unique Message-ID.
			hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{MessageID: msgID, Limit: 10})
			if err != nil {
				t.Fatalf("SearchAdminMessages: %v", err)
			}
			if len(hits) != 1 {
				t.Fatalf("SearchAdminMessages(%q) hits = %d, want 1", msgID, len(hits))
			}
			if hits[0].Disposition != tc.wantDisp {
				t.Errorf("Disposition = %v, want %v", hits[0].Disposition, tc.wantDisp)
			}

			have := strings.Join(msg.Keywords, ",")
			if tc.wantKeyword != "" && !strings.Contains(have, tc.wantKeyword) {
				t.Errorf("keywords = %v, want %q present", msg.Keywords, tc.wantKeyword)
			}
			for _, unwanted := range tc.unwantKeywords {
				if strings.Contains(have, unwanted) {
					t.Errorf("keywords = %v, want %q absent", msg.Keywords, unwanted)
				}
			}
		})
	}
}

// TestDelivery_SpamVerdict_ExplicitSieveFileIntoWins verifies that an
// explicit Sieve fileinto action overrides the default spam-verdict
// mapping (REQ-FILT-02, REQ-FILT-102): a message the classifier scores
// as spam still lands in the user-chosen mailbox, not Junk, when the
// user's script explicitly files it elsewhere.
func TestDelivery_SpamVerdict_ExplicitSieveFileIntoWins(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	f.spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.99}`), nil
	})

	ctx := context.Background()
	if err := f.ha.Store.Meta().SetSieveScript(
		ctx, f.principal,
		`require "fileinto";
fileinto "Archive";`,
	); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

	cli, closeFn := f.dial(t)
	defer closeFn()
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
		"Subject: explicit fileinto wins\r\n\r\nBody text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	archive, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "Archive")
	if err != nil {
		t.Fatalf("GetMailboxByName(Archive): %v", err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, archive.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(Archive): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in Archive = %d, want 1", len(msgs))
	}

	junk, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "Junk")
	if err != nil {
		t.Fatalf("GetMailboxByName(Junk): %v", err)
	}
	junkMsgs, err := f.ha.Store.Meta().ListMessages(ctx, junk.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(Junk): %v", err)
	}
	if len(junkMsgs) != 0 {
		t.Fatalf("messages in Junk = %d, want 0 (explicit fileinto must win over the default spam mapping)", len(junkMsgs))
	}
}
