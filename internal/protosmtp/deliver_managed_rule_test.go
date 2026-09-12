package protosmtp_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// pgStoreFactory opens a fresh Postgres-backed store on HEROLD_PG_DSN,
// truncated to an empty state, mirroring the pattern in
// spam_classification_outcome_test.go (re #326). A fresh pool per subtest
// is required because testharness.Server.Close unconditionally closes
// Options.Store on t.Cleanup.
func pgStoreFactory(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			_ = st.Close()
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	return st
}

// installManagedRule compiles the given managed-rule actions exactly as
// the JMAP ManagedRule handlers do (internal/protojmap/mail/managedrule.
// recompileAndPersist: sieve.CompileRules -> sieve.EffectiveScript ->
// SetSieveScript) and persists the result as the principal's active
// Sieve script.
func installManagedRule(t *testing.T, f *fixture, actions []store.RuleAction) {
	t.Helper()
	rule := store.ManagedRule{
		PrincipalID: f.principal,
		Enabled:     true,
		Conditions: []store.RuleCondition{
			{Field: "subject", Op: "contains", Value: "widget"},
		},
		Actions: actions,
	}
	preamble, err := sieve.CompileRules([]store.ManagedRule{rule})
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	effective := sieve.EffectiveScript(preamble, "")
	if err := f.ha.Store.Meta().SetSieveScript(context.Background(), f.principal, effective); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}
}

// deliverManagedRuleMessage drives one matching SMTP DATA transaction
// against the fixture's listener.
func deliverManagedRuleMessage(t *testing.T, f *fixture) {
	t.Helper()
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
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: new widget\r\n\r\nBuy a widget.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
}

// mailboxByName resolves a principal's mailbox row by name (case-
// insensitive), fatal if absent.
func mailboxByName(t *testing.T, f *fixture, name string) store.Mailbox {
	t.Helper()
	ctx := context.Background()
	mbs, err := f.ha.Store.Meta().ListMailboxes(ctx, f.principal)
	if err != nil {
		t.Fatalf("list mailboxes: %v", err)
	}
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, name) {
			return mb
		}
	}
	t.Fatalf("mailbox %q not found (have %v)", name, mbNames(mbs))
	return store.Mailbox{}
}

// scanAllMessages walks message IDs 1..maxScan and returns every message
// row for the fixture's principal. GetMessage returns the full membership
// set (msg.Mailboxes) for each row, letting the caller distinguish "one
// message, several mailbox memberships" from "several message rows, one
// membership each".
func scanAllMessages(t *testing.T, f *fixture) []store.Message {
	t.Helper()
	ctx := context.Background()
	const maxScan = 200
	var out []store.Message
	for mid := store.MessageID(1); mid < maxScan; mid++ {
		m, err := f.ha.Store.Meta().GetMessage(ctx, mid)
		if err != nil {
			continue
		}
		if m.PrincipalID != f.principal {
			continue
		}
		out = append(out, m)
	}
	return out
}

// testManagedRuleLabelAndSkipInbox is the reproduction + regression check
// for re #363: a managed rule with `apply-label <label>` + `skip-inbox`
// compiles to Sieve fileinto action(s) (internal/sieve/compile_managed.go).
// Per docs/design/web/requirements/03-labels.md ("a message is never stored
// twice") and 04-filters.md, "label it and keep it out of the inbox" is
// ONE message whose only membership is the label mailbox.
func testManagedRuleLabelAndSkipInbox(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	installManagedRule(t, f, []store.RuleAction{
		{Kind: "apply-label", Params: map[string]any{"label": "Widgets"}},
		{Kind: "skip-inbox"},
	})

	deliverManagedRuleMessage(t, f)

	all := scanAllMessages(t, f)
	if len(all) != 1 {
		ids := make([]int64, len(all))
		for i, m := range all {
			ids[i] = int64(m.ID)
		}
		t.Fatalf("message rows for principal = %d, want 1 (label + skip-inbox must not fan out); ids=%v", len(all), ids)
	}
	msg := all[0]
	widgets := mailboxByName(t, f, "Widgets")
	archive := mailboxByName(t, f, "Archive")
	inbox := mailboxByName(t, f, "INBOX")

	var inWidgets, inArchive, inInbox bool
	for _, mm := range msg.Mailboxes {
		switch mm.MailboxID {
		case widgets.ID:
			inWidgets = true
		case archive.ID:
			inArchive = true
		case inbox.ID:
			inInbox = true
		}
	}
	if !inWidgets {
		t.Fatalf("message is not a member of the Widgets label mailbox: %+v", msg.Mailboxes)
	}
	if inArchive {
		t.Fatalf("skip-inbox rule must not file into Archive, only the label: %+v", msg.Mailboxes)
	}
	if inInbox {
		t.Fatalf("skip-inbox must keep the message out of INBOX: %+v", msg.Mailboxes)
	}
	if len(msg.Mailboxes) != 1 {
		t.Fatalf("message membership = %v, want exactly [Widgets]", msg.Mailboxes)
	}
}

func TestManagedRule_LabelAndSkipInbox_OneMessageOneMembership_SQLite(t *testing.T) {
	testManagedRuleLabelAndSkipInbox(t, func(*testing.T) store.Store { return nil })
}

func TestManagedRule_LabelAndSkipInbox_OneMessageOneMembership_Postgres(t *testing.T) {
	testManagedRuleLabelAndSkipInbox(t, pgStoreFactory)
}

// testManagedRuleLabelOnly covers the "label alone" half of re #363:
// without skip-inbox, the message is one row that is a member of both the
// label mailbox and INBOX -- not two separate messages.
func testManagedRuleLabelOnly(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	installManagedRule(t, f, []store.RuleAction{
		{Kind: "apply-label", Params: map[string]any{"label": "Widgets"}},
	})

	deliverManagedRuleMessage(t, f)

	all := scanAllMessages(t, f)
	if len(all) != 1 {
		ids := make([]int64, len(all))
		for i, m := range all {
			ids[i] = int64(m.ID)
		}
		t.Fatalf("message rows for principal = %d, want 1 (label-only must not fan out); ids=%v", len(all), ids)
	}
	msg := all[0]
	widgets := mailboxByName(t, f, "Widgets")
	inbox := mailboxByName(t, f, "INBOX")

	var inWidgets, inInbox bool
	for _, mm := range msg.Mailboxes {
		if mm.MailboxID == widgets.ID {
			inWidgets = true
		}
		if mm.MailboxID == inbox.ID {
			inInbox = true
		}
	}
	if !inWidgets {
		t.Fatalf("message is not a member of the Widgets label mailbox: %+v", msg.Mailboxes)
	}
	if !inInbox {
		t.Fatalf("label-only rule must keep the message in INBOX too: %+v", msg.Mailboxes)
	}
	if len(msg.Mailboxes) != 2 {
		t.Fatalf("message membership = %v, want exactly [Widgets, INBOX]", msg.Mailboxes)
	}
}

func TestManagedRule_LabelOnly_OneMessageInLabelAndInbox_SQLite(t *testing.T) {
	testManagedRuleLabelOnly(t, func(*testing.T) store.Store { return nil })
}

func TestManagedRule_LabelOnly_OneMessageInLabelAndInbox_Postgres(t *testing.T) {
	testManagedRuleLabelOnly(t, pgStoreFactory)
}
