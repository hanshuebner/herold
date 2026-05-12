package protoadmin_test

// tagged_address_dismissals_test.go exercises the REST endpoints added
// in tagged_address_dismissals.go (REQ-TAG-50..57, REQ-TAG-60..62,
// REQ-TAG-90). Coverage:
//
//   - POST creates a row; the response body echoes the canonical
//     (base_identity_id, suffix, dismissed_at) triple and the status
//     is 201 on first create.
//   - POST against an already-dismissed (identity, suffix) returns 200
//     and the row is unchanged (idempotent per REQ-TAG-60).
//   - GET returns every dismissal owned by the caller; non-callers
//     never see rows belonging to other principals.
//   - DELETE removes the row and is idempotent on a missing row.
//   - The 500-per-principal cap (REQ-TAG-11, REQ-TAG-56) surfaces as
//     422 with the `too_many_dismissals` type token on the 501st
//     attempt.
//   - Audit entries land for create + destroy; an idempotent re-POST
//     does NOT generate a duplicate audit entry.
//
// The harness mounts the admin server's full route set (RegisterRoutes)
// rather than the trimmed self-service mux so the tests can hit
// /api/v1/bootstrap and the JMAP identity surface on the same listener.
// The mount-on-public-listener invariant is checked separately in
// internal/admin/public_routes_contract_test.go (the mount test that
// asserts publicMux serves /api/v1/tagged-address-dismissals etc.).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// dismissalHarness wraps the submissionHarness for dismissal tests so
// we can reuse the bootstrap / createUser / identity-insertion plumbing
// without re-implementing it.
type dismissalHarness struct {
	*submissionHarness
	adminEmail string
	adminKey   string
	// adminPID is the principal_id of the bootstrap admin returned by
	// whoami; the dismissal endpoints scope every read+write to the
	// caller, so tests need this id to insert identity FKs on the
	// correct side.
	adminPID uint64
}

func newDismissalHarness(t *testing.T) *dismissalHarness {
	t.Helper()
	sh := newSubmissionHarness(t, alwaysOKProbe)
	email, key := sh.bootstrap("admin@example.com")
	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d: %s", res.StatusCode, buf)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	if err := json.Unmarshal(buf, &who); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	return &dismissalHarness{
		submissionHarness: sh,
		adminEmail:        email,
		adminKey:          key,
		adminPID:          who.PrincipalID,
	}
}

