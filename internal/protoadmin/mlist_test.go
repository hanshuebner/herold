package protoadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// Hosted mailing lists, Stage 1 admin REST (epic #183,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-40a/41/42).
// These tests exercise the REST surface end to end against the same
// httptest harness the rest of protoadmin's tests use, on both SQLite and
// (when HEROLD_PG_DSN is set) Postgres via openSubmissionBackends; the
// authorization matrix (TestMailingLists_AuthzMatrix) is the highest-risk
// part of this ticket per REQ-MLIST-05 and is asserted explicitly and
// fail-closed.

// newHarnessWithStore builds a harness (server_test.go's type) against an
// externally supplied store/clock, so mailing-list tests can run the same
// scenario on both openSubmissionBackends() entries instead of always
// getting a fresh SQLite store the way newHarness does.
func newHarnessWithStore(t *testing.T, fs store.Store, clk *clock.FakeClock) *harness {
	t.Helper()
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &harness{
		t: t, h: h, srv: srv, client: client, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
}

// mlistTestEnv bundles a superadmin key and a helper for creating a local
// domain, shared by every test in this file.
type mlistTestEnv struct {
	h        *harness
	adminID  uint64
	adminKey string
}

func newMlistTestEnv(t *testing.T, be submissionBackend, adminEmail string) *mlistTestEnv {
	t.Helper()
	h := newHarnessWithStore(t, be.fs, be.clk)
	ctx := context.Background()
	id, key := h.bootstrap(adminEmail)
	p, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(id))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	return &mlistTestEnv{h: h, adminID: id, adminKey: key}
}

// insertLocalDomain inserts a locally hosted domain AND provisions it
// with an active DKIM key, mirroring an onboarded domain in production.
// A mailing list's arc_seal defaults to true (REQ-MLIST-01), and issue
// #183's config-time gate (requireActiveDKIMKeyForARCSeal) now refuses to
// create or configure an arc_seal-enabled list on a domain with no active
// key -- so every test in this file that exercises the default-arc_seal
// list-creation path needs one. Tests that specifically exercise the
// keyless-domain gate use insertLocalDomainNoKey instead.
func (e *mlistTestEnv) insertLocalDomain(t *testing.T, name string) {
	t.Helper()
	e.insertLocalDomainNoKey(t, name)
	if err := e.h.Store().UpsertDKIMKey(context.Background(), store.DKIMKey{
		Domain:        name,
		Selector:      "s1",
		Algorithm:     store.DKIMAlgorithmEd25519SHA256,
		PrivateKeyPEM: "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n", // gitleaks:allow (synthetic test fixture, not a real key)
		PublicKeyB64:  "dGVzdA==",
		Status:        store.DKIMKeyStatusActive,
	}); err != nil {
		t.Fatalf("UpsertDKIMKey(%s): %v", name, err)
	}
}

// insertLocalDomainNoKey inserts a locally hosted domain with no DKIM key
// at all, for tests exercising the REQ-MLIST-21 config-time gate itself.
func (e *mlistTestEnv) insertLocalDomainNoKey(t *testing.T, name string) {
	t.Helper()
	if err := e.h.Store().InsertDomain(context.Background(), store.Domain{Name: name, IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain(%s): %v", name, err)
	}
}

// createOperator creates an admin-flagged (non-super-admin) principal with
// an admin-scoped API key and, if domain != "", assigns it as a
// domain-scoped operator on domain (REQ-ADM-307).
func (e *mlistTestEnv) createOperator(t *testing.T, email, domain string) (uint64, string) {
	t.Helper()
	ctx := context.Background()
	pid := e.h.createPrincipal(e.adminKey, email)
	p, err := e.h.Store().GetPrincipalByID(ctx, store.PrincipalID(pid))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	p.Flags = store.PrincipalFlagAdmin
	if err := e.h.Store().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	_, key := e.h.createAPIKey(e.adminKey, pid)
	if domain != "" {
		res, buf := e.h.doRequest("POST",
			fmt.Sprintf("/api/v1/principals/%d/managed-domains", pid),
			e.adminKey, map[string]string{"domain": domain})
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("assign domain %s to %d: %d: %s", domain, pid, res.StatusCode, buf)
		}
	}
	return pid, key
}

// createList creates a mailing list as e's superadmin caller and returns
// the decoded response body.
func (e *mlistTestEnv) createList(t *testing.T, address, displayName string) map[string]any {
	t.Helper()
	res, buf := e.h.doRequest("POST", "/api/v1/lists", e.adminKey, map[string]any{
		"posting_address": address,
		"display_name":    displayName,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create list %s: %d: %s", address, res.StatusCode, buf)
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func idOf(t *testing.T, m map[string]any) uint64 {
	t.Helper()
	v, ok := m["id"].(float64)
	if !ok {
		t.Fatalf("no numeric id in %v", m)
	}
	return uint64(v)
}

// Store exposes the harness's store.Store so this file does not need to
// reach through h.h.Store repeatedly.
func (h *harness) Store() store.Metadata { return h.h.Store.Meta() }

// TestMailingLists_CRUD_SuperAdmin exercises the full list lifecycle as a
// super-admin: create, get, patch (rename, config, owner reassignment),
// and delete, including the pageDTO envelope shape on the collection
// endpoint (REQ-MLIST-40a). Runs on both SQLite and (when HEROLD_PG_DSN is
// set) Postgres.
func TestMailingLists_CRUD_SuperAdmin(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_CRUD_SuperAdmin(t, be) })
	}
}

