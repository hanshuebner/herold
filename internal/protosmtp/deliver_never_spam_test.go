package protosmtp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// testNeverSpamKeepsAllowedSenderOutOfJunk covers REQ-FILT-02a / REQ-FLT-16
// (issue #382): a "never classify as spam" managed rule matching the
// sender keeps a spam-verdict message in Inbox, with the transparency
// record naming the rule (REQ-FILT-66); a message from a different sender,
// not matched by the rule, still goes to Junk under the same spam verdict
// (REQ-FILT-02).
func testNeverSpamKeepsAllowedSenderOutOfJunk(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.92}`), nil
	})

	ctx0 := context.Background()
	// InsertManagedRule (not just the compiled Sieve script) is required
	// here: the transparency-record attribution
	// (sieve.NeverSpamOverrideLabel, called from deliverOne) reads the
	// principal's ManagedRule rows from the store directly, independent
	// of the compiled-and-executed Sieve script that decides routing.
	inserted, err := f.ha.Store.Meta().InsertManagedRule(ctx0, store.ManagedRule{
		PrincipalID: f.principal,
		Name:        "Trusted senders",
		Enabled:     true,
		Conditions: []store.RuleCondition{
			{Field: "from", Op: "contains", Value: "@accountprotection.microsoft.com"},
		},
		Actions: []store.RuleAction{{Kind: "never-spam"}},
	})
	if err != nil {
		t.Fatalf("InsertManagedRule: %v", err)
	}
	preamble, err := sieve.CompileRules([]store.ManagedRule{inserted})
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	effective := sieve.EffectiveScript(preamble, "")
	if err := f.ha.Store.Meta().SetSieveScript(ctx0, f.principal, effective); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

	// Matched sender: expect Inbox, not Junk, with the override recorded.
	deliverNeverSpamMessage(t, f, "notify@accountprotection.microsoft.com", "allowed-sender@test")
	// Unmatched sender: the classifier still says spam, and REQ-FILT-02's
	// default mapping still applies.
	deliverNeverSpamMessage(t, f, "someone@unrelated.example", "unmatched-sender@test")

	ctx := context.Background()
	inbox := mailboxByName(t, f, "INBOX")
	junk := mailboxByName(t, f, "Junk")

	inboxMsgs, err := f.ha.Store.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(INBOX): %v", err)
	}
	if len(inboxMsgs) != 1 {
		t.Fatalf("messages in INBOX = %d, want 1 (only the allow-listed sender)", len(inboxMsgs))
	}

	junkMsgs, err := f.ha.Store.Meta().ListMessages(ctx, junk.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(Junk): %v", err)
	}
	if len(junkMsgs) != 1 {
		t.Fatalf("messages in Junk = %d, want 1 (the unmatched sender)", len(junkMsgs))
	}

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, inboxMsgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification(inbox message): %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Fatalf("SpamVerdict = %v, want \"spam\"", rec.SpamVerdict)
	}
	if rec.SpamDeliveryOverride == nil {
		t.Fatal("SpamDeliveryOverride is nil, want \"filter:Trusted senders\"")
	}
	if !strings.Contains(*rec.SpamDeliveryOverride, "Trusted senders") {
		t.Errorf("SpamDeliveryOverride = %q, want it to name the rule", *rec.SpamDeliveryOverride)
	}

	junkRec, err := f.ha.Store.Meta().GetLLMClassification(ctx, junkMsgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification(junk message): %v", err)
	}
	if junkRec.SpamDeliveryOverride != nil {
		t.Errorf("SpamDeliveryOverride = %q for the unmatched sender, want nil", *junkRec.SpamDeliveryOverride)
	}
}

// deliverNeverSpamMessage sends one matching SMTP DATA transaction with a
// unique Message-ID (needed for persistLLMRecord's lookup) from the given
// sender.
func deliverNeverSpamMessage(t *testing.T, f *fixture, from, msgIDLocal string) {
	t.Helper()
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<"+from+">")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: " + from + "\r\nTo: alice@example.test\r\n" +
		"Message-ID: <" + msgIDLocal + "@sender.test>\r\n" +
		"Subject: never-spam test\r\n\r\nBody text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

func TestNeverSpam_AllowedSenderStaysInInbox_SQLite(t *testing.T) {
	testNeverSpamKeepsAllowedSenderOutOfJunk(t, func(*testing.T) store.Store { return nil })
}

func TestNeverSpam_AllowedSenderStaysInInbox_Postgres(t *testing.T) {
	testNeverSpamKeepsAllowedSenderOutOfJunk(t, pgStoreFactory)
}
