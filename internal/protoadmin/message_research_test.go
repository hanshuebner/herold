package protoadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// seedResearchFixture seeds the test store with a received message
// (with spam verdict, in INBOX), a system event (SMTP-time reject of
// carol@alpha.test), and a queue item (outbound from alice@alpha.test).
// Returns (adminKey, alicePrincipalID, bobPrincipalID) for assertions.
func seedResearchFixture(t *testing.T, h *harness) (adminKey string, aliceID, bobID uint64) {
	t.Helper()
	ctx := context.Background()
	s := h.h.Store

	// Bootstrap admin and promote to super-admin.
	_, adminKey = h.bootstrap("superadmin@example.com")
	admin, err := s.Meta().GetPrincipalByEmail(ctx, "superadmin@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	admin.Flags |= store.PrincipalFlagSuperAdmin
	if err := s.Meta().UpdatePrincipal(ctx, admin); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	// Register alpha.test and beta.test as local domains so principals can
	// be created with those email addresses (domain validation in REST API).
	for _, dom := range []string{"alpha.test", "beta.test"} {
		res, buf := h.doRequest("POST", "/api/v1/domains", adminKey, map[string]any{"name": dom})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create domain %s: %d: %s", dom, res.StatusCode, buf)
		}
	}

	// Create alice@alpha.test.
	aliceID = h.createPrincipal(adminKey, "alice@alpha.test")
	alice, err := s.Meta().GetPrincipalByID(ctx, store.PrincipalID(aliceID))
	if err != nil {
		t.Fatalf("GetPrincipalByID alice: %v", err)
	}

	// The REST API auto-provisions default mailboxes (INBOX, Sent, etc.).
	// Find the existing INBOX rather than inserting a duplicate.
	allMbs, err := s.Meta().ListMailboxes(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListMailboxes alice: %v", err)
	}
	var inbox store.Mailbox
	for _, mb := range allMbs {
		if mb.Name == "INBOX" {
			inbox = mb
			break
		}
	}
	if inbox.ID == 0 {
		t.Fatalf("INBOX not provisioned for alice; mailboxes=%v", allMbs)
	}

	// Insert a blob and a message for alice.
	blobRef, err := s.Blobs().Put(ctx, strings.NewReader("test message body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	rcv := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  alice.ID,
		Blob:         blobRef,
		Size:         blobRef.Size,
		ReceivedAt:   rcv,
		InternalDate: rcv,
		Envelope: store.Envelope{
			Subject:   "Hello from research test",
			From:      "sender@outside.test",
			To:        "alice@alpha.test",
			MessageID: "research-test-msg-1@outside.test",
		},
	}, []store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Retrieve stable MessageID via ListMessages.
	insertedMsgs, err := s.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 1})
	if err != nil || len(insertedMsgs) == 0 {
		t.Fatalf("ListMessages inbox: %v (len=%d)", err, len(insertedMsgs))
	}
	msgID := insertedMsgs[0].ID

	// Add spam verdict.
	verdict := "ham"
	conf := 0.9
	if err := s.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID:      msgID,
		PrincipalID:    alice.ID,
		SpamVerdict:    &verdict,
		SpamConfidence: &conf,
	}); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	// Create bob@beta.test (different domain for operator scope testing).
	bobID = h.createPrincipal(adminKey, "bob@beta.test")

	// System event: SMTP-time reject (never stored as message).
	if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
		At:      time.Date(2026, 6, 1, 11, 59, 0, 0, time.UTC),
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
	outRef, err := s.Blobs().Put(ctx, strings.NewReader("outbound body"))
	if err != nil {
		t.Fatalf("Blobs.Put outbound: %v", err)
	}
	if _, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
		PrincipalID:   alice.ID,
		MailFrom:      "alice@alpha.test",
		RcptTo:        "dest@remote.example",
		EnvelopeID:    "env-research-1",
		BodyBlobHash:  outRef.Hash,
		State:         store.QueueStateQueued,
		CreatedAt:     time.Date(2026, 6, 1, 12, 1, 0, 0, time.UTC),
		NextAttemptAt: time.Date(2026, 6, 1, 12, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("EnqueueMessage: %v", err)
	}

	return adminKey, aliceID, bobID
}

