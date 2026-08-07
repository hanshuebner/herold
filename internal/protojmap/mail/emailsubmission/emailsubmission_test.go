package emailsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fakeSubmitter records every Submit call so tests can assert the
// shape EmailSubmission/set hands the queue. Cancel iterates the
// underlying store the same way *queue.Queue.Cancel does so the
// JMAP destroy path observes the same semantics in tests.
type fakeSubmitter struct {
	mu        sync.Mutex
	calls     []queue.Submission
	bodies    [][]byte
	bodyTypes []reflect.Type // concrete type of sub.Body at Submit time (REQ-STORE-17/18)
	envs      []queue.EnvelopeID
	cancels   []queue.EnvelopeID
	store     store.Store
}

func (f *fakeSubmitter) Submit(ctx context.Context, sub queue.Submission) (queue.EnvelopeID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodyTypes = append(f.bodyTypes, reflect.TypeOf(sub.Body))
	body, _ := io.ReadAll(sub.Body)
	f.calls = append(f.calls, sub)
	f.bodies = append(f.bodies, body)
	env := queue.EnvelopeID("env-" + string(rune('a'+len(f.envs))))
	f.envs = append(f.envs, env)
	// Persist queue rows so EmailSubmission/get can read them back.
	for _, rcpt := range sub.Recipients {
		bodyHash := "fakebody"
		ref, err := f.store.Blobs().Put(ctx, bytes.NewReader(body))
		if err == nil {
			bodyHash = ref.Hash
		}
		var pid store.PrincipalID
		if sub.PrincipalID != nil {
			pid = *sub.PrincipalID
		}
		_, err = f.store.Meta().EnqueueMessage(ctx, store.QueueItem{
			PrincipalID:  pid,
			MailFrom:     sub.MailFrom,
			RcptTo:       rcpt,
			EnvelopeID:   env,
			BodyBlobHash: bodyHash,
			State:        store.QueueStateQueued,
		})
		if err != nil {
			return "", err
		}
	}
	return env, nil
}

// Cancel iterates the rows belonging to env and removes any that are
// still in queued/deferred/held state. Inflight rows count toward the
// inflight return; terminal rows are ignored. Mirrors *queue.Queue.Cancel.
func (f *fakeSubmitter) Cancel(ctx context.Context, env queue.EnvelopeID) (cancelled, inflight int, err error) {
	f.mu.Lock()
	f.cancels = append(f.cancels, env)
	f.mu.Unlock()
	rows, err := f.store.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: env})
	if err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		switch r.State {
		case store.QueueStateQueued, store.QueueStateDeferred, store.QueueStateHeld:
			if dErr := f.store.Meta().DeleteQueueItem(ctx, r.ID); dErr == nil {
				cancelled++
			}
		case store.QueueStateInflight:
			inflight++
		}
	}
	return cancelled, inflight, nil
}

// stubResolver returns a fixed (email, ok=true) for any IdentityID.
type stubResolver struct {
	email string
}

func (s stubResolver) IdentityEmail(_ context.Context, _ store.Principal, _ string) (string, bool) {
	if s.email == "" {
		return "", false
	}
	return s.email, true
}

func newSetup(t *testing.T) (*handlerSet, store.Store, store.Principal, store.Mailbox, store.MessageID, *fakeSubmitter) {
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
	body := "From: alice@example.test\r\nTo: bob@example.test\r\nSubject: hi\r\n\r\nbody.\r\n"
	ref, _ := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	uid, _, _ := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "hi",
			From:    "alice@example.test",
			To:      "bob@example.test",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
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
	// Drain background goroutines (currently seed-on-send) before
	// closing the store so the t.TempDir RemoveAll is not racing
	// further writes into the temp directory. Cleanup runs in LIFO,
	// so this single registration both waits and closes in the
	// required order.
	t.Cleanup(func() {
		h.Wait()
		_ = st.Close()
	})
	return h, st, p, mb, mid, sub
}

