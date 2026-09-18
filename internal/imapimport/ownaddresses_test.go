package imapimport

// ownaddresses_test.go covers the per-account own-address learning
// mechanism (ownaddresses.go, re #396, third round):
//
//   - parseHeaderAddresses / extractDeliveredAddresses: header-parsing
//     unit tests (case normalisation, angle brackets, comments, multiple
//     headers, malformed values, the 64 KiB header-scan cap).
//   - runOwnAddressBackfill: an end-to-end pass over pre-existing
//     imported messages, following seenbackfill_test.go's precedent --
//     seeding message_state/blob rows directly to reproduce exactly the
//     row shape already-imported mail has, then running the pass and
//     asserting the persisted result. Covers the once-ever gate (a
//     second start does not rescan) and a context cancelled mid-pass
//     (the marker stays unset; a later run completes it).
//   - the incremental path via sync.go's ingestMessage: a freshly
//     ingested message's own Delivered-To/X-Original-To is learned
//     without a backfill pass running at all.
//   - the maxLearnedAddresses bound: many distinct addresses do not
//     grow the persisted set beyond the cap.
//
// The store-touching cases (backfill, incremental, cap) exercise
// store.Store through testharness.Start and so run on both SQLite (the
// suite default) and Postgres (HEROLD_TEST_STORE=postgres), like every
// other imapimport test.

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// --------------------------------------------------------------------------
// parseHeaderAddresses / extractDeliveredAddresses
// --------------------------------------------------------------------------