func testMailingLists_CRUD_SuperAdmin(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-crud@example.test")
	e.insertLocalDomain(t, "lists.example")

	created := e.createList(t, "Announce@Lists.Example", "Announce")
	if created["posting_address"] != "announce@lists.example" {
		t.Errorf("posting_address = %v; want lowercased", created["posting_address"])
	}
	if created["domain"] != "lists.example" {
		t.Errorf("domain = %v; want lists.example", created["domain"])
	}
	if created["owner_principal_id"].(float64) != float64(e.adminID) {
		t.Errorf("owner_principal_id = %v; want default-to-caller %d", created["owner_principal_id"], e.adminID)
	}
	if created["arc_seal"] != true {
		t.Errorf("arc_seal = %v; want true (default)", created["arc_seal"])
	}
	if created["posting_policy"] != "open" {
		t.Errorf("posting_policy = %v; want open", created["posting_policy"])
	}
	if created["subscribe_policy"] != "closed" {
		t.Errorf("subscribe_policy = %v; want closed", created["subscribe_policy"])
	}
	if created["unsubscribe_enabled"] != true {
		t.Errorf("unsubscribe_enabled = %v; want true (default, epic #184)", created["unsubscribe_enabled"])
	}
	id := idOf(t, created)

	// GET by id.
	res, buf := e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get list: %d: %s", res.StatusCode, buf)
	}

	// GET collection: pageDTO{items,next} envelope.
	res, buf = e.h.doRequest("GET", "/api/v1/lists", e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list lists: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
		Next  *string          `json:"next"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("unmarshal page: %v", err)
	}
	if len(page.Items) != 1 || idOf(t, page.Items[0]) != id {
		t.Fatalf("GET /api/v1/lists items = %v; want exactly [list %d]", page.Items, id)
	}
	if page.Next != nil {
		t.Errorf("Next = %v; want nil (single page)", *page.Next)
	}

	// PATCH: rename (posting_address only).
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
		map[string]any{"posting_address": "ann@lists.example"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d: %s", res.StatusCode, buf)
	}
	var renamed map[string]any
	_ = json.Unmarshal(buf, &renamed)
	if renamed["posting_address"] != "ann@lists.example" {
		t.Errorf("posting_address after rename = %v", renamed["posting_address"])
	}
	if renamed["domain"] != "lists.example" {
		t.Errorf("domain after same-domain rename = %v; want unchanged", renamed["domain"])
	}

	// PATCH: config fields + subject_tag set then cleared.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
		map[string]any{"display_name": "Announcements", "subject_tag": "ANN", "arc_seal": false, "max_message_size_bytes": 1024, "unsubscribe_enabled": false})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set config: %d: %s", res.StatusCode, buf)
	}
	var configured map[string]any
	_ = json.Unmarshal(buf, &configured)
	if configured["display_name"] != "Announcements" {
		t.Errorf("display_name = %v", configured["display_name"])
	}
	if configured["subject_tag"] != "ANN" {
		t.Errorf("subject_tag = %v; want ANN", configured["subject_tag"])
	}
	if configured["arc_seal"] != false {
		t.Errorf("arc_seal = %v; want false", configured["arc_seal"])
	}
	if configured["max_message_size_bytes"].(float64) != 1024 {
		t.Errorf("max_message_size_bytes = %v; want 1024", configured["max_message_size_bytes"])
	}
	if configured["unsubscribe_enabled"] != false {
		t.Errorf("unsubscribe_enabled = %v; want false (epic #184)", configured["unsubscribe_enabled"])
	}

	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
		map[string]any{"subject_tag": ""})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear subject_tag: %d: %s", res.StatusCode, buf)
	}
	var cleared map[string]any
	_ = json.Unmarshal(buf, &cleared)
	if _, present := cleared["subject_tag"]; present {
		t.Errorf("subject_tag present after clear: %v", cleared["subject_tag"])
	}

	// PATCH: reassign owner, verify the grant transfers.
	newOwnerID, _ := e.createOperator(t, "new-owner@example.test", "")
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
		map[string]any{"owner_principal_id": newOwnerID})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reassign owner: %d: %s", res.StatusCode, buf)
	}
	grants, err := e.h.Store().ListGrantsOnResource(context.Background(), store.GrantResourceList, strconv.FormatUint(id, 10))
	if err != nil {
		t.Fatalf("ListGrantsOnResource: %v", err)
	}
	foundNewOwner := false
	for _, g := range grants {
		if g.SubjectID == newOwnerID && g.Level == store.GrantLevelOwner {
			foundNewOwner = true
		}
	}
	if !foundNewOwner {
		t.Errorf("no list:owner grant for new owner %d after PATCH: %+v", newOwnerID, grants)
	}

	// DELETE.
	res, buf = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d: %s", res.StatusCode, buf)
	}
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete: status=%d; want 404", res.StatusCode)
	}
	grants, err = e.h.Store().ListGrantsOnResource(context.Background(), store.GrantResourceList, strconv.FormatUint(id, 10))
	if err != nil {
		t.Fatalf("ListGrantsOnResource after delete: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("grants survive list delete: %+v", grants)
	}
}

// TestMailingLists_AuthzMatrix is the core risk surface of this ticket
// (REQ-MLIST-05): a super-admin manages every list; a domain-scoped
// operator manages lists only on its own domain(s) and cannot see or
// touch another domain's list; a list:owner grant governs exactly that
// one list and confers no domain-wide authority; a principal with no
// applicable grant is refused everywhere. Every negative case must be a
// 403, and cross-domain/cross-list rows must never appear in a listing.
// Runs on both SQLite and (when HEROLD_PG_DSN is set) Postgres.
func TestMailingLists_AuthzMatrix(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_AuthzMatrix(t, be) })
	}
}

func testMailingLists_AuthzMatrix(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-authz@example.test")
	e.insertLocalDomain(t, "domain-a.example")
	e.insertLocalDomain(t, "domain-b.example")

	listA := e.createList(t, "team@domain-a.example", "Team A")
	listAID := idOf(t, listA)
	listB := e.createList(t, "team@domain-b.example", "Team B")
	listBID := idOf(t, listB)

	_, opAKey := e.createOperator(t, "op-a@example.test", "domain-a.example")
	_, opBKey := e.createOperator(t, "op-b@example.test", "domain-b.example")
	ownerOnlyID, ownerOnlyKey := e.createOperator(t, "owner-only@example.test", "")
	_, noGrantKey := e.createOperator(t, "no-grant@example.test", "")

	// Reassign listB's ownership to ownerOnly so it has list:owner on
	// listB and nothing else (no domain grant at all).
	res, buf := e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listBID), e.adminKey,
		map[string]any{"owner_principal_id": ownerOnlyID})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reassign listB owner: %d: %s", res.StatusCode, buf)
	}

	// --- domain operator on its own domain: full access ---
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listAID), opAKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("opA GET listA: status=%d body=%s; want 200", res.StatusCode, buf)
	}
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listAID), opAKey,
		map[string]any{"display_name": "Team A (renamed)"})
	if res.StatusCode != http.StatusOK {
		t.Errorf("opA PATCH listA: status=%d body=%s; want 200", res.StatusCode, buf)
	}

	// --- domain operator on a FOREIGN domain: 403, and excluded from listing ---
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listBID), opAKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("opA GET listB (foreign domain): status=%d; want 403", res.StatusCode)
	}
	res, _ = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listBID), opAKey,
		map[string]any{"display_name": "hijacked"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("opA PATCH listB (foreign domain): status=%d; want 403", res.StatusCode)
	}
	res, _ = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d", listBID), opAKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("opA DELETE listB (foreign domain): status=%d; want 403", res.StatusCode)
	}
	assertListingExactly(t, e, opAKey, listAID)

	// --- opB symmetric check ---
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listAID), opBKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("opB GET listA (foreign domain): status=%d; want 403", res.StatusCode)
	}
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listBID), opBKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("opB GET listB (own domain): status=%d body=%s; want 200", res.StatusCode, buf)
	}
	assertListingExactly(t, e, opBKey, listBID)

	// --- list:owner-only (no domain grant): access to its list, 403
	// elsewhere, and an EMPTY collection listing (no domain authority to
	// list under) — the fail-closed behaviour the ticket calls out
	// explicitly.
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listBID), ownerOnlyKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("ownerOnly GET listB (list:owner grant): status=%d body=%s; want 200", res.StatusCode, buf)
	}
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listAID), ownerOnlyKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("ownerOnly GET listA (no grant on this list): status=%d; want 403", res.StatusCode)
	}
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listBID), ownerOnlyKey,
		map[string]any{"external_address": "member@example.net"})
	if res.StatusCode != http.StatusCreated {
		t.Errorf("ownerOnly add member to listB: status=%d body=%s; want 201", res.StatusCode, buf)
	}
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listAID), ownerOnlyKey,
		map[string]any{"external_address": "member@example.net"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("ownerOnly add member to listA: status=%d; want 403", res.StatusCode)
	}
	assertListingExactly(t, e, ownerOnlyKey /* nothing */)

	// --- no grant at all: 403 everywhere, empty listing, cannot create ---
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listAID), noGrantKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant GET listA: status=%d; want 403", res.StatusCode)
	}
	res, _ = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listBID), noGrantKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant GET listB: status=%d; want 403", res.StatusCode)
	}
	assertListingExactly(t, e, noGrantKey /* nothing */)
	res, _ = e.h.doRequest("POST", "/api/v1/lists", noGrantKey,
		map[string]any{"posting_address": "x@domain-a.example", "display_name": "X"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("noGrant create on domain-a: status=%d; want 403", res.StatusCode)
	}

	// --- cross-domain move: opA must not relocate listA into domain-b,
	// which it does not manage (privilege-escalation guard). ---
	res, _ = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listAID), opAKey,
		map[string]any{"posting_address": "team@domain-b.example"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("opA move listA into domain-b: status=%d; want 403", res.StatusCode)
	}
}

// assertListingExactly asserts that GET /api/v1/lists as key returns
// exactly the given ids (order-independent), or none when ids is empty.
func assertListingExactly(t *testing.T, e *mlistTestEnv, key string, ids ...uint64) {
	t.Helper()
	res, buf := e.h.doRequest("GET", "/api/v1/lists", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list lists: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[uint64]bool{}
	for _, it := range page.Items {
		got[idOf(t, it)] = true
	}
	if len(got) != len(ids) {
		t.Fatalf("listing = %v; want exactly %v", got, ids)
	}
	for _, id := range ids {
		if !got[id] {
			t.Errorf("listing missing expected id %d: got %v", id, got)
		}
	}
}

// TestMailingLists_Roster_CRUD_And_BulkImportExport exercises roster add /
// patch / remove, the cross-list member-id guard, and the bulk
// import/export round trip (REQ-MLIST-02/03/04, REQ-MLIST-40a). Runs on
// both SQLite and (when HEROLD_PG_DSN is set) Postgres.
func TestMailingLists_Roster_CRUD_And_BulkImportExport(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_Roster_CRUD_And_BulkImportExport(t, be) })
	}
}

func testMailingLists_Roster_CRUD_And_BulkImportExport(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-roster@example.test")
	e.insertLocalDomain(t, "roster.example")
	listID := idOf(t, e.createList(t, "roster@roster.example", "Roster"))
	otherListID := idOf(t, e.createList(t, "other@roster.example", "Other"))

	// Add an external-address member.
	res, buf := e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"external_address": "ext@example.net"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add external member: %d: %s", res.StatusCode, buf)
	}
	var extMember map[string]any
	_ = json.Unmarshal(buf, &extMember)
	extMemberID := idOf(t, extMember)
	if extMember["state"] != "active" || extMember["delivery_mode"] != "each" {
		t.Errorf("external member defaults = %v", extMember)
	}

	// Add a principal member.
	memberPID := e.h.createPrincipal(e.adminKey, "internal-member@example.test")
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"principal_id": memberPID})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add principal member: %d: %s", res.StatusCode, buf)
	}

	// Validation: both fields set, or neither.
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"principal_id": memberPID, "external_address": "x@example.net"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("add member both fields: status=%d; want 400", res.StatusCode)
	}
	res, _ = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("add member neither field: status=%d; want 400", res.StatusCode)
	}

	// GET roster: pageDTO envelope with both members.
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list members: %d: %s", res.StatusCode, buf)
	}
	var rosterPage struct {
		Items []map[string]any `json:"items"`
		Next  *string          `json:"next"`
	}
	if err := json.Unmarshal(buf, &rosterPage); err != nil {
		t.Fatalf("unmarshal roster page: %v", err)
	}
	if len(rosterPage.Items) != 2 {
		t.Fatalf("roster items = %d; want 2", len(rosterPage.Items))
	}

	// PATCH state.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, extMemberID), e.adminKey,
		map[string]any{"state": "suspended"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch member state: %d: %s", res.StatusCode, buf)
	}
	var patched map[string]any
	_ = json.Unmarshal(buf, &patched)
	if patched["state"] != "suspended" {
		t.Errorf("state after patch = %v", patched["state"])
	}

	// PATCH delivery_mode=nomail on an external-address member: rejected
	// (REQ-MLIST-04 requires an internal principal).
	res, _ = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, extMemberID), e.adminKey,
		map[string]any{"delivery_mode": "nomail"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("nomail on external member: status=%d; want 400", res.StatusCode)
	}

	// Cross-list guard: a member id that exists but belongs to a
	// different list must 404, not silently operate cross-list.
	res, _ = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d/members/%d", otherListID, extMemberID), e.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("delete member via wrong list: status=%d; want 404", res.StatusCode)
	}

	// DELETE (correct list), then repeat is 404.
	res, buf = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, extMemberID), e.adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete member: %d: %s", res.StatusCode, buf)
	}
	res, _ = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, extMemberID), e.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("delete member again: status=%d; want 404", res.StatusCode)
	}

	// Bulk import: a mix of valid, duplicate (conflict), and malformed
	// (invalid) entries in one request.
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members/import", listID), e.adminKey,
		map[string]any{"members": []map[string]any{
			{"external_address": "bulk1@example.net"},
			{"external_address": "bulk2@example.net", "delivery_mode": "each", "state": "active"},
			{"principal_id": memberPID}, // conflict: already a member
			{},                          // invalid: neither field set
		}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bulk import: %d: %s", res.StatusCode, buf)
	}
	var importResp struct {
		Added   int `json:"added"`
		Skipped int `json:"skipped"`
		Results []struct {
			Index  int    `json:"index"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(buf, &importResp); err != nil {
		t.Fatalf("unmarshal import response: %v", err)
	}
	if importResp.Added != 2 || importResp.Skipped != 2 {
		t.Fatalf("import added=%d skipped=%d; want 2/2 (results=%+v)", importResp.Added, importResp.Skipped, importResp.Results)
	}
	if importResp.Results[2].Status != "conflict" {
		t.Errorf("results[2].status = %q; want conflict", importResp.Results[2].Status)
	}
	if importResp.Results[3].Status != "invalid" {
		t.Errorf("results[3].status = %q; want invalid", importResp.Results[3].Status)
	}

	// Bulk export: round trip — every address added across the test
	// (principal member + the two bulk-imported external addresses; the
	// deleted extMember must NOT reappear) shows up in the export.
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/members/export", listID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export: %d: %s", res.StatusCode, buf)
	}
	var exportPage struct {
		Items []map[string]any `json:"items"`
		Next  *string          `json:"next"`
	}
	if err := json.Unmarshal(buf, &exportPage); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if exportPage.Next != nil {
		t.Errorf("export Next = %v; want nil (under cap)", *exportPage.Next)
	}
	wantAddresses := map[string]bool{"bulk1@example.net": false, "bulk2@example.net": false}
	sawPrincipal := false
	sawDeleted := false
	for _, it := range exportPage.Items {
		if addr, _ := it["external_address"].(string); addr != "" {
			if addr == "ext@example.net" {
				sawDeleted = true
			}
			if _, ok := wantAddresses[addr]; ok {
				wantAddresses[addr] = true
			}
		}
		if pid, _ := it["principal_id"].(float64); uint64(pid) == memberPID {
			sawPrincipal = true
		}
	}
	if sawDeleted {
		t.Errorf("export includes the deleted member ext@example.net")
	}
	if !sawPrincipal {
		t.Errorf("export missing the principal member")
	}
	for addr, seen := range wantAddresses {
		if !seen {
			t.Errorf("export missing bulk-imported address %s", addr)
		}
	}
}

