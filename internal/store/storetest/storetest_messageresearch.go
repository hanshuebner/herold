package storetest

import (
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// seedMessageResearchFixture inserts two principals with messages plus queue
// items and system events that the message research tests use:
//
//   - alice@alpha.test gets one INBOX message (env_from=sender@outside.example,
//     env_to=alice@alpha.test, subject="Hello Alice") with a spam verdict.
//   - bob@beta.test gets one Junk folder message (env_from=spam@outside.example,
//     env_to=bob@beta.test, subject="Win a prize").
//   - A system event for a never-stored message (reject of carol@alpha.test).
//   - A queue item (outbound send from alice@alpha.test).
//
// Returns the two principals and the inserted message IDs.
func seedMessageResearchFixture(t *testing.T, s store.Store) (alice, bob store.Principal, aliceMsg, bobMsg store.MessageID) {
	t.Helper()
	ctx := ctxT(t)

	alice = mustInsertPrincipal(t, s, "alice@alpha.test")
	bob = mustInsertPrincipal(t, s, "bob@beta.test")

	// Alice: INBOX message with spam verdict.
	aliceInbox := mustInsertMailbox(t, s, alice.ID, "INBOX")
	aliceBlob := putBlob(t, s, "Hello Alice body")
	rcvAlice := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  alice.ID,
		Blob:         aliceBlob,
		Size:         aliceBlob.Size,
		ReceivedAt:   rcvAlice,
		InternalDate: rcvAlice,
		Envelope: store.Envelope{
			Subject:   "Hello Alice",
			From:      "sender@outside.example",
			To:        "alice@alpha.test",
			MessageID: "msg-alice-1@outside.example",
		},
	}, []store.MessageMailbox{{MailboxID: aliceInbox.ID}}); err != nil {
		t.Fatalf("InsertMessage alice: %v", err)
	}
	// Retrieve the stable MessageID via ListMessages.
	aliceMsgs, err := s.Meta().ListMessages(ctx, aliceInbox.ID, store.MessageFilter{Limit: 1})
	if err != nil || len(aliceMsgs) == 0 {
		t.Fatalf("ListMessages alice: %v (len=%d)", err, len(aliceMsgs))
	}
	aliceMsg = aliceMsgs[0].ID

	// Add spam verdict for Alice's message.
	verdict := "ham"
	conf := 0.95
	if err := s.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID:      aliceMsg,
		PrincipalID:    alice.ID,
		SpamVerdict:    &verdict,
		SpamConfidence: &conf,
	}); err != nil {
		t.Fatalf("SetLLMClassification alice: %v", err)
	}

	// Bob: Junk folder message.
	bobJunk := mustInsertMailboxWithAttrs(t, s, bob.ID, "Junk", store.MailboxAttrJunk)
	bobBlob := putBlob(t, s, "Win a prize body")
	rcvBob := time.Date(2026, 5, 1, 10, 1, 0, 0, time.UTC)
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  bob.ID,
		Blob:         bobBlob,
		Size:         bobBlob.Size,
		ReceivedAt:   rcvBob,
		InternalDate: rcvBob,
		Envelope: store.Envelope{
			Subject:   "Win a prize",
			From:      "spam@outside.example",
			To:        "bob@beta.test",
			MessageID: "msg-bob-1@outside.example",
		},
	}, []store.MessageMailbox{{MailboxID: bobJunk.ID}}); err != nil {
		t.Fatalf("InsertMessage bob: %v", err)
	}
	// Retrieve the stable MessageID via ListMessages.
	bobMsgs, err := s.Meta().ListMessages(ctx, bobJunk.ID, store.MessageFilter{Limit: 1})
	if err != nil || len(bobMsgs) == 0 {
		t.Fatalf("ListMessages bob: %v (len=%d)", err, len(bobMsgs))
	}
	bobMsg = bobMsgs[0].ID

	// System event: SMTP-time reject of carol@alpha.test (never stored as message).
	if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
		At:      time.Date(2026, 5, 1, 9, 59, 0, 0, time.UTC),
		Action:  "smtp.rcpt.reject",
		ActorID: "smtp",
		Subject: "rcpt:carol@alpha.test",
		Outcome: store.OutcomeFailure,
		Message: "user unknown",
		Domain:  "alpha.test",
	}); err != nil {
		t.Fatalf("AppendSystemEvent: %v", err)
	}

	// Queue item: outbound send from alice@alpha.test.
	outRef := putBlob(t, s, "outbound body")
	if _, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
		PrincipalID:   alice.ID,
		MailFrom:      "alice@alpha.test",
		RcptTo:        "dest@remote.example",
		EnvelopeID:    "env-alice-out-1",
		BodyBlobHash:  outRef.Hash,
		State:         store.QueueStateQueued,
		CreatedAt:     time.Date(2026, 5, 1, 10, 2, 0, 0, time.UTC),
		NextAttemptAt: time.Date(2026, 5, 1, 10, 2, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("EnqueueMessage: %v", err)
	}

	return alice, bob, aliceMsg, bobMsg
}