func TestParseHeaderAddresses(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   []string
	}{
		{
			name:   "bare address",
			values: []string{"info@example.com"},
			want:   []string{"info@example.com"},
		},
		{
			name:   "case normalisation",
			values: []string{"INFO@Example.COM"},
			want:   []string{"info@example.com"},
		},
		{
			name:   "angle brackets with display name",
			values: []string{"Info Corp <Info@Example.com>"},
			want:   []string{"info@example.com"},
		},
		{
			name:   "RFC 5322 comment",
			values: []string{"info@example.com (Info Corp)"},
			want:   []string{"info@example.com"},
		},
		{
			name:   "multiple headers, multiple values",
			values: []string{"info@example.com", "vorstand@example.com"},
			want:   []string{"info@example.com", "vorstand@example.com"},
		},
		{
			name:   "one header, comma-separated list",
			values: []string{"a@example.com, B@Example.com"},
			want:   []string{"a@example.com", "b@example.com"},
		},
		{
			name:   "surrounding whitespace trimmed",
			values: []string{"  spaced@example.com  "},
			want:   []string{"spaced@example.com"},
		},
		{
			name:   "malformed with no @ is dropped entirely",
			values: []string{"not-an-address"},
			want:   nil,
		},
		{
			name:   "malformed but contains @ falls back to the raw trimmed value",
			values: []string{"<info@example.com"},
			want:   []string{"<info@example.com"},
		},
		{
			name:   "empty value yields nothing",
			values: []string{""},
			want:   nil,
		},
		{
			name:   "nil input yields nothing",
			values: nil,
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseHeaderAddresses(tc.values)
			if len(got) != len(tc.want) {
				t.Fatalf("parseHeaderAddresses(%v) = %v; want %v", tc.values, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseHeaderAddresses(%v)[%d] = %q; want %q", tc.values, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// buildRawMessage returns a minimal RFC 5322 message with the given
// extra header lines (each already in "Name: Value" form, CRLF added by
// this helper) inserted after Subject/From/To, followed by the blank
// line and a short body.
func buildRawMessage(subject string, extraHeaders []string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("From: sender@example.test\r\n")
	b.WriteString("To: recipient@example.test\r\n")
	for _, h := range extraHeaders {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString("body\r\n")
	return b.Bytes()
}

func TestExtractDeliveredAddresses(t *testing.T) {
	raw := buildRawMessage("multi-header", []string{
		"Delivered-To: Info@Example.com",
		"X-Original-To: vorstand@example.com",
		"Delivered-To: second@example.com (also delivered here)",
	})
	msg, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	got := extractDeliveredAddresses(msg)
	want := map[string]bool{
		"info@example.com":     true,
		"vorstand@example.com": true,
		"second@example.com":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("extractDeliveredAddresses = %v; want addresses %v", got, want)
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("unexpected address %q in %v", a, got)
		}
	}
}

func TestExtractDeliveredAddresses_NoHeaders(t *testing.T) {
	raw := buildRawMessage("plain", nil)
	msg, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	if got := extractDeliveredAddresses(msg); len(got) != 0 {
		t.Errorf("extractDeliveredAddresses on a message with no Delivered-To/X-Original-To = %v; want empty", got)
	}
}

// --------------------------------------------------------------------------
// loadDeliveredAddressesFromBlob: the 64 KiB header-scan cap
// --------------------------------------------------------------------------

// newBareAccountWorker returns an accountWorker wired to a store, with no
// upstream connection -- enough for the blob/store-only methods this file
// tests (loadDeliveredAddressesFromBlob, learnOwnAddresses,
// runOwnAddressBackfill).
func newBareAccountWorker(t *testing.T, ha *testharness.Server, acc store.IMAPImportAccount) *accountWorker {
	t.Helper()
	return newAccountWorker(accountWorkerOpts{
		account: acc,
		store:   ha.Store,
		dataKey: testDataKey(t),
		log:     newTestLogger(t),
		clk:     ha.Clock,
	})
}

func TestLoadDeliveredAddressesFromBlob_HeaderScanCap(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{})
	ctx := context.Background()
	p, err := ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "cap@example.test",
		DisplayName:    "cap@example.test",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	acc, err := ha.Store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      "Cap",
		Host:             "imap.example.test",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "capuser",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     sealCred(t, "pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	w := newBareAccountWorker(t, ha, acc)

	// Within the cap: Delivered-To appears before a padding header, and
	// the header/body blank line still falls well inside
	// ownAddressHeaderScanBytes.
	within := buildRawMessage("within-cap", []string{
		"Delivered-To: info@classic-computing.de",
		"X-Pad: " + strings.Repeat("a", 40000),
	})
	blobWithin, err := ha.Store.Blobs().Put(ctx, bytes.NewReader(within))
	if err != nil {
		t.Fatalf("Blobs.Put (within): %v", err)
	}
	got, err := w.loadDeliveredAddressesFromBlob(ctx, blobWithin.Hash)
	if err != nil {
		t.Fatalf("loadDeliveredAddressesFromBlob (within cap): %v", err)
	}
	if len(got) != 1 || got[0] != "info@classic-computing.de" {
		t.Errorf("within-cap result = %v; want [info@classic-computing.de]", got)
	}

	// Beyond the cap: a large padding header pushes Delivered-To (and
	// the header/body separator) past ownAddressHeaderScanBytes. The
	// truncated read must not see that address.
	beyond := buildRawMessage("beyond-cap", []string{
		"X-Pad: " + strings.Repeat("a", ownAddressHeaderScanBytes+4000),
		"Delivered-To: vorstand@classic-computing.de",
	})
	if len(beyond) <= ownAddressHeaderScanBytes {
		t.Fatalf("test message (%d bytes) does not exceed the scan cap (%d bytes)", len(beyond), ownAddressHeaderScanBytes)
	}
	blobBeyond, err := ha.Store.Blobs().Put(ctx, bytes.NewReader(beyond))
	if err != nil {
		t.Fatalf("Blobs.Put (beyond): %v", err)
	}
	got2, _ := w.loadDeliveredAddressesFromBlob(ctx, blobBeyond.Hash)
	for _, a := range got2 {
		if a == "vorstand@classic-computing.de" {
			t.Fatalf("loadDeliveredAddressesFromBlob (beyond cap) found an address past the %d-byte scan bound: %v", ownAddressHeaderScanBytes, got2)
		}
	}
}

// --------------------------------------------------------------------------
// runOwnAddressBackfill: pre-existing imported messages, the once-ever
// gate, and cancellation mid-pass.
// --------------------------------------------------------------------------

// seedPreExistingMessage inserts a blob + message + imapimport_message_state
// row directly through the store, bypassing ingestMessage entirely, so the
// fixture reproduces the row shape already-imported mail has (the same
// technique seenbackfill_test.go uses for its pre-fix fixtures).
func seedPreExistingMessage(t *testing.T, ha *testharness.Server, acc store.IMAPImportAccount, mb store.Mailbox, upstreamUID uint32, msgID string, extraHeaders []string) store.MessageID {
	t.Helper()
	ctx := context.Background()
	raw := buildRawMessage("pre-existing "+msgID, append([]string{
		fmt.Sprintf("Message-ID: <%s>", msgID),
	}, extraHeaders...))
	blob, err := ha.Store.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = ha.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: acc.PrincipalID,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "pre-existing " + msgID, MessageID: msgID},
	}, []store.MessageMailbox{{MailboxID: mb.ID, Flags: 0}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, msgID)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if err := ha.Store.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID:       acc.ID,
		UpstreamFolder:  "INBOX",
		UpstreamUID:     upstreamUID,
		HeroldMessageID: msg.ID,
		HeroldMailboxID: mb.ID,
		MappedMailboxID: mb.ID,
	}); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState: %v", err)
	}
	return msg.ID
}