// TestMailingLists_AuditRecords verifies REQ-MLIST-42: list config
// changes and roster edits produce audit records. Runs on both SQLite and
// (when HEROLD_PG_DSN is set) Postgres.
func TestMailingLists_AuditRecords(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_AuditRecords(t, be) })
	}
}

func testMailingLists_AuditRecords(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-audit@example.test")
	e.insertLocalDomain(t, "audit.example")

	created := e.createList(t, "audit-list@audit.example", "Audit List")
	listID := idOf(t, created)
	assertAuditAction(t, e, "mlist.create", fmt.Sprintf("mlist:%d", listID))

	res, buf := e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"display_name": "Audit List Renamed"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d: %s", res.StatusCode, buf)
	}
	assertAuditAction(t, e, "mlist.update", fmt.Sprintf("mlist:%d", listID))

	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"external_address": "audit-member@example.net"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add member: %d: %s", res.StatusCode, buf)
	}
	var member map[string]any
	_ = json.Unmarshal(buf, &member)
	memberID := idOf(t, member)
	assertAuditAction(t, e, "mlist.member.add", fmt.Sprintf("mlist:%d", listID))

	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, memberID), e.adminKey,
		map[string]any{"state": "suspended"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch member: %d: %s", res.StatusCode, buf)
	}
	assertAuditAction(t, e, "mlist.member.update", fmt.Sprintf("mlist:%d", listID))

	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members/import", listID), e.adminKey,
		map[string]any{"members": []map[string]any{{"external_address": "audit-bulk@example.net"}}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bulk import: %d: %s", res.StatusCode, buf)
	}
	assertAuditAction(t, e, "mlist.member.import", fmt.Sprintf("mlist:%d", listID))

	res, buf = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, memberID), e.adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove member: %d: %s", res.StatusCode, buf)
	}
	assertAuditAction(t, e, "mlist.member.remove", fmt.Sprintf("mlist:%d", listID))

	res, buf = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete list: %d: %s", res.StatusCode, buf)
	}
	assertAuditAction(t, e, "mlist.delete", fmt.Sprintf("mlist:%d", listID))
}

