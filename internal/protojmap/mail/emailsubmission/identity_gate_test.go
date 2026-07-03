package emailsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
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
	// example.test is already registered as a local domain by newSetup.
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
	// example.test is already local (registered by newSetup). The identity
	// lives in gmail.com which is NOT local, so the REQ-IDENT-62 gate fires.
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
	// example.test is already local (registered by newExternalSetup), so the
	// gate sees gmail.com as external.
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
	// Wait for the relay goroutine to complete before asserting call count.
	h.Wait()

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

// -- re #74: local-queue foreign-domain gate (new check) -----------------

// testLocalQueueForeignDomainRejected is the backend-agnostic body of
// TestEmailSubmission_Set_Gate_LocalQueueForeignDomain_Rejected_*.
//
// It exercises the structural invariant added at the local-queue boundary:
// an identity whose domain is NOT authoritative on this server must be
// rejected with forbiddenFrom even when it carries an IdentitySubmission
// row (which satisfies REQ-IDENT-62) but the externalRouter is absent or
// returns HasExternalSubmission=false. The local queue must not be reached.
func testLocalQueueForeignDomainRejected(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	body := "From: alice@gmail.com\r\nTo: bob@example.test\r\nSubject: test\r\n\r\nbody.\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref, Size: int64(len(body)),
		Envelope: store.Envelope{Subject: "test", From: "alice@gmail.com", To: "bob@example.test"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}

	const idID = "id-lq-foreign"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: idID, PrincipalID: p.ID, Name: "Foreign",
		Email: "alice@gmail.com", MayDelete: true, VerifiedAtUs: 1,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	// IdentitySubmission row present so the REQ-IDENT-62 gate passes.
	// The test scenario is: external router is nil (disabled), so the
	// request falls through to the new local-queue domain gate.
	if err := st.Meta().UpsertIdentitySubmission(ctx, store.IdentitySubmission{
		IdentityID: idID, SubmitHost: "smtp.gmail.com", SubmitPort: 587,
		SubmitSecurity: "starttls", SubmitAuthMethod: "password",
		PasswordCT: []byte("v1:fake"),
	}); err != nil {
		t.Fatalf("UpsertIdentitySubmission: %v", err)
	}

	sub := &fakeSubmitter{store: st}
	h := &handlerSet{
		store:    st,
		queue:    sub,
		clk:      clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity: mapResolver{m: map[string]string{idID: "alice@gmail.com"}},
		// externalRouter intentionally nil: external submission configured
		// in DB but router absent, so falls through to local-queue gate.
	}
	t.Cleanup(func() { h.Wait() })

	// Admin flag lets sendpolicy accept alice@gmail.com as a from
	// address for the principal, so the sendpolicy check does not fire
	// before the new local-queue gate.
	pAdmin := p
	pAdmin.Flags |= store.PrincipalFlagAdmin

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{"identityId": idID, "emailId": renderEmailID(mid)},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(pAdmin, args)
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
	if len(se.Properties) == 0 || se.Properties[0] != "identityId" {
		t.Fatalf("expected properties=[identityId], got %v", se.Properties)
	}
	// The local queue must not have been reached.
	if len(sub.calls) != 0 {
		t.Fatalf("expected 0 queue submits, got %d", len(sub.calls))
	}
	// No submission row persisted.
	rows, _ := st.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{Limit: 10})
	if len(rows) != 0 {
		t.Fatalf("expected 0 submission rows, got %d", len(rows))
	}
}

// TestEmailSubmission_Set_Gate_LocalQueueForeignDomain_Rejected confirms (1):
// a foreign-domain identity that falls through to the local queue is rejected
// with forbiddenFrom and does not enqueue.
func TestEmailSubmission_Set_Gate_LocalQueueForeignDomain_Rejected(t *testing.T) {
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testLocalQueueForeignDomainRejected(t, st)
}

// TestEmailSubmission_Set_Gate_LocalQueueForeignDomain_Rejected_Postgres
// runs the same assertion against the Postgres backend. Skips when
// HEROLD_PG_DSN is not set.
func TestEmailSubmission_Set_Gate_LocalQueueForeignDomain_Rejected_Postgres(t *testing.T) {
	testLocalQueueForeignDomainRejected(t, openPostgresStore(t))
}

// testLocalDomainLocalQueueSucceeds is the backend-agnostic body of
// TestEmailSubmission_Set_Gate_LocalDomainLocalQueue_Succeeds_*. It
// confirms (2): an identity on an authoritative domain is not blocked
// by the local-queue domain gate and submits successfully.
func testLocalDomainLocalQueueSucceeds(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	body := "From: alice@example.test\r\nTo: bob@remote.test\r\nSubject: test\r\n\r\nbody.\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref, Size: int64(len(body)),
		Envelope: store.Envelope{Subject: "test", From: "alice@example.test", To: "bob@remote.test"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}

	sub := &fakeSubmitter{store: st}
	h := &handlerSet{
		store:    st,
		queue:    sub,
		clk:      clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity: stubResolver{email: "alice@example.test"},
	}
	t.Cleanup(func() { h.Wait() })

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{"identityId": "default", "emailId": renderEmailID(mid)},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	sresp := resp.(setResponse)
	if len(sresp.Created) != 1 {
		t.Fatalf("expected 1 created entry, got %d", len(sresp.Created))
	}
	if len(sresp.NotCreated) != 0 {
		js, _ := json.Marshal(sresp.NotCreated)
		t.Fatalf("expected no notCreated, got %s", js)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 local queue submit, got %d", len(sub.calls))
	}
}

