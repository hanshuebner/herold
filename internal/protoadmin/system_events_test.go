package protoadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// TestSystemEvents_SuperAdminSeesAll verifies that a super-admin can list all
// system events regardless of domain (REQ-ADM-304, re #142).
func TestSystemEvents_SuperAdminSeesAll(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	ctx := context.Background()

	// Promote bootstrap admin to super-admin.
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	// Insert two system events for different domains.
	for _, domain := range []string{"alpha.example", "beta.example"} {
		if err := h.h.Store.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Action:  "smtp.accept",
			ActorID: "smtp",
			Subject: "message:" + domain,
			Outcome: store.OutcomeSuccess,
			Domain:  domain,
		}); err != nil {
			t.Fatalf("AppendSystemEvent %s: %v", domain, err)
		}
	}

	res, buf := h.doRequest("GET", "/api/v1/admin/system-events", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET system-events: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	// Super-admin sees both events.
	if len(out.Items) < 2 {
		t.Errorf("super-admin: got %d items; want >= 2", len(out.Items))
	}
}

// TestSystemEvents_OperatorScope verifies that a domain-scoped operator sees
// only system events for their managed domains (REQ-ADM-307, re #142).
func TestSystemEvents_OperatorScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Bootstrap and promote to super-admin.
	_, adminKey := h.bootstrap("superadmin@example.com")
	admin, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "superadmin@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	admin.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, admin); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	// Create an operator: admin flag, no super-admin, managed domain = alpha.example.
	opID := h.createPrincipal(adminKey, "op@example.com")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin // no super-admin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}
	if err := h.h.Store.Meta().AssignManagedDomain(ctx, store.PrincipalID(opID), "alpha.example"); err != nil {
		t.Fatalf("AssignManagedDomain: %v", err)
	}
	_, opKey := h.createAPIKey(adminKey, opID)

	// Insert events for alpha.example and beta.example.
	for _, domain := range []string{"alpha.example", "beta.example"} {
		if err := h.h.Store.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			Action:  "smtp.accept",
			ActorID: "smtp",
			Subject: "message:" + domain,
			Outcome: store.OutcomeSuccess,
			Domain:  domain,
		}); err != nil {
			t.Fatalf("AppendSystemEvent %s: %v", domain, err)
		}
	}

	res, buf := h.doRequest("GET", "/api/v1/admin/system-events?action=smtp.accept", opKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("operator GET system-events: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	// Operator should see only alpha.example events, not beta.example.
	for _, item := range out.Items {
		dom, _ := item["domain"].(string)
		if dom == "beta.example" {
			t.Errorf("operator leaked beta.example event: %v", item)
		}
	}
	var sawAlpha bool
	for _, item := range out.Items {
		if d, _ := item["domain"].(string); d == "alpha.example" {
			sawAlpha = true
		}
	}
	if !sawAlpha {
		t.Errorf("operator did not see alpha.example event; items=%+v", out.Items)
	}
}

// TestSystemEvents_ActionFilter verifies the action query parameter on the
// REST endpoint (REQ-ADM-304, re #142).
func TestSystemEvents_ActionFilter(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	ctx := context.Background()

	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}

	for _, action := range []string{"smtp.accept", "smtp.rcpt.resolve", "smtp.accept"} {
		if err := h.h.Store.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			Action:  action,
			ActorID: "smtp",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent %s: %v", action, err)
		}
	}

	res, buf := h.doRequest("GET", "/api/v1/admin/system-events?action=smtp.accept", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET system-events: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	for _, item := range out.Items {
		if a, _ := item["action"].(string); a != "smtp.accept" {
			t.Errorf("action filter leaked action=%q", a)
		}
	}
	if len(out.Items) < 2 {
		t.Errorf("action filter: got %d items; want >= 2", len(out.Items))
	}
}

