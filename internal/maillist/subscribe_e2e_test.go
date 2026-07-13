package maillist_test

// subscribe_e2e_test.go drives the whole Stage 3 double opt-in flow
// through the REAL outbound queue delivery worker (issue #185,
// REQ-MLIST-60..63), mirroring delivery_e2e_test.go's rigor: the
// confirm token is extracted from the bytes the queue actually placed
// on the wire, not read off the store, so this proves the confirmation
// email genuinely carries a working token end to end. It then confirms
// the newly active member receives a subsequent list post through the
// same real queue + Expander path.

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// TestSubscribe_EndToEnd_ThroughQueue is the acceptance-level test for
// issue #185: POST subscribe -> the real queue delivers the
// confirmation mail -> the token is extracted from the DELIVERED
// message -> GET/POST confirm with that token activates the member ->
// a subsequent list post reaches the now-active member, all through
// the real outbound queue's delivery worker (no test double stands in
// for EnqueueMessage, the queue's scheduler, or DKIM signing).
func TestSubscribe_EndToEnd_ThroughQueue(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()

	ml := mustInsertList(t, st, "list@example.test", false)
	ml.SubscribePolicy = store.MailingListSubscribeOpen
	if err := st.Meta().UpdateMailingList(ctx, ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}

	// A REAL clock, not FakeClock: sendConfirmToken/notifyOwnerApprovalPending
	// enqueue via store.Metadata.EnqueueMessage directly (the same
	// pattern notify.go's owner-suspend-notification already uses),
	// which does not ping the queue's wake channel the way Queue.Submit
	// does. In production that is fine (queue.Run's PollInterval ticker
	// is driven by a real clock that elapses on its own); a FakeClock
	// that never advances would leave the scheduler's select blocked
	// forever waiting on a timer that never fires. Real time keeps this
	// end-to-end test honest about the actual delivery path without
	// depending on Queue.Submit/wake internals this flow does not use.
	clk := clock.NewReal()
	km := keymgmt.NewManager(st.Meta(), discardLogger(), clk, rand.Reader)
	if _, err := km.GenerateKey(ctx, ml.Domain, store.DKIMAlgorithmEd25519SHA256); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dkimSigner := maildkim.NewSigner(km, discardLogger(), clk)
	deliverer := newCapturingDeliverer()

	q := queue.New(queue.Options{
		Store:        st,
		Deliverer:    deliverer,
		Signer:       dkimSigner,
		Logger:       discardLogger(),
		Clock:        clk,
		PollInterval: 20 * time.Millisecond,
		Hostname:     "mail.example.test",
	})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = q.Run(runCtx)
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	ts := maillist.NewTokenSigner(testDataKey())
	srv := maillist.NewPublicServer(st.Meta(), ts, st.Blobs(), testPublicBaseURL, clk, discardLogger())

	// Step 1: subscribe.
	subRec := doRequest(t, srv.Handler(), http.MethodPost, "/lists/"+itoaListID(ml.ID)+"/subscribe",
		"address=newsub%40example.net", "application/x-www-form-urlencoded")
	if subRec.Code != http.StatusAccepted {
		t.Fatalf("subscribe status = %d, want 202; body=%s", subRec.Code, subRec.Body.String())
	}

	// Step 2: the real queue delivers the confirmation email.
	if !waitFor(t, 5*time.Second, func() bool { return deliverer.count() >= 1 }) {
		t.Fatalf("confirmation email never delivered")
	}
	delivered, ok := deliverer.messages()["newsub@example.net"]
	if !ok {
		t.Fatalf("no delivered message addressed to newsub@example.net")
	}
	confirmURL := extractConfirmURL(t, string(delivered))
	target := strings.TrimPrefix(confirmURL, testPublicBaseURL)

	// Step 3: GET must not mutate (prefetch safety).
	getRec := doRequest(t, srv.Handler(), http.MethodGet, target, "", "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET confirm status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	pending, err := st.Meta().GetMailingListMemberByAddress(ctx, ml.ID, "newsub@example.net")
	if err != nil {
		t.Fatalf("GetMailingListMemberByAddress: %v", err)
	}
	if pending.State != store.MailingListMemberPending {
		t.Fatalf("GET confirm mutated state to %q", pending.State)
	}

	// Step 4: POST the token extracted from the DELIVERED message
	// (not re-minted, not read from the store) activates the member.
	postRec := doRequest(t, srv.Handler(), http.MethodPost, target, "", "")
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST confirm status = %d, want 200; body=%s", postRec.Code, postRec.Body.String())
	}
	active, err := st.Meta().GetMailingListMemberByAddress(ctx, ml.ID, "newsub@example.net")
	if err != nil {
		t.Fatalf("GetMailingListMemberByAddress: %v", err)
	}
	if active.State != store.MailingListMemberActive {
		t.Fatalf("member State after confirm = %q, want active", active.State)
	}

	// Step 5: a subsequent list post reaches the now-active member,
	// through the same real queue + Expander fan-out path (DKIM signed).
	sealer := mailarc.NewSealer(km, nil, discardLogger())
	exp := maillist.NewExpander(st.Meta(), q, sealer, clk, discardLogger())
	exp.TokenSigner = ts
	exp.PublicBaseURL = testPublicBaseURL
	res, err := exp.Expand(ctx, maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
		Auth:   mailauth.AuthResults{ARC: mailauth.ARCResult{Status: mailauth.AuthNone}},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.MemberCount != 1 {
		t.Fatalf("Expand result = %+v, want exactly one delivered copy (the newly active member)", res)
	}

	if !waitFor(t, 5*time.Second, func() bool {
		b, ok := deliverer.messages()["newsub@example.net"]
		return ok && strings.Contains(string(b), "quarterly update")
	}) {
		t.Fatalf("the now-active member never received the subsequent list post")
	}
}
