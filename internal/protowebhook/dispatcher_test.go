package protowebhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protowebhook"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// dispatcherHarness wires a fakestore + dispatcher + httptest receiver
// for the test cases below. Construct with newDispatcherHarness; tests
// drive it deterministically via the FakeClock and a per-test signal
// channel that fires on every received POST.
type dispatcherHarness struct {
	t          *testing.T
	ctx        context.Context
	cancel     context.CancelFunc
	clk        *clock.FakeClock
	store      store.Store
	dispatcher *protowebhook.Dispatcher
	receiver   *httptest.Server
	signingKey []byte
	fetchSrv   *httptest.Server
	fetchKey   []byte

	mu       sync.Mutex
	received []receivedReq
	notify   chan struct{}
	respond  func(w http.ResponseWriter, r *http.Request)

	done chan error
}

type receivedReq struct {
	headers http.Header
	body    []byte
}

func newDispatcherHarness(t *testing.T, opts dispatcherHarnessOptions) *dispatcherHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	fake, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	h := &dispatcherHarness{
		t:          t,
		clk:        clk,
		store:      fake,
		signingKey: []byte("test-signing-key-32-bytes-long!!"),
		fetchKey:   []byte("test-signing-key-32-bytes-long!!"),
		notify:     make(chan struct{}, 64),
	}
	if opts.respond == nil {
		h.respond = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}
	} else {
		h.respond = opts.respond
	}
	h.receiver = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		h.mu.Lock()
		h.received = append(h.received, receivedReq{
			headers: r.Header.Clone(),
			body:    body,
		})
		h.mu.Unlock()
		// h.respond may swap responses across attempts; call it under
		// a snapshot.
		h.mu.Lock()
		respond := h.respond
		h.mu.Unlock()
		respond(w, r)
		select {
		case h.notify <- struct{}{}:
		default:
		}
	}))

	fetchSrv := protowebhook.NewFetchServer(protowebhook.FetchOptions{
		Store:      fake,
		Clock:      clk,
		SigningKey: h.fetchKey,
	})
	mux := http.NewServeMux()
	mux.Handle(protowebhook.FetchPath, fetchSrv.Handler())
	h.fetchSrv = httptest.NewServer(mux)

	dispatcherOpts := protowebhook.Options{
		Store:                   fake,
		Clock:                   clk,
		HTTPClient:              h.receiver.Client(),
		MaxConcurrentDeliveries: 4,
		PollInterval:            10 * time.Millisecond,
		BatchSize:               16,
		InlineBodyMaxSize:       opts.inlineMax,
		FetchURLBaseURL:         h.fetchSrv.URL,
		FetchURLTTL:             time.Hour,
		SigningKey:              h.fetchKey,
		CursorKey:               opts.cursorKey,
		RetrySchedule:           opts.retrySchedule,
	}
	if dispatcherOpts.InlineBodyMaxSize == 0 {
		dispatcherOpts.InlineBodyMaxSize = 1 << 20
	}
	if dispatcherOpts.CursorKey == "" {
		dispatcherOpts.CursorKey = "webhooks-test"
	}
	h.dispatcher = protowebhook.New(dispatcherOpts)

	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() {
		h.done <- h.dispatcher.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Fatalf("dispatcher Run did not return")
		}
		h.receiver.Close()
		h.fetchSrv.Close()
		_ = fake.Close()
	})
	return h
}

type dispatcherHarnessOptions struct {
	respond       func(w http.ResponseWriter, r *http.Request)
	inlineMax     int64
	cursorKey     string
	retrySchedule []time.Duration
}

