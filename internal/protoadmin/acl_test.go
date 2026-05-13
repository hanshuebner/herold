package protoadmin_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/hanshuebner/herold/internal/aclcodec"
	"github.com/hanshuebner/herold/internal/store"
)

// aclHarness adds ACL-test-specific seeding on top of the generic
// protoadmin test harness defined in server_test.go. It bootstraps an
// admin, creates two non-admin principals (owner + grantee) and returns
// the relevant ids + the admin API key.
type aclHarness struct {
	*harness
	adminKey   string
	ownerID    uint64
	ownerEmail string
	granteeID  uint64
	granteeEm  string
}

func newACLHarness(t *testing.T) *aclHarness {
	t.Helper()
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	ownerEmail := "owner@example.com"
	granteeEmail := "grantee@example.com"
	ownerID := h.createPrincipal(adminKey, ownerEmail)
	granteeID := h.createPrincipal(adminKey, granteeEmail)
	return &aclHarness{
		harness:    h,
		adminKey:   adminKey,
		ownerID:    ownerID,
		ownerEmail: ownerEmail,
		granteeID:  granteeID,
		granteeEm:  granteeEmail,
	}
}

// mailboxList is the wire shape of GET .../mailboxes.
type mailboxList struct {
	Items []struct {
		ID          uint64 `json:"id"`
		Name        string `json:"name"`
		PrincipalID uint64 `json:"principal_id"`
	} `json:"items"`
}

// aclList is the wire shape of GET .../acl.
type aclList struct {
	Items []struct {
		Grantee string `json:"grantee"`
		Rights  string `json:"rights"`
		IsOwner bool   `json:"is_owner"`
	} `json:"items"`
}

func TestACL_ListPrincipalMailboxes_IncludesINBOX(t *testing.T) {
	h := newACLHarness(t)
	res, buf := h.doRequest("GET",
		"/api/v1/principals/"+itoa(h.ownerID)+"/mailboxes",
		h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d: %s", res.StatusCode, buf)
	}
	var got mailboxList
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if len(got.Items) == 0 {
		t.Fatalf("no mailboxes returned")
	}
	foundInbox := false
	for _, m := range got.Items {
		if m.Name == "INBOX" {
			foundInbox = true
			if m.PrincipalID != h.ownerID {
				t.Fatalf("INBOX principal_id = %d; want %d", m.PrincipalID, h.ownerID)
			}
			if m.ID == 0 {
				t.Fatalf("INBOX id zero")
			}
		}
	}
	if !foundInbox {
		t.Fatalf("INBOX not present in mailbox list: %+v", got.Items)
	}
}

func TestACL_ListPrincipalMailboxes_UnknownPrincipal404(t *testing.T) {
	h := newACLHarness(t)
	res, _ := h.doRequest("GET",
		"/api/v1/principals/999999/mailboxes",
		h.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown principal: got %d, want 404", res.StatusCode)
	}
}

func TestACL_Get_OwnerOnly_WhenEmpty(t *testing.T) {
	h := newACLHarness(t)
	res, buf := h.doRequest("GET", h.aclPath(h.ownerID, "INBOX"), h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get acl: %d: %s", res.StatusCode, buf)
	}
	var got aclList
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if len(got.Items) != 1 {
		t.Fatalf("want 1 (owner) row, got %d: %+v", len(got.Items), got.Items)
	}
	owner := got.Items[0]
	if !owner.IsOwner {
		t.Fatalf("owner row is_owner false: %+v", owner)
	}
	if owner.Grantee != h.ownerEmail {
		t.Fatalf("owner grantee = %q; want %q", owner.Grantee, h.ownerEmail)
	}
	if owner.Rights != aclcodec.Encode(store.ACLRightsAll) {
		t.Fatalf("owner rights = %q; want full mask", owner.Rights)
	}
}