// TestSystemEvents_Pagination verifies before_id keyset pagination on the
// REST endpoint.
func TestSystemEvents_Pagination(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	ctx := context.Background()

	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := h.h.Store.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      time.Date(2026, 4, 1, 0, 0, i, 0, time.UTC),
			Action:  "page.test",
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent %d: %v", i, err)
		}
	}

	// Page 1 with limit=2.
	res, buf := h.doRequest("GET", "/api/v1/admin/system-events?action=page.test&limit=2", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("page 1: %d: %s", res.StatusCode, buf)
	}
	var page1 struct {
		Items []map[string]any `json:"items"`
		Next  *string          `json:"next"`
	}
	if err := json.Unmarshal(buf, &page1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("page 1: got %d items; want 2", len(page1.Items))
	}
	if page1.Next == nil {
		t.Fatalf("page 1: next cursor missing")
	}

	// Page 2 using before_id.
	res, buf = h.doRequest("GET",
		fmt.Sprintf("/api/v1/admin/system-events?action=page.test&limit=2&before_id=%s", *page1.Next),
		adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("page 2: %d: %s", res.StatusCode, buf)
	}
	var page2 struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &page2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(page2.Items) == 0 {
		t.Fatalf("page 2: expected items but got none")
	}
	// Page2 items must have IDs strictly less than page1's last item.
	id1, _ := page1.Items[len(page1.Items)-1]["id"].(float64)
	id2, _ := page2.Items[0]["id"].(float64)
	if id2 >= id1 {
		t.Errorf("page2[0].id=%v >= page1[last].id=%v; expected strictly less", id2, id1)
	}
}

// TestSystemEvents_AllowlistedActionsStayInAuditLog verifies that the allowlisted
// ActorSystem action smtp.phase1_rcpt_leak stays in the audit log and NOT in
// the system events table (REQ-ADM-300, REQ-ADM-304, re #142).
func TestSystemEvents_AllowlistedActionsStayInAuditLog(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	ctx := context.Background()

	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}

	// Write the allowlisted action directly to audit log (as the production
	// protosmtp code does — it was NOT redirected to system_events).
	if err := h.h.Store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:        time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		ActorKind: store.ActorSystem,
		ActorID:   "smtp",
		Action:    "smtp.phase1_rcpt_leak",
		Subject:   "recipient:leaked@example.com",
		Outcome:   store.OutcomeFailure,
		Message:   "non-local recipient leaked past RCPT TO",
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}

	// It must appear in the audit log.
	res, buf := h.doRequest("GET", "/api/v1/audit?action=smtp.phase1_rcpt_leak", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET audit: %d: %s", res.StatusCode, buf)
	}
	var auditOut struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &auditOut); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(auditOut.Items) == 0 {
		t.Errorf("allowlisted smtp.phase1_rcpt_leak not found in audit log")
	}

	// It must NOT appear in system events (was not written there).
	res, buf = h.doRequest("GET", "/api/v1/admin/system-events?action=smtp.phase1_rcpt_leak", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET system-events: %d: %s", res.StatusCode, buf)
	}
	var sysOut struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &sysOut); err != nil {
		t.Fatalf("decode system-events: %v", err)
	}
	if len(sysOut.Items) != 0 {
		t.Errorf("allowlisted smtp.phase1_rcpt_leak leaked into system events: %+v", sysOut.Items)
	}
}

// TestSystemEvents_NoDomainAdminSeesEmpty is a regression test for the
// fail-closed leak: before the fix, a non-super-admin with NO managed domains
// had ResolveOperatorScope return Domains=nil, which SystemEventFilter treats
// as unrestricted (nil=all). That admin could see all events. After the fix,
// ResolveOperatorScope returns a non-nil empty Domains slice, causing the store
// to return zero results (fail-closed per REQ-ADM-307, re #145).
func TestSystemEvents_NoDomainAdminSeesEmpty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Bootstrap the first admin (now gets both Admin+SuperAdmin per Bug B fix).
	_, superKey := h.bootstrap("super@example.com")

	// Create a second admin with no super-admin flag and no managed domains.
	noDomainID := h.createPrincipal(superKey, "nodomain@example.com")
	nd, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(noDomainID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	nd.Flags = store.PrincipalFlagAdmin // admin but NOT super-admin, NO domains
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, nd); err != nil {
		t.Fatalf("UpdatePrincipal no-domain admin: %v", err)
	}
	_, ndKey := h.createAPIKey(superKey, noDomainID)

	// Insert a system event with a concrete domain.
	if err := h.h.Store.Meta().AppendSystemEvent(ctx, store.SystemEvent{
		At:      time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Action:  "regression.nodomain",
		ActorID: "test",
		Outcome: store.OutcomeSuccess,
		Domain:  "secret.example",
	}); err != nil {
		t.Fatalf("AppendSystemEvent: %v", err)
	}

	// The no-domain admin must get an empty result (fail-closed), not leak
	// the event for secret.example.
	res, buf := h.doRequest("GET", "/api/v1/admin/system-events?action=regression.nodomain", ndKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET system-events: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if len(out.Items) != 0 {
		t.Errorf("no-domain admin leaked %d event(s); want 0 (fail-closed): %+v",
			len(out.Items), out.Items)
	}
}