func assertAuditAction(t *testing.T, e *mlistTestEnv, action, subject string) {
	t.Helper()
	rows, err := e.h.Store().ListAuditLog(context.Background(), store.AuditLogFilter{Action: action})
	if err != nil {
		t.Fatalf("ListAuditLog(%s): %v", action, err)
	}
	for _, row := range rows {
		if row.Subject == subject {
			return
		}
	}
	t.Errorf("no audit row for action=%s subject=%s (rows=%+v)", action, subject, rows)
}

// TestMailingLists_ARCSealRequiresDKIMKey is the issue #183 config-time
// regression test for REQ-MLIST-21: creating a list, enabling arc_seal,
// or relocating an arc_seal-enabled list into a domain with no active
// DKIM key must be refused with a clear 400 -- a freshly onboarded
// domain (no `dkim generate` run yet) is the NORMAL state, not an
// exotic edge case. The gate only fires on those three actions, not on
// every unrelated PATCH; a domain's key can also be revoked well after
// list creation, so GET must independently surface dkim_key_missing at
// read time regardless of when the key disappeared. Runs on both SQLite
// and (when HEROLD_PG_DSN is set) Postgres.
func TestMailingLists_ARCSealRequiresDKIMKey(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_ARCSealRequiresDKIMKey(t, be) })
	}
}

