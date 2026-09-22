package emailsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// gateFakeExternalSubmitter blocks in Submit until the test releases the gate
// by closing the release channel. This lets tests prove the relay is async
// without depending on wall-clock timing: Execute returning while the gate is
// still closed is structural proof that Submit was not called synchronously.
type gateFakeExternalSubmitter struct {
	release chan struct{}
	outcome extsubmit.Outcome
}

func (f *gateFakeExternalSubmitter) Submit(_ context.Context, _ store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome {
	_, _ = io.ReadAll(env.Body) // drain body before blocking so the reader is consumed
	<-f.release                 // block until the test closes the gate
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

// TestEmailSubmission_External_RelayBlobMissing verifies that when the blob
// backing the submitted message is unreadable (e.g. removed from the store
// before the relay goroutine runs), execExternalRelay transitions the row to
// a terminal failure state (undoStatus=final, delivered=no) rather than
// leaving it stuck as undoStatus=pending indefinitely (re #108).
func TestEmailSubmission_External_RelayBlobMissing(t *testing.T) {
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

	// Delete the underlying blob file so that Blobs().Get() in execExternalRelay
	// returns ErrNotFound. GetMessage still succeeds (the message row references
	// the hash via blob_refs FK), but the blob content is gone.
	if err := st.Blobs().Delete(ctx, ref.Hash); err != nil {
		t.Fatalf("Blobs().Delete: %v", err)
	}

	extSub := &fakeExternalSubmitter{outcome: extsubmit.Outcome{State: extsubmit.OutcomeOK}}
	h := &handlerSet{
		store:          st,
		queue:          &fakeSubmitter{store: st},
		clk:            clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity:       stubResolver{email: "alice@example.test"},
		externalSubmit: extSub,
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
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	sresp := resp.(setResponse)
	if len(sresp.Created) == 0 {
		t.Fatal("no created entries")
	}
	var subID string
	for _, v := range sresp.Created {
		subID = v.ID
	}

	// Wait for the relay goroutine to complete.
	h.Wait()

	// The row must be finalized as a permanent failure, not stuck as pending.
	subRow, getErr := st.Meta().GetEmailSubmission(ctx, subID)
	if getErr != nil {
		t.Fatalf("GetEmailSubmission: %v", getErr)
	}
	if subRow.UndoStatus != string(undoStatusFinal) {
		t.Fatalf("undoStatus = %q, want final (blob was deleted)", subRow.UndoStatus)
	}

	// /get must reflect delivered=no.
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	getResp, _ := getHandler{h: h}.executeAs(p, getArgs)
	gjs, _ := json.Marshal(getResp)
	if !strings.Contains(string(gjs), `"no"`) {
		t.Fatalf("expected delivered=no in /get response after missing blob: %s", gjs)
	}

	// Submit must not have been called — the blob failure precedes it.
	if len(extSub.calls) != 0 {
		t.Fatalf("expected 0 Submit calls when blob is missing, got %d", len(extSub.calls))
	}
}

// TestEmailSubmission_External_RelayAsync verifies that the external relay runs
// asynchronously and does not block Execute (re #108).
//
// The fake submitter blocks on an unbuffered channel ("gate") that the test
// controls. Execute is called with context.Background() so the test does not
// race wall-clock against Execute's own synchronous SQLite work. Async is
// proven structurally: Execute returns with undoStatus=pending while the gate
// is still closed, which means Submit could not have been called on the
// critical path. Releasing the gate then lets h.Wait() confirm the relay
// goroutine ran to completion and updated the row to undoStatus=final.
func TestEmailSubmission_External_RelayAsync(t *testing.T) {
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

	// gate blocks Submit until the test closes it. Using an unbuffered channel
	// guarantees the relay goroutine is provably still inside Submit while the
	// gate is open, so Execute returning before that point is structural proof
	// of async dispatch with no wall-clock dependency.
	gate := make(chan struct{})
	gatedSub := &gateFakeExternalSubmitter{
		release: gate,
		outcome: extsubmit.Outcome{State: extsubmit.OutcomeOK, Diagnostic: "250 ok"},
	}
	h := &handlerSet{
		store:          st,
		queue:          &fakeSubmitter{store: st},
		clk:            clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity:       stubResolver{email: "alice@example.test"},
		externalSubmit: gatedSub,
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

	// No artificial deadline: async is proven by the gate, not by a stopwatch.
	resp, mErr := setHandler{h: h}.Execute(contextWithTestPrincipal(context.Background(), p), args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set returned error: %v", mErr)
	}

	// Execute returned with the gate still closed, so Submit has not returned
	// yet. The response carries undoStatus=pending, which processCreateExternal
	// sets synchronously before launching the goroutine.
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

	// Release the gate and drain the relay goroutine.
	close(gate)
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

// TestEmailSubmission_External_ScheduledDoesNotRelayBeforeWindow verifies
// that an external submission created with a future sendAt (the undo-send
// window) is NOT handed to the relay when EmailSubmission/set create
// returns: the row is persisted RelayHeld=true and the fake submitter sees
// no Submit call until the relay scheduler dispatches it (re #478).
func TestEmailSubmission_External_ScheduledDoesNotRelayBeforeWindow(t *testing.T) {
	h, st, p, mid, extSub, _ := newExternalSetup(t, extsubmit.Outcome{
		State:      extsubmit.OutcomeOK,
		Diagnostic: "250 ok",
	})
	now := h.clk.Now()
	sendAt := now.Add(10 * time.Second)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"sendAt":     sendAt.Format(time.RFC3339),
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	sresp := resp.(setResponse)
	var subID string
	for _, v := range sresp.Created {
		subID = v.ID
	}
	if subID == "" {
		t.Fatal("no created submission id")
	}

	// Drain any (unrelated) background goroutines -- there must be no relay
	// goroutine to wait for, which is exactly what this test proves: with
	// the pre-fix code the relay goroutine is launched unconditionally and
	// h.Wait() would race it to completion, making extSub.calls == 1.
	h.Wait()

	if len(extSub.calls) != 0 {
		t.Fatalf("expected 0 Submit calls before the sendAt window elapses, got %d", len(extSub.calls))
	}

	subRow, err := st.Meta().GetEmailSubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if !subRow.RelayHeld {
		t.Fatal("expected RelayHeld=true for a submission scheduled inside its undo window")
	}
	if subRow.UndoStatus != string(undoStatusPending) {
		t.Fatalf("undoStatus = %q, want pending", subRow.UndoStatus)
	}
}

// TestEmailSubmission_External_DestroyWithinWindowCancelsBeforeRelay
// verifies the undo-send contract end to end (re #478): destroy called
// while RelayHeld is true cancels the submission, and the relay never sees
// the message even after the scheduled sendAt has passed and the scheduler
// runs.
func TestEmailSubmission_External_DestroyWithinWindowCancelsBeforeRelay(t *testing.T) {
	h, st, p, mid, extSub, _ := newExternalSetup(t, extsubmit.Outcome{
		State: extsubmit.OutcomeOK,
	})
	now := h.clk.Now()
	sendAt := now.Add(10 * time.Second)
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"sendAt":     sendAt.Format(time.RFC3339),
			},
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

	// Destroy inside the window: must succeed (no notDestroyed entry).
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"destroy":   []string{subID},
	})
	dresp, mErr := setHandler{h: h}.executeAs(p, destroyArgs)
	if mErr != nil {
		t.Fatalf("set destroy: %v", mErr)
	}
	djs, _ := json.Marshal(dresp)
	if strings.Contains(string(djs), `"notDestroyed"`) {
		t.Fatalf("expected the row to be destroyed (undo succeeded), got: %s", djs)
	}
	if !strings.Contains(string(djs), `"destroyed"`) {
		t.Fatalf("expected a destroyed entry: %s", djs)
	}

	if _, err := st.Meta().GetEmailSubmission(context.Background(), subID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected the row to be gone after a successful undo, got err=%v", err)
	}

	// Advance past the scheduled sendAt and run the scheduler: the relay
	// must never see the message.
	dispatched, dErr := h.DispatchDueExternalRelays(context.Background(), sendAt.Add(time.Second))
	if dErr != nil {
		t.Fatalf("DispatchDueExternalRelays: %v", dErr)
	}
	if dispatched != 0 {
		t.Fatalf("expected 0 rows dispatched (the row was canceled), got %d", dispatched)
	}
	h.Wait()
	if len(extSub.calls) != 0 {
		t.Fatalf("expected 0 Submit calls -- the relay must never see a message undone inside the window, got %d", len(extSub.calls))
	}
}

// TestEmailSubmission_External_SchedulerDispatchesAfterWindow verifies that
// the relay scheduler claims and relays a scheduled row once its sendAt has
// elapsed, and that destroy after that point answers cannotUnsend exactly
// as it does for an immediately-dispatched external submission (re #478).
func TestEmailSubmission_External_SchedulerDispatchesAfterWindow(t *testing.T) {
	h, st, p, mid, extSub, _ := newExternalSetup(t, extsubmit.Outcome{
		State:      extsubmit.OutcomeOK,
		Diagnostic: "250 ok",
	})
	now := h.clk.Now()
	sendAt := now.Add(10 * time.Second)
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"sendAt":     sendAt.Format(time.RFC3339),
			},
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

	// The window has not elapsed yet: the scheduler finds nothing due.
	if n, dErr := h.DispatchDueExternalRelays(context.Background(), now.Add(5*time.Second)); dErr != nil || n != 0 {
		t.Fatalf("DispatchDueExternalRelays before sendAt: n=%d err=%v, want n=0", n, dErr)
	}
	h.Wait()
	if len(extSub.calls) != 0 {
		t.Fatalf("expected 0 Submit calls before sendAt, got %d", len(extSub.calls))
	}

	// The window has elapsed: the scheduler claims and relays the row.
	n, dErr := h.DispatchDueExternalRelays(context.Background(), sendAt.Add(time.Second))
	if dErr != nil {
		t.Fatalf("DispatchDueExternalRelays after sendAt: %v", dErr)
	}
	if n != 1 {
		t.Fatalf("expected 1 row dispatched, got %d", n)
	}
	h.Wait()
	if len(extSub.calls) != 1 {
		t.Fatalf("expected 1 Submit call after sendAt elapsed, got %d", len(extSub.calls))
	}

	subRow, err := st.Meta().GetEmailSubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if subRow.RelayHeld {
		t.Fatal("expected RelayHeld=false once the scheduler has claimed the row")
	}
	if subRow.UndoStatus != string(undoStatusFinal) {
		t.Fatalf("undoStatus = %q, want final", subRow.UndoStatus)
	}

	// Destroy after the window closed answers cannotUnsend, same as today's
	// behaviour for a dispatched external submission.
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
		t.Fatalf("expected cannotUnsend after the window closed: %s", djs)
	}
}