// seedPrincipalDomain inserts a principal "user@domain" plus an
// INBOX mailbox and returns them.
func (h *dispatcherHarness) seedPrincipalDomain(t *testing.T, email string) (store.Principal, store.Mailbox) {
	t.Helper()
	p, err := h.store.Meta().InsertPrincipal(h.ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	mb, err := h.store.Meta().InsertMailbox(h.ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	return p, mb
}

// insertWebhookForDomain registers an active domain-scoped webhook.
func (h *dispatcherHarness) insertWebhookForDomain(t *testing.T, domain string, mode store.DeliveryMode, secret []byte) store.Webhook {
	t.Helper()
	w, err := h.store.Meta().InsertWebhook(h.ctx, store.Webhook{
		OwnerKind:    store.WebhookOwnerDomain,
		OwnerID:      domain,
		TargetURL:    h.receiver.URL,
		HMACSecret:   secret,
		DeliveryMode: mode,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("insert webhook: %v", err)
	}
	return w
}

// deliverMessage inserts a message with the supplied raw body and
// returns the resulting store.Message.
func (h *dispatcherHarness) deliverMessage(t *testing.T, mb store.Mailbox, subject, raw string) store.Message {
	t.Helper()
	ref, err := h.store.Blobs().Put(h.ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	msg := store.Message{
		PrincipalID: mb.PrincipalID,
		Size:        ref.Size,
		Blob:        ref,
		Envelope: store.Envelope{
			Subject: subject,
			From:    "alice@example.net",
			To:      "user@example.com",
		},
	}
	if _, _, err := h.store.Meta().InsertMessage(h.ctx, msg, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	return msg
}

// waitForDelivery blocks until a POST is received on the test endpoint
// or the context times out. It also drives the FakeClock forward at a
// small step so the dispatcher's poll loop runs.
func (h *dispatcherHarness) waitForDelivery(t *testing.T, timeout time.Duration) receivedReq {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Advance the fake clock so any pending poll-interval After()
		// fires; the production loop uses h.clk.After when the feed
		// is empty.
		h.clk.Advance(20 * time.Millisecond)
		select {
		case <-h.notify:
			h.mu.Lock()
			req := h.received[len(h.received)-1]
			h.mu.Unlock()
			return req
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("no delivery received within %s", timeout)
	return receivedReq{}
}

func (h *dispatcherHarness) receivedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.received)
}

// TestDispatch_NewMessage_Triggers_HTTPPost: seed a domain-scoped
// webhook, insert a message, observe a POST.
func TestDispatch_NewMessage_Triggers_HTTPPost(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	hook := h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, []byte("shh"))

	h.deliverMessage(t, mb, "hello", "Subject: hello\r\n\r\nbody\r\n")

	req := h.waitForDelivery(t, 2*time.Second)
	if got := req.headers.Get(protowebhook.HeaderEvent); got != protowebhook.EventMailArrived {
		t.Fatalf("event header = %q, want %q", got, protowebhook.EventMailArrived)
	}
	if got := req.headers.Get(protowebhook.HeaderWebhookID); got != strconv.FormatUint(uint64(hook.ID), 10) {
		t.Fatalf("webhook id header = %q, want %d", got, hook.ID)
	}
	if got := req.headers.Get(protowebhook.HeaderDeliveryID); got == "" {
		t.Fatalf("missing delivery id header")
	}

	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.Event != protowebhook.EventMailArrived {
		t.Fatalf("payload event = %q", pl.Event)
	}
	if pl.Envelope.Subject != "hello" {
		t.Fatalf("envelope subject = %q", pl.Envelope.Subject)
	}
	if pl.Body.Mode != "inline" {
		t.Fatalf("body mode = %q, want inline", pl.Body.Mode)
	}
	if pl.Body.Inline == nil || pl.Body.Inline.RawBase64 == "" {
		t.Fatalf("inline body missing")
	}
}

// TestDispatch_HMAC_Signature_Verifies: signature header must equal
// hex(HMAC-SHA256(secret, body)).
func TestDispatch_HMAC_Signature_Verifies(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	secret := []byte("super-secret-key")
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, secret)
	h.deliverMessage(t, mb, "x", "Subject: x\r\n\r\nbody\r\n")

	req := h.waitForDelivery(t, 2*time.Second)
	got := req.headers.Get(protowebhook.HeaderSignature)
	mac := hmac.New(sha256.New, secret)
	mac.Write(req.body)
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

// TestDispatch_InlineBody_BelowThreshold: the message body is small,
// the receiver gets it inline base64-encoded.
func TestDispatch_InlineBody_BelowThreshold(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{
		inlineMax: 1024,
	})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, []byte("k"))

	body := "Subject: small\r\n\r\nhello world\r\n"
	h.deliverMessage(t, mb, "small", body)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.Body.Mode != "inline" || pl.Body.Inline == nil {
		t.Fatalf("expected inline body, got mode=%q", pl.Body.Mode)
	}
	decoded, err := base64.StdEncoding.DecodeString(pl.Body.Inline.RawBase64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != body {
		t.Fatalf("inline body = %q, want %q", decoded, body)
	}
}

// TestDispatch_FetchURLBody_AboveThreshold: when message size exceeds
// InlineBodyMaxSize the payload switches to fetch_url mode and the
// signed URL fetches the underlying body.
func TestDispatch_FetchURLBody_AboveThreshold(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{
		inlineMax: 32, // tiny so even small messages overflow
	})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, []byte("k"))

	raw := "Subject: big\r\n\r\n" + strings.Repeat("xyz", 200) + "\r\n"
	h.deliverMessage(t, mb, "big", raw)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.Body.Mode != "fetch_url" || pl.Body.FetchURL == nil {
		t.Fatalf("expected fetch_url body, got mode=%q", pl.Body.Mode)
	}
	resp, err := http.Get(pl.Body.FetchURL.URL)
	if err != nil {
		t.Fatalf("GET fetch url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fetch status = %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != raw {
		t.Fatalf("fetched body mismatch: got %q want %q", got, raw)
	}
}

// TestDispatch_5xx_RetriesPerPolicy_ThenFails: receiver returns 503
// repeatedly; after the configured retry schedule is exhausted the
// dispatcher gives up.
func TestDispatch_5xx_RetriesPerPolicy_ThenFails(t *testing.T) {
	var attempts atomic.Int32
	h := newDispatcherHarness(t, dispatcherHarnessOptions{
		respond: func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		},
		retrySchedule: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond},
	})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, []byte("k"))

	h.deliverMessage(t, mb, "x", "Subject: x\r\n\r\nbody\r\n")

	// Wait for the first attempt.
	h.waitForDelivery(t, 2*time.Second)

	// Drive the FakeClock past the scheduled backoffs; the dispatcher
	// makes 1 + len(schedule) = 3 attempts then gives up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() < 3 {
		h.clk.Advance(50 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	// Give a moment for any spurious extra attempt; assert there is none.
	h.clk.Advance(time.Second)
	time.Sleep(100 * time.Millisecond)
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts after grace = %d, want 3", got)
	}
}

