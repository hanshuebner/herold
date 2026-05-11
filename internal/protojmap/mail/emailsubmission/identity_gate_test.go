package emailsubmission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// mapResolver lets the per-test code map IdentityIDs to email
// addresses. Used by the REQ-IDENT-60 / REQ-IDENT-62 gate tests where
// the persistent-identity row's email differs from "default".
type mapResolver struct{ m map[string]string }

func (r mapResolver) IdentityEmail(_ context.Context, _ store.Principal, id string) (string, bool) {
	email, ok := r.m[id]
	if !ok || email == "" {
		return "", false
	}
	return email, true
}

// TestEmailSubmission_Set_Gate_VerifiedHostedDomain confirms the
// happy path of REQ-IDENT-60 / REQ-IDENT-62: a persistent identity
// with VerifiedAtUs != 0 living in a local domain passes the gate
// and the submission is enqueued. Regression for the existing
// "default" identity hosted-domain path.
func TestEmailSubmission_Set_Gate_VerifiedHostedDomain(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	ctx := context.Background()
	// Mark example.test as a hosted (local) domain so the
	// REQ-IDENT-62 external-domain branch is skipped.
	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	// Insert a verified persistent identity for the principal whose
	// email matches the principal's canonical address (so the
	// sendpolicy ownership check passes without an alias row).
	const idID = "id-hosted-verified"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           idID,
		PrincipalID:  p.ID,
		Name:         "Alice",
		Email:        "alice@example.test",
		MayDelete:    true,
		VerifiedAtUs: 1,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	h.identity = mapResolver{m: map[string]string{idID: "alice@example.test"}}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": idID,
				"emailId":    renderEmailID(mid),
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"created"`) {
		t.Fatalf("expected created entry: %s", js)
	}
	if strings.Contains(string(js), `"notCreated"`) {
		t.Fatalf("did not expect notCreated for verified hosted identity: %s", js)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 queue submit, got %d", len(sub.calls))
	}
}

// TestEmailSubmission_Set_Gate_DefaultIdentitySkipsVerify confirms
// that the synthesised default identity (id "default") bypasses the
// REQ-IDENT-60 verification gate. The default is verified-by-
// construction per REQ-IDENT-02; no GetJMAPIdentity lookup happens.
func TestEmailSubmission_Set_Gate_DefaultIdentitySkipsVerify(t *testing.T) {
	h, _, p, _, mid, sub := newSetup(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"created"`) {
		t.Fatalf("expected created (default identity bypasses gate): %s", js)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 queue submit, got %d", len(sub.calls))
	}
}

// TestEmailSubmission_Set_Gate_UnverifiedIdentityRejected confirms
// REQ-IDENT-60: a persistent identity whose VerifiedAtUs is zero
// is rejected at the JMAP boundary with
// forbiddenFrom { description: "identity is not verified" }. No
// queue submit, no message dispatch, no internal panic.
func TestEmailSubmission_Set_Gate_UnverifiedIdentityRejected(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	ctx := context.Background()
	const idID = "id-unverified"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           idID,
		PrincipalID:  p.ID,
		Name:         "Alice (pending)",
		Email:        "alice@example.test",
		MayDelete:    true,
		VerifiedAtUs: 0, // explicit; the gate point
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	h.identity = mapResolver{m: map[string]string{idID: "alice@example.test"}}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": idID,
				"emailId":    renderEmailID(mid),
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("unexpected method error: %v", mErr)
	}
	sresp := resp.(setResponse)
	if len(sresp.Created) != 0 {
		t.Fatalf("expected no created entries, got %d", len(sresp.Created))
	}
	se, ok := sresp.NotCreated["k1"]
	if !ok {
		t.Fatalf("expected notCreated[k1]")
	}
	if se.Type != "forbiddenFrom" {
		t.Fatalf("expected type=forbiddenFrom, got %q", se.Type)
	}
	if se.Description != "identity is not verified" {
		t.Fatalf("expected description='identity is not verified', got %q", se.Description)
	}
	if len(se.Properties) != 1 || se.Properties[0] != "identityId" {
		t.Fatalf("expected properties=[identityId], got %v", se.Properties)
	}
	// REQ-IDENT-60: no message dispatched.
	if len(sub.calls) != 0 {
		t.Fatalf("expected 0 queue submits, got %d", len(sub.calls))
	}
	// And no submission row persisted.
	rows, _ := st.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{Limit: 10})
	if len(rows) != 0 {
		t.Fatalf("expected 0 persisted submission rows, got %d", len(rows))
	}
}

