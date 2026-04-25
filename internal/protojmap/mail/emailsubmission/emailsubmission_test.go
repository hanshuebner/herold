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

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fakeSubmitter records every Submit call so tests can assert the
// shape EmailSubmission/set hands the queue.
type fakeSubmitter struct {
	mu     sync.Mutex
	calls  []queue.Submission
	bodies [][]byte
	envs   []queue.EnvelopeID
	store  store.Store
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

func newSetup(t *testing.T) (*handlerSet, *fakestore.Store, store.Principal, store.Mailbox, store.MessageID, *fakeSubmitter) {
	t.Helper()
	st, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
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
		MailboxID: mb.ID,
		Blob:      ref,
		Size:      int64(len(body)),
		Envelope: store.Envelope{
			Subject: "hi",
			From:    "alice@example.test",
			To:      "bob@example.test",
		},
	})
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
		meta:     newMetaTable(),
	}
	return h, st, p, mb, mid, sub
}

func TestEmailSubmission_Set_DispatchesIntoQueue(t *testing.T) {
	h, _, p, _, mid, sub := newSetup(t)
	args, _ := json.Marshal(map[string]any{
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
	getArgs, _ := json.Marshal(map[string]any{})
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