func testMailingLists_ARCSealRequiresDKIMKey(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-arcseal-gate@example.test")
	e.insertLocalDomainNoKey(t, "keyless.example")
	e.insertLocalDomain(t, "keyed.example") // insertLocalDomain also provisions an active key

	// 1. Create with the default arc_seal (true) on a keyless domain:
	// refused with a 400 naming the problem and the remedy.
	res, buf := e.h.doRequest("POST", "/api/v1/lists", e.adminKey, map[string]any{
		"posting_address": "ann@keyless.example",
		"display_name":    "Ann",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create arc_seal-default on keyless domain: status=%d body=%s; want 400", res.StatusCode, buf)
	}
	var problem map[string]any
	if err := json.Unmarshal(buf, &problem); err != nil {
		t.Fatalf("unmarshal problem: %v", err)
	}
	if problem["type"] != "https://netzhansa.com/problems/dkim_key_required" {
		t.Errorf("problem type = %v; want .../dkim_key_required", problem["type"])
	}
	if detail, _ := problem["detail"].(string); !strings.Contains(detail, "keyless.example") {
		t.Errorf("problem detail = %q; want it to name the domain and a remedy", detail)
	}

	// 2. Explicit arc_seal=false on the same keyless domain: allowed --
	// the gate only applies when arc_seal is (to become) enabled.
	res, buf = e.h.doRequest("POST", "/api/v1/lists", e.adminKey, map[string]any{
		"posting_address": "ann@keyless.example",
		"display_name":    "Ann",
		"arc_seal":        false,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create arc_seal=false on keyless domain: status=%d body=%s; want 201", res.StatusCode, buf)
	}
	var noSealList map[string]any
	_ = json.Unmarshal(buf, &noSealList)
	noSealID := idOf(t, noSealList)

	// 3. Default arc_seal=true on a domain WITH an active key: allowed,
	// and the response carries no dkim_key_missing warning.
	res, buf = e.h.doRequest("POST", "/api/v1/lists", e.adminKey, map[string]any{
		"posting_address": "team@keyed.example",
		"display_name":    "Team",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create arc_seal-default on keyed domain: status=%d body=%s; want 201", res.StatusCode, buf)
	}
	var sealedList map[string]any
	_ = json.Unmarshal(buf, &sealedList)
	sealedID := idOf(t, sealedList)
	if v, present := sealedList["dkim_key_missing"]; present && v != false {
		t.Errorf("dkim_key_missing on a keyed-domain list = %v; want absent/false", v)
	}

	// 4. PATCH arc_seal false->true on the keyless-domain list: refused.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", noSealID), e.adminKey,
		map[string]any{"arc_seal": true})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("PATCH arc_seal=true on keyless-domain list: status=%d body=%s; want 400", res.StatusCode, buf)
	}

	// 5. PATCH relocating the keyed-domain (arc_seal=true) list INTO the
	// keyless domain: refused, and the list must not have actually moved.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", sealedID), e.adminKey,
		map[string]any{"posting_address": "team@keyless.example"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("PATCH relocate arc_seal-enabled list into keyless domain: status=%d body=%s; want 400", res.StatusCode, buf)
	}
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", sealedID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET after refused relocate: %d: %s", res.StatusCode, buf)
	}
	var stillSealed map[string]any
	_ = json.Unmarshal(buf, &stillSealed)
	if stillSealed["posting_address"] != "team@keyed.example" {
		t.Errorf("posting_address after refused relocate = %v; want unchanged", stillSealed["posting_address"])
	}

	// 6. An unrelated PATCH (display_name) on the arc_seal=true/keyed list
	// still works: the gate is not a blanket re-check on every PATCH.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", sealedID), e.adminKey,
		map[string]any{"display_name": "Team Renamed"})
	if res.StatusCode != http.StatusOK {
		t.Errorf("unrelated PATCH on arc_seal-enabled/keyed list: status=%d body=%s; want 200", res.StatusCode, buf)
	}

	// 7. Revoke keyed.example's DKIM key well after list creation (the
	// runtime-degradation scenario internal/maillist/expand.go handles at
	// fan-out time). An unrelated PATCH must still succeed -- the
	// config-time gate only fires on the three actions above -- but GET
	// must now surface dkim_key_missing so the operator sees the
	// degradation regardless of when the key disappeared.
	keys, err := e.h.Store().ListDKIMKeys(context.Background(), "keyed.example")
	if err != nil {
		t.Fatalf("ListDKIMKeys: %v", err)
	}
	for _, k := range keys {
		k.Status = store.DKIMKeyStatusRetired
		if err := e.h.Store().UpsertDKIMKey(context.Background(), k); err != nil {
			t.Fatalf("retire key: %v", err)
		}
	}
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", sealedID), e.adminKey,
		map[string]any{"display_name": "Team Renamed Again"})
	if res.StatusCode != http.StatusOK {
		t.Errorf("unrelated PATCH after key revoked: status=%d body=%s; want 200 (gate fires only on arc_seal-on/relocate)", res.StatusCode, buf)
	}
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", sealedID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET after key revoked: %d: %s", res.StatusCode, buf)
	}
	var afterRevoke map[string]any
	_ = json.Unmarshal(buf, &afterRevoke)
	if afterRevoke["dkim_key_missing"] != true {
		t.Errorf("dkim_key_missing after key revoked = %v; want true", afterRevoke["dkim_key_missing"])
	}

	// 8. GET /api/v1/lists collection also surfaces the flag (exercises
	// the per-page dkimKeyMissingChecker path, not just the single-item
	// GET).
	res, buf = e.h.doRequest("GET", "/api/v1/lists?domain=keyed.example", e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list lists: %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("unmarshal page: %v", err)
	}
	found := false
	for _, it := range page.Items {
		if idOf(t, it) == sealedID {
			found = true
			if it["dkim_key_missing"] != true {
				t.Errorf("collection dkim_key_missing = %v; want true", it["dkim_key_missing"])
			}
		}
	}
	if !found {
		t.Fatalf("collection listing missing list %d", sealedID)
	}
}

