package maillist_test

// subscribe_test.go exercises Stage 3 self-subscription (issue #185,
// REQ-MLIST-60..63): the public subscribe/confirm endpoints, the
// no-member-enumeration property, the confirm-send rate bound, and the
// GET-is-safe / cross-purpose / cross-list / expired / tampered token
// properties the confirm route must uphold exactly like the S2
// unsubscribe surface (publicserver_test.go).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// extractConfirmURL pulls the confirm URL out of the plain-text
// confirmation email body built by buildConfirmationEmail
// (subscribe.go): the line "  https://...&token=...\r\n" following
// "Confirm by opening this link:".
func extractConfirmURL(t *testing.T, body string) string {
	t.Helper()
	const marker = "Confirm by opening this link:\r\n\r\n  "
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no confirm link marker in body:\n%s", body)
	}
	rest := body[i+len(marker):]
	end := strings.Index(rest, "\r\n")
	if end < 0 {
		t.Fatalf("could not find end of confirm URL in body:\n%s", body)
	}
	return rest[:end]
}

// queuedBodyForRecipient finds the most recently enqueued queue item
// addressed to rcpt and returns its persisted blob body as a string.
// Fails the test if no such item exists.
func queuedBodyForRecipient(t *testing.T, st store.Store, rcpt string) string {
	t.Helper()
	ctx := context.Background()
	items, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	var found *store.QueueItem
	for i := range items {
		if items[i].RcptTo == rcpt {
			found = &items[i]
		}
	}
	if found == nil {
		t.Fatalf("no queue item addressed to %s; items=%+v", rcpt, items)
	}
	r, err := st.Blobs().Get(ctx, found.BodyBlobHash)
	if err != nil {
		t.Fatalf("Blobs().Get(%s): %v", found.BodyBlobHash, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	return string(b)
}

func subscribeTarget(listID store.MailingListID) string {
	return fmt.Sprintf("/lists/%d/subscribe", uint64(listID))
}

func confirmTarget(listID store.MailingListID, token string) string {
	return fmt.Sprintf("/lists/%d/confirm?token=%s", uint64(listID), token)
}

func doSubscribe(t *testing.T, h http.Handler, listID store.MailingListID, address string) *httpResponseRecorderResult {
	t.Helper()
	rec := doRequest(t, h, http.MethodPost, subscribeTarget(listID),
		(url.Values{"address": {address}}).Encode(), "application/x-www-form-urlencoded")
	return &httpResponseRecorderResult{code: rec.Code, body: rec.Body.String()}
}

type httpResponseRecorderResult struct {
	code int
	body string
}

// TestPublicServer_Subscribe_ClosedPolicy_NotFound: REQ-MLIST-60's
// "closed... no public subscribe endpoint" -- the response is
// byte-identical to an unknown list id, so a probe cannot even tell a
// closed list apart from a nonexistent one.
func TestPublicServer_Subscribe_ClosedPolicy_NotFound(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false) // SubscribePolicy defaults to closed

	clk := clock.NewFake(time.Now())
	srv, _ := newTestPublicServer(t, st, clk)

	gotClosed := doSubscribe(t, srv.Handler(), ml.ID, "someone@example.net")
	gotUnknown := doSubscribe(t, srv.Handler(), ml.ID+9999, "someone@example.net")

	if gotClosed.code != http.StatusNotFound {
		t.Fatalf("closed list subscribe status = %d, want 404", gotClosed.code)
	}
	if gotClosed.code != gotUnknown.code || gotClosed.body != gotUnknown.body {
		t.Fatalf("closed-list response differs from unknown-list response (must be identical):\nclosed=%q\nunknown=%q",
			gotClosed.body, gotUnknown.body)
	}

	if _, err := st.Meta().GetMailingListMemberByAddress(context.Background(), ml.ID, "someone@example.net"); err == nil {
		t.Fatalf("closed-policy subscribe must not create a member row")
	}
}

// TestPublicServer_Subscribe_Open_CreatesPendingAndEmailsConfirm is the
// REQ-MLIST-61 core flow: a fresh subscribe on an open list creates a
// pending member, delivers no list mail, and emails a signed confirm
// token whose link the confirm endpoint accepts.
func TestPublicServer_Subscribe_Open_CreatesPendingAndEmailsConfirm(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)

	got := doSubscribe(t, srv.Handler(), ml.ID, "NewSub@Example.NET")
	if got.code != http.StatusAccepted {
		t.Fatalf("subscribe status = %d, want 202; body=%s", got.code, got.body)
	}

	member, err := st.Meta().GetMailingListMemberByAddress(context.Background(), ml.ID, "newsub@example.net")
	if err != nil {
		t.Fatalf("GetMailingListMemberByAddress: %v", err)
	}
	if member.State != store.MailingListMemberPending {
		t.Fatalf("member State = %q, want pending", member.State)
	}

	body := queuedBodyForRecipient(t, st, "newsub@example.net")
	confirmURL := extractConfirmURL(t, body)
	target := strings.TrimPrefix(confirmURL, testPublicBaseURL)

	// GET the landing page: must not mutate.
	getRec := doRequest(t, srv.Handler(), http.MethodGet, target, "", "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET confirm status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	stillPending, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if stillPending.State != store.MailingListMemberPending {
		t.Fatalf("GET confirm mutated state to %q -- GET must be safe", stillPending.State)
	}

	// POST confirms.
	postRec := doRequest(t, srv.Handler(), http.MethodPost, target, "", "")
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST confirm status = %d, want 200; body=%s", postRec.Code, postRec.Body.String())
	}
	activated, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if activated.State != store.MailingListMemberActive {
		t.Fatalf("member State after confirm = %q, want active", activated.State)
	}

	logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.member.subscribe_confirmed"})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("subscribe_confirmed audit rows = %d, want 1", len(logs))
	}
	_ = ts
}