// mustInsertMailboxWithAttrs inserts a mailbox with specific attributes.
func mustInsertMailboxWithAttrs(t *testing.T, s store.Store, pID store.PrincipalID, name string, attrs store.MailboxAttributes) store.Mailbox {
	t.Helper()
	mb, err := s.Meta().InsertMailbox(ctxT(t), store.Mailbox{
		PrincipalID: pID,
		Name:        name,
		Attributes:  attrs,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(%s): %v", name, err)
	}
	return mb
}

// testSearchAdminMessages_ReceivedMessage verifies that SearchAdminMessages
// returns received messages with envelope fields, mailbox name, Junk flag,
// and spam verdict (REQ-ADM-306, re #143).
func testSearchAdminMessages_ReceivedMessage(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	alice, bob, aliceMsg, bobMsg := seedMessageResearchFixture(t, s)

	// Unrestricted search (nil Domains = super-admin).
	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{Limit: 100})
	if err != nil {
		t.Fatalf("SearchAdminMessages: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected >= 2 hits; got %d", len(hits))
	}

	byID := make(map[store.MessageID]store.AdminMessageHit, len(hits))
	for _, h := range hits {
		byID[h.MessageID] = h
	}

	// Alice's message: INBOX, ham verdict.
	if ah, ok := byID[aliceMsg]; ok {
		if ah.Envelope.Subject != "Hello Alice" {
			t.Errorf("alice subject: got %q", ah.Envelope.Subject)
		}
		if ah.MailboxName == "" {
			t.Errorf("alice mailbox_name empty")
		}
		if ah.IsJunk {
			t.Errorf("alice is_junk should be false")
		}
		if ah.SpamVerdict == nil || *ah.SpamVerdict != "ham" {
			t.Errorf("alice spam_verdict: got %v", ah.SpamVerdict)
		}
		if ah.SpamConfidence == nil || *ah.SpamConfidence < 0.9 {
			t.Errorf("alice spam_confidence: got %v", ah.SpamConfidence)
		}
		if ah.PrincipalID != alice.ID {
			t.Errorf("alice principal_id: got %d, want %d", ah.PrincipalID, alice.ID)
		}
	} else {
		t.Errorf("alice message not found in hits; ids in result: %v", hitsIDs(hits))
	}

	// Bob's message: Junk, no spam verdict.
	if bh, ok := byID[bobMsg]; ok {
		if bh.Envelope.Subject != "Win a prize" {
			t.Errorf("bob subject: got %q", bh.Envelope.Subject)
		}
		if !bh.IsJunk {
			t.Errorf("bob is_junk should be true")
		}
		if bh.PrincipalID != bob.ID {
			t.Errorf("bob principal_id: got %d, want %d", bh.PrincipalID, bob.ID)
		}
	} else {
		t.Errorf("bob message not found; ids: %v", hitsIDs(hits))
	}
}

// testSearchAdminMessages_SenderFilter verifies the Sender substring filter.
func testSearchAdminMessages_SenderFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		Sender: "spam@outside",
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages sender: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("sender filter: got %d hits, want 1", len(hits))
	}
	if hits[0].Envelope.Subject != "Win a prize" {
		t.Errorf("sender filter: got subject %q", hits[0].Envelope.Subject)
	}
}

// testSearchAdminMessages_RecipientFilter verifies the Recipient substring filter.
func testSearchAdminMessages_RecipientFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		Recipient: "alice@alpha",
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages recipient: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("recipient filter: got %d hits, want 1", len(hits))
	}
	if hits[0].Envelope.Subject != "Hello Alice" {
		t.Errorf("recipient filter: got subject %q", hits[0].Envelope.Subject)
	}
}

// testSearchAdminMessages_MessageIDFilter verifies exact Message-ID matching.
func testSearchAdminMessages_MessageIDFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		MessageID: "msg-alice-1@outside.example",
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages message_id: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("message_id filter: got %d hits, want 1", len(hits))
	}
	if hits[0].Envelope.MessageID != "msg-alice-1@outside.example" {
		t.Errorf("message_id filter: got %q", hits[0].Envelope.MessageID)
	}
}

// testSearchAdminMessages_SubjectFilter verifies Subject substring filter.
func testSearchAdminMessages_SubjectFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		Subject: "prize",
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages subject: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("subject filter: got %d hits, want 1", len(hits))
	}
	if hits[0].Envelope.Subject != "Win a prize" {
		t.Errorf("subject filter: got %q", hits[0].Envelope.Subject)
	}
}

// testSearchAdminMessages_DateRangeFilter verifies DateFrom/DateTo filtering.
func testSearchAdminMessages_DateRangeFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	// Narrow window: only Alice's message (10:00:00) is in range [10:00:00, 10:01:00).
	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		DateFrom: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		DateTo:   time.Date(2026, 5, 1, 10, 1, 0, 0, time.UTC),
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages date range: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("date range: got %d hits, want 1 (alice only)", len(hits))
	}
	if hits[0].Envelope.Subject != "Hello Alice" {
		t.Errorf("date range: got subject %q", hits[0].Envelope.Subject)
	}
}