// TestMailingLists_BouncePolicy_ConfigureAndDefault exercises REQ-MLIST-53:
// a freshly created list's bounce_policy reflects the deployment
// defaults; a partial PATCH overrides only the given fields and leaves
// the rest at their default. Runs on both SQLite and (when HEROLD_PG_DSN
// is set) Postgres.
func TestMailingLists_BouncePolicy_ConfigureAndDefault(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_BouncePolicy_ConfigureAndDefault(t, be) })
	}
}

func testMailingLists_BouncePolicy_ConfigureAndDefault(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-bouncepolicy@example.test")
	e.insertLocalDomain(t, "bouncepolicy.example")

	created := e.createList(t, "bp@bouncepolicy.example", "BP")
	id := idOf(t, created)
	bp, ok := created["bounce_policy"].(map[string]any)
	if !ok {
		t.Fatalf("bounce_policy missing or wrong shape in create response: %v", created)
	}
	if bp["hard_weight"] != 1.0 || bp["soft_weight"] != 0.25 || bp["suspend_threshold"] != 1.0 {
		t.Errorf("default bounce_policy = %v; want hard_weight=1 soft_weight=0.25 suspend_threshold=1", bp)
	}
	if bp["decay_window_seconds"].(float64) != float64(7*24*3600) {
		t.Errorf("default decay_window_seconds = %v; want %d (7 days)", bp["decay_window_seconds"], 7*24*3600)
	}

	// Partial PATCH: only suspend_threshold overridden.
	res, buf := e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
		map[string]any{"bounce_policy": map[string]any{"suspend_threshold": 2.5}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch bounce_policy: %d: %s", res.StatusCode, buf)
	}
	var patched map[string]any
	_ = json.Unmarshal(buf, &patched)
	patchedBP := patched["bounce_policy"].(map[string]any)
	if patchedBP["suspend_threshold"] != 2.5 {
		t.Errorf("suspend_threshold after patch = %v; want 2.5", patchedBP["suspend_threshold"])
	}
	if patchedBP["hard_weight"] != 1.0 {
		t.Errorf("hard_weight after unrelated patch = %v; want unchanged default 1", patchedBP["hard_weight"])
	}

	// Invalid values are rejected with 400 and do not mutate the policy.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
		map[string]any{"bounce_policy": map[string]any{"suspend_threshold": -1}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch bounce_policy negative threshold: status=%d body=%s; want 400", res.StatusCode, buf)
	}
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get after rejected patch: %d: %s", res.StatusCode, buf)
	}
	var afterReject map[string]any
	_ = json.Unmarshal(buf, &afterReject)
	if afterReject["bounce_policy"].(map[string]any)["suspend_threshold"] != 2.5 {
		t.Errorf("suspend_threshold changed by a rejected patch: %v", afterReject["bounce_policy"])
	}
}