func TestEmailSubmission_Set_DispatchesIntoQueue(t *testing.T) {
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
		t.Fatalf("expected created: %s", js)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 queue submit, got %d", len(sub.calls))
	}
	got := sub.calls[0]
	if got.MailFrom != "alice@example.test" {
		t.Fatalf("MailFrom: got %q want alice@example.test", got.MailFrom)
	}
	if len(got.Recipients) != 1 || got.Recipients[0] != "bob@example.test" {
		t.Fatalf("Recipients: got %v want [bob@example.test]", got.Recipients)
	}
	if !got.Sign {
		t.Fatalf("Sign should be true")
	}
	if got.SigningDomain != "example.test" {
		t.Fatalf("SigningDomain: got %q want example.test", got.SigningDomain)
	}
}

func TestEmailSubmission_Get_RendersQueueState(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	// Submit one message.
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
			},
		},
	})
	if _, mErr := (setHandler{h: h}).executeAs(p, createArgs); mErr != nil {
		t.Fatalf("set: %v", mErr)
	}
	envID := sub.envs[0]
	// /get should return undoStatus=pending while the queue row is queued.
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, _ := getHandler{h: h}.executeAs(p, getArgs)
	g := resp.(getResponse)
	if len(g.List) != 1 {
		t.Fatalf("expected 1 submission, got %d", len(g.List))
	}
	if g.List[0].UndoStatus != undoStatusPending {
		t.Fatalf("expected pending, got %q", g.List[0].UndoStatus)
	}
	// Mark the row done; /get should now return undoStatus=final.
	rows, _ := st.Meta().ListQueueItems(context.Background(), store.QueueFilter{EnvelopeID: envID})
	if err := st.Meta().CompleteQueueItem(context.Background(), rows[0].ID, true, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	resp, _ = getHandler{h: h}.executeAs(p, getArgs)
	g = resp.(getResponse)
	if g.List[0].UndoStatus != undoStatusFinal {
		t.Fatalf("expected final, got %q", g.List[0].UndoStatus)
	}
}

func TestEmailSubmission_Set_RejectsUnknownIdentity(t *testing.T) {
	h, _, p, _, mid, _ := newSetup(t)
	h.identity = stubResolver{email: ""}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "999",
				"emailId":    renderEmailID(mid),
			},
		},
	})
	resp, _ := setHandler{h: h}.executeAs(p, args)
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"notCreated"`) {
		t.Fatalf("expected notCreated: %s", js)
	}
	if !strings.Contains(string(js), `"identityId"`) {
		t.Fatalf("expected identityId in error: %s", js)
	}
}

func TestEmailSubmission_Set_RejectsUnknownEmail(t *testing.T) {
	h, _, p, _, _, _ := newSetup(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    "9999999",
			},
		},
	})
	resp, _ := setHandler{h: h}.executeAs(p, args)
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"notCreated"`) {
		t.Fatalf("expected notCreated: %s", js)
	}
}

// -- REQ-PROTO-58 / REQ-FLOW-63 sendAt + destroy ---------------------

func TestSet_Create_HonoursSendAt(t *testing.T) {
	h, _, p, _, mid, sub := newSetup(t)
	sendAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"sendAt":     sendAt,
			},
		},
	})
	if _, mErr := (setHandler{h: h}).executeAs(p, args); mErr != nil {
		t.Fatalf("set: %v", mErr)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 submit, got %d", len(sub.calls))
	}
	got := sub.calls[0].SendAt
	want, _ := time.Parse(time.RFC3339, sendAt)
	if !got.Equal(want) {
		t.Fatalf("Submission.SendAt: got %v want %v", got, want)
	}
}