// TestMessageResearch_JoinedTimeline verifies that the endpoint returns entries
// from all three sources (received, smtp_event, send_outcome) for a super-admin.
func TestMessageResearch_JoinedTimeline(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)

	res, buf := h.doRequest("GET", "/api/v1/admin/message-research", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET message-research: %d: %s", res.StatusCode, buf)
	}

	var out struct {
		Items []map[string]any `json:"items"`
		Next  *string          `json:"next"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}

	sources := map[string]bool{}
	for _, item := range out.Items {
		if src, ok := item["source"].(string); ok {
			sources[src] = true
		}
	}
	for _, want := range []string{"received", "smtp_event", "send_outcome"} {
		if !sources[want] {
			t.Errorf("source %q missing from timeline; sources=%v", want, sources)
		}
	}
}

// TestMessageResearch_ReceivedFields verifies that received entries carry
// envelope, mailbox_name, is_junk, spam_verdict, and spam_confidence.
func TestMessageResearch_ReceivedFields(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)

	res, buf := h.doRequest("GET", "/api/v1/admin/message-research?sender=sender%40outside.test", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d: %s", res.StatusCode, buf)
	}

	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}

	var found bool
	for _, item := range out.Items {
		if item["source"] != "received" {
			continue
		}
		found = true
		env, ok := item["envelope"].(map[string]any)
		if !ok {
			t.Errorf("envelope missing or wrong type: %T", item["envelope"])
			continue
		}
		if env["subject"] != "Hello from research test" {
			t.Errorf("envelope.subject: got %v", env["subject"])
		}
		if env["from"] != "sender@outside.test" {
			t.Errorf("envelope.from: got %v", env["from"])
		}
		if env["message_id"] != "research-test-msg-1@outside.test" {
			t.Errorf("envelope.message_id: got %v", env["message_id"])
		}
		if item["spam_verdict"] != "ham" {
			t.Errorf("spam_verdict: got %v", item["spam_verdict"])
		}
		if item["is_junk"] != false {
			t.Errorf("is_junk: got %v", item["is_junk"])
		}
	}
	if !found {
		t.Errorf("no 'received' entry found; items=%+v", out.Items)
	}
}

// TestMessageResearch_SenderFilter verifies the sender query parameter filters
// the received messages by From substring.
func TestMessageResearch_SenderFilter(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)

	res, buf := h.doRequest("GET", "/api/v1/admin/message-research?sender=sender%40outside.test", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	// Only received entries should appear for this sender filter.
	for _, item := range out.Items {
		if item["source"] == "received" {
			env, _ := item["envelope"].(map[string]any)
			if env != nil {
				from, _ := env["from"].(string)
				if !strings.Contains(strings.ToLower(from), "sender@outside.test") {
					t.Errorf("sender filter leaked envelope from=%q", from)
				}
			}
		}
	}
}

// TestMessageResearch_MessageIDFilter verifies the message_id query parameter.
func TestMessageResearch_MessageIDFilter(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)

	res, buf := h.doRequest("GET",
		"/api/v1/admin/message-research?message_id=research-test-msg-1%40outside.test",
		adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}

	var foundReceived bool
	for _, item := range out.Items {
		if item["source"] == "received" {
			foundReceived = true
			env, _ := item["envelope"].(map[string]any)
			if env != nil {
				mid, _ := env["message_id"].(string)
				if mid != "research-test-msg-1@outside.test" {
					t.Errorf("message_id filter: got %q", mid)
				}
			}
		}
	}
	if !foundReceived {
		t.Errorf("message_id filter: no received entry found; items=%+v", out.Items)
	}
}

// TestMessageResearch_DateRange verifies date_from / date_to filtering.
func TestMessageResearch_DateRange(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)

	// Window covers only the received message at 2026-06-01T12:00:00Z.
	res, buf := h.doRequest("GET",
		"/api/v1/admin/message-research?date_from=2026-06-01T12:00:00Z&date_to=2026-06-01T12:01:00Z",
		adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	// Should not include the smtp_event (at 11:59) or send_outcome (at 12:01).
	for _, item := range out.Items {
		src, _ := item["source"].(string)
		at, _ := item["at"].(string)
		if src == "smtp_event" {
			t.Errorf("date range leaked smtp_event at %q", at)
		}
		if src == "send_outcome" {
			t.Errorf("date range leaked send_outcome at %q", at)
		}
	}
	var gotReceived bool
	for _, item := range out.Items {
		if item["source"] == "received" {
			gotReceived = true
		}
	}
	if !gotReceived {
		t.Errorf("date range: received message missing; items=%+v", out.Items)
	}
}

// TestMessageResearch_OperatorScope verifies that domain-scoped operators see
// only entries for their managed domains, and super-admins see everything.
func TestMessageResearch_OperatorScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	adminKey, _, _ := seedResearchFixture(t, h)

	// Add a message for bob@beta.test so the beta domain has data.
	bob, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "bob@beta.test")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail bob: %v", err)
	}
	// bob's INBOX was auto-provisioned by POST /api/v1/principals; look it up.
	bobMbs, err := h.h.Store.Meta().ListMailboxes(ctx, bob.ID)
	if err != nil {
		t.Fatalf("ListMailboxes bob: %v", err)
	}
	var betaInbox store.Mailbox
	for _, mb := range bobMbs {
		if mb.Name == "INBOX" {
			betaInbox = mb
			break
		}
	}
	if betaInbox.ID == 0 {
		t.Fatalf("INBOX not provisioned for bob; mailboxes=%v", bobMbs)
	}
	betaBlob, err := h.h.Store.Blobs().Put(ctx, strings.NewReader("beta body"))
	if err != nil {
		t.Fatalf("Blobs.Put beta: %v", err)
	}
	betaRcv := time.Date(2026, 6, 1, 12, 2, 0, 0, time.UTC)
	_, _, err = h.h.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  bob.ID,
		Blob:         betaBlob,
		Size:         betaBlob.Size,
		ReceivedAt:   betaRcv,
		InternalDate: betaRcv,
		Envelope: store.Envelope{
			Subject:   "Beta message",
			From:      "sender@beta-outside.test",
			To:        "bob@beta.test",
			MessageID: "beta-msg-1@beta-outside.test",
		},
	}, []store.MessageMailbox{{MailboxID: betaInbox.ID}})
	if err != nil {
		t.Fatalf("InsertMessage bob: %v", err)
	}

	// Create an operator for alpha.test only.
	opID := h.createPrincipal(adminKey, "op@alpha.test")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID op: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin // no super-admin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal op: %v", err)
	}
	if err := h.h.Store.Meta().AssignManagedDomain(ctx, store.PrincipalID(opID), "alpha.test"); err != nil {
		t.Fatalf("AssignManagedDomain: %v", err)
	}
	_, opKey := h.createAPIKey(adminKey, opID)

	// Operator query: should see alpha.test messages, not beta.test messages.
	res, buf := h.doRequest("GET", "/api/v1/admin/message-research", opKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("operator GET: %d: %s", res.StatusCode, buf)
	}
	var opOut struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &opOut); err != nil {
		t.Fatalf("decode op: %v: %s", err, buf)
	}
	for _, item := range opOut.Items {
		if item["source"] != "received" {
			continue
		}
		env, _ := item["envelope"].(map[string]any)
		if env == nil {
			continue
		}
		to, _ := env["to"].(string)
		if strings.Contains(to, "@beta.test") {
			t.Errorf("operator leaked beta.test message: %v", item)
		}
	}
	// Should see alice's message.
	var sawAlpha bool
	for _, item := range opOut.Items {
		if item["source"] != "received" {
			continue
		}
		env, _ := item["envelope"].(map[string]any)
		if env != nil {
			if subj, _ := env["subject"].(string); subj == "Hello from research test" {
				sawAlpha = true
			}
		}
	}
	if !sawAlpha {
		t.Errorf("operator: alpha.test received message not found; items=%+v", opOut.Items)
	}

	// Super-admin query: should see both domains.
	res, buf = h.doRequest("GET", "/api/v1/admin/message-research", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("super-admin GET: %d: %s", res.StatusCode, buf)
	}
	var adminOut struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &adminOut); err != nil {
		t.Fatalf("decode admin: %v: %s", err, buf)
	}

	var sawBeta bool
	for _, item := range adminOut.Items {
		if item["source"] != "received" {
			continue
		}
		env, _ := item["envelope"].(map[string]any)
		if env != nil {
			if to, _ := env["to"].(string); strings.Contains(to, "@beta.test") {
				sawBeta = true
			}
		}
	}
	if !sawBeta {
		t.Errorf("super-admin: beta.test message not found; items=%+v", adminOut.Items)
	}
}

// TestMessageResearch_OperatorScope_Unresolvable verifies that an admin with
// no managed domains (unresolvable scope) gets an empty result (fail-closed).
func TestMessageResearch_OperatorScope_Unresolvable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	adminKey, _, _ := seedResearchFixture(t, h)

	// Create an operator with no managed domains.
	nodomainID := h.createPrincipal(adminKey, "nodomain@example.com")
	nodomain, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(nodomainID))
	if err != nil {
		t.Fatalf("GetPrincipalByID nodomain: %v", err)
	}
	nodomain.Flags = store.PrincipalFlagAdmin // admin, but no super-admin, no managed domains
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, nodomain); err != nil {
		t.Fatalf("UpdatePrincipal nodomain: %v", err)
	}
	_, nodomainKey := h.createAPIKey(adminKey, nodomainID)

	res, buf := h.doRequest("GET", "/api/v1/admin/message-research", nodomainKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("nodomain GET: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if len(out.Items) != 0 {
		t.Errorf("unresolvable scope: expected 0 items; got %d: %+v", len(out.Items), out.Items)
	}
}

// TestMessageResearch_Pagination verifies the before_us cursor advances
// the page correctly.
func TestMessageResearch_Pagination(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)

	// Page 1: limit=1.
	res, buf := h.doRequest("GET", "/api/v1/admin/message-research?limit=1", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("page 1: %d: %s", res.StatusCode, buf)
	}
	var page1 struct {
		Items []map[string]any `json:"items"`
		Next  *string          `json:"next"`
	}
	if err := json.Unmarshal(buf, &page1); err != nil {
		t.Fatalf("decode page 1: %v", err)
	}
	if len(page1.Items) != 1 {
		t.Fatalf("page 1: got %d items; want 1", len(page1.Items))
	}
	if page1.Next == nil {
		t.Fatalf("page 1: next cursor missing")
	}

	// Page 2.
	res, buf = h.doRequest("GET",
		fmt.Sprintf("/api/v1/admin/message-research?limit=1&before_us=%s", *page1.Next),
		adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("page 2: %d: %s", res.StatusCode, buf)
	}
	var page2 struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page2); err != nil {
		t.Fatalf("decode page 2: %v", err)
	}
	if len(page2.Items) == 0 {
		t.Fatalf("page 2: got 0 items; want >= 1")
	}

	// The `at` of page 2's first item must be strictly before page 1's first item.
	at1, _ := page1.Items[0]["at"].(string)
	at2, _ := page2.Items[0]["at"].(string)
	if at2 >= at1 {
		t.Errorf("page 2 item at=%q is not older than page 1 item at=%q", at2, at1)
	}
}

// TestMessageResearch_NoBody verifies that no body content is present in
// the response (REQ-ADM-306 security constraint).
func TestMessageResearch_NoBody(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)

	_, buf := h.doRequest("GET", "/api/v1/admin/message-research", adminKey, nil)

	// The body of the seeded message is "test message body".
	// It must not appear in the response.
	if strings.Contains(string(buf), "test message body") {
		t.Errorf("response contains message body text; REQ-ADM-306 violated: %s", buf)
	}
	if strings.Contains(string(buf), "outbound body") {
		t.Errorf("response contains outbound body text; REQ-ADM-306 violated: %s", buf)
	}
}

// TestMessageResearch_ForwardRelayNewestFirst reproduces the reported shape
// (re #143): an alias forwarded to an external target produces an outbound
// relay leg (SRS-rewritten mail_from, external rcpt_to) that must surface as
// a send_outcome entry. Several older, unrelated queue rows are seeded first
// so a small limit forces the queue fetch window below the total historical
// row count -- exactly the condition under which the pre-fix ORDER BY id ASC
// fetch silently dropped the newest row (the relay leg) from the timeline
// entirely.
func TestMessageResearch_ForwardRelayNewestFirst(t *testing.T) {
	h := newHarness(t)
	adminKey, _, _ := seedResearchFixture(t, h)
	ctx := context.Background()
	s := h.h.Store

	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		ref, err := s.Blobs().Put(ctx, strings.NewReader(fmt.Sprintf("old body %d", i)))
		if err != nil {
			t.Fatalf("Blobs.Put old %d: %v", i, err)
		}
		created := base.Add(time.Duration(i) * time.Minute)
		if _, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
			MailFrom:      fmt.Sprintf("old%d@unrelated.test", i),
			RcptTo:        "old-dest@unrelated.test",
			EnvelopeID:    store.EnvelopeID(fmt.Sprintf("env-old-%d", i)),
			BodyBlobHash:  ref.Hash,
			State:         store.QueueStateDone,
			CreatedAt:     created,
			NextAttemptAt: created,
		}); err != nil {
			t.Fatalf("EnqueueMessage old %d: %v", i, err)
		}
	}

	// The alias-forward relay leg: SRS-rewritten mail_from, external
	// rcpt_to, queued after everything else above.
	fwdRef, err := s.Blobs().Put(ctx, strings.NewReader("forwarded body"))
	if err != nil {
		t.Fatalf("Blobs.Put forward: %v", err)
	}
	fwdCreated := time.Date(2026, 6, 1, 12, 5, 0, 0, time.UTC)
	if _, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
		MailFrom:      "srs0=abcd=aa=netzhansa.com=sender@alpha.test",
		RcptTo:        "hans.huebner@gmail.example",
		EnvelopeID:    "env-alias-forward",
		BodyBlobHash:  fwdRef.Hash,
		State:         store.QueueStateDone,
		CreatedAt:     fwdCreated,
		NextAttemptAt: fwdCreated,
	}); err != nil {
		t.Fatalf("EnqueueMessage forward: %v", err)
	}

	// limit=1 forces the queue fetch window (fetchLimit = limit+1 = 2)
	// well below the 6 total historical rows -- the condition the bug
	// needs to manifest.
	res, buf := h.doRequest("GET", "/api/v1/admin/message-research?limit=1", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if len(out.Items) != 1 {
		t.Fatalf("got %d items; want 1 (limit=1)", len(out.Items))
	}
	item := out.Items[0]
	if item["source"] != "send_outcome" {
		t.Fatalf("newest timeline item source = %v; want send_outcome (the alias-forward relay leg was excluded)",
			item["source"])
	}
	if item["mail_from"] != "srs0=abcd=aa=netzhansa.com=sender@alpha.test" {
		t.Errorf("newest send_outcome mail_from = %v; want the alias-forward relay row", item["mail_from"])
	}
}