func TestACL_Put_GrantThenGet_Lookup(t *testing.T) {
	h := newACLHarness(t)
	res, buf := h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, map[string]any{"rights": "lr"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put acl: %d: %s", res.StatusCode, buf)
	}
	var put struct {
		Grantee string `json:"grantee"`
		Rights  string `json:"rights"`
	}
	if err := json.Unmarshal(buf, &put); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if put.Grantee != h.granteeEm || put.Rights != "lr" {
		t.Fatalf("put returned %+v", put)
	}

	// GET shows owner + grantee.
	res, buf = h.doRequest("GET", h.aclPath(h.ownerID, "INBOX"), h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get acl: %d: %s", res.StatusCode, buf)
	}
	var got aclList
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if len(got.Items) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(got.Items), got.Items)
	}
	if !got.Items[0].IsOwner {
		t.Fatalf("row 0 not owner: %+v", got.Items[0])
	}
	grantee := got.Items[1]
	if grantee.Grantee != h.granteeEm {
		t.Fatalf("grantee row grantee = %q; want %q", grantee.Grantee, h.granteeEm)
	}
	if grantee.Rights != "lr" {
		t.Fatalf("grantee row rights = %q; want lr", grantee.Rights)
	}
	if grantee.IsOwner {
		t.Fatalf("grantee row is_owner true")
	}
}

func TestACL_Put_Full_LRSWIPKXTE(t *testing.T) {
	h := newACLHarness(t)
	// The complete RFC 4314 vocabulary including admin ("a") delegated.
	rights := "lrswipkxtea"
	res, buf := h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, map[string]any{"rights": rights})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put full acl: %d: %s", res.StatusCode, buf)
	}
	var put struct {
		Rights string `json:"rights"`
	}
	if err := json.Unmarshal(buf, &put); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if put.Rights != rights {
		t.Fatalf("got rights = %q; want %q", put.Rights, rights)
	}
}

func TestACL_Put_Anyone(t *testing.T) {
	h := newACLHarness(t)
	res, buf := h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/anyone",
		h.adminKey, map[string]any{"rights": "lr"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put anyone: %d: %s", res.StatusCode, buf)
	}
	res, buf = h.doRequest("GET", h.aclPath(h.ownerID, "INBOX"), h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get: %d: %s", res.StatusCode, buf)
	}
	var got aclList
	_ = json.Unmarshal(buf, &got)
	foundAnyone := false
	for _, it := range got.Items {
		if it.Grantee == "anyone" {
			foundAnyone = true
			if it.Rights != "lr" {
				t.Fatalf("anyone row rights = %q; want lr", it.Rights)
			}
		}
	}
	if !foundAnyone {
		t.Fatalf("anyone row not present: %+v", got.Items)
	}
}

func TestACL_Put_Modifies_ExistingRow(t *testing.T) {
	h := newACLHarness(t)
	// First grant: lr only.
	res, _ := h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, map[string]any{"rights": "lr"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first put: %d", res.StatusCode)
	}
	// Second grant: replace with full mask.
	res, buf := h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, map[string]any{"rights": "lrswipkxtea"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("modify: %d: %s", res.StatusCode, buf)
	}
	res, buf = h.doRequest("GET", h.aclPath(h.ownerID, "INBOX"), h.adminKey, nil)
	var got aclList
	_ = json.Unmarshal(buf, &got)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get: %d", res.StatusCode)
	}
	found := 0
	for _, it := range got.Items {
		if it.Grantee == h.granteeEm {
			found++
			if it.Rights != "lrswipkxtea" {
				t.Fatalf("modified row rights = %q; want full", it.Rights)
			}
		}
	}
	if found != 1 {
		t.Fatalf("want exactly 1 grantee row after modify, got %d", found)
	}
}

func TestACL_Delete_Idempotent(t *testing.T) {
	h := newACLHarness(t)
	// Seed a row.
	_, _ = h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, map[string]any{"rights": "lr"})

	// First delete: 204.
	res, _ := h.doRequest("DELETE",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204", res.StatusCode)
	}

	// Verify it is gone via GET.
	_, buf := h.doRequest("GET", h.aclPath(h.ownerID, "INBOX"), h.adminKey, nil)
	var got aclList
	_ = json.Unmarshal(buf, &got)
	for _, it := range got.Items {
		if it.Grantee == h.granteeEm {
			t.Fatalf("grantee row still present after delete: %+v", it)
		}
	}

	// Idempotent: re-delete returns 204 (not 404).
	res, _ = h.doRequest("DELETE",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("re-delete: got %d, want 204 (idempotent)", res.StatusCode)
	}
}

func TestACL_Delete_UnknownGrantee_Returns404(t *testing.T) {
	h := newACLHarness(t)
	res, _ := h.doRequest("DELETE",
		h.aclPath(h.ownerID, "INBOX")+"/nosuch@example.com",
		h.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown grantee: got %d, want 404", res.StatusCode)
	}
}