// TestEmailSubmission_External_ScheduledSurvivesRestart verifies that a
// scheduled external submission is not lost or double-sent across a server
// restart (re #478): the row is created and left RelayHeld=true by one
// handlerSet backed by a sqlite file, the store is closed and reopened by a
// second, independent handlerSet (simulating a process restart), and the
// second handlerSet's scheduler -- with no knowledge of the first's
// in-memory state -- finds and relays the row exactly once.
func TestEmailSubmission_External_ScheduledSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	stA, err := storesqlite.Open(context.Background(), dbPath, nil, clock.NewFake(start))
	if err != nil {
		t.Fatalf("storesqlite.Open (A): %v", err)
	}
	ctx := context.Background()
	if err := stA.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, _ := stA.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	mb, _ := stA.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})
	body := "From: alice@example.test\r\nTo: bob@remote.test\r\nSubject: hi\r\n\r\nbody.\r\n"
	ref, _ := stA.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	uid, _, _ := stA.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "hi",
			From:    "alice@example.test",
			To:      "bob@remote.test",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	msgs, _ := stA.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}

	extSubA := &fakeExternalSubmitter{outcome: extsubmit.Outcome{State: extsubmit.OutcomeOK, Diagnostic: "250 ok"}}
	hA := &handlerSet{
		store:          stA,
		queue:          &fakeSubmitter{store: stA},
		clk:            clock.NewFake(start),
		identity:       stubResolver{email: "alice@example.test"},
		externalSubmit: extSubA,
		externalRouter: &fakeExternalRouter{has: true},
	}

	sendAt := start.Add(10 * time.Second)
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"sendAt":     sendAt.Format(time.RFC3339),
			},
		},
	})
	resp, mErr := setHandler{h: hA}.executeAs(p, createArgs)
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
	hA.Wait()
	if len(extSubA.calls) != 0 {
		t.Fatalf("expected 0 Submit calls before restart, got %d", len(extSubA.calls))
	}

	// Simulate a restart: close the first store handle and everything that
	// held process A's in-memory state (there is no scheduled goroutine or
	// timer to lose -- that is the point).
	if err := stA.Close(); err != nil {
		t.Fatalf("close store A: %v", err)
	}

	stB, err := storesqlite.Open(context.Background(), dbPath, nil, clock.NewFake(sendAt.Add(time.Second)))
	if err != nil {
		t.Fatalf("storesqlite.Open (B): %v", err)
	}
	t.Cleanup(func() { _ = stB.Close() })
	extSubB := &fakeExternalSubmitter{outcome: extsubmit.Outcome{State: extsubmit.OutcomeOK, Diagnostic: "250 ok"}}
	hB := &handlerSet{
		store:          stB,
		queue:          &fakeSubmitter{store: stB},
		clk:            clock.NewFake(sendAt.Add(time.Second)),
		identity:       stubResolver{email: "alice@example.test"},
		externalSubmit: extSubB,
		externalRouter: &fakeExternalRouter{has: true},
	}
	t.Cleanup(hB.Wait)

	n, dErr := hB.DispatchDueExternalRelays(context.Background(), sendAt.Add(time.Second))
	if dErr != nil {
		t.Fatalf("DispatchDueExternalRelays (B): %v", dErr)
	}
	if n != 1 {
		t.Fatalf("expected the restarted process to find and dispatch the 1 scheduled row, got %d", n)
	}
	hB.Wait()
	if len(extSubB.calls) != 1 {
		t.Fatalf("expected process B to relay the row exactly once, got %d calls", len(extSubB.calls))
	}

	subRow, err := stB.Meta().GetEmailSubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if subRow.UndoStatus != string(undoStatusFinal) {
		t.Fatalf("undoStatus = %q, want final", subRow.UndoStatus)
	}
	if subRow.RelayHeld {
		t.Fatal("expected RelayHeld=false after dispatch")
	}
}