// TestDispatch_4xx_PermanentNoRetry: a 400 response is permanent;
// only one attempt is made.
func TestDispatch_4xx_PermanentNoRetry(t *testing.T) {
	var attempts atomic.Int32
	h := newDispatcherHarness(t, dispatcherHarnessOptions{
		respond: func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		},
		retrySchedule: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond},
	})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, []byte("k"))
	h.deliverMessage(t, mb, "x", "Subject: x\r\n\r\nbody\r\n")

	h.waitForDelivery(t, 2*time.Second)
	// Give time for any (incorrect) retry.
	h.clk.Advance(time.Second)
	time.Sleep(100 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (permanent failure)", got)
	}
}

// TestFetchURL_ExpiredToken_403: a token whose exp has passed returns
// 403 from the FetchHandler.
func TestFetchURL_ExpiredToken_403(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	fake, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	defer fake.Close()
	key := []byte("k")
	srv := protowebhook.NewFetchServer(protowebhook.FetchOptions{
		Store:      fake,
		Clock:      clk,
		SigningKey: key,
	})
	mux := http.NewServeMux()
	mux.Handle(protowebhook.FetchPath, srv.Handler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	exp := clk.Now().Add(-time.Minute).Unix() // already expired
	delivery := "abcdef0123456789"
	hash := "deadbeef"
	tok := hmacHex(key, fmt.Sprintf("%s:%s:%d", delivery, hash, exp))
	url := fmt.Sprintf("%s%s%s?blob=%s&exp=%d&token=%s", ts.URL, protowebhook.FetchPath, delivery, hash, exp, tok)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestFetchURL_BadHMAC_403: any bit-flip in the token rejects the
// request.
func TestFetchURL_BadHMAC_403(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))
	dbPath2 := filepath.Join(t.TempDir(), "test.db")
	fake, err := storesqlite.OpenWithRand(context.Background(), dbPath2, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	defer fake.Close()
	key := []byte("k")
	srv := protowebhook.NewFetchServer(protowebhook.FetchOptions{
		Store:      fake,
		Clock:      clk,
		SigningKey: key,
	})
	mux := http.NewServeMux()
	mux.Handle(protowebhook.FetchPath, srv.Handler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	exp := clk.Now().Add(time.Hour).Unix()
	delivery := "abcdef0123456789"
	hash := "deadbeef"
	tok := hmacHex(key, fmt.Sprintf("%s:%s:%d", delivery, hash, exp))
	// Flip one nibble of the token.
	tampered := tok[:len(tok)-1] + "0"
	if tampered == tok {
		tampered = tok[:len(tok)-1] + "1"
	}
	url := fmt.Sprintf("%s%s%s?blob=%s&exp=%d&token=%s", ts.URL, protowebhook.FetchPath, delivery, hash, exp, tampered)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestRunResume_FromCursor_AfterRestart: deliver one message under one
// dispatcher, restart with the same cursor key, observe that the
// second dispatcher does not re-deliver the same message.
func TestRunResume_FromCursor_AfterRestart(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))
	dbPath3 := filepath.Join(t.TempDir(), "test.db")
	fake, err := storesqlite.OpenWithRand(context.Background(), dbPath3, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	defer fake.Close()

	var deliveries atomic.Int32
	notify := make(chan struct{}, 16)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		deliveries.Add(1)
		w.WriteHeader(http.StatusOK)
		select {
		case notify <- struct{}{}:
		default:
		}
	}))
	defer receiver.Close()

	// Seed: principal + domain + webhook.
	ctx := context.Background()
	p, err := fake.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "user@example.com",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	mb, err := fake.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	if _, err := fake.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind:    store.WebhookOwnerDomain,
		OwnerID:      "example.com",
		TargetURL:    receiver.URL,
		HMACSecret:   []byte("k"),
		DeliveryMode: store.DeliveryModeInline,
		Active:       true,
	}); err != nil {
		t.Fatalf("insert webhook: %v", err)
	}

	cursorKey := "webhooks-resume-test"
	startDispatcher := func() (*protowebhook.Dispatcher, context.CancelFunc, chan error) {
		dctx, cancel := context.WithCancel(context.Background())
		d := protowebhook.New(protowebhook.Options{
			Store:                   fake,
			Clock:                   clk,
			HTTPClient:              receiver.Client(),
			MaxConcurrentDeliveries: 4,
			PollInterval:            10 * time.Millisecond,
			BatchSize:               16,
			InlineBodyMaxSize:       1 << 20,
			FetchURLBaseURL:         "http://unused.local",
			SigningKey:              []byte("k"),
			CursorKey:               cursorKey,
		})
		done := make(chan error, 1)
		go func() { done <- d.Run(dctx) }()
		return d, cancel, done
	}

	// Round 1: deliver one message.
	ref, err := fake.Blobs().Put(ctx, strings.NewReader("Subject: x\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	if _, _, err := fake.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		Size:        ref.Size,
		Blob:        ref,
		Envelope:    store.Envelope{Subject: "x", From: "a@example.net", To: "user@example.com"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	d1, cancel1, done1 := startDispatcher()
	// Drive the loop.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && deliveries.Load() < 1 {
		clk.Advance(20 * time.Millisecond)
		select {
		case <-notify:
		case <-time.After(20 * time.Millisecond):
		}
	}
	if got := deliveries.Load(); got != 1 {
		t.Fatalf("first round deliveries = %d, want 1", got)
	}
	// Allow the cursor write to flush.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && d1.Cursor() == 0 {
		clk.Advance(20 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
	}
	if d1.Cursor() == 0 {
		t.Fatalf("cursor never advanced on first dispatcher")
	}
	cancel1()
	<-done1

	// Round 2: restart with same cursor key. No new messages — should
	// not re-deliver.
	_, cancel2, done2 := startDispatcher()
	clk.Advance(time.Second)
	time.Sleep(200 * time.Millisecond)
	if got := deliveries.Load(); got != 1 {
		cancel2()
		<-done2
		t.Fatalf("after restart deliveries = %d, want 1 (no double delivery)", got)
	}
	cancel2()
	<-done2
}

// hmacHex is the test-side computation of the fetch-URL token. Mirrors
// the formula used inside the package; lifting it here keeps the tests
// honest about the wire contract.
func hmacHex(key []byte, msg string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