// testSearchAdminMessages_DomainScope_SuperAdmin verifies nil Domains = no
// restriction (super-admin sees all).
func testSearchAdminMessages_DomainScope_SuperAdmin(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	// nil Domains = unrestricted
	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		Domains: nil,
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages super-admin: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("super-admin: expected >= 2 hits; got %d", len(hits))
	}
}

// testSearchAdminMessages_DomainScope_Operator verifies that a non-nil
// Domains slice restricts to only messages for principals in those domains.
func testSearchAdminMessages_DomainScope_Operator(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	// Operator for alpha.test only.
	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		Domains: []string{"alpha.test"},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages operator: %v", err)
	}
	// Must not see bob@beta.test's messages.
	for _, h := range hits {
		if strings.HasSuffix(h.Envelope.To, "@beta.test") || strings.HasSuffix(h.Envelope.From, "@beta.test") {
			// Domain scope check is on principal's canonical_email domain,
			// not envelope addresses, but beta.test messages must still be absent.
			t.Errorf("operator for alpha.test leaked message to/from beta.test: subj=%q", h.Envelope.Subject)
		}
	}
	if len(hits) == 0 {
		t.Errorf("operator for alpha.test got 0 hits; want Alice's message")
	}
}

// testSearchAdminMessages_DomainScope_FailClosed verifies that a non-nil
// empty Domains slice returns no results (fail-closed per REQ-ADM-307).
func testSearchAdminMessages_DomainScope_FailClosed(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	hits, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		Domains: []string{}, // non-nil empty = fail closed
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("SearchAdminMessages fail-closed: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("fail-closed: expected 0 hits; got %d", len(hits))
	}
}

// testSearchAdminMessages_CursorPagination verifies the BeforeReceivedUs
// keyset cursor advances correctly.
func testSearchAdminMessages_CursorPagination(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	// Page 1: limit=1 returns the newest message.
	page1, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{Limit: 1})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 1 {
		t.Fatalf("page 1: got %d; want 1", len(page1))
	}

	// Page 2: cursor = newest message's received_at_us.
	cursor := page1[0].ReceivedAt.UnixMicro()
	page2, err := s.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{
		Limit:            1,
		BeforeReceivedUs: cursor,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page 2: got %d; want 1", len(page2))
	}
	if page2[0].MessageID == page1[0].MessageID {
		t.Errorf("page 2 repeated page 1 message id")
	}
	// Page 2 message must be older than page 1.
	if !page2[0].ReceivedAt.Before(page1[0].ReceivedAt) {
		t.Errorf("page 2 (%v) is not older than page 1 (%v)",
			page2[0].ReceivedAt, page1[0].ReceivedAt)
	}
}

// testQueueFilter_SenderDomainsAndContains verifies the new SenderDomains,
// MailFromContains, and RcptToContains fields on QueueFilter (re #143).
func testQueueFilter_SenderDomainsAndContains(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	seedMessageResearchFixture(t, s)

	// SenderDomains: only rows whose mail_from domain is alpha.test.
	items, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{
		SenderDomains: []string{"alpha.test"},
	})
	if err != nil {
		t.Fatalf("SenderDomains filter: %v", err)
	}
	for _, qi := range items {
		if !strings.Contains(qi.MailFrom, "@alpha.test") {
			t.Errorf("SenderDomains leaked mail_from=%q", qi.MailFrom)
		}
	}
	if len(items) == 0 {
		t.Errorf("SenderDomains alpha.test: got 0 items; want >= 1")
	}

	// MailFromContains: substring match.
	items2, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{
		MailFromContains: "alice@alpha",
	})
	if err != nil {
		t.Fatalf("MailFromContains: %v", err)
	}
	for _, qi := range items2 {
		if !strings.Contains(strings.ToLower(qi.MailFrom), "alice@alpha") {
			t.Errorf("MailFromContains leaked mail_from=%q", qi.MailFrom)
		}
	}

	// RcptToContains: substring match.
	items3, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{
		RcptToContains: "dest@remote",
	})
	if err != nil {
		t.Fatalf("RcptToContains: %v", err)
	}
	for _, qi := range items3 {
		if !strings.Contains(strings.ToLower(qi.RcptTo), "dest@remote") {
			t.Errorf("RcptToContains leaked rcpt_to=%q", qi.RcptTo)
		}
	}

	// Fail-closed: non-nil empty SenderDomains.
	items4, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{
		SenderDomains: []string{},
	})
	if err != nil {
		t.Fatalf("SenderDomains fail-closed: %v", err)
	}
	if len(items4) != 0 {
		t.Errorf("SenderDomains fail-closed: got %d items; want 0", len(items4))
	}
}

// hitsIDs returns the MessageID list from a hits slice for test diagnostics.
func hitsIDs(hits []store.AdminMessageHit) []store.MessageID {
	ids := make([]store.MessageID, len(hits))
	for i, h := range hits {
		ids[i] = h.MessageID
	}
	return ids
}