// setupBackfillFixture creates a principal, an INBOX mailbox, and an
// IMAP-import account with no configured own_addresses, ready for
// seedPreExistingMessage.
func setupBackfillFixture(t *testing.T, ha *testharness.Server, email, accountName string) (store.IMAPImportAccount, store.Mailbox) {
	t.Helper()
	ctx := context.Background()
	p, err := ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
		DisplayName:    email,
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	acc, err := ha.Store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      accountName,
		Host:             "imap.example.test",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "bfuser",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     sealCred(t, "pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	return acc, mb
}

// TestOwnAddressBackfillLearnsFromPreExistingMessages reproduces the shape
// comment 5010 reported: an account whose already-imported mail carries
// Delivered-To/X-Original-To headers for addresses the principal's
// identities never name. Running the backfill once must learn both and
// mark the set complete; a second start (the once-ever gate) must not
// rescan and must not lose what was already learned.
func TestOwnAddressBackfillLearnsFromPreExistingMessages(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{})
	acc, mb := setupBackfillFixture(t, ha, "bf1@example.test", "Backfill")

	seedPreExistingMessage(t, ha, acc, mb, 1, "bf-1@test", []string{
		"Delivered-To: info@classic-computing.de",
	})
	seedPreExistingMessage(t, ha, acc, mb, 2, "bf-2@test", []string{
		"X-Original-To: vorstand@classic-computing.de",
	})

	w := newBareAccountWorker(t, ha, acc)
	ctx := context.Background()
	w.runOwnAddressBackfill(ctx)

	got, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if got.AddressesLearnedAt == nil {
		t.Fatal("AddressesLearnedAt is nil after runOwnAddressBackfill; want set")
	}
	want := map[string]bool{"info@classic-computing.de": true, "vorstand@classic-computing.de": true}
	if len(got.LearnedAddresses) != len(want) {
		t.Fatalf("LearnedAddresses = %v; want %v", got.LearnedAddresses, want)
	}
	for _, a := range got.LearnedAddresses {
		if !want[a] {
			t.Errorf("unexpected learned address %q", a)
		}
	}

	// Second start: seed a further pre-existing message with a brand new
	// address, but do NOT reset AddressesLearnedAt (a fresh worker
	// process restart would find it already set, exactly as
	// runOwnAddressBackfill's persisted-column gate intends). The
	// once-ever gate must skip the scan entirely, so the new address
	// must NOT appear.
	seedPreExistingMessage(t, ha, acc, mb, 3, "bf-3@test", []string{
		"Delivered-To: should-not-be-learned@classic-computing.de",
	})
	w2 := newBareAccountWorker(t, ha, got) // got carries AddressesLearnedAt non-nil
	w2.runOwnAddressBackfill(ctx)

	after, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount (after second start): %v", err)
	}
	if len(after.LearnedAddresses) != len(want) {
		t.Errorf("LearnedAddresses after a second worker start = %v; want unchanged %v (once-ever gate should have skipped the rescan)", after.LearnedAddresses, want)
	}
	for _, a := range after.LearnedAddresses {
		if a == "should-not-be-learned@classic-computing.de" {
			t.Error("second worker start rescanned pre-existing mail; the once-ever gate should have prevented this")
		}
	}
}