// TestDismissals_PostCreate_201 verifies that a fresh POST inserts a
// row, returns 201, and the response body carries the canonical
// (base_identity_id, suffix, dismissed_at) triple.
func TestDismissals_PostCreate_201(t *testing.T) {
	h := newDismissalHarness(t)
	identityID := h.insertIdentity(h.adminPID, "admin@example.com")

	res, buf := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey,
		map[string]any{
			"base_identity_id": identityID,
			"suffix":           "Amazon",
		})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		BaseIdentityID string `json:"base_identity_id"`
		Suffix         string `json:"suffix"`
		DismissedAt    string `json:"dismissed_at"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if out.BaseIdentityID != identityID {
		t.Errorf("base_identity_id = %q; want %q", out.BaseIdentityID, identityID)
	}
	// REQ-TAG-02: storage normalises to lower-case; the response
	// echoes the stored form so the SPA never sees mixed case.
	if out.Suffix != "amazon" {
		t.Errorf("suffix = %q; want %q (lower-cased per REQ-TAG-02)", out.Suffix, "amazon")
	}
	if out.DismissedAt == "" {
		t.Errorf("dismissed_at missing in response")
	}
}

// TestDismissals_PostCreate_Idempotent verifies that a duplicate POST
// returns 200 (not 201) and emits only one audit entry total.
func TestDismissals_PostCreate_Idempotent(t *testing.T) {
	h := newDismissalHarness(t)
	identityID := h.insertIdentity(h.adminPID, "admin@example.com")

	body := map[string]any{
		"base_identity_id": identityID,
		"suffix":           "newsletters",
	}
	res, _ := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey, body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("first POST: %d", res.StatusCode)
	}
	res2, buf2 := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey, body)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("second POST: %d: %s (want 200 idempotent)", res2.StatusCode, buf2)
	}

	// Audit: a single dismissal_create entry should be present in the
	// store. The fakeclock is paused so timestamps are deterministic.
	audits := auditEntriesFor(t, h.fs, "tagged_address_dismissal.create")
	if len(audits) != 1 {
		t.Errorf("audit entries: got %d, want 1 (duplicate POSTs must not re-audit)", len(audits))
	}
}

// TestDismissals_PostCreate_RejectsForeignIdentity verifies that a
// caller cannot post a dismissal scoped to an identity belonging to
// another principal. The response is 404 (not 403) so existence is not
// leaked across principals.
func TestDismissals_PostCreate_RejectsForeignIdentity(t *testing.T) {
	h := newDismissalHarness(t)
	// Insert an identity belonging to a synthetic non-admin principal
	// via the store's InsertPrincipal (which assigns the id) so the
	// FK in jmap_identities is satisfied.
	foreign, err := h.fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "other@example.com",
		Kind:           store.PrincipalKindUser,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	foreignID := h.insertIdentity(uint64(foreign.ID), "other@example.com")

	res, buf := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey,
		map[string]any{
			"base_identity_id": foreignID,
			"suffix":           "amazon",
		})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("POST: %d: %s (want 404 for cross-principal probe)", res.StatusCode, buf)
	}
}

// TestDismissals_PostCreate_RejectsMissingFields covers the basic input
// validation: empty body, missing suffix, oversize suffix, control
// bytes — all reject with 400 problem+json before any store write.
func TestDismissals_PostCreate_RejectsMissingFields(t *testing.T) {
	h := newDismissalHarness(t)
	identityID := h.insertIdentity(h.adminPID, "admin@example.com")

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing_base_identity_id",
			body: map[string]any{"suffix": "amazon"},
		},
		{
			name: "missing_suffix",
			body: map[string]any{"base_identity_id": identityID},
		},
		{
			name: "empty_suffix",
			body: map[string]any{"base_identity_id": identityID, "suffix": ""},
		},
		{
			name: "control_byte_in_suffix",
			body: map[string]any{"base_identity_id": identityID, "suffix": "ama\x01zon"},
		},
		{
			name: "at_sign_in_suffix",
			body: map[string]any{"base_identity_id": identityID, "suffix": "abc@def"},
		},
		{
			name: "oversize_suffix",
			body: map[string]any{"base_identity_id": identityID, "suffix": strings.Repeat("a", 65)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, buf := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey, tc.body)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d: %s (want 400)", res.StatusCode, buf)
			}
			ct := res.Header.Get("Content-Type")
			if ct != "application/problem+json" {
				t.Errorf("Content-Type = %q; want application/problem+json", ct)
			}
		})
	}
}

// TestDismissals_Get_ReturnsCallerOnly verifies that GET returns the
// caller's dismissals and excludes rows belonging to other principals.
func TestDismissals_Get_ReturnsCallerOnly(t *testing.T) {
	h := newDismissalHarness(t)
	identityID := h.insertIdentity(h.adminPID, "admin@example.com")
	// Seed two dismissals via POST so the GET path covers the full
	// request -> store -> response loop.
	for _, suffix := range []string{"amazon", "newsletters"} {
		res, _ := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey,
			map[string]any{"base_identity_id": identityID, "suffix": suffix})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("seed POST %s: %d", suffix, res.StatusCode)
		}
	}
	// Insert a foreign principal + identity + dismissal directly via
	// the store so the GET response can be checked for leakage.
	other, err := h.fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "leak@example.com",
		Kind:           store.PrincipalKindUser,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	otherPID := other.ID
	otherIdentity := h.insertIdentity(uint64(otherPID), "leak@example.com")
	if err := h.fs.Meta().InsertTaggedAddressDismissal(context.Background(),
		store.TaggedAddressDismissal{
			PrincipalID: otherPID, BaseIdentityID: otherIdentity, Suffix: "shopping",
		}); err != nil {
		t.Fatalf("InsertTaggedAddressDismissal(other): %v", err)
	}

	res, buf := h.doRequest("GET", "/api/v1/tagged-address-dismissals", h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Dismissals []struct {
			BaseIdentityID string `json:"base_identity_id"`
			Suffix         string `json:"suffix"`
			DismissedAt    string `json:"dismissed_at"`
		} `json:"dismissals"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if len(out.Dismissals) != 2 {
		t.Fatalf("dismissals = %d; want 2 (foreign row must be invisible)", len(out.Dismissals))
	}
	for _, d := range out.Dismissals {
		if d.BaseIdentityID == otherIdentity {
			t.Errorf("foreign dismissal leaked into caller's GET: %+v", d)
		}
	}
}