// TestPublicServer_Subscribe_RequestApproval_ConfirmGoesToAwaitingApproval
// is the REQ-MLIST-62 flow: confirming on a request-approval list moves
// pending -> awaiting-approval, NOT active, and notifies the owner.
func TestPublicServer_Subscribe_RequestApproval_ConfirmGoesToAwaitingApproval(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeRequestApproval
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}

	clk := clock.NewFake(time.Now())
	srv, _ := newTestPublicServer(t, st, clk)

	got := doSubscribe(t, srv.Handler(), ml.ID, "wannabe@example.net")
	if got.code != http.StatusAccepted {
		t.Fatalf("subscribe status = %d, want 202", got.code)
	}
	body := queuedBodyForRecipient(t, st, "wannabe@example.net")
	confirmURL := extractConfirmURL(t, body)
	target := strings.TrimPrefix(confirmURL, testPublicBaseURL)

	rec := doRequest(t, srv.Handler(), http.MethodPost, target, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST confirm status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	member, err := st.Meta().GetMailingListMemberByAddress(context.Background(), ml.ID, "wannabe@example.net")
	if err != nil {
		t.Fatalf("GetMailingListMemberByAddress: %v", err)
	}
	if member.State != store.MailingListMemberAwaitingApproval {
		t.Fatalf("member State after confirm = %q, want awaiting-approval (request-approval must not skip straight to active)", member.State)
	}

	// The owner is notified.
	owner, err := st.Meta().GetPrincipalByID(context.Background(), ml.OwnerID)
	if err != nil {
		t.Fatalf("GetPrincipalByID(owner): %v", err)
	}
	ownerBody := queuedBodyForRecipient(t, st, owner.CanonicalEmail)
	if !strings.Contains(ownerBody, "wannabe@example.net") {
		t.Fatalf("owner notification does not mention the requesting address:\n%s", ownerBody)
	}

	// Operator approval (mirrors the admin REST PATCH state=active path):
	// ReactivateMailingListMember is exactly what handlePatchMailingListMember
	// calls for a PATCH {state: active}.
	if err := st.Meta().ReactivateMailingListMember(context.Background(), member.ID); err != nil {
		t.Fatalf("ReactivateMailingListMember (approve): %v", err)
	}
	approved, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if approved.State != store.MailingListMemberActive {
		t.Fatalf("member State after approval = %q, want active", approved.State)
	}
}

