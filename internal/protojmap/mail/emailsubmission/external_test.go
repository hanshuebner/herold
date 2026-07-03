package emailsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"path/filepath"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fakeExternalSubmitter records Submit calls and returns a preset Outcome.
type fakeExternalSubmitter struct {
	calls   []extsubmit.Envelope
	outcome extsubmit.Outcome
}

func (f *fakeExternalSubmitter) Submit(_ context.Context, _ store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome {
	body, _ := io.ReadAll(env.Body)
	env.Body = bytes.NewReader(body) // reset for inspection
	cp := extsubmit.Envelope{
		MailFrom:      env.MailFrom,
		RcptTo:        append([]string(nil), env.RcptTo...),
		CorrelationID: env.CorrelationID,
	}
	f.calls = append(f.calls, cp)
	return f.outcome
}

// delayingFakeExternalSubmitter sleeps delay before returning a preset Outcome.
// Used to verify that the relay runs asynchronously (re #108).
type delayingFakeExternalSubmitter struct {
	delay   time.Duration
	outcome extsubmit.Outcome
}

func (f *delayingFakeExternalSubmitter) Submit(_ context.Context, _ store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome {
	_, _ = io.ReadAll(env.Body) // drain the body so the test is clean
	time.Sleep(f.delay)
	return f.outcome
}

// fakeExternalRouter implements ExternalRouter with configurable responses.
type fakeExternalRouter struct {
	has    bool
	cfg    store.IdentitySubmission
	bumped []store.PrincipalID
}

func (r *fakeExternalRouter) HasExternalSubmission(_ context.Context, _ store.PrincipalID, _ string) bool {
	return r.has
}

func (r *fakeExternalRouter) SubmissionConfig(_ context.Context, _ store.PrincipalID, _ string) (store.IdentitySubmission, error) {
	return r.cfg, nil
}

func (r *fakeExternalRouter) BumpIdentityPushState(_ context.Context, pid store.PrincipalID) error {
	r.bumped = append(r.bumped, pid)
	return nil
}

// newExternalSetup builds a handlerSet wired with fakeExternalSubmitter and
// fakeExternalRouter. It returns the handler, store, principal, mailbox,
// message id, the external submitter, and the external router for assertions.
func newExternalSetup(t *testing.T, outcome extsubmit.Outcome) (
	*handlerSet, store.Store, store.Principal, store.MessageID,
	*fakeExternalSubmitter, *fakeExternalRouter,
) {
	t.Helper()
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain example.test: %v", err)
	}
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	mb, _ := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	body := "From: alice@example.test\r\nTo: bob@remote.test\r\nSubject: hi\r\n\r\nbody.\r\n"
	ref, _ := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	uid, _, _ := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "hi",
			From:    "alice@example.test",
			To:      "bob@remote.test",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}

	extSub := &fakeExternalSubmitter{outcome: outcome}
	extRouter := &fakeExternalRouter{has: true}

	h := &handlerSet{
		store:          st,
		queue:          &fakeSubmitter{store: st},
		clk:            clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity:       stubResolver{email: "alice@example.test"},
		externalSubmit: extSub,
		externalRouter: extRouter,
	}
	// Drain background goroutines before closing the store; see
	// newSetup in emailsubmission_test.go for rationale.
	t.Cleanup(func() {
		h.Wait()
		_ = st.Close()
	})
	return h, st, p, mid, extSub, extRouter
}

// TestEmailSubmission_External_OKOutcome verifies that a successful external
// submission produces a row with External=true and delivered=yes for the
// recipient.
func TestEmailSubmission_External_OKOutcome(t *testing.T) {
	h, st, p, mid, extSub, _ := newExternalSetup(t, extsubmit.Outcome{
		State:      extsubmit.OutcomeOK,
		Diagnostic: "accepted by smtp.example.test: <id@example.test>",
	})
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
		t.Fatalf("expected created: %s", js)
	}

	// The EnvelopeID starts with "ext:".
	sresp := resp.(setResponse)
	if len(sresp.Created) == 0 {
		t.Fatal("no created entries")
	}
	var createdID string
	for _, v := range sresp.Created {
		createdID = v.ID
	}
	if !strings.HasPrefix(createdID, "ext:") {
		t.Fatalf("expected ext: prefix on id, got %q", createdID)
	}

	// Wait for the relay goroutine to complete, then check final state.
	h.Wait()

	// Exactly one Submit call was made.
	if len(extSub.calls) != 1 {
		t.Fatalf("expected 1 external submit call, got %d", len(extSub.calls))
	}
	env := extSub.calls[0]
	if env.MailFrom != "alice@example.test" {
		t.Fatalf("MailFrom: got %q", env.MailFrom)
	}
	if len(env.RcptTo) != 1 || env.RcptTo[0] != "bob@remote.test" {
		t.Fatalf("RcptTo: got %v", env.RcptTo)
	}

	// The row is persisted with External=true and final undo status.
	ctx := context.Background()
	subRow, err := st.Meta().GetEmailSubmission(ctx, createdID)
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if !subRow.External {
		t.Fatal("expected External=true")
	}
	if subRow.UndoStatus != string(undoStatusFinal) {
		t.Fatalf("expected UndoStatus=final, got %q", subRow.UndoStatus)
	}

	// /get returns delivered=yes.
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	getResp, _ := getHandler{h: h}.executeAs(p, getArgs)
	gjs, _ := json.Marshal(getResp)
	if !strings.Contains(string(gjs), `"yes"`) {
		t.Fatalf("expected delivered=yes in /get response: %s", gjs)
	}
}

