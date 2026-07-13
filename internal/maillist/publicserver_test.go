package maillist_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// publicserver_test.go exercises the REQ-MLIST-57/58/59 public,
// token-authorised subscriber routes (issue #184, part 3): the one-click
// unsubscribe endpoint, the self-service management page, and above all
// the GET-is-safe guarantee that makes the management page prefetch-safe.

const testPublicBaseURL = "https://mail.example.test"

func newTestPublicServer(t *testing.T, st store.Store, clk clock.Clock) (*maillist.PublicServer, *maillist.TokenSigner) {
	t.Helper()
	ts := maillist.NewTokenSigner(testDataKey())
	return maillist.NewPublicServer(st.Meta(), ts, st.Blobs(), testPublicBaseURL, clk, discardLogger()), ts
}

func doRequest(t *testing.T, h http.Handler, method, target string, body string, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *strings.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	} else {
		reqBody = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reqBody)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestPublicServer_GetIsSafe is the PRIMARY hazard this ticket calls
// out: MUAs and link-scanners prefetch List-Unsubscribe URLs, so GET
// MUST NEVER mutate membership. Drives many GETs against the same token
// URL and asserts the member's state never changes and no unsubscribe
// audit row is ever written.
func TestPublicServer_GetIsSafe(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", false)
		ml.SubscribePolicy = store.MailingListSubscribeOpen
		if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
			t.Fatalf("UpdateMailingList: %v", err)
		}
		member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

		clk := clock.NewFake(time.Now())
		srv, ts := newTestPublicServer(t, st, clk)
		u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
		if err != nil {
			t.Fatalf("UnsubscribeURL: %v", err)
		}
		target := strings.TrimPrefix(u, testPublicBaseURL)

		h := srv.Handler()
		for i := 0; i < 10; i++ {
			rec := doRequest(t, h, http.MethodGet, target, "", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET #%d status = %d, want 200; body=%s", i, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "member@example.net") {
				t.Fatalf("GET #%d body missing subscribed address:\n%s", i, rec.Body.String())
			}
			got, gerr := st.Meta().GetMailingListMember(context.Background(), member.ID)
			if gerr != nil {
				t.Fatalf("GetMailingListMember: %v", gerr)
			}
			if got.State != store.MailingListMemberActive {
				t.Fatalf("GET #%d mutated member state to %q -- GET MUST be safe against MUA prefetch", i, got.State)
			}
		}

		logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.member.unsubscribe"})
		if err != nil {
			t.Fatalf("ListAuditLog: %v", err)
		}
		if len(logs) != 0 {
			t.Fatalf("unexpected unsubscribe audit rows after GET-only traffic: %+v", logs)
		}
	})
}

// TestPublicServer_OneClickPost_Unsubscribes is the REQ-MLIST-57 e2e:
// the exact RFC 8058 body/content-type, no session, no cookie, no API
// key -- and the addressed member transitions to unsubscribed.
func TestPublicServer_OneClickPost_Unsubscribes(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	target := strings.TrimPrefix(u, testPublicBaseURL)

	rec := doRequest(t, srv.Handler(), http.MethodPost, target,
		"List-Unsubscribe=One-Click", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberUnsubscribed {
		t.Fatalf("member State = %q, want unsubscribed", got.State)
	}

	logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.member.unsubscribe"})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("audit rows = %d, want 1: %+v", len(logs), logs)
	}
}

// TestPublicServer_OneClickPost_Idempotent verifies a second one-click
// POST (an MUA retry) is a harmless no-op: still 200, member stays
// unsubscribed, no second audit row.
func TestPublicServer_OneClickPost_Idempotent(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	target := strings.TrimPrefix(u, testPublicBaseURL)

	for i := 0; i < 2; i++ {
		rec := doRequest(t, srv.Handler(), http.MethodPost, target,
			"List-Unsubscribe=One-Click", "application/x-www-form-urlencoded")
		if rec.Code != http.StatusOK {
			t.Fatalf("POST #%d status = %d, want 200", i, rec.Code)
		}
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberUnsubscribed {
		t.Fatalf("member State = %q, want unsubscribed", got.State)
	}
	logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.member.unsubscribe"})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1 (idempotent retry must not duplicate)", len(logs))
	}
}

