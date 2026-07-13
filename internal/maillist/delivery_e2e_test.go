package maillist_test

// delivery_e2e_test.go drives a mailing-list fan-out all the way through
// the real outbound queue's delivery worker (issue #184, REQ-MLIST-11):
// ARC sealing at expand time, the queue's own DKIM signing at delivery
// time, and the REQ-MLIST-56 per-member List-Unsubscribe header overlay
// spliced onto the wire on top of both. It checks the bytes actually
// placed on the wire, not the persisted queue row or blob, since that is
// the only place a signature-invalidation regression could show up.

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

// deliveryFakeResolver bridges fakedns.Resolver to mailauth.Resolver for
// the DKIM/ARC verifiers below. Only TXTLookup is exercised (DKIM/ARC key
// publication); MX/IP lookups are never called by those verifiers.
type deliveryFakeResolver struct{ inner *fakedns.Resolver }

func (f deliveryFakeResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	v, err := f.inner.LookupTXT(ctx, name)
	if err != nil {
		return nil, mailauth.ErrNoRecords
	}
	return v, nil
}

func (f deliveryFakeResolver) MXLookup(ctx context.Context, _ string) ([]*net.MX, error) {
	return nil, mailauth.ErrNoRecords
}

func (f deliveryFakeResolver) IPLookup(ctx context.Context, _ string) ([]net.IP, error) {
	return nil, mailauth.ErrNoRecords
}

// capturingDeliverer is a queue.Deliverer that records the fully-rendered
// wire bytes -- post header-overlay, post DKIM signing -- for each
// delivery attempt, keyed by recipient, and always reports success.
type capturingDeliverer struct {
	mu     sync.Mutex
	byRcpt map[string][]byte
}

func newCapturingDeliverer() *capturingDeliverer {
	return &capturingDeliverer{byRcpt: make(map[string][]byte)}
}

func (d *capturingDeliverer) Deliver(_ context.Context, req queue.DeliveryRequest) (queue.DeliveryOutcome, error) {
	b, err := io.ReadAll(req.Message)
	if err != nil {
		return queue.DeliveryOutcome{Status: queue.DeliveryStatusTransient}, err
	}
	d.mu.Lock()
	d.byRcpt[req.Recipient] = b
	d.mu.Unlock()
	return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
}

func (d *capturingDeliverer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.byRcpt)
}

func (d *capturingDeliverer) messages() map[string][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string][]byte, len(d.byRcpt))
	for k, v := range d.byRcpt {
		out[k] = v
	}
	return out
}

// waitFor polls pred (real wall-clock, independent of any fake clock the
// system under test uses) until it reports true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}