// TestEmailSubmission_External_AuthFailedOutcome verifies that an auth-failed
// outcome parks the submission (undoStatus=pending, HeldForReauth=true,
// delivered=queued / smtpReply="pending re-authentication") and bumps
// JMAPStateKindIdentity (re #70, REQ-AUTH-EXT-SUBMIT-05).
func TestEmailSubmission_External_AuthFailedOutcome(t *testing.T) {
	h, st, p, mid, _, extRouter := newExternalSetup(t, extsubmit.Outcome{
		State:      extsubmit.OutcomeAuthFailed,
		Diagnostic: "535 auth failed",
	})
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
		t.Fatalf("expected created: %s", js)
	}

	// Wait for the relay goroutine to complete before checking outcomes.
	h.Wait()

	// BumpIdentityPushState was called once.
	if len(extRouter.bumped) != 1 {
		t.Fatalf("expected 1 identity state bump, got %d", len(extRouter.bumped))
	}
	if extRouter.bumped[0] != p.ID {
		t.Fatalf("bumped wrong principal: got %v want %v", extRouter.bumped[0], p.ID)
	}

	// /get reflects delivered=queued and "pending re-authentication" smtpReply
	// (not "no" — the submission is parked for retry, not finally failed).
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	getResp, _ := getHandler{h: h}.executeAs(p, getArgs)
	gjs, _ := json.Marshal(getResp)
	if !strings.Contains(string(gjs), `"queued"`) {
		t.Fatalf("expected delivered=queued in /get response: %s", gjs)
	}
	if !strings.Contains(string(gjs), "pending re-authentication") {
		t.Fatalf("expected 'pending re-authentication' smtpReply in /get response: %s", gjs)
	}
	if strings.Contains(string(gjs), `"no"`) {
		t.Fatalf("unexpected delivered=no in /get response (submission should be parked): %s", gjs)
	}

	// The store row carries External=true, undoStatus=pending, HeldForReauth=true.
	ctx := context.Background()
	subs, _ := st.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{Limit: 10})
	if len(subs) != 1 {
		t.Fatalf("expected 1 submission row, got %d", len(subs))
	}
	if !subs[0].External {
		t.Fatal("expected External=true")
	}
	if subs[0].UndoStatus != string(undoStatusPending) {
		t.Fatalf("undoStatus = %q, want pending (row is parked)", subs[0].UndoStatus)
	}
	if !subs[0].HeldForReauth {
		t.Fatal("HeldForReauth = false, want true")
	}
	if subs[0].HoldDeadlineUs == 0 {
		t.Fatal("HoldDeadlineUs is zero, want a future timestamp")
	}
}

// TestEmailSubmission_External_UnreachableOutcome verifies that an unreachable
// outcome also bumps the identity push state.
func TestEmailSubmission_External_UnreachableOutcome(t *testing.T) {
	h, _, p, mid, _, extRouter := newExternalSetup(t, extsubmit.Outcome{
		State:      extsubmit.OutcomeUnreachable,
		Diagnostic: "dial tcp: connection refused",
	})
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{"identityId": "default", "emailId": renderEmailID(mid)},
		},
	})
	_, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("set: %v", mErr)
	}
	// Wait for the relay goroutine to complete before asserting bump count.
	h.Wait()
	if len(extRouter.bumped) != 1 {
		t.Fatalf("expected 1 identity state bump for unreachable, got %d", len(extRouter.bumped))
	}
}

// TestEmailSubmission_External_PermanentOutcome verifies that a permanent
// failure does NOT bump the identity push state (only auth-failed and
// unreachable do).
func TestEmailSubmission_External_PermanentOutcome(t *testing.T) {
	h, _, p, mid, _, extRouter := newExternalSetup(t, extsubmit.Outcome{
		State:      extsubmit.OutcomePermanent,
		Diagnostic: "550 user unknown",
	})
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{"identityId": "default", "emailId": renderEmailID(mid)},
		},
	})
	_, mErr2 := setHandler{h: h}.executeAs(p, args)
	if mErr2 != nil {
		t.Fatalf("set: %v", mErr2)
	}
	// Wait for relay goroutine before asserting no bump.
	h.Wait()
	if len(extRouter.bumped) != 0 {
		t.Fatalf("expected no identity state bump for permanent, got %d", len(extRouter.bumped))
	}
}