// TestPublicServer_Subscribe_NoEnumeration: the HTTP response from
// POST /subscribe on an open list must be byte-identical regardless of
// whether the address is brand new, already active, already pending,
// already suspended, or already unsubscribed -- REQ-MLIST-61 combined
// with the S2 unsubscribe surface's own no-enumeration stance
// (publicserver.go's header comment) extends to this endpoint too.
func TestPublicServer_Subscribe_NoEnumeration(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	mustAddExternalMember(t, st, ml.ID, "already-active@example.net", store.MailingListMemberActive)
	mustAddExternalMember(t, st, ml.ID, "already-pending@example.net", store.MailingListMemberPending)
	mustAddExternalMember(t, st, ml.ID, "already-suspended@example.net", store.MailingListMemberSuspended)
	mustAddExternalMember(t, st, ml.ID, "already-unsubscribed@example.net", store.MailingListMemberUnsubscribed)

	clk := clock.NewFake(time.Now())
	srv, _ := newTestPublicServer(t, st, clk)
	h := srv.Handler()

	addrs := []string{
		"brand-new@example.net",
		"already-active@example.net",
		"already-pending@example.net",
		"already-suspended@example.net",
		"already-unsubscribed@example.net",
	}
	var first *httpResponseRecorderResult
	for _, addr := range addrs {
		got := doSubscribe(t, h, ml.ID, addr)
		if first == nil {
			first = got
			continue
		}
		if got.code != first.code || got.body != first.body {
			t.Fatalf("subscribe response for %q differs from the first (%q): code=%d body=%q vs code=%d body=%q -- this leaks membership state",
				addr, addrs[0], got.code, got.body, first.code, first.body)
		}
	}
}

// TestPublicServer_Subscribe_RateLimited_SilentlyDebounced: repeatedly
// subscribing the same address on an open list mints at most
// confirmSendLimit confirmation emails per confirmSendWindow -- an
// attacker cannot use the subscribe endpoint to mail-bomb a victim
// address, and the HTTP response never signals that the send was
// suppressed (still identical to every other call).
func TestPublicServer_Subscribe_RateLimited_SilentlyDebounced(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}

	clk := clock.NewFake(time.Now())
	srv, _ := newTestPublicServer(t, st, clk)
	h := srv.Handler()

	const attempts = 8 // well beyond confirmSendLimit
	var responses []*httpResponseRecorderResult
	for i := 0; i < attempts; i++ {
		responses = append(responses, doSubscribe(t, h, ml.ID, "victim@example.net"))
	}
	for i, r := range responses {
		if r.code != http.StatusAccepted || r.body != responses[0].body {
			t.Fatalf("attempt %d response differs (code=%d body=%q) -- rate limiting must not be visible on the wire", i, r.code, r.body)
		}
	}

	items, err := st.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	sent := 0
	for _, it := range items {
		if it.RcptTo == "victim@example.net" {
			sent++
		}
	}
	if sent == 0 {
		t.Fatalf("expected at least one confirmation email to be sent")
	}
	if sent >= attempts {
		t.Fatalf("sent %d confirmation emails for %d subscribe attempts; want the rate limit to have suppressed most of them", sent, attempts)
	}
}

// TestPublicServer_Confirm_TokenPurposeMismatch_Rejected asserts the
// cross-purpose case explicitly: a token minted for Unsubscribe must
// not work at /confirm, and a token minted for Confirm must not work
// at /unsubscribe.
func TestPublicServer_Confirm_TokenPurposeMismatch_Rejected(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberPending)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	h := srv.Handler()

	unsubToken, err := ts.Sign(maillist.TokenPurposeUnsubscribe, ml.ID, member.ID, clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Sign(Unsubscribe): %v", err)
	}
	confirmToken, err := ts.Sign(maillist.TokenPurposeConfirm, ml.ID, member.ID, clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Sign(Confirm): %v", err)
	}

	// An Unsubscribe-purpose token must not work at /confirm.
	rec := doRequest(t, h, http.MethodPost, confirmTarget(ml.ID, unsubToken), "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Unsubscribe token at /confirm status = %d, want 403", rec.Code)
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberPending {
		t.Fatalf("member State = %q after rejected cross-purpose confirm, want unchanged pending", got.State)
	}

	// A Confirm-purpose token must not work at /unsubscribe.
	unsubTarget := fmt.Sprintf("/lists/%d/unsubscribe?token=%s", uint64(ml.ID), confirmToken)
	rec = doRequest(t, h, http.MethodPost, unsubTarget, "List-Unsubscribe=One-Click", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Confirm token at /unsubscribe status = %d, want 403", rec.Code)
	}
	got, err = st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberPending {
		t.Fatalf("member State = %q after rejected cross-purpose unsubscribe, want unchanged pending", got.State)
	}
}

