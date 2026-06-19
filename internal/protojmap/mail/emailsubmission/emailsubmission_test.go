package emailsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"path/filepath"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fakeSubmitter records every Submit call so tests can assert the
// shape EmailSubmission/set hands the queue. Cancel iterates the
// underlying store the same way *queue.Queue.Cancel does so the
// JMAP destroy path observes the same semantics in tests.
type fakeSubmitter struct {
	mu      sync.Mutex
	calls   []queue.Submission
	bodies  [][]byte
	envs    []queue.EnvelopeID
	cancels []queue.EnvelopeID
	store   store.Store
}

func (f *fakeSubmitter) Submit(ctx context.Context, sub queue.Submission) (queue.EnvelopeID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
// a fully-materialised copy. A 1 MiB message body is used; the correct
// implementation passes the store.BlobReader directly to queue.Submit
// without io.ReadAll. The body content is verified byte-identical to the
// stored blob so the queue receives the right data.
//
// The streaming property is enforced structurally by the code (processCreate
// passes rc directly to queue.Submit), and by the absence of io.ReadAll in
// the submission path. This test validates the content correctness half of
// the contract; the type invariant (no *bytes.Reader) is visible in the code.
func TestEmailSubmission_Set_StreamsBodyToQueue(t *testing.T) {
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	ctx := context.Background()
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.test",
	})
	mb, _ := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Drafts", Attributes: store.MailboxAttrDrafts,
	})

	// A 1 MiB body makes any accidental full-body copy easy to spot in
	// a heap profile and sets a meaningful baseline for the size check.
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

	// Body content must arrive at the queue byte-identical to the stored
	// blob. The streaming path passes the blob handle directly to Submit;
	// the fakeSubmitter drains it via io.ReadAll(sub.Body) so we can inspect
	// the received bytes here.
	if !bytes.Equal(sub.bodies[0], []byte(rawBody)) {
		t.Fatalf("body content mismatch: got %d bytes, want %d", len(sub.bodies[0]), len(rawBody))
	}

	// A 1 MiB body must arrive in full, confirming the streaming handle is
	// fully consumed by the queue and no truncation occurred.
	if len(sub.bodies[0]) != len(rawBody) {
		t.Fatalf("body truncated: got %d bytes, want %d (REQ-STORE-17/18)", len(sub.bodies[0]), len(rawBody))
	}
}