// TestMailingLists_MemberReactivate_ResetsScoreAndAudits exercises
// REQ-MLIST-55 through the admin REST/PATCH surface `list member-set`
// wraps: PATCH state=active on a suspended, non-zero-score member resets
// bounce_score/last_bounce_at and produces a distinct
// mlist.member.reactivate audit row. Runs on both SQLite and (when
// HEROLD_PG_DSN is set) Postgres.
func TestMailingLists_MemberReactivate_ResetsScoreAndAudits(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_MemberReactivate_ResetsScoreAndAudits(t, be) })
	}
}

func testMailingLists_MemberReactivate_ResetsScoreAndAudits(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-reactivate@example.test")
	e.insertLocalDomain(t, "reactivate.example")
	listID := idOf(t, e.createList(t, "reactivate@reactivate.example", "Reactivate"))

	res, buf := e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"external_address": "bouncy@example.net"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add member: %d: %s", res.StatusCode, buf)
	}
	var member map[string]any
	_ = json.Unmarshal(buf, &member)
	memberID := idOf(t, member)

	// Simulate an auto-suspend's aftermath directly at the store layer
	// (this test exercises the REST reactivation surface, not scoring
	// itself -- internal/maillist's own tests cover the scoring path).
	if _, err := e.h.Store().RecordMailingListMemberBounce(context.Background(), store.MailingListMemberID(memberID), time.Now(), 3.0, time.Hour); err != nil {
		t.Fatalf("RecordMailingListMemberBounce: %v", err)
	}
	if _, err := e.h.Store().SuspendMailingListMemberIfActive(context.Background(), store.MailingListMemberID(memberID)); err != nil {
		t.Fatalf("SuspendMailingListMemberIfActive: %v", err)
	}

	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, memberID), e.adminKey,
		map[string]any{"state": "active"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reactivate: %d: %s", res.StatusCode, buf)
	}
	var reactivated map[string]any
	_ = json.Unmarshal(buf, &reactivated)
	if reactivated["state"] != "active" {
		t.Errorf("state after reactivate = %v; want active", reactivated["state"])
	}
	if reactivated["bounce_score"] != nil && reactivated["bounce_score"] != float64(0) {
		t.Errorf("bounce_score after reactivate = %v; want 0/omitted", reactivated["bounce_score"])
	}
	if _, present := reactivated["last_bounce_at"]; present && reactivated["last_bounce_at"] != nil {
		t.Errorf("last_bounce_at after reactivate = %v; want nil/omitted", reactivated["last_bounce_at"])
	}
	assertAuditAction(t, e, "mlist.member.reactivate", fmt.Sprintf("mlist:%d", listID))
}

// TestMailingLists_MemberSummary exercises the REQ-MLIST-54 operator
// bounce/suspension view's summary endpoint: per-list roster counts by
// state. Runs on both SQLite and (when HEROLD_PG_DSN is set) Postgres.
func TestMailingLists_MemberSummary(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_MemberSummary(t, be) })
	}
}

func testMailingLists_MemberSummary(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-summary@example.test")
	e.insertLocalDomain(t, "summary.example")
	listID := idOf(t, e.createList(t, "summary@summary.example", "Summary"))

	addrs := map[string]string{
		"active1@example.net":       "",
		"active2@example.net":       "",
		"suspended1@example.net":    "suspended",
		"unsubscribed1@example.net": "unsubscribed",
	}
	for addr, state := range addrs {
		body := map[string]any{"external_address": addr}
		if state != "" {
			body["state"] = state
		}
		res, buf := e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey, body)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("add member %s: %d: %s", addr, res.StatusCode, buf)
		}
	}

	res, buf := e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/members/summary", listID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("summary: %d: %s", res.StatusCode, buf)
	}
	var summary struct {
		Active       int `json:"active"`
		Suspended    int `json:"suspended"`
		Unsubscribed int `json:"unsubscribed"`
		Pending      int `json:"pending"`
	}
	if err := json.Unmarshal(buf, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.Active != 2 || summary.Suspended != 1 || summary.Unsubscribed != 1 || summary.Pending != 0 {
		t.Fatalf("summary = %+v; want active=2 suspended=1 unsubscribed=1 pending=0", summary)
	}
}