// TestPublicServer_Confirm_CrossListRejected mirrors
// TestPublicServer_CrossListIsolation for the confirm token.
func TestPublicServer_Confirm_CrossListRejected(t *testing.T) {
	st := openSQLiteStore(t)
	listX := mustInsertList(t, st, "listx@example.test", false)
	listY := mustInsertList(t, st, "listy@example.test", false)
	memberX := mustAddExternalMember(t, st, listX.ID, "member@example.net", store.MailingListMemberPending)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	token, err := ts.Sign(maillist.TokenPurposeConfirm, listX.ID, memberX.ID, clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	rec := doRequest(t, srv.Handler(), http.MethodPost, confirmTarget(listY.ID, token), "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-list confirm status = %d, want 403", rec.Code)
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), memberX.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberPending {
		t.Fatalf("member State = %q, want unaffected pending", got.State)
	}
}

// TestPublicServer_Confirm_ExpiredTokenRejected mirrors
// TestPublicServer_ExpiredToken_Refused for the confirm token.
func TestPublicServer_Confirm_ExpiredTokenRejected(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberPending)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	srv, ts := newTestPublicServer(t, st, clk)
	token, err := ts.Sign(maillist.TokenPurposeConfirm, ml.ID, member.ID, start.Add(-time.Second))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	rec := doRequest(t, srv.Handler(), http.MethodPost, confirmTarget(ml.ID, token), "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expired confirm token status = %d, want 403", rec.Code)
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberPending {
		t.Fatalf("member State = %q, want unaffected pending", got.State)
	}
}

// TestPublicServer_Confirm_TamperedTokenRejected mirrors
// TestPublicServer_TamperedToken_Refused for the confirm token.
func TestPublicServer_Confirm_TamperedTokenRejected(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberPending)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	token, err := ts.Sign(maillist.TokenPurposeConfirm, ml.ID, member.ID, clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	tampered := token[:len(token)-1] + flipChar(token[len(token)-1])

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := doRequest(t, srv.Handler(), method, confirmTarget(ml.ID, tampered), "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s tampered confirm token status = %d, want 403", method, rec.Code)
		}
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberPending {
		t.Fatalf("member State = %q, want unaffected pending", got.State)
	}
}

// TestPublicServer_Confirm_SuspendedMemberReactivates verifies
// REQ-MLIST-55's "or (from S3) by the member re-confirming via the
// subscription flow": a bounce-suspended member who re-confirms is
// reactivated with its bounce score reset, on an open list.
func TestPublicServer_Confirm_SuspendedMemberReactivates(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	member := mustAddExternalMember(t, st, ml.ID, "bouncy@example.net", store.MailingListMemberActive)
	if _, err := st.Meta().RecordMailingListMemberBounce(context.Background(), member.ID, time.Now(), 1.0, time.Hour); err != nil {
		t.Fatalf("RecordMailingListMemberBounce: %v", err)
	}
	if _, err := st.Meta().SuspendMailingListMemberIfActive(context.Background(), member.ID); err != nil {
		t.Fatalf("SuspendMailingListMemberIfActive: %v", err)
	}

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	// Confirming from the suspended state uses the same token shape a
	// fresh subscribe/resubscribe request would mint.
	token, err := ts.Sign(maillist.TokenPurposeConfirm, ml.ID, member.ID, clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	rec := doRequest(t, srv.Handler(), http.MethodPost, confirmTarget(ml.ID, token), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST confirm status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberActive {
		t.Fatalf("member State = %q, want active", got.State)
	}
	if got.BounceScore != 0 {
		t.Fatalf("BounceScore = %v, want reset to 0", got.BounceScore)
	}
}

// TestPublicServer_Confirm_PolicyChangedToClosed_Refuses: if the list's
// subscribe_policy was changed to closed between the subscribe request
// and the confirm click, the confirm must refuse rather than silently
// activating against a policy that no longer allows self-service
// subscription.
func TestPublicServer_Confirm_PolicyChangedToClosed_Refuses(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberPending)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	token, err := ts.Sign(maillist.TokenPurposeConfirm, ml.ID, member.ID, clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Operator flips the policy back to closed before the click.
	ml.SubscribePolicy = store.MailingListSubscribeClosed
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList (close): %v", err)
	}

	rec := doRequest(t, srv.Handler(), http.MethodPost, confirmTarget(ml.ID, token), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST confirm status = %d, want 200 (renders a refusal notice, not an HTTP error); body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberPending {
		t.Fatalf("member State = %q, want unchanged pending (closed policy must refuse activation)", got.State)
	}
}