// TestExpand_DeliveredMessage_ARCAndDKIMVerifyWithOverlay is the
// REQ-MLIST-11/21/56 full-pipeline e2e for issue #184: a two-member,
// ARC-sealed, unsubscribe-enabled fan-out is driven all the way through
// the real queue's delivery worker (DKIM signing included). The bytes
// actually placed on the wire are checked three ways:
//
//   - the ARC seal (sealed once, at expand time, over the shared body)
//     still verifies with the overlay present;
//   - the queue's own outbound DKIM signature (computed at delivery
//     time, over the ARC-sealed body, BEFORE the overlay is spliced on)
//     still verifies -- an unsigned header prepended after signing does
//     not invalidate it;
//   - each member's delivered copy carries its OWN List-Unsubscribe
//     token, never a sibling's, and the one-click endpoint accepts a
//     token read back from the DELIVERED bytes end to end.
func TestExpand_DeliveredMessage_ARCAndDKIMVerifyWithOverlay(t *testing.T) {
	st := openSQLiteStore(t)
	ctx := context.Background()

	ml := mustInsertList(t, st, "list@example.test", true) // ARCSeal=true
	ml.UnsubscribeEnabled = true
	if err := st.Meta().UpdateMailingList(ctx, ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	m1 := mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)
	m2 := mustAddExternalMember(t, st, ml.ID, "b@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	km := keymgmt.NewManager(st.Meta(), discardLogger(), clk, rand.Reader)
	if _, err := km.GenerateKey(ctx, ml.Domain, store.DKIMAlgorithmEd25519SHA256); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	activeKey, err := st.Meta().GetActiveDKIMKey(ctx, ml.Domain)
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	dns := fakedns.New()
	dns.AddTXT(activeKey.Selector+"._domainkey."+ml.Domain, "v=DKIM1; k=ed25519; p="+activeKey.PublicKeyB64)
	resolver := deliveryFakeResolver{inner: dns}

	sealer := mailarc.NewSealer(km, nil, discardLogger())
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
	exp := maillist.NewExpander(st.Meta(), q, sealer, clk, discardLogger())
	exp.TokenSigner = ts
	exp.PublicBaseURL = "https://mail.example.test"
	res, err := exp.Expand(ctx, maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
		Auth:   mailauth.AuthResults{ARC: mailauth.ARCResult{Status: mailauth.AuthNone}},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.MemberCount != 2 {
		t.Fatalf("result = %+v, want two delivered copies", res)
	}
	if res.Unsealed {
		t.Fatalf("result.Unsealed = true; want a clean ARC seal")
	}

	if !waitFor(t, 5*time.Second, func() bool { return deliverer.count() >= 2 }) {
		t.Fatalf("delivery never completed: got %d of 2", deliverer.count())
	}

	msgs := deliverer.messages()
	if len(msgs) != 2 {
		t.Fatalf("delivered messages = %d, want 2", len(msgs))
	}

	arcVerifier := mailarc.New(resolver)
	dkimVerifier := maildkim.New(resolver, discardLogger(), clk)

	byRecipientMember := map[string]store.MailingListMemberID{
		"a@example.net": m1.ID,
		"b@example.net": m2.ID,
	}
	tokensByMember := map[store.MailingListMemberID]string{}
	var deliveredTokenForM1 string
	for rcpt, raw := range msgs {
		body := string(raw)

		arcRes, aerr := arcVerifier.Verify(ctx, bytes.NewReader(raw))
		if aerr != nil {
			t.Fatalf("ARC verify(%s): %v", rcpt, aerr)
		}
		if arcRes.Status != mailauth.AuthPass {
			t.Fatalf("ARC verify(%s) = %v (%s), want pass -- overlay must not invalidate the seal:\n%s",
				rcpt, arcRes.Status, arcRes.Reason, body)
		}

		dkimResults, derr := dkimVerifier.Verify(ctx, bytes.NewReader(raw))
		if derr != nil {
			t.Fatalf("DKIM verify(%s): %v", rcpt, derr)
		}
		if len(dkimResults) != 1 || dkimResults[0].Status != mailauth.AuthPass {
			t.Fatalf("DKIM verify(%s) = %+v, want exactly one passing signature -- an unsigned overlay header must not invalidate the outbound DKIM signature:\n%s",
				rcpt, dkimResults, body)
		}

		if !strings.Contains(body, "List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n") {
			t.Fatalf("delivered copy to %s missing List-Unsubscribe-Post:\n%s", rcpt, body)
		}
		wantMember, ok := byRecipientMember[rcpt]
		if !ok {
			t.Fatalf("unexpected recipient %q", rcpt)
		}
		token := extractUnsubscribeToken(t, body)
		gotMember, verr := ts.Verify(maillist.TokenPurposeUnsubscribe, ml.ID, token, clk.Now())
		if verr != nil {
			t.Fatalf("Verify unsubscribe token for %s: %v", rcpt, verr)
		}
		if gotMember != wantMember {
			t.Fatalf("token delivered to %s verified to member %d, want %d (cross-member isolation broken)",
				rcpt, gotMember, wantMember)
		}
		tokensByMember[gotMember] = token
		if rcpt == "a@example.net" {
			deliveredTokenForM1 = token
		}
	}
	if len(tokensByMember) != 2 || tokensByMember[m1.ID] == tokensByMember[m2.ID] {
		t.Fatalf("expected two distinct per-member delivered tokens, got %v", tokensByMember)
	}

	// End-to-end: the one-click endpoint accepts a token read back from
	// the DELIVERED wire bytes -- not minted directly, not read off the
	// queue row -- and unsubscribes exactly the addressed member.
	srv := maillist.NewPublicServer(st.Meta(), ts, st.Blobs(), "https://mail.example.test", clk, discardLogger())
	target := fmt.Sprintf("/lists/%d/unsubscribe?token=%s", uint64(ml.ID), deliveredTokenForM1)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("List-Unsubscribe=One-Click"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("one-click POST with delivered token status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got1, err := st.Meta().GetMailingListMember(ctx, m1.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember(m1): %v", err)
	}
	if got1.State != store.MailingListMemberUnsubscribed {
		t.Fatalf("m1 State = %q, want unsubscribed", got1.State)
	}
	got2, err := st.Meta().GetMailingListMember(ctx, m2.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember(m2): %v", err)
	}
	if got2.State != store.MailingListMemberActive {
		t.Fatalf("m2 State = %q, want unaffected (still active) -- one-click must only touch the addressed member", got2.State)
	}
}