// TestEmailSubmission_External_DestroyCannotUnsend verifies that destroy on
// an External=true submission row returns cannotUnsend.
func TestEmailSubmission_External_DestroyCannotUnsend(t *testing.T) {
	h, _, p, mid, _, _ := newExternalSetup(t, extsubmit.Outcome{
		State: extsubmit.OutcomeOK,
	})
	// Create the submission first.
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{"identityId": "default", "emailId": renderEmailID(mid)},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, createArgs)
	if mErr != nil {
		t.Fatalf("set create: %v", mErr)
	}
	sresp := resp.(setResponse)
	var subID string
	for _, v := range sresp.Created {
		subID = v.ID
	}
	if subID == "" {
		t.Fatal("no created submission id")
	}

	// Now attempt destroy — must return cannotUnsend in notDestroyed.
	// The row carries External=true even before the relay goroutine runs.
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"destroy":   []string{subID},
	})
	dresp, mErr := setHandler{h: h}.executeAs(p, destroyArgs)
	if mErr != nil {
		t.Fatalf("set destroy: %v", mErr)
	}
	djs, _ := json.Marshal(dresp)
	if !strings.Contains(string(djs), `"cannotUnsend"`) {
		t.Fatalf("expected cannotUnsend in notDestroyed: %s", djs)
	}
	if strings.Contains(string(djs), `"destroyed"`) && !strings.Contains(string(djs), `"notDestroyed"`) {
		t.Fatalf("submission was unexpectedly destroyed: %s", djs)
	}
}

// TestEmailSubmission_External_LocalFallbackWhenNoRouter verifies that when
// externalRouter is nil the submission falls through to the local queue.
func TestEmailSubmission_External_LocalFallbackWhenNoRouter(t *testing.T) {
	h, _, p, _, mid, sub := newSetup(t)
	// Ensure external router is nil (default from newSetup).
	if h.externalRouter != nil {
		t.Fatal("expected nil externalRouter from newSetup")
	}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{"identityId": "default", "emailId": renderEmailID(mid)},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"created"`) {
		t.Fatalf("expected created: %s", js)
	}
	// Queue submitter was called (not external).
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 queue submit, got %d", len(sub.calls))
	}
}

// TestEmailSubmission_External_RelayAsync verifies that the external relay runs
// asynchronously and does not block the JMAP method beyond the deadline (re #108).
//
// Before the fix, processCreateExternal called Submit synchronously, so the method
// would block for the full delay and the dispatcher would see DeadlineExceeded.
// After the fix, the method returns immediately with undoStatus=pending and the row
// reaches undoStatus=final after the background goroutine completes.
func TestEmailSubmission_External_RelayAsync(t *testing.T) {
	const relayDelay = 200 * time.Millisecond

	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	ctx := context.Background()
	_ = st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true})
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	mb, _ := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	body := "From: alice@example.test\r\nTo: bob@remote.test\r\nSubject: hi\r\n\r\nbody.\r\n"
	ref, _ := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	uid, _, _ := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "hi",
			From:    "alice@example.test",
			To:      "bob@remote.test",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}

	slowSub := &delayingFakeExternalSubmitter{
		delay:   relayDelay,
		outcome: extsubmit.Outcome{State: extsubmit.OutcomeOK, Diagnostic: "250 ok"},
	}
	h := &handlerSet{
		store:          st,
		queue:          &fakeSubmitter{store: st},
		clk:            clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity:       stubResolver{email: "alice@example.test"},
		externalSubmit: slowSub,
		externalRouter: &fakeExternalRouter{has: true},
	}
	t.Cleanup(func() {
		h.Wait()
		_ = st.Close()
	})

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
			},
		},
	})

	// Use a context with a deadline shorter than the relay delay.
	// Before the fix, Execute would block for relayDelay and the
	// method would exceed this deadline; after the fix it returns
	// immediately with undoStatus=pending.
	deadline := relayDelay / 4 // 50 ms << 200 ms relay delay
	callCtx, cancel := context.WithTimeout(contextWithTestPrincipal(context.Background(), p), deadline)
	defer cancel()

	start := time.Now()
	resp, mErr := setHandler{h: h}.Execute(callCtx, args)
	elapsed := time.Since(start)

	if mErr != nil {
		t.Fatalf("EmailSubmission/set returned error: %v (elapsed %v)", mErr, elapsed)
	}
	if elapsed >= relayDelay {
		t.Fatalf("Submit ran synchronously: elapsed %v >= relayDelay %v; async fix not in effect", elapsed, relayDelay)
	}

	// The created submission should be pending (relay still in flight).
	sresp := resp.(setResponse)
	if len(sresp.Created) == 0 {
		t.Fatal("no created entries in response")
	}
	var sub jmapEmailSubmission
	for _, v := range sresp.Created {
		sub = v
	}
	if sub.UndoStatus != undoStatusPending {
		t.Fatalf("expected undoStatus=pending immediately after create, got %q", sub.UndoStatus)
	}

	// Wait for the relay goroutine to finish.
	h.Wait()

	// After the goroutine completes the row must be finalized.
	subRow, err := st.Meta().GetEmailSubmission(context.Background(), sub.ID)
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if subRow.UndoStatus != string(undoStatusFinal) {
		t.Fatalf("expected undoStatus=final after relay, got %q", subRow.UndoStatus)
	}
	if !subRow.External {
		t.Fatal("expected External=true")
	}
}