// TestMailingLists_SubscribePolicy_SettableViaPATCH is Stage 3's REQ-MLIST-60
// admin surface (issue #185): subscribe_policy defaults to closed on create
// (already asserted in TestMailingLists_CRUD_SuperAdmin), is settable to
// request-approval and open via PATCH, and rejects an unrecognised value.
func TestMailingLists_SubscribePolicy_SettableViaPATCH(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_SubscribePolicy_SettableViaPATCH(t, be) })
	}
}

func testMailingLists_SubscribePolicy_SettableViaPATCH(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-subpolicy@example.test")
	e.insertLocalDomain(t, "subpolicy.example")
	id := idOf(t, e.createList(t, "list@subpolicy.example", "List"))

	for _, policy := range []string{"request-approval", "open", "closed"} {
		res, buf := e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
			map[string]any{"subscribe_policy": policy})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("PATCH subscribe_policy=%s: %d: %s", policy, res.StatusCode, buf)
		}
		var got map[string]any
		if err := json.Unmarshal(buf, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["subscribe_policy"] != policy {
			t.Errorf("subscribe_policy after PATCH = %v; want %s", got["subscribe_policy"], policy)
		}
	}

	// Also settable at creation time.
	created := e.createList(t, "list2@subpolicy.example", "List2")
	res, buf := e.h.doRequest("POST", "/api/v1/lists", e.adminKey, map[string]any{
		"posting_address":  "list3@subpolicy.example",
		"display_name":     "List3",
		"subscribe_policy": "open",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create with subscribe_policy=open: %d: %s", res.StatusCode, buf)
	}
	var withPolicy map[string]any
	_ = json.Unmarshal(buf, &withPolicy)
	if withPolicy["subscribe_policy"] != "open" {
		t.Errorf("subscribe_policy at create = %v; want open", withPolicy["subscribe_policy"])
	}
	_ = created

	// An invalid value is rejected (400), not silently accepted.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", id), e.adminKey,
		map[string]any{"subscribe_policy": "bogus"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("PATCH subscribe_policy=bogus: %d: %s; want 400", res.StatusCode, buf)
	}
}

// TestMailingLists_ApproveReject_RequestApprovalMember is the REQ-MLIST-62
// operator surface: an awaiting-approval member is visible in the roster
// and the summary counts, PATCH state=active approves it (recorded as
// mlist.member.approve, distinct from a bare reactivation), and on a
// separate member PATCH state=unsubscribed rejects it (recorded as
// mlist.member.reject).
func TestMailingLists_ApproveReject_RequestApprovalMember(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_ApproveReject_RequestApprovalMember(t, be) })
	}
}

func testMailingLists_ApproveReject_RequestApprovalMember(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-approve@example.test")
	e.insertLocalDomain(t, "approve.example")
	id := idOf(t, e.createList(t, "list@approve.example", "List"))
	listID := store.MailingListID(id)

	approveMe, err := e.h.Store().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID:          listID,
		ExternalAddress: strPtrTest("approve-me@example.net"),
		State:           store.MailingListMemberAwaitingApproval,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember(approveMe): %v", err)
	}
	rejectMe, err := e.h.Store().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID:          listID,
		ExternalAddress: strPtrTest("reject-me@example.net"),
		State:           store.MailingListMemberAwaitingApproval,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember(rejectMe): %v", err)
	}

	// The summary surfaces the awaiting-approval count for the operator.
	res, buf := e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d/members/summary", id), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("summary: %d: %s", res.StatusCode, buf)
	}
	var summary struct {
		AwaitingApproval int `json:"awaiting_approval"`
	}
	if err := json.Unmarshal(buf, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.AwaitingApproval != 2 {
		t.Fatalf("summary.awaiting_approval = %d; want 2", summary.AwaitingApproval)
	}

	// Approve.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", id, approveMe.ID), e.adminKey,
		map[string]any{"state": "active"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d: %s", res.StatusCode, buf)
	}
	var approved map[string]any
	_ = json.Unmarshal(buf, &approved)
	if approved["state"] != "active" {
		t.Errorf("state after approve = %v; want active", approved["state"])
	}

	// Reject.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", id, rejectMe.ID), e.adminKey,
		map[string]any{"state": "unsubscribed"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reject: %d: %s", res.StatusCode, buf)
	}
	var rejected map[string]any
	_ = json.Unmarshal(buf, &rejected)
	if rejected["state"] != "unsubscribed" {
		t.Errorf("state after reject = %v; want unsubscribed", rejected["state"])
	}

	logs, err := e.h.Store().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "mlist.member.approve"})
	if err != nil {
		t.Fatalf("ListAuditLog(approve): %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("mlist.member.approve audit rows = %d; want 1", len(logs))
	}
	logs, err = e.h.Store().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "mlist.member.reject"})
	if err != nil {
		t.Fatalf("ListAuditLog(reject): %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("mlist.member.reject audit rows = %d; want 1", len(logs))
	}
}

func strPtrTest(s string) *string { return &s }