func TestSet_Create_RejectsMalformedSendAt(t *testing.T) {
	h, _, p, _, mid, _ := newSetup(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"sendAt":     "not-a-date",
			},
		},
	})
	resp, _ := setHandler{h: h}.executeAs(p, args)
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"notCreated"`) {
		t.Fatalf("expected notCreated: %s", js)
	}
	if !strings.Contains(string(js), `"sendAt"`) {
		t.Fatalf("expected sendAt in error: %s", js)
	}
}

func TestSet_Destroy_BeforeSendAt_CancelsAtomically(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	ctx := context.Background()
	sendAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"sendAt":     sendAt,
			},
		},
	})
	if _, mErr := (setHandler{h: h}).executeAs(p, createArgs); mErr != nil {
		t.Fatalf("create: %v", mErr)
	}
	envID := sub.envs[0]
	rows, _ := st.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: envID})
	if len(rows) != 1 {
		t.Fatalf("expected 1 queue row pre-destroy, got %d", len(rows))
	}
	subID := renderSubmissionID(envID)
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"destroy":   []string{string(subID)},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, destroyArgs)
	if mErr != nil {
		t.Fatalf("destroy: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"destroyed"`) {
		t.Fatalf("expected destroyed entry: %s", js)
	}
	if strings.Contains(string(js), `"notDestroyed"`) &&
		strings.Contains(string(js), string(subID)) {
		t.Fatalf("did not expect notDestroyed: %s", js)
	}
	if len(sub.cancels) != 1 || sub.cancels[0] != envID {
		t.Fatalf("Cancel calls: got %v", sub.cancels)
	}
	rows, _ = st.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: envID})
	if len(rows) != 0 {
		t.Fatalf("expected 0 queue rows post-destroy, got %d", len(rows))
	}
	if _, err := st.Meta().GetEmailSubmission(ctx, string(subID)); err == nil {
		t.Fatalf("expected EmailSubmissionRow gone after destroy")
	}
}

func TestSet_Destroy_AlreadyInflight_ReturnsNotDestroyedWithReason(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	ctx := context.Background()
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
			},
		},
	})
	if _, mErr := (setHandler{h: h}).executeAs(p, createArgs); mErr != nil {
		t.Fatalf("create: %v", mErr)
	}
	envID := sub.envs[0]
	// Force the queue row into the inflight state to simulate a
	// hand-off to remote SMTP that beat the destroy.
	rows, _ := st.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: envID})
	if len(rows) != 1 {
		t.Fatalf("expected 1 queue row, got %d", len(rows))
	}
	if _, err := st.Meta().ClaimDueQueueItems(ctx, time.Now(), 10); err != nil {
		t.Fatalf("claim: %v", err)
	}
	subID := renderSubmissionID(envID)
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"destroy":   []string{string(subID)},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, destroyArgs)
	if mErr != nil {
		t.Fatalf("destroy: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"notDestroyed"`) {
		t.Fatalf("expected notDestroyed entry: %s", js)
	}
	if !strings.Contains(string(js), `"alreadyInflight"`) {
		t.Fatalf("expected alreadyInflight type: %s", js)
	}
	if !strings.Contains(string(js), "deliveredCount=") {
		t.Fatalf("expected deliveredCount property: %s", js)
	}
	// Submission row must remain (destroy refused).
	if _, err := st.Meta().GetEmailSubmission(ctx, string(subID)); err != nil {
		t.Fatalf("EmailSubmissionRow vanished after refused destroy: %v", err)
	}
}

// TestEmailSubmission_Set_ForbiddenFrom confirms that EmailSubmission/set
// returns a "forbiddenFrom" SetError when the from address resolved by the
// identity is not owned by the submitting principal (REQ-SEND-12 /
// REQ-FLOW-41).
func TestEmailSubmission_Set_ForbiddenFrom(t *testing.T) {
	h, _, p, _, mid, _ := newSetup(t)
	// Swap the identity resolver to return someone else's address.
	h.identity = stubResolver{email: "eve@other.test"}

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
		t.Fatalf("unexpected method error: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"notCreated"`) {
		t.Fatalf("expected notCreated: %s", js)
	}
	if !strings.Contains(string(js), `"forbiddenFrom"`) {
		t.Fatalf("expected forbiddenFrom type: %s", js)
	}
}

// TestEmailSubmission_Set_AllowedFrom_CanonicalAddress confirms that the
// principal's canonical address is accepted without needing an alias entry.
func TestEmailSubmission_Set_AllowedFrom_CanonicalAddress(t *testing.T) {
	h, _, p, _, mid, sub := newSetup(t)
	// Default resolver returns alice@example.test which is p's canonical.
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
			},
		},
	})
	_, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("unexpected error for canonical address: %v", mErr)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 submit, got %d", len(sub.calls))
	}
}