// TestPublicServer_TamperedToken_Refused: a forged/garbled token is
// refused (fail closed) and touches no member, on both GET and POST.
func TestPublicServer_TamperedToken_Refused(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	// Flip the token's FIRST character to invalidate the AEAD tag. The
	// first character's six bits are always significant, so the sealed
	// bytes always change. The last character is not a sound choice: when
	// the sealed length is not a multiple of three it carries unused low
	// bits, and flipping only those leaves the decoded bytes -- and hence
	// the token -- identical.
	tampered := strings.TrimPrefix(u, testPublicBaseURL)
	tok := tampered[strings.Index(tampered, "token=")+len("token="):]
	tampered = strings.Replace(tampered, "token="+tok, "token="+flipChar(tok[0])+tok[1:], 1)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		body, ct := "", ""
		if method == http.MethodPost {
			body, ct = "List-Unsubscribe=One-Click", "application/x-www-form-urlencoded"
		}
		rec := doRequest(t, srv.Handler(), method, tampered, body, ct)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s tampered token status = %d, want 403; body=%s", method, rec.Code, rec.Body.String())
		}
	}

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberActive {
		t.Fatalf("member State = %q, want unaffected active (tampered token must touch no member)", got.State)
	}
}

func flipChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

// TestPublicServer_CrossMemberIsolation: a token minted for member A
// unsubscribes A only -- sibling member B on the same list is untouched.
func TestPublicServer_CrossMemberIsolation(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	memberA := mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)
	memberB := mustAddExternalMember(t, st, ml.ID, "b@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, memberA.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	target := strings.TrimPrefix(u, testPublicBaseURL)
	rec := doRequest(t, srv.Handler(), http.MethodPost, target,
		"List-Unsubscribe=One-Click", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", rec.Code)
	}

	gotA, err := st.Meta().GetMailingListMember(context.Background(), memberA.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember(A): %v", err)
	}
	if gotA.State != store.MailingListMemberUnsubscribed {
		t.Fatalf("member A State = %q, want unsubscribed", gotA.State)
	}
	gotB, err := st.Meta().GetMailingListMember(context.Background(), memberB.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember(B): %v", err)
	}
	if gotB.State != store.MailingListMemberActive {
		t.Fatalf("member B State = %q, want unaffected active (a token for A must never act on B)", gotB.State)
	}
}