// TestDismissals_Delete_RemovesRow verifies that DELETE removes the
// row and a subsequent GET returns the smaller set. The endpoint is
// idempotent: a second DELETE returns 204 (no error) and emits no
// duplicate audit entry.
func TestDismissals_Delete_RemovesRow(t *testing.T) {
	h := newDismissalHarness(t)
	identityID := h.insertIdentity(h.adminPID, "admin@example.com")
	res, _ := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey,
		map[string]any{"base_identity_id": identityID, "suffix": "shopping"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("seed POST: %d", res.StatusCode)
	}

	delPath := "/api/v1/tagged-address-dismissals/" + identityID + "/shopping"
	res2, buf2 := h.doRequest("DELETE", delPath, h.adminKey, nil)
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d: %s", res2.StatusCode, buf2)
	}
	// Second DELETE: idempotent — store returns nil for missing rows
	// and the handler does not emit a duplicate audit entry.
	res3, _ := h.doRequest("DELETE", delPath, h.adminKey, nil)
	if res3.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE (idempotent): %d", res3.StatusCode)
	}
	destroys := auditEntriesFor(t, h.fs, "tagged_address_dismissal.destroy")
	if len(destroys) != 1 {
		t.Errorf("destroy audit entries = %d; want 1 (second delete must not re-audit)", len(destroys))
	}

	// GET now returns no rows.
	res4, buf4 := h.doRequest("GET", "/api/v1/tagged-address-dismissals", h.adminKey, nil)
	if res4.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d: %s", res4.StatusCode, buf4)
	}
	var out struct {
		Dismissals []struct{} `json:"dismissals"`
	}
	if err := json.Unmarshal(buf4, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Dismissals) != 0 {
		t.Errorf("dismissals after delete = %d; want 0", len(out.Dismissals))
	}
}

// TestDismissals_CapEnforced verifies that the 500-per-principal cap
// (REQ-TAG-11, REQ-TAG-56) surfaces on the 501st new dismissal as 422
// with the `too_many_dismissals` problem-type. Idempotent re-POSTs of
// already-stored dismissals do NOT count against the cap because the
// store short-circuits the duplicate check (REQ-TAG-60).
//
// The cap is asserted by seeding 500 rows directly via the store (much
// faster than 500 POSTs through the HTTP path) and then issuing one
// more POST through the REST surface.
func TestDismissals_CapEnforced(t *testing.T) {
	h := newDismissalHarness(t)
	identityID := h.insertIdentity(h.adminPID, "admin@example.com")
	ctx := context.Background()
	for i := 0; i < store.MaxTaggedAddressDismissalsPerPrincipal; i++ {
		err := h.fs.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
			PrincipalID:    store.PrincipalID(h.adminPID),
			BaseIdentityID: identityID,
			Suffix:         fmt.Sprintf("s%d", i),
		})
		if err != nil {
			t.Fatalf("seed dismissal %d: %v", i, err)
		}
	}
	res, buf := h.doRequest("POST", "/api/v1/tagged-address-dismissals", h.adminKey,
		map[string]any{"base_identity_id": identityID, "suffix": "overflow"})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("501st POST: %d: %s (want 422)", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), "too_many_dismissals") {
		t.Errorf("response missing too_many_dismissals problem type: %s", buf)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q; want application/problem+json", ct)
	}
}

// auditEntriesFor returns the audit_log rows whose action equals the
// supplied value. Tests use it to assert audit-side effects of the
// REST handlers (REQ-TAG-90).
func auditEntriesFor(t *testing.T, s store.Store, action string) []store.AuditLogEntry {
	t.Helper()
	rows, err := s.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: action,
		Limit:  256,
	})
	if err != nil {
		t.Fatalf("ListAuditLog(%q): %v", action, err)
	}
	return rows
}

// Compile-time touch to keep imports stable when individual tests are
// commented out during local debugging. The fake clock from
// submissionHarness is wrapped here so the linter does not strip the
// import.
var _ = clock.NewFake(time.Time{})