// cancelAfterNCtx wraps a context.Context and makes Err() report
// context.Canceled starting from the (n+1)th call, while every other
// method (Done/Deadline/Value) delegates unchanged. This lets a test
// deterministically stop runOwnAddressBackfill partway through its loop
// over message_state rows without any real time-based race.
type cancelAfterNCtx struct {
	context.Context
	calls int
	after int
}

func (c *cancelAfterNCtx) Err() error {
	c.calls++
	if c.calls > c.after {
		return context.Canceled
	}
	return nil
}

// TestOwnAddressBackfillCancelledMidwayLeavesMarkerUnset verifies that a
// context cancelled partway through the backfill loop leaves
// AddressesLearnedAt unset (no partial learning is persisted) and that a
// later, uncancelled run completes the pass from scratch.
func TestOwnAddressBackfillCancelledMidwayLeavesMarkerUnset(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{})
	acc, mb := setupBackfillFixture(t, ha, "bf2@example.test", "Cancel")

	seedPreExistingMessage(t, ha, acc, mb, 1, "cancel-1@test", []string{
		"Delivered-To: info@classic-computing.de",
	})
	seedPreExistingMessage(t, ha, acc, mb, 2, "cancel-2@test", []string{
		"X-Original-To: vorstand@classic-computing.de",
	})

	w := newBareAccountWorker(t, ha, acc)

	// after=1: ctx.Err() reports nil on the first check (message 1's
	// iteration proceeds) and non-nil from the second check onward, so
	// the loop returns before processing message 2 and before ever
	// reaching the trailing learnOwnAddresses call.
	cancelled := &cancelAfterNCtx{Context: context.Background(), after: 1}
	w.runOwnAddressBackfill(cancelled)

	mid, err := ha.Store.Meta().GetIMAPImportAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount (after cancelled pass): %v", err)
	}
	if mid.AddressesLearnedAt != nil {
		t.Fatal("AddressesLearnedAt is set after a context cancelled mid-backfill; want nil (no partial pass persisted)")
	}
	if len(mid.LearnedAddresses) != 0 {
		t.Errorf("LearnedAddresses after a cancelled pass = %v; want empty", mid.LearnedAddresses)
	}

	// A later run with a live context completes the pass in full.
	w2 := newBareAccountWorker(t, ha, mid)
	w2.runOwnAddressBackfill(context.Background())

	final, err := ha.Store.Meta().GetIMAPImportAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount (after completed pass): %v", err)
	}
	if final.AddressesLearnedAt == nil {
		t.Fatal("AddressesLearnedAt is still nil after the follow-up uncancelled run")
	}
	want := map[string]bool{"info@classic-computing.de": true, "vorstand@classic-computing.de": true}
	if len(final.LearnedAddresses) != len(want) {
		t.Fatalf("LearnedAddresses after the follow-up run = %v; want %v", final.LearnedAddresses, want)
	}
}

// --------------------------------------------------------------------------
// Incremental learning via sync.go's ingestMessage: no backfill involved.
// --------------------------------------------------------------------------

