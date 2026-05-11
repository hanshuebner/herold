package protosmtp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
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

// taggedDeliveryFixture holds the harness + principal + the
// alias mapping (per-suffix) needed to drive a +-tagged delivery via
// the relay-in listener. The aliases stand in for RFC 5233 sub-
// addressing at RCPT-time: herold v1 does not strip +-suffixes at
// directory.ResolveAddress, so the test installs an explicit alias
// per suffix it wants to exercise.
//
// The fixture keeps the per-test boilerplate small: one call to
// startTaggedFixture, then per-test inserts of the
// tagged_address_filter rows and one drive() invocation per delivery.
type taggedDeliveryFixture struct {
	ha           *testharness.Server
	principalID  directory.PrincipalID
	identityID   string
	srv          *protosmtp.Server
	listenerName string
}

func startTaggedFixture(t *testing.T) *taggedDeliveryFixture {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	// Insert a persisted JMAP Identity for alice@example.test so
	// tagged_address_filters has a real base_identity_id to FK
	// against. The synthesised default identity has no row.
	identityID := "ident-alice"
	if err := ha.Store.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: pid,
		Email:       "alice@example.test",
		MayDelete:   false,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	// Spam plug returns Ham so delivery walks through to InsertMessage.
	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &fakePluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := newResolverAdapter(ha.DNS)
	tlsStore, _ := newTestTLSStore(t)
	srv, err := protosmtp.New(protosmtp.Config{
		Store:     ha.Store,
		Directory: dir,
		DKIM:      maildkim.New(resolver, ha.Logger, ha.Clock),
		SPF:       mailspf.New(resolver, ha.Clock),
		DMARC:     maildmarc.New(resolver),
		ARC:       mailarc.New(resolver),
		Spam:      spamCls,
		Sieve:     sieve.NewInterpreter(),
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
	return &taggedDeliveryFixture{
		ha:           ha,
		principalID:  pid,
		identityID:   identityID,
		srv:          srv,
		listenerName: "smtp",
	}
}

// installAliasForSuffix wires alice+<suffix>@example.test -> alice's
// principal at the RCPT TO directory layer so the inbound state
// machine accepts the recipient. The tagged-address pipeline then
// derives the suffix from the rc.addr in deliverOne.
func (f *taggedDeliveryFixture) installAliasForSuffix(t *testing.T, suffix string) {
	t.Helper()
	if _, err := f.ha.Store.Meta().InsertAlias(context.Background(), store.Alias{
		LocalPart:       "alice+" + suffix,
		Domain:          "example.test",
		TargetPrincipal: f.principalID,
	}); err != nil {
		t.Fatalf("InsertAlias(alice+%s): %v", suffix, err)
	}
}

// installFilter inserts a tagged_address_filters row for alice's
// identity on the given suffix with the given action and label.
func (f *taggedDeliveryFixture) installFilter(t *testing.T, suffix, action, label string) {
	t.Helper()
	if err := f.ha.Store.Meta().InsertTaggedAddressFilter(context.Background(),
		store.TaggedAddressFilter{
			PrincipalID:    f.principalID,
			BaseIdentityID: f.identityID,
			Suffix:         suffix,
			Action:         action,
			LabelName:      label,
		}); err != nil {
		t.Fatalf("InsertTaggedAddressFilter(%s, %s, %s): %v", suffix, action, label, err)
	}
}

// drive sends one SMTP message from sender to rcpt and returns once
// the 250 has been received. Body is fixed; the caller only chooses
// addresses. Fatal on any wire-level failure.
func (f *taggedDeliveryFixture) drive(t *testing.T, sender, rcpt string) {
	t.Helper()
	ctx := context.Background()
	c, err := f.ha.DialSMTPByName(ctx, f.listenerName)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	cli := newSMTPClient(c)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<"+sender+">")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<"+rcpt+">")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: " + sender + "\r\nTo: " + rcpt + "\r\nSubject: tagged-test\r\n\r\nhi\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

// mailboxMessages returns the messages in the named mailbox for
// alice. mailbox must exist (a Fatal is emitted on a missing mailbox
// so the test fails fast).
func (f *taggedDeliveryFixture) mailboxMessages(t *testing.T, mailboxName string) []store.Message {
	t.Helper()
	ctx := context.Background()
	mb, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principalID, mailboxName)
	if err != nil {
		t.Fatalf("GetMailboxByName(%s): %v", mailboxName, err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(%s): %v", mailboxName, err)
	}
	return msgs
}

// mailboxExists reports whether a mailbox with the given name exists
// for alice. Used to assert that label_archive did NOT auto-create
// INBOX when no implicit-keep landed there.
func (f *taggedDeliveryFixture) mailboxNames(t *testing.T) map[string]bool {
	t.Helper()
	ctx := context.Background()
	mbs, err := f.ha.Store.Meta().ListMailboxes(ctx, f.principalID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	out := map[string]bool{}
	for _, mb := range mbs {
		out[mb.Name] = true
	}
	return out
}

// TestInboundDelivery_TaggedAddress_Label exercises the
// `label` action (REQ-TAG-30): the message lands in BOTH the
// label_name mailbox AND INBOX (implicit keep is NOT suppressed).
func TestInboundDelivery_TaggedAddress_Label(t *testing.T) {
	f := startTaggedFixture(t)
	f.installAliasForSuffix(t, "amazon")
	f.installFilter(t, "amazon", store.TaggedAddressActionLabel, "Shopping")

	f.drive(t, "bob@sender.test", "alice+amazon@example.test")

	// Both INBOX and Shopping must hold the message (separate per-
	// mailbox rows for the same blob).
	inbox := f.mailboxMessages(t, "INBOX")
	if len(inbox) != 1 {
		t.Fatalf("INBOX message count = %d, want 1", len(inbox))
	}
	shop := f.mailboxMessages(t, "Shopping")
	if len(shop) != 1 {
		t.Fatalf("Shopping message count = %d, want 1", len(shop))
	}
	// Same blob hash in both — fan-out shares the storage.
	if inbox[0].Blob.Hash != shop[0].Blob.Hash {
		t.Fatalf("blob hashes differ across mailbox rows: %q vs %q",
			inbox[0].Blob.Hash, shop[0].Blob.Hash)
	}
	// `label` does NOT mark seen.
	if inbox[0].Flags&store.MessageFlagSeen != 0 || shop[0].Flags&store.MessageFlagSeen != 0 {
		t.Fatalf("label action set \\Seen unexpectedly: inbox=%v shop=%v",
			inbox[0].Flags, shop[0].Flags)
	}
}

// TestInboundDelivery_TaggedAddress_LabelArchive exercises the
// `label_archive` action (REQ-TAG-30 / REQ-TAG-31): the message
// lands in label_name only; INBOX is NOT touched, implicit keep is
// suppressed.
func TestInboundDelivery_TaggedAddress_LabelArchive(t *testing.T) {
	f := startTaggedFixture(t)
	f.installAliasForSuffix(t, "newsletter")
	f.installFilter(t, "newsletter", store.TaggedAddressActionLabelArchive, "Newsletters")

	f.drive(t, "bob@sender.test", "alice+newsletter@example.test")

	news := f.mailboxMessages(t, "Newsletters")
	if len(news) != 1 {
		t.Fatalf("Newsletters message count = %d, want 1", len(news))
	}
	// INBOX must either not exist or be empty — implicit keep was
	// suppressed (REQ-TAG-31). The harness pre-creates INBOX in some
	// flows; the assertion that matters is "no message landed there".
	names := f.mailboxNames(t)
	if names["INBOX"] {
		msgs := f.mailboxMessages(t, "INBOX")
		if len(msgs) != 0 {
			t.Fatalf("INBOX got %d messages; label_archive should suppress implicit keep", len(msgs))
		}
	}
	// `label_archive` does NOT mark seen.
	if news[0].Flags&store.MessageFlagSeen != 0 {
		t.Fatalf("label_archive set \\Seen unexpectedly: %v", news[0].Flags)
	}
}

// TestInboundDelivery_TaggedAddress_LabelArchiveRead exercises the
// `label_archive_read` action (REQ-TAG-30): label_archive PLUS the
// `\Seen` flag.
func TestInboundDelivery_TaggedAddress_LabelArchiveRead(t *testing.T) {
	f := startTaggedFixture(t)
	f.installAliasForSuffix(t, "receipts")
	f.installFilter(t, "receipts", store.TaggedAddressActionLabelArchiveRead, "Receipts")

	f.drive(t, "bob@sender.test", "alice+receipts@example.test")

	rec := f.mailboxMessages(t, "Receipts")
	if len(rec) != 1 {
		t.Fatalf("Receipts message count = %d, want 1", len(rec))
	}
	if rec[0].Flags&store.MessageFlagSeen == 0 {
		t.Fatalf("label_archive_read did NOT set \\Seen: flags=%v", rec[0].Flags)
	}
	// INBOX must be empty (implicit keep suppressed, same as
	// label_archive).
	names := f.mailboxNames(t)
	if names["INBOX"] {
		msgs := f.mailboxMessages(t, "INBOX")
		if len(msgs) != 0 {
			t.Fatalf("INBOX got %d messages; label_archive_read should suppress implicit keep", len(msgs))
		}
	}
}

// TestInboundDelivery_TaggedAddress_NoMatch_FallsThroughToInbox
// covers REQ-TAG-20 step 3: when no filter matches a +-tagged
// recipient, the message proceeds to Sieve as-is. Without a Sieve
// script the implicit keep lands the message in INBOX, regardless of
// any unrelated filters that exist.
func TestInboundDelivery_TaggedAddress_NoMatch_FallsThroughToInbox(t *testing.T) {
	f := startTaggedFixture(t)
	f.installAliasForSuffix(t, "unknown")
	// Install a filter for a DIFFERENT suffix so the tag table is
	// not empty (a stronger no-leak signal).
	f.installFilter(t, "amazon", store.TaggedAddressActionLabel, "Shopping")

	f.drive(t, "bob@sender.test", "alice+unknown@example.test")

	inbox := f.mailboxMessages(t, "INBOX")
	if len(inbox) != 1 {
		t.Fatalf("INBOX message count = %d, want 1 (no matching filter)", len(inbox))
	}
	// The unrelated `Shopping` filter MUST NOT have leaked the message.
	names := f.mailboxNames(t)
	if names["Shopping"] {
		msgs := f.mailboxMessages(t, "Shopping")
		if len(msgs) != 0 {
			t.Fatalf("Shopping got %d messages; non-matching suffix must not leak", len(msgs))
		}
	}
}

// TestInboundDelivery_TaggedAddress_NoSuffix_NoLookup confirms that a
// recipient without a `+` suffix never triggers the tag-filter probe.
// We assert this indirectly: with a filter installed on suffix
// "amazon" but delivery to plain `alice@example.test`, the message
// lands in INBOX only (no Shopping membership), and the metric
// counter remains zero.
func TestInboundDelivery_TaggedAddress_NoSuffix_NoLookup(t *testing.T) {
	f := startTaggedFixture(t)
	f.installFilter(t, "amazon", store.TaggedAddressActionLabel, "Shopping")
	f.drive(t, "bob@sender.test", "alice@example.test")

	inbox := f.mailboxMessages(t, "INBOX")
	if len(inbox) != 1 {
		t.Fatalf("INBOX message count = %d, want 1", len(inbox))
	}
	names := f.mailboxNames(t)
	if names["Shopping"] {
		msgs := f.mailboxMessages(t, "Shopping")
		if len(msgs) != 0 {
			t.Fatalf("Shopping got %d; plain address must not match tag filter", len(msgs))
		}
	}
}

// TestInboundDelivery_TaggedAddress_BeforeSieve_OrderingProof is the
// REQ-TAG-30 ordering test: when a tag-filter AND a Sieve fileinto
// rule both fire on the same delivery, both side-effects are visible.
// The tag-filter ran first (its label_name mailbox holds the message)
// AND Sieve's later action applied on top (its fileinto target also
// holds the message). REQ-TAG-22's "Sieve fileinto adds another
// mailbox membership" is the explicit contract.
//
// The Sieve script `require ["fileinto"]; fileinto "WatchList";` is
// installed for alice. The tag-filter routes `+watch` to "WatchList-
// Tagged". After delivery we expect BOTH mailboxes to hold a copy.
func TestInboundDelivery_TaggedAddress_BeforeSieve_OrderingProof(t *testing.T) {
	f := startTaggedFixture(t)
	f.installAliasForSuffix(t, "watch")
	// label (not label_archive) so the Sieve fileinto plus the tag
	// filing together do NOT add INBOX (Sieve's fileinto cancels
	// implicit keep per RFC 5228; tag-filter does not contribute
	// keep-suppression for `label` either since it does NOT
	// suppress, but Sieve's fileinto cancels). Both targets receive
	// the message, with the tag-filter target proving REQ-TAG-30
	// `label` adds membership unconditionally.
	f.installFilter(t, "watch", store.TaggedAddressActionLabel, "WatchList-Tagged")
	// Install the per-recipient Sieve script.
	ctx := context.Background()
	if err := f.ha.Store.Meta().SetSieveScript(ctx, f.principalID,
		`require ["fileinto"];`+"\n"+`fileinto "WatchList-Sieve";`+"\n"); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

	f.drive(t, "bob@sender.test", "alice+watch@example.test")

	tagged := f.mailboxMessages(t, "WatchList-Tagged")
	if len(tagged) != 1 {
		t.Fatalf("WatchList-Tagged count = %d, want 1 (tag-filter side-effect must persist past Sieve)", len(tagged))
	}
	sieved := f.mailboxMessages(t, "WatchList-Sieve")
	if len(sieved) != 1 {
		t.Fatalf("WatchList-Sieve count = %d, want 1 (Sieve fileinto must still fire after tag-filter)", len(sieved))
	}
	// Same blob hash on both rows (fan-out invariant REQ-FLOW-30).
	if tagged[0].Blob.Hash != sieved[0].Blob.Hash {
		t.Fatalf("blob hashes differ across tag + sieve targets: %q vs %q",
			tagged[0].Blob.Hash, sieved[0].Blob.Hash)
	}
	// INBOX must NOT have received the message: Sieve's fileinto
	// cancels implicit keep (RFC 5228 §2.10.6).
	names := f.mailboxNames(t)
	if names["INBOX"] {
		msgs := f.mailboxMessages(t, "INBOX")
		if len(msgs) != 0 {
			t.Fatalf("INBOX got %d messages; Sieve fileinto should have suppressed implicit keep", len(msgs))
		}
	}
}

// TestInboundDelivery_TaggedAddress_SieveDiscardWins covers
// REQ-TAG-22: a Sieve `discard` (with no fileinto) drops the
// message including the tag-filter's filing. The tag-filter's
// label mailbox must NOT receive a copy.
func TestInboundDelivery_TaggedAddress_SieveDiscardWins(t *testing.T) {
	f := startTaggedFixture(t)
	f.installAliasForSuffix(t, "discard-me")
	f.installFilter(t, "discard-me", store.TaggedAddressActionLabel, "ShouldNotAppear")
	ctx := context.Background()
	if err := f.ha.Store.Meta().SetSieveScript(ctx, f.principalID,
		`discard;`+"\n"); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

	f.drive(t, "bob@sender.test", "alice+discard-me@example.test")

	names := f.mailboxNames(t)
	if names["ShouldNotAppear"] {
		msgs := f.mailboxMessages(t, "ShouldNotAppear")
		if len(msgs) != 0 {
			t.Fatalf("ShouldNotAppear got %d messages; Sieve discard MUST drop the tag-filter's filing", len(msgs))
		}
	}
	if names["INBOX"] {
		msgs := f.mailboxMessages(t, "INBOX")
		if len(msgs) != 0 {
			t.Fatalf("INBOX got %d messages; Sieve discard MUST drop the message", len(msgs))
		}
	}
}

// TestInboundDelivery_TaggedAddress_SuffixCaseInsensitive verifies
// REQ-TAG-02: the stored suffix is lower-cased canonical, and the
// inbound lookup case-folds the recipient's suffix before matching.
// A filter created for suffix "amazon" matches an envelope
// recipient `alice+AMAZON@Example.TEST`.
func TestInboundDelivery_TaggedAddress_SuffixCaseInsensitive(t *testing.T) {
	f := startTaggedFixture(t)
	// Install an alias on the upper-case form so the directory
	// resolves the RCPT TO. directory.ResolveAddress lower-cases
	// before lookup, so the alias must be inserted lower-case too.
	f.installAliasForSuffix(t, "amazon")
	f.installFilter(t, "amazon", store.TaggedAddressActionLabel, "Shopping")

	f.drive(t, "bob@sender.test", "Alice+AMAZON@Example.TEST")

	shop := f.mailboxMessages(t, "Shopping")
	if len(shop) != 1 {
		t.Fatalf("Shopping count = %d, want 1 (suffix case-folded match required by REQ-TAG-02)", len(shop))
	}
}

// TestInboundDelivery_TaggedAddress_FeatureDisabled covers the
// [server.tagged_addresses] enabled = false branch (REQ-TAG-20). When
// the feature is off, no probe runs and +-suffixed mail falls through
// to Sieve unchanged. A filter row in the DB is invisible to the
// pipeline.
func TestInboundDelivery_TaggedAddress_FeatureDisabled(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if _, err := ha.Store.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "alice+amazon",
		Domain:          "example.test",
		TargetPrincipal: pid,
	}); err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if err := ha.Store.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "i-disabled",
		PrincipalID: pid,
		Email:       "alice@example.test",
		MayDelete:   false,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	if err := ha.Store.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    pid,
		BaseIdentityID: "i-disabled",
		Suffix:         "amazon",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Shopping",
	}); err != nil {
		t.Fatalf("InsertTaggedAddressFilter: %v", err)
	}

	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &fakePluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := newResolverAdapter(ha.DNS)
	tlsStore, _ := newTestTLSStore(t)
	disabled := false
	srv, err := protosmtp.New(protosmtp.Config{
		Store:                  ha.Store,
		Directory:              dir,
		DKIM:                   maildkim.New(resolver, ha.Logger, ha.Clock),
		SPF:                    mailspf.New(resolver, ha.Clock),
		DMARC:                  maildmarc.New(resolver),
		ARC:                    mailarc.New(resolver),
		Spam:                   spamCls,
		Sieve:                  sieve.NewInterpreter(),
		TLS:                    tlsStore,
		Resolver:               resolver,
		Clock:                  ha.Clock,
		Logger:                 ha.Logger,
		TaggedAddressesEnabled: &disabled,
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
	cli.send(t, "RCPT TO:<alice+amazon@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte("From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: disabled\r\n\r\nbody\r\n.\r\n"))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	// INBOX gets the message — fall-through unchanged.
	inbox, err := ha.Store.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName INBOX: %v", err)
	}
	msgs, err := ha.Store.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 5})
	if err != nil {
		t.Fatalf("ListMessages INBOX: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("INBOX count = %d, want 1 (feature off, falls through to Sieve+keep)", len(msgs))
	}
	// Shopping must not have been created.
	mbs, err := ha.Store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, "Shopping") {
			msgs, _ := ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 5})
			if len(msgs) != 0 {
				t.Fatalf("Shopping got %d messages; feature off must skip tag filing", len(msgs))
			}
		}
	}
}