// TestEmailSubmission_Set_Gate_LocalDomainLocalQueue_Succeeds confirms (2):
// a local/authoritative-domain identity submits via the local queue.
func TestEmailSubmission_Set_Gate_LocalDomainLocalQueue_Succeeds(t *testing.T) {
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testLocalDomainLocalQueueSucceeds(t, st)
}

// TestEmailSubmission_Set_Gate_LocalDomainLocalQueue_Succeeds_Postgres
// runs the same assertion against the Postgres backend. Skips when
// HEROLD_PG_DSN is not set.
func TestEmailSubmission_Set_Gate_LocalDomainLocalQueue_Succeeds_Postgres(t *testing.T) {
	testLocalDomainLocalQueueSucceeds(t, openPostgresStore(t))
}

// testForeignDomainExternalRouterNotRejected is the backend-agnostic body of
// TestEmailSubmission_Set_Gate_ForeignDomainExternalRouter_NotRejected_*.
//
// It confirms (3): a foreign-domain identity WITH external submission
// configured routes through processCreateExternal and is never blocked by the
// local-queue domain gate. The gate only guards the queue.Submit fall-through.
func testForeignDomainExternalRouterNotRejected(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	body := "From: alice@gmail.com\r\nTo: bob@remote.test\r\nSubject: test\r\n\r\nbody.\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref, Size: int64(len(body)),
		Envelope: store.Envelope{Subject: "test", From: "alice@gmail.com", To: "bob@remote.test"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}

	const idID = "id-foreign-ext-router"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: idID, PrincipalID: p.ID, Name: "Foreign",
		Email: "alice@gmail.com", MayDelete: true, VerifiedAtUs: 1,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	if err := st.Meta().UpsertIdentitySubmission(ctx, store.IdentitySubmission{
		IdentityID: idID, SubmitHost: "smtp.gmail.com", SubmitPort: 587,
		SubmitSecurity: "starttls", SubmitAuthMethod: "password",
		PasswordCT: []byte("v1:fake"),
	}); err != nil {
		t.Fatalf("UpsertIdentitySubmission: %v", err)
	}

	extSub := &fakeExternalSubmitter{outcome: extsubmit.Outcome{
		State: extsubmit.OutcomeOK, Diagnostic: "accepted",
	}}
	extRouter := &fakeExternalRouter{has: true}
	sub := &fakeSubmitter{store: st}
	h := &handlerSet{
		store:          st,
		queue:          sub,
		clk:            clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity:       mapResolver{m: map[string]string{idID: "alice@gmail.com"}},
		externalSubmit: extSub,
		externalRouter: extRouter,
	}
	t.Cleanup(func() { h.Wait() })

	// Admin flag lets sendpolicy accept alice@gmail.com.
	pAdmin := p
	pAdmin.Flags |= store.PrincipalFlagAdmin

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{"identityId": idID, "emailId": renderEmailID(mid)},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(pAdmin, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	sresp := resp.(setResponse)
	if len(sresp.Created) != 1 {
		js, _ := json.Marshal(sresp)
		t.Fatalf("expected 1 created entry, got %d (resp=%s)", len(sresp.Created), js)
	}
	if len(sresp.NotCreated) != 0 {
		js, _ := json.Marshal(sresp.NotCreated)
		t.Fatalf("expected no notCreated, got %s", js)
	}
	// Wait for the relay goroutine to complete before asserting call count.
	h.Wait()

	// External submitter was used; local queue was bypassed.
	if len(extSub.calls) != 1 {
		t.Fatalf("expected 1 external submit, got %d", len(extSub.calls))
	}
	if len(sub.calls) != 0 {
		t.Fatalf("expected 0 local queue submits (external route taken), got %d", len(sub.calls))
	}
}

// TestEmailSubmission_Set_Gate_ForeignDomainExternalRouter_NotRejected
// confirms (3): a foreign-domain identity with external submission configured
// routes externally and is not blocked by the local-queue domain gate.
func TestEmailSubmission_Set_Gate_ForeignDomainExternalRouter_NotRejected(t *testing.T) {
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testForeignDomainExternalRouterNotRejected(t, st)
}

// TestEmailSubmission_Set_Gate_ForeignDomainExternalRouter_NotRejected_Postgres
// runs the same assertion against the Postgres backend. Skips when
// HEROLD_PG_DSN is not set.
func TestEmailSubmission_Set_Gate_ForeignDomainExternalRouter_NotRejected_Postgres(t *testing.T) {
	testForeignDomainExternalRouterNotRejected(t, openPostgresStore(t))
}