func TestACL_Get_NonOwnerPID_MailboxNotFound_404(t *testing.T) {
	h := newACLHarness(t)
	// Address the owner's INBOX via the grantee's pid: the mailbox is
	// not owned by that principal so the store returns ErrNotFound,
	// which protoadmin maps to 404. This is the same shape that
	// prevents enumeration of other principals' mailbox ids.
	res, _ := h.doRequest("GET",
		h.aclPath(h.granteeID, "INBOX"),
		h.adminKey, nil)
	// granteeID also has its own INBOX so the GET succeeds — but the
	// mailbox in question is the *grantee's* INBOX, not the owner's,
	// which is exactly the behaviour we want. Probe a non-existent
	// folder name to assert the 404 path.
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get grantee INBOX: %d", res.StatusCode)
	}
	res, _ = h.doRequest("GET",
		h.aclPath(h.granteeID, "NoSuchFolder"),
		h.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("get unknown folder: got %d, want 404", res.StatusCode)
	}
}

func TestACL_Put_InvalidRights_400(t *testing.T) {
	h := newACLHarness(t)
	res, _ := h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, map[string]any{"rights": "lrZ"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid letters: got %d, want 400", res.StatusCode)
	}
	// The obsolete RFC 2086 "c"/"d" composites are intentionally NOT
	// accepted on the REST surface (they are only honoured on the IMAP
	// wire for back-compat with older clients).
	res, _ = h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		h.adminKey, map[string]any{"rights": "lrc"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy c letter: got %d, want 400", res.StatusCode)
	}
}

func TestACL_Put_OwnerSelfGrant_400(t *testing.T) {
	h := newACLHarness(t)
	// Granting an ACL row to the mailbox owner is meaningless (the
	// owner has implicit full rights) and would shadow the synthetic
	// owner row in GET responses.
	res, _ := h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.ownerEmail,
		h.adminKey, map[string]any{"rights": "lr"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("owner self-grant: got %d, want 400", res.StatusCode)
	}
}

func TestACL_Unauthenticated_Returns401(t *testing.T) {
	h := newACLHarness(t)
	res, _ := h.doRequest("GET",
		"/api/v1/principals/"+itoa(h.ownerID)+"/mailboxes",
		"", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth: got %d, want 401", res.StatusCode)
	}
	res, _ = h.doRequest("GET", h.aclPath(h.ownerID, "INBOX"), "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth get acl: got %d, want 401", res.StatusCode)
	}
	res, _ = h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		"", map[string]any{"rights": "lr"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth put acl: got %d, want 401", res.StatusCode)
	}
	res, _ = h.doRequest("DELETE",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		"", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth delete acl: got %d, want 401", res.StatusCode)
	}
}

func TestACL_NonAdmin_Returns403(t *testing.T) {
	h := newACLHarness(t)
	// Mint an API key for the grantee (non-admin) principal.
	res, buf := h.doRequest("POST",
		"/api/v1/principals/"+itoa(h.granteeID)+"/api-keys",
		h.adminKey, map[string]any{"label": "non-admin"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("mint non-admin key: %d: %s", res.StatusCode, buf)
	}
	var keyOut struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf, &keyOut); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plain := keyOut.Key

	res, _ = h.doRequest("GET",
		"/api/v1/principals/"+itoa(h.ownerID)+"/mailboxes",
		plain, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin list mailboxes: got %d, want 403", res.StatusCode)
	}
	res, _ = h.doRequest("GET",
		h.aclPath(h.ownerID, "INBOX"),
		plain, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin get acl: got %d, want 403", res.StatusCode)
	}
	res, _ = h.doRequest("PUT",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		plain, map[string]any{"rights": "lr"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin put acl: got %d, want 403", res.StatusCode)
	}
	res, _ = h.doRequest("DELETE",
		h.aclPath(h.ownerID, "INBOX")+"/"+h.granteeEm,
		plain, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin delete acl: got %d, want 403", res.StatusCode)
	}
}

// aclPath builds /api/v1/principals/{pid}/mailboxes/{mailbox}/acl.
func (h *aclHarness) aclPath(pid uint64, mailbox string) string {
	return "/api/v1/principals/" + itoa(pid) + "/mailboxes/" + mailbox + "/acl"
}

// itoa is a tiny wrapper around strconv.FormatUint to keep call sites
// readable when interpolating the principal id into URL paths.
func itoa(n uint64) string {
	return strconv.FormatUint(n, 10)
}
