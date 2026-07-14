package protoadmin_test

// mlist_moderation_test.go — REQ-MLIST-80 / REQ-AC-41 admin REST tests
// (issue #189, the moderation v2 milestone): the held-post queue read
// surface, approve/reject/discard, the list:moderator grant, and the
// authorization boundary between "member" / "list:moderator" /
// "list:owner". Business-logic correctness (fan-out on approve, blob
// dedup, never-fan-out on reject/discard) is exercised in
// internal/maillist's own test suite; these tests exercise the REST
// wiring and authz on top of it end to end against the same httptest
// harness the rest of protoadmin's mailing-list tests use.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

func discardModerationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newMlistModerationTestEnv builds a mlistTestEnv whose server has a REAL
// *maillist.Expander (backed by a real outbound queue.Queue over the same
// store) wired as MailingListModerator, so POST .../held/{hid}/approve
// exercises the actual fan-out path rather than a stub.
func newMlistModerationTestEnv(t *testing.T, be submissionBackend, adminEmail string) (*mlistTestEnv, *maillist.Expander) {
	t.Helper()
	h, _ := testharness.Start(t, testharness.Options{
		Store: be.fs,
		Clock: be.clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(be.fs.Meta(), nil, be.clk, nil)
	rp := directoryoidc.New(be.fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, be.clk)
	q := queue.New(queue.Options{Store: be.fs, Logger: discardModerationLogger(), Clock: be.clk})
	exp := maillist.NewExpander(be.fs.Meta(), q, nil, be.clk, discardModerationLogger())
	exp.Blobs = be.fs.Blobs()
	srv := protoadmin.NewServer(be.fs, dir, rp, nil, be.clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		MailingListModerator:    exp,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	hh := &harness{
		t: t, h: h, srv: srv, client: client, baseURL: base,
		clk: be.clk, dir: dir, rp: rp,
	}
	ctx := context.Background()
	id, key := hh.bootstrap(adminEmail)
	p, err := hh.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(id))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := hh.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	return &mlistTestEnv{h: hh, adminID: id, adminKey: key}, exp
}

// postToModeratedList directly inserts a held post for l via a real
// Expand call (mirroring what an SMTP post to a `moderated` list
// produces), bypassing SMTP transport itself -- this file's tests are
// about the REST moderation surface, not mail ingestion, which
// internal/maillist's own tests already cover end to end.
func postToModeratedList(t *testing.T, exp *maillist.Expander, ml store.MailingList, from string) store.MailingListHeldPostID {
	t.Helper()
	raw := "From: " + from + "\r\n" +
		"To: " + ml.PostingAddress + "\r\n" +
		"Subject: moderation REST test\r\n" +
		"Message-ID: <mod-rest-test@sender.test>\r\n" +
		"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
		"\r\n" +
		"Body.\r\n"
	msg, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: msg, Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Held {
		t.Fatalf("Expand result = %+v, want Held=true (list must be posting_policy=moderated)", res)
	}
	return res.HeldPostID
}

// TestMailingListModeration_OwnerCanApprove_MemberCannot is the
// REQ-AC-41 authorization boundary: the list owner can approve a held
// post; a principal with no grant on the list at all (a "member" in the
// sense the ticket uses -- an authenticated admin-flagged principal
// with no list-scoped authority) gets 403.
func TestMailingListModeration_OwnerCanApprove_MemberCannot(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingListModeration_OwnerCanApprove_MemberCannot(t, be) })
	}
}

func testMailingListModeration_OwnerCanApprove_MemberCannot(t *testing.T, be submissionBackend) {
	e, exp := newMlistModerationTestEnv(t, be, "sa-mod@example.test")
	e.insertLocalDomain(t, "domain-mod.example")
	list := e.createList(t, "modlist@domain-mod.example", "Mod List")
	listID := idOf(t, list)
	res, buf := e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"posting_policy": "moderated"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set posting_policy=moderated: %d: %s", res.StatusCode, buf)
	}
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"external_address": "recipient@example.net"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add member: %d: %s", res.StatusCode, buf)
	}

	ml, err := e.h.Store().GetMailingList(context.Background(), store.MailingListID(listID))
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}
	heldID := postToModeratedList(t, exp, ml, "poster@sender.test")

	_, noGrantKey := e.createOperator(t, "no-grant-mod@example.test", "")

	// A principal with no grant on this list gets 403 on every held-post
	// action -- it must not be able to enumerate, approve, reject, or
	// discard.
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/held", listID), noGrantKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant GET held queue: status=%d; want 403", res.StatusCode)
	}
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/held/%d", listID, heldID), noGrantKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant GET held post: status=%d; want 403", res.StatusCode)
	}
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/held/%d/approve", listID, heldID), noGrantKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant approve: status=%d; want 403", res.StatusCode)
	}
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/held/%d/reject", listID, heldID), noGrantKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant reject: status=%d; want 403", res.StatusCode)
	}
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/held/%d/discard", listID, heldID), noGrantKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant discard: status=%d; want 403", res.StatusCode)
	}

	// The list owner (the super-admin caller who created the list) CAN
	// see the queue and approve.
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/held?status=pending", listID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("owner GET held queue: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("held queue = %d items, want 1: %s", len(page.Items), buf)
	}

	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/held/%d/approve", listID, heldID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("owner approve: %d: %s", res.StatusCode, buf)
	}
	var decided map[string]any
	if err := json.Unmarshal(buf, &decided); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decided["status"] != "approved" {
		t.Fatalf("decided status = %v, want approved", decided["status"])
	}

	items, err := e.h.Store().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("approve did not fan out: 0 queue items")
	}

	// A second approve on the same (now-decided) post is a conflict, not
	// a silent re-fan-out.
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/held/%d/approve", listID, heldID), e.adminKey, nil)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("second approve: status=%d; want 409", res.StatusCode)
	}
}