// -- dedupRecipients unit tests ------------------------------------------

// TestDedupRecipients_SameCase verifies that an exact duplicate is dropped.
func TestDedupRecipients_SameCase(t *testing.T) {
	got := dedupRecipients([]string{"a@example.com", "a@example.com"})
	if len(got) != 1 || got[0] != "a@example.com" {
		t.Fatalf("got %v, want [a@example.com]", got)
	}
}

// TestDedupRecipients_DifferentCase verifies case-insensitive dedup;
// the first occurrence's casing is preserved.
func TestDedupRecipients_DifferentCase(t *testing.T) {
	got := dedupRecipients([]string{"A@Example.Com", "a@example.com"})
	if len(got) != 1 || got[0] != "A@Example.Com" {
		t.Fatalf("got %v, want [A@Example.Com]", got)
	}
}

// TestDedupRecipients_Whitespace verifies that surrounding whitespace is
// trimmed for the purpose of comparison (the original string is preserved).
func TestDedupRecipients_Whitespace(t *testing.T) {
	got := dedupRecipients([]string{"a@example.com", "  a@example.com  "})
	if len(got) != 1 || got[0] != "a@example.com" {
		t.Fatalf("got %v, want [a@example.com]", got)
	}
}

// TestDedupRecipients_EmptyDropped verifies that empty entries are dropped.
func TestDedupRecipients_EmptyDropped(t *testing.T) {
	got := dedupRecipients([]string{"a@example.com", "", "  ", "b@example.com"})
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 entries", got)
	}
}

// TestDedupRecipients_OrderPreserved verifies first-occurrence order.
func TestDedupRecipients_OrderPreserved(t *testing.T) {
	got := dedupRecipients([]string{"a@example.com", "b@example.com", "a@example.com", "c@example.com"})
	want := []string{"a@example.com", "b@example.com", "c@example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// -- integration: duplicate envelope.rcptTo → exactly one queue row -------

// TestEmailSubmission_Set_DedupsEnvelopeRcptTo_SameCase submits with the
// same recipient address twice in envelope.rcptTo and asserts that exactly
// one queue row (and one Recipients entry) is created.
func TestEmailSubmission_Set_DedupsEnvelopeRcptTo_SameCase(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	ctx := context.Background()
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": "alice@example.test"},
					"rcptTo": []map[string]any{
						{"email": "bob@example.test"},
						{"email": "bob@example.test"},
					},
				},
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
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 Submit call, got %d", len(sub.calls))
	}
	if len(sub.calls[0].Recipients) != 1 {
		t.Fatalf("expected 1 recipient, got %d: %v", len(sub.calls[0].Recipients), sub.calls[0].Recipients)
	}
	// Exactly one queue row should exist.
	rows, err := st.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: sub.envs[0]})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 queue row, got %d", len(rows))
	}
}

// TestEmailSubmission_Set_DedupsEnvelopeRcptTo_DifferentCase verifies that
// case-insensitive dedup works via the full handler path.
func TestEmailSubmission_Set_DedupsEnvelopeRcptTo_DifferentCase(t *testing.T) {
	h, _, p, _, mid, sub := newSetup(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": "alice@example.test"},
					"rcptTo": []map[string]any{
						{"email": "Bob@Example.Test"},
						{"email": "bob@example.test"},
					},
				},
			},
		},
	})
	_, mErr := (setHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 Submit call, got %d", len(sub.calls))
	}
	if len(sub.calls[0].Recipients) != 1 {
		t.Fatalf("expected 1 recipient after case-insensitive dedup, got %d: %v",
			len(sub.calls[0].Recipients), sub.calls[0].Recipients)
	}
	// First-occurrence casing is preserved.
	if sub.calls[0].Recipients[0] != "Bob@Example.Test" {
		t.Fatalf("expected first-occurrence casing Bob@Example.Test, got %q", sub.calls[0].Recipients[0])
	}
}