// TestPublicServer_CrossListIsolation: a token minted for list X must
// not act against list Y's URL, even though both lists exist and the
// token is otherwise well-formed.
func TestPublicServer_CrossListIsolation(t *testing.T) {
	st := openSQLiteStore(t)
	listX := mustInsertList(t, st, "listx@example.test", false)
	listY := mustInsertList(t, st, "listy@example.test", false)
	memberX := mustAddExternalMember(t, st, listX.ID, "member@example.net", store.MailingListMemberActive)
	memberY := mustAddExternalMember(t, st, listY.ID, "member@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	// Mint a token for (listX, memberX), then splice it onto listY's URL.
	uForX, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, listX, memberX.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	q := uForX[strings.Index(uForX, "?"):]
	crossListTarget := "/lists/" + itoaListID(listY.ID) + "/unsubscribe" + q

	rec := doRequest(t, srv.Handler(), http.MethodPost, crossListTarget,
		"List-Unsubscribe=One-Click", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-list token status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	gotX, err := st.Meta().GetMailingListMember(context.Background(), memberX.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember(X): %v", err)
	}
	if gotX.State != store.MailingListMemberActive {
		t.Fatalf("member on list X State = %q, want unaffected active", gotX.State)
	}
	gotY, err := st.Meta().GetMailingListMember(context.Background(), memberY.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember(Y): %v", err)
	}
	if gotY.State != store.MailingListMemberActive {
		t.Fatalf("member on list Y State = %q, want unaffected active (a token for list X must never act on list Y)", gotY.State)
	}
}

// TestPublicServer_ExpiredToken_Refused: an expired token is refused,
// touching no member.
func TestPublicServer_ExpiredToken_Refused(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	srv, ts := newTestPublicServer(t, st, clk)
	// Mint a token that already expired one second ago.
	token, err := ts.Sign(maillist.TokenPurposeUnsubscribe, ml.ID, member.ID, start.Add(-time.Second))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	target := "/lists/" + itoaListID(ml.ID) + "/unsubscribe?token=" + token

	rec := doRequest(t, srv.Handler(), http.MethodGet, target, "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expired token GET status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, srv.Handler(), http.MethodPost, target,
		"List-Unsubscribe=One-Click", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expired token POST status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberActive {
		t.Fatalf("member State = %q, want unaffected active", got.State)
	}
}

// TestPublicServer_ManagePage_NoEnumeration: the rendered page never
// mentions a sibling member's address -- the token's authority is
// scoped to exactly the addressed member (REQ-MLIST-59).
func TestPublicServer_ManagePage_NoEnumeration(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)
	mustAddExternalMember(t, st, ml.ID, "sibling-secret@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	target := strings.TrimPrefix(u, testPublicBaseURL)
	rec := doRequest(t, srv.Handler(), http.MethodGet, target, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sibling-secret") {
		t.Fatalf("management page enumerates a sibling member's address:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "member@example.net") {
		t.Fatalf("management page missing the addressed member's own address:\n%s", rec.Body.String())
	}
}

// TestPublicServer_Resubscribe_DoesNotReactivateDirectly is the
// REQ-MLIST-59 e2e: requesting resubscribe from the page leaves the
// member's state UNCHANGED (never directly active again) and instead
// enqueues a confirmation email to the member's own address.
func TestPublicServer_Resubscribe_DoesNotReactivateDirectly(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberUnsubscribed)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	target := strings.TrimPrefix(u, testPublicBaseURL)

	rec := doRequest(t, srv.Handler(), http.MethodPost, target,
		(url.Values{"action": {"resubscribe"}}).Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resubscribe status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberUnsubscribed {
		t.Fatalf("member State = %q, want STILL unsubscribed -- resubscribe must never reactivate directly", got.State)
	}

	items, err := st.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	found := false
	for _, it := range items {
		if it.RcptTo == "member@example.net" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no confirmation email enqueued to member@example.net; items=%+v", items)
	}

	logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.member.resubscribe_requested"})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("audit rows = %d, want 1: %+v", len(logs), logs)
	}
}

// TestPublicServer_Resubscribe_ClosedPolicy_NoOp verifies a list with
// the S1 default subscribe_policy=closed does not offer resubscribe:
// no email enqueued, member state unchanged.
func TestPublicServer_Resubscribe_ClosedPolicy_NoOp(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false) // SubscribePolicy defaults to closed.
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberUnsubscribed)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	target := strings.TrimPrefix(u, testPublicBaseURL)

	rec := doRequest(t, srv.Handler(), http.MethodPost, target,
		(url.Values{"action": {"resubscribe"}}).Encode(), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resubscribe status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberUnsubscribed {
		t.Fatalf("member State = %q, want unchanged unsubscribed", got.State)
	}
	items, err := st.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("queue items = %d, want 0 (closed subscribe policy must not email a confirmation)", len(items))
	}
}

// TestPublicServer_UnknownAction_BadRequest: a POST body naming neither
// the RFC 8058 one-click marker nor a recognised action is refused.
func TestPublicServer_UnknownAction_BadRequest(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	srv, ts := newTestPublicServer(t, st, clk)
	u, err := maillist.UnsubscribeURL(testPublicBaseURL, ts, ml, member.ID, clk.Now())
	if err != nil {
		t.Fatalf("UnsubscribeURL: %v", err)
	}
	target := strings.TrimPrefix(u, testPublicBaseURL)

	rec := doRequest(t, srv.Handler(), http.MethodPost, target, "foo=bar", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unrecognized action status = %d, want 400", rec.Code)
	}
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberActive {
		t.Fatalf("member State = %q, want unaffected active", got.State)
	}
}
