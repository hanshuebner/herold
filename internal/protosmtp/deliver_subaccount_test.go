package protosmtp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

// TestDelivery_SeparatedIdentityCanonicalEmail_RoutesToSubAccount is a
// regression guard for the REQ-SUBACCT-07 delivery path (issue #312):
// local SMTP delivery to a hosted address equal to a separated
// identity's email lands in the sub-account's INBOX and is absent from
// the parent's, and reverts to the parent's INBOX once the sub-account
// is removed with keep-mail.
//
// This is the canonical-email lookup carrying the delivery, not the
// alias row: store.SeparateIdentity gives the sub-principal its own
// canonical email equal to identity.Email, and
// directory.ResolveAddress checks GetPrincipalByEmail (canonical)
// before it ever consults an alias row, so the routing this test
// observes over the wire would hold even for an identity with no alias
// row at all. The alias row this test also sets up is retargeted by
// store.SeparateIdentity/RemoveSubAccount as a consistency fix for
// ResolveAlias, the admin alias views and the audit trail (verified
// directly in internal/store/storetest); this test additionally reads
// ResolveAlias after separation so it observes that side effect too,
// but that read is not what makes the SMTP delivery below land where
// it does.
func TestDelivery_SeparatedIdentityCanonicalEmail_RoutesToSubAccount(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	ctx := context.Background()

	// alice already owns "alice@example.test" (the fixture principal).
	// "board@example.test" is a second address she receives at, routed
	// today by a plain alias row -- the shape SeparateIdentity is
	// documented to retarget.
	if _, err := f.ha.Store.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "board",
		Domain:          "example.test",
		TargetPrincipal: f.principal,
	}); err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if err := f.ha.Store.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "board-identity",
		PrincipalID: f.principal,
		Email:       "board@example.test",
		Name:        "Board",
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	mig, err := store.SeparateIdentity(ctx, f.ha.Store, f.principal, "board-identity")
	if err != nil {
		t.Fatalf("SeparateIdentity: %v", err)
	}
	subID := mig.SubPrincipalID

	// The alias row itself also follows the identity (re #312): checked
	// here as a side observation, not as what routes the delivery below.
	if got, err := f.ha.Store.Meta().ResolveAlias(ctx, "board", "example.test"); err != nil || got != subID {
		t.Fatalf("ResolveAlias after separate = (%d, %v), want (%d, nil)", got, err, subID)
	}

	deliver := func(subject, body string) {
		t.Helper()
		cli, closeFn := f.dial(t)
		defer closeFn()
		mustOK(t, cli, 220)
		cli.send(t, "EHLO client.example.test")
		mustOK(t, cli, 250)
		cli.send(t, "MAIL FROM:<member@sender.test>")
		mustOK(t, cli, 250)
		cli.send(t, "RCPT TO:<board@example.test>")
		mustOK(t, cli, 250)
		cli.send(t, "DATA")
		mustOK(t, cli, 354)
		msg := "From: member@sender.test\r\nTo: board@example.test\r\n" +
			"Subject: " + subject + "\r\n\r\n" + body + "\r\n.\r\n"
		cli.sendRaw(t, []byte(msg))
		mustOK(t, cli, 250)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	}

	// Post-separation: the message lands in the sub-account's INBOX,
	// not the parent's.
	deliver("agenda", "Meeting agenda attached.")
	assertMessageInMailbox(t, f, subID, "INBOX", "agenda", "Meeting agenda attached.")
	assertMailboxMessageCount(t, f, f.principal, "INBOX", 0)

	// Remove the sub-account with keep-mail: the alias retargets back
	// to the parent, and a fresh delivery lands there.
	if err := store.RemoveSubAccount(ctx, f.ha.Store, subID, false); err != nil {
		t.Fatalf("RemoveSubAccount(keep): %v", err)
	}

	deliver("minutes", "Meeting minutes attached.")
	assertMessageInMailbox(t, f, f.principal, "INBOX", "minutes", "Meeting minutes attached.")
}

// assertMailboxMessageCount fails the test unless pid's mailbox named
// mbName holds exactly want messages.
func assertMailboxMessageCount(t *testing.T, f *fixture, pid store.PrincipalID, mbName string, want int) {
	t.Helper()
	ctx := context.Background()
	mbs, err := f.ha.Store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		t.Fatalf("list mailboxes: %v", err)
	}
	for _, mb := range mbs {
		if !strings.EqualFold(mb.Name, mbName) {
			continue
		}
		msgs, err := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(msgs) != want {
			t.Fatalf("mailbox %q for principal %d has %d messages, want %d", mbName, pid, len(msgs), want)
		}
		return
	}
	if want != 0 {
		t.Fatalf("mailbox %q not found for principal %d", mbName, pid)
	}
}