// TestEmailSubmission_Set_DedupsEnvelopeRcptTo_WithWhitespace verifies that
// the surrounding-whitespace normalization applies through the handler.
func TestEmailSubmission_Set_DedupsEnvelopeRcptTo_WithWhitespace(t *testing.T) {
	h, _, p, _, mid, sub := newSetup(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(mid),
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": "alice@example.test"},
					"rcptTo": []map[string]any{
						{"email": "bob@example.test"},
						{"email": " bob@example.test "},
					},
				},
			},
		},
	})
	_, mErr2 := (setHandler{h: h}).executeAs(p, args)
	if mErr2 != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr2)
	}
	if len(sub.calls[0].Recipients) != 1 {
		t.Fatalf("expected 1 recipient, got %d: %v",
			len(sub.calls[0].Recipients), sub.calls[0].Recipients)
	}
}

// TestEmailSubmission_Set_StreamsBodyToQueue verifies REQ-STORE-17/18
// (Phase 1): EmailSubmission/set must hand the queue a streaming
// io.Reader backed by the blob store rather than a *bytes.Reader wrapping
// a fully-materialised copy. A 1 MiB message body is used so that any
// accidental full-body allocation is observable via the Body type captured
// in fakeSubmitter.bodyTypes.
//
// The correct streaming path passes a store.BlobReader (concrete type
// *os.File from storeblobfs) directly, which is not *bytes.Reader.
// The body content is also verified byte-identical to the stored blob
// so the queue receives the right data.
func TestEmailSubmission_Set_StreamsBodyToQueue(t *testing.T) {
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

	// A 1 MiB body makes any accidental full-body copy easy to spot.
	const bodySize = 1 << 20
	rawBody := "From: alice@example.test\r\nTo: bob@example.test\r\nSubject: large\r\n\r\n" +
		strings.Repeat("x", bodySize)
	ref, _ := st.Blobs().Put(ctx, bytes.NewReader([]byte(rawBody)))
	uid, _, _ := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(rawBody)),
		Envelope: store.Envelope{
			Subject: "large",
			From:    "alice@example.test",
			To:      "bob@example.test",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
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
	_, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("expected 1 queue submit, got %d", len(sub.calls))
	}

	// REQ-STORE-17/18: the Body must not be *bytes.Reader — that would
	// indicate the full message was buffered in RAM before submission.
	// storeblobfs.Get returns *os.File (implements store.BlobReader) which
	// is never *bytes.Reader.
	if sub.bodyTypes[0] == reflect.TypeOf((*bytes.Reader)(nil)) {
		t.Fatalf("Body was *bytes.Reader — full message was materialised before submission (REQ-STORE-17/18)")
	}

	// The body content must be byte-identical to the stored blob.
	if !bytes.Equal(sub.bodies[0], []byte(rawBody)) {
		t.Fatalf("body content mismatch: got %d bytes, want %d", len(sub.bodies[0]), len(rawBody))
	}
}

// -- thread-id correctness tests (REQ-PROTO-40) ---------------------------

// openPostgresStore opens a Postgres store for testing. It skips the test
// (a silent no-op) if HEROLD_PG_DSN is not set. If HEROLD_PG_DSN IS set but
// the connection cannot be established (e.g. the checkout's max migration
// is behind the shared database's schema version, which storepg.Open
// refuses as a downgrade), it fails the test rather than skipping it: a
// skip there is indistinguishable from "no server configured" in a
// non-verbose `go test` run and would report the package as a passing
// `ok` without the Postgres leg ever having executed.
func openPostgresStore(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("HEROLD_PG_DSN is set but storepg.Open failed (Postgres leg NOT exercised): %v", err)
	}
	// HEROLD_PG_DSN is a single shared throwaway database; reset row state
	// before each test so the fixed example.test domain / alice@example.test
	// principal that newSetupFromStore inserts do not collide with rows a
	// prior test in this run — or a prior failed run — left behind. Mirrors
	// the pattern in internal/admin/loopback_queue_integration_test.go.
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			_ = st.Close()
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newSetupFromStore is like newSetup but accepts a pre-opened store. This
// allows the same test logic to run against both SQLite and Postgres.
func newSetupFromStore(t *testing.T, st store.Store) (*handlerSet, store.Principal, store.Mailbox, *fakeSubmitter) {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain example.test: %v", err)
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
		t.Fatalf("InsertMailbox Drafts: %v", err)
	}
	sub := &fakeSubmitter{store: st}
	h := &handlerSet{
		store:    st,
		queue:    sub,
		clk:      clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		identity: stubResolver{email: "alice@example.test"},
	}
	t.Cleanup(func() { h.Wait() })
	return h, p, mb, sub
}