// TestIncrementalLearnFromNewlyIngestedMessage verifies that a freshly
// synced message's own Delivered-To is folded into the account's learned
// set through the live ingest path (sync.go's ingestMessage ->
// learnOwnAddresses), with no backfill pass ever running: the account's
// AddressesLearnedAt is pre-set (as if a prior, empty backfill already
// completed) and runSyncOnceCfgSpam only ever calls syncAllFolders, never
// runOwnAddressBackfill.
func TestIncrementalLearnFromNewlyIngestedMessage(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("inc1", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "inc1@example.test",
		username:            "inc1",
		credentialPlaintext: "pw",
	}, nil)

	ctx := context.Background()
	// Simulate a prior backfill pass that completed and found nothing --
	// the common steady-state for an account whose historical mail
	// carried no Delivered-To/X-Original-To at all.
	if err := ha.Store.Meta().SetIMAPImportLearnedAddresses(ctx, acc.ID, nil); err != nil {
		t.Fatalf("SetIMAPImportLearnedAddresses: %v", err)
	}
	acc, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if acc.AddressesLearnedAt == nil {
		t.Fatal("precondition: AddressesLearnedAt should be set")
	}

	d := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRawMessage("incremental", []string{
		"Message-ID: <inc-1@test>",
		"Date: " + d.Format("Mon, 02 Jan 2006 15:04:05 -0700"),
		"Delivered-To: newalias@classic-computing.de",
	})
	appendToServer(t, ts, "inc1", "pw", "INBOX", raw, nil, d)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount (after sync): %v", err)
	}
	if len(got.LearnedAddresses) != 1 || got.LearnedAddresses[0] != "newalias@classic-computing.de" {
		t.Errorf("LearnedAddresses after incremental ingest = %v; want [newalias@classic-computing.de]", got.LearnedAddresses)
	}
}

// --------------------------------------------------------------------------
// maxLearnedAddresses: the bound on the learned set.
// --------------------------------------------------------------------------

// TestLearnOwnAddressesBoundedByMaxLearnedAddresses verifies that
// learnOwnAddresses, the single merge path both the incremental
// (sync.go) and backfill (runOwnAddressBackfill) callers funnel through,
// never grows the persisted learned-address set past maxLearnedAddresses
// -- an upstream that stamps a distinct X-Original-To on effectively
// every message (a rewriting mailing-list relay) must not grow the set
// without limit (REQ-IMAP-IMP-36).
func TestLearnOwnAddressesBoundedByMaxLearnedAddresses(t *testing.T) {
	ha, _ := testharness.Start(t, testharness.Options{})
	acc, _ := setupBackfillFixture(t, ha, "cap-many@example.test", "Cap")
	w := newBareAccountWorker(t, ha, acc)
	ctx := context.Background()

	// Feed far more than the cap's worth of distinct addresses, split
	// across several calls the way distinct incoming messages would
	// each call learnOwnAddresses separately.
	total := maxLearnedAddresses + 50
	for i := 0; i < total; i += 10 {
		batch := make([]string, 0, 10)
		for j := i; j < i+10 && j < total; j++ {
			batch = append(batch, fmt.Sprintf("addr%d@example.test", j))
		}
		if err := w.learnOwnAddresses(ctx, batch); err != nil {
			t.Fatalf("learnOwnAddresses(batch starting at %d): %v", i, err)
		}
	}

	got, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if len(got.LearnedAddresses) != maxLearnedAddresses {
		t.Fatalf("LearnedAddresses length = %d; want exactly the cap %d after feeding %d distinct addresses",
			len(got.LearnedAddresses), maxLearnedAddresses, total)
	}

	// Dedup still applies at the cap: re-submitting an already-learned
	// address is a no-op, not a new admission that would (incorrectly)
	// require evicting something to stay at the cap.
	already := got.LearnedAddresses[0]
	if err := w.learnOwnAddresses(ctx, []string{already, "brand-new-after-cap@example.test"}); err != nil {
		t.Fatalf("learnOwnAddresses (post-cap): %v", err)
	}
	final, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount (final): %v", err)
	}
	if len(final.LearnedAddresses) != maxLearnedAddresses {
		t.Fatalf("LearnedAddresses length after a post-cap call = %d; want unchanged %d", len(final.LearnedAddresses), maxLearnedAddresses)
	}
	for _, a := range final.LearnedAddresses {
		if a == "brand-new-after-cap@example.test" {
			t.Error("a new address was admitted past the cap")
		}
	}
}