// TestEmailSubmission_Set_Gate_ExternalDomainNoSubmissionRejected
// confirms REQ-IDENT-62: a verified identity whose email domain is
// not in ListLocalDomains and has no IdentitySubmission row is
// rejected with
// forbiddenFrom { description: "external identity requires
// submission configuration" }. The rejection happens BEFORE the
// sendpolicy ownership check, so no alias is needed.
func TestEmailSubmission_Set_Gate_ExternalDomainNoSubmissionRejected(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	ctx := context.Background()
	// Mark example.test as local so we can distinguish: the identity
	// lives in gmail.com which is NOT local.
	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	const idID = "id-extdom-nosubmit"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           idID,
		PrincipalID:  p.ID,
		Name:         "Alice external",
		Email:        "hans@gmail.com",
		MayDelete:    true,
		VerifiedAtUs: 1,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	h.identity = mapResolver{m: map[string]string{idID: "hans@gmail.com"}}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": idID,
				"emailId":    renderEmailID(mid),
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("unexpected method error: %v", mErr)
	}
	sresp := resp.(setResponse)
	if len(sresp.Created) != 0 {
		t.Fatalf("expected no created entries, got %d", len(sresp.Created))
	}
	se, ok := sresp.NotCreated["k1"]
	if !ok {
		t.Fatalf("expected notCreated[k1]")
	}
	if se.Type != "forbiddenFrom" {
		t.Fatalf("expected type=forbiddenFrom, got %q", se.Type)
	}
	if se.Description != "external identity requires submission configuration" {
		t.Fatalf("expected description='external identity requires submission configuration', got %q",
			se.Description)
	}
	if len(se.Properties) != 1 || se.Properties[0] != "identityId" {
		t.Fatalf("expected properties=[identityId], got %v", se.Properties)
	}
	// No message dispatched, no submission row.
	if len(sub.calls) != 0 {
		t.Fatalf("expected 0 queue submits, got %d", len(sub.calls))
	}
	rows, _ := st.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{Limit: 10})
	if len(rows) != 0 {
		t.Fatalf("expected 0 persisted submission rows, got %d", len(rows))
	}
}

// TestEmailSubmission_Set_Gate_ExternalDomainWithSubmissionSucceeds
// confirms the positive REQ-IDENT-62 path: a verified external-
// domain identity backed by an IdentitySubmission row is accepted
// and dispatched through the external submitter. The principal is
// flagged admin so the post-gate sendpolicy ownership check accepts
// the external address without a full alias-table mock.
func TestEmailSubmission_Set_Gate_ExternalDomainWithSubmissionSucceeds(t *testing.T) {
	h, st, p, mid, extSub, _ := newExternalSetup(t, extsubmit.Outcome{
		State:      extsubmit.OutcomeOK,
		Diagnostic: "accepted",
	})
	ctx := context.Background()
	// Hosted-domain marker so the gate sees gmail.com as external.
	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	const idID = "id-extdom-with-submit"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           idID,
		PrincipalID:  p.ID,
		Name:         "Alice external",
		Email:        "hans@gmail.com",
		MayDelete:    true,
		VerifiedAtUs: 1,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	// An IdentitySubmission row keyed by the wire JMAP id.
	if err := st.Meta().UpsertIdentitySubmission(ctx, store.IdentitySubmission{
		IdentityID:       idID,
		SubmitHost:       "smtp.gmail.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:fake-sealed-password"),
	}); err != nil {
		t.Fatalf("UpsertIdentitySubmission: %v", err)
	}
	// Resolver returns the external address; tests below rely on
	// sendpolicy bypass via admin to accept hans@gmail.com as a
	// from-address owned by the principal.
	h.identity = mapResolver{m: map[string]string{idID: "hans@gmail.com"}}
	// Re-stamp the principal with the admin flag so sendpolicy
	// allows the external from-address without a full alias mock.
	pAdmin := p
	pAdmin.Flags = pAdmin.Flags | store.PrincipalFlagAdmin

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": idID,
				"emailId":    renderEmailID(mid),
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(pAdmin, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	sresp := resp.(setResponse)
	if len(sresp.Created) != 1 {
		js, _ := json.Marshal(resp)
		t.Fatalf("expected 1 created entry, got %d (resp=%s)", len(sresp.Created), js)
	}
	if len(sresp.NotCreated) != 0 {
		js, _ := json.Marshal(resp)
		t.Fatalf("did not expect notCreated entries: %s", js)
	}
	// External submitter was called exactly once.
	if len(extSub.calls) != 1 {
		t.Fatalf("expected 1 external submit, got %d", len(extSub.calls))
	}
	env := extSub.calls[0]
	if env.MailFrom != "hans@gmail.com" {
		t.Fatalf("MailFrom: got %q, want hans@gmail.com", env.MailFrom)
	}
	// The persisted row carries External=true.
	subs, _ := st.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{Limit: 10})
	if len(subs) != 1 || !subs[0].External {
		t.Fatalf("expected 1 external submission row, got %+v", subs)
	}
}