// TestMailingListModeration_ModeratorGrant_CanActionButNotConfigure is
// REQ-AC-41 exactly: a principal holding ONLY a list:moderator grant can
// approve/reject/discard held posts and read the roster, but PATCHing
// the list config or writing the roster is refused (403) -- the grant
// carries no config or roster-write authority.
func TestMailingListModeration_ModeratorGrant_CanActionButNotConfigure(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingListModeration_ModeratorGrant_CanActionButNotConfigure(t, be) })
	}
}

func testMailingListModeration_ModeratorGrant_CanActionButNotConfigure(t *testing.T, be submissionBackend) {
	e, exp := newMlistModerationTestEnv(t, be, "sa-mod2@example.test")
	e.insertLocalDomain(t, "domain-mod2.example")
	list := e.createList(t, "modlist2@domain-mod2.example", "Mod List 2")
	listID := idOf(t, list)
	res, buf := e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"posting_policy": "moderated"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set posting_policy=moderated: %d: %s", res.StatusCode, buf)
	}
	ml, err := e.h.Store().GetMailingList(context.Background(), store.MailingListID(listID))
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}

	modID, modKey := e.createOperator(t, "moderator@example.test", "")

	// Before the grant: 403 on the held queue.
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/held", listID), modKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("pre-grant GET held queue: status=%d; want 403", res.StatusCode)
	}

	// Owner assigns the list:moderator grant.
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/moderators", listID), e.adminKey,
		map[string]any{"principal_id": modID})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("grant moderator: %d: %s", res.StatusCode, buf)
	}

	heldID := postToModeratedList(t, exp, ml, "poster2@sender.test")

	// After the grant: the moderator can list, view, and reject a held
	// post...
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/held", listID), modKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("moderator GET held queue: %d: %s", res.StatusCode, buf)
	}
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/held/%d/reject", listID, heldID), modKey,
		map[string]any{"note": "off-topic"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("moderator reject: %d: %s", res.StatusCode, buf)
	}

	// ...and can read the roster (REQ-AC-41: "read of the list's
	// roster")...
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/members", listID), modKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("moderator read roster: %d: %s", res.StatusCode, buf)
	}

	// ...but cannot edit list config...
	res, _ = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), modKey,
		map[string]any{"display_name": "hijacked"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("moderator PATCH list config: status=%d; want 403", res.StatusCode)
	}

	// ...cannot write the roster...
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), modKey,
		map[string]any{"external_address": "sneaky@example.net"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("moderator write roster: status=%d; want 403", res.StatusCode)
	}

	// ...and cannot mint another moderator (no config-write authority).
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/moderators", listID), modKey,
		map[string]any{"principal_id": e.adminID})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("moderator grant another moderator: status=%d; want 403", res.StatusCode)
	}
}

// TestMailingListModeration_DiscardNeverFansOut confirms the REST
// discard endpoint's disposal: no queue rows are ever created for a
// discarded held post.
func TestMailingListModeration_DiscardNeverFansOut(t *testing.T) {
	e, exp := newMlistModerationTestEnv(t, openSubmissionBackends(t)[0], "sa-mod3@example.test")
	e.insertLocalDomain(t, "domain-mod3.example")
	list := e.createList(t, "modlist3@domain-mod3.example", "Mod List 3")
	listID := idOf(t, list)
	res, buf := e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"posting_policy": "moderated"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set posting_policy=moderated: %d: %s", res.StatusCode, buf)
	}
	ml, err := e.h.Store().GetMailingList(context.Background(), store.MailingListID(listID))
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}
	heldID := postToModeratedList(t, exp, ml, "poster3@sender.test")

	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/held/%d/discard", listID, heldID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("discard: %d: %s", res.StatusCode, buf)
	}
	var decided map[string]any
	if err := json.Unmarshal(buf, &decided); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decided["status"] != "discarded" {
		t.Fatalf("decided status = %v, want discarded", decided["status"])
	}

	items, err := e.h.Store().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("queue items after discard = %d, want 0", len(items))
	}

	// GET .../held/{hid}/raw still works post-decision (the moderator
	// can review what was discarded).
	res, rawBody := e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/held/%d/raw", listID, heldID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET raw: %d: %s", res.StatusCode, rawBody)
	}
	if res.Header.Get("Content-Type") != "message/rfc822" {
		t.Errorf("raw Content-Type = %q, want message/rfc822", res.Header.Get("Content-Type"))
	}
}