// testEmailSubmissionThreadID is the backend-agnostic body of
// TestEmailSubmission_Set_ReplyJoinsParentThread_*.
//
// It inserts a parent message with a known Message-ID, creates a reply draft
// with an In-Reply-To pointing at the parent, submits the reply via
// EmailSubmission/set, and asserts that:
//
//  1. The store assigns the reply's thread_id correctly at insert time
//     (insertMessageTx resolution).
//  2. The EmailSubmission.threadId in the response is a valid JMAP Thread ID
//     ("t" + decimal) and matches the parent message's JMAP threadId.
//  3. After moving the draft to a Sent mailbox via MoveMessage, the Sent copy
//     retains the correct thread_id.
func testEmailSubmissionThreadID(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()

	h, p, draftsMB, _ := newSetupFromStore(t, st)

	// Create Inbox and Sent mailboxes in addition to the Drafts mailbox that
	// newSetupFromStore already created.
	inboxMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox INBOX: %v", err)
	}
	sentMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Sent", Attributes: store.MailboxAttrSent,
	})
	if err != nil {
		t.Fatalf("InsertMailbox Sent: %v", err)
	}

	// Insert the parent message (Bob's email to Alice) with a known Message-ID.
	// The Message-ID is stored with angle brackets in Envelope.MessageID;
	// insertMessageTx normalises it to the bare form before storing in
	// env_message_id.
	parentMsgID := "<parent-msg@example.test>"
	parentBody := "From: bob@example.test\r\nTo: alice@example.test\r\n" +
		"Subject: Hello\r\nMessage-ID: " + parentMsgID + "\r\n\r\nhi\r\n"
	parentRef, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(parentBody)))
	if err != nil {
		t.Fatalf("Blobs.Put parent: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		Blob:        parentRef,
		Size:        parentRef.Size,
		Envelope: store.Envelope{
			Subject:   "Hello",
			From:      "bob@example.test",
			To:        "alice@example.test",
			MessageID: parentMsgID,
		},
	}, []store.MessageMailbox{{MailboxID: inboxMB.ID}}); err != nil {
		t.Fatalf("InsertMessage parent: %v", err)
	}
	// Discover the parent's message ID from the change feed.
	feed, err := st.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var parentID store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			parentID = store.MessageID(e.EntityID)
		}
	}
	if parentID == 0 {
		t.Fatalf("parent message ID not found in change feed")
	}
	// The parent is a thread root: thread_id = 0 in the store, so its JMAP
	// threadId = "t" + decimal(parentID).
	wantThreadID := fmt.Sprintf("t%d", parentID)

	// Insert a reply draft in Drafts with In-Reply-To pointing at the parent.
	// Envelope.InReplyTo uses angle brackets; insertMessageTx calls
	// ParseReferences which strips them before the lookup.
	replyBody := "From: alice@example.test\r\nTo: bob@example.test\r\n" +
		"Subject: Re: Hello\r\nIn-Reply-To: " + parentMsgID +
		"\r\nReferences: " + parentMsgID + "\r\n\r\nreply\r\n"
	replyRef, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(replyBody)))
	if err != nil {
		t.Fatalf("Blobs.Put reply: %v", err)
	}
	replyUID, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		Blob:        replyRef,
		Size:        replyRef.Size,
		Envelope: store.Envelope{
			Subject:   "Re: Hello",
			From:      "alice@example.test",
			To:        "bob@example.test",
			InReplyTo: parentMsgID,
		},
	}, []store.MessageMailbox{{MailboxID: draftsMB.ID}})
	if err != nil {
		t.Fatalf("InsertMessage reply draft: %v", err)
	}

	// Discover the reply draft's message ID.
	msgs, err := st.Meta().ListMessages(ctx, draftsMB.ID, store.MessageFilter{Limit: 10, WithEnvelope: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var replyMsgID store.MessageID
	for _, m := range msgs {
		if m.UID == replyUID {
			replyMsgID = m.ID
		}
	}
	if replyMsgID == 0 {
		t.Fatalf("reply draft message ID not found")
	}

	// Verify that insertMessageTx assigned the correct thread_id at insert time
	// (this is the store-level assertion; the JMAP response assertion follows).
	replyMsg, err := st.Meta().GetMessage(ctx, replyMsgID)
	if err != nil {
		t.Fatalf("GetMessage reply: %v", err)
	}
	if replyMsg.ThreadID == 0 {
		t.Errorf("reply draft ThreadID = 0; expected insertMessageTx to resolve it to %d (parent)", parentID)
	} else if replyMsg.ThreadID != uint64(parentID) {
		t.Errorf("reply draft ThreadID = %d, want %d (parent ID)", replyMsg.ThreadID, parentID)
	}

	// Submit the reply via EmailSubmission/set (no onSuccessUpdateEmail here;
	// the move-to-Sent assertion is done separately below via MoveMessage).
	args, marshalErr := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": "default",
				"emailId":    renderEmailID(replyMsgID),
			},
		},
	})
	if marshalErr != nil {
		t.Fatalf("marshal args: %v", marshalErr)
	}
	respAny, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	js, _ := json.Marshal(respAny)

	// Verify EmailSubmission.threadId is a proper JMAP Thread ID ("t<N>") and
	// matches the parent's expected threadId (REQ-PROTO-40 Fix 2).
	var setResp struct {
		Created map[string]struct {
			ThreadID string `json:"threadId"`
		} `json:"created"`
	}
	if err := json.Unmarshal(js, &setResp); err != nil {
		t.Fatalf("unmarshal response: %v: %s", err, js)
	}
	sub, ok := setResp.Created["k1"]
	if !ok {
		t.Fatalf("k1 not in created: %s", js)
	}
	if !strings.HasPrefix(sub.ThreadID, "t") {
		t.Errorf("EmailSubmission.threadId = %q has no 't' prefix; want JMAP Thread ID format", sub.ThreadID)
	}
	if sub.ThreadID != wantThreadID {
		t.Errorf("EmailSubmission.threadId = %q, want %q (parent thread)", sub.ThreadID, wantThreadID)
	}

	// Simulate the onSuccessUpdateEmail Sent move: verify that MoveMessage
	// preserves the correct thread_id on the Sent copy.
	if err := st.Meta().MoveMessage(ctx, replyMsgID, draftsMB.ID, sentMB.ID); err != nil {
		t.Fatalf("MoveMessage Drafts→Sent: %v", err)
	}
	sentMsg, err := st.Meta().GetMessage(ctx, replyMsgID)
	if err != nil {
		t.Fatalf("GetMessage sent copy: %v", err)
	}
	if sentMsg.ThreadID != uint64(parentID) {
		t.Errorf("Sent copy ThreadID = %d, want %d (parent)", sentMsg.ThreadID, parentID)
	}
}

// TestEmailSubmission_Set_ReplyJoinsParentThread_SQLite runs the thread-ID
// round-trip test against the SQLite backend.
func TestEmailSubmission_Set_ReplyJoinsParentThread_SQLite(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testEmailSubmissionThreadID(t, st)
}

// TestEmailSubmission_Set_ReplyJoinsParentThread_Postgres runs the same test
// against the Postgres backend. Skips when HEROLD_PG_DSN is not set.
func TestEmailSubmission_Set_ReplyJoinsParentThread_Postgres(t *testing.T) {
	st := openPostgresStore(t)
	testEmailSubmissionThreadID(t, st)
}
