package protosend_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protosend"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fakeQueue is a minimal Submitter that records every Submit call and
// returns canned envelope ids. Tests assert on calls.
type fakeQueue struct {
	mu          sync.Mutex
	calls       []queue.Submission
	bodies      [][]byte
	idempStore  map[string]queue.EnvelopeID
	nextID      int64
	failNext    bool
	failNextErr error
}

func newFakeQueue() *fakeQueue {
	return &fakeQueue{idempStore: make(map[string]queue.EnvelopeID)}
}

func (f *fakeQueue) Submit(ctx context.Context, msg queue.Submission) (queue.EnvelopeID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return "", f.failNextErr
	}
	// Read the body so tests can inspect it later. We stash it.
	body, _ := io.ReadAll(msg.Body)
	msg.Body = nil
	if msg.IdempotencyKey != "" {
		if existing, ok := f.idempStore[msg.IdempotencyKey]; ok {
			f.calls = append(f.calls, msg)
			f.bodies = append(f.bodies, body)
			return existing, queue.ErrConflict
		}
	}
	atomic.AddInt64(&f.nextID, 1)
	envID := queue.EnvelopeID(fmt.Sprintf("env-%d", f.nextID))
	if msg.IdempotencyKey != "" {
		f.idempStore[msg.IdempotencyKey] = envID
	}
	f.calls = append(f.calls, msg)
	f.bodies = append(f.bodies, body)
	return envID, nil
}

func (f *fakeQueue) Calls() []queue.Submission {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]queue.Submission, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeQueue) Bodies() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.bodies))
	copy(out, f.bodies)
	return out
}

// sendHarness wires the fakestore + a real protosend.Server backed by
// a fakeQueue to a testharness listener.
type sendHarness struct {
	t       *testing.T
	h       *testharness.Server
	srv     *protosend.Server
	q       *fakeQueue
	client  *http.Client
	baseURL string
	clk     *clock.FakeClock
	store   store.Store

	// canned credentials
	apiKey      string
	apiKeyID    store.APIKeyID
	principalID store.PrincipalID
}

func newSendHarness(t *testing.T) *sendHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "send", Protocol: "send"},
		},
	})

	// Seed a local domain.
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	// Seed a principal who owns example.test.
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	// Mint an API key.
	plain, hash := mintKey(t)
	apiKey, err := fs.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: p.ID,
		Hash:        hash,
		Name:        "alice-send",
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	q := newFakeQueue()
	srv := protosend.NewServer(fs, nil, q, nil, nil, clk, protosend.Options{
		MaxConcurrentRequests: 8,
		RateLimitPerKey:       60,
		MaxRecipients:         100,
		MaxBatchItems:         50,
		Hostname:              "mx.example.test",
	})
	if err := h.AttachSend("send", srv, protosend.ListenerModePlain); err != nil {
		t.Fatalf("AttachSend: %v", err)
	}
	client, base := h.DialSendByName(context.Background(), "send")
	return &sendHarness{
		t: t, h: h, srv: srv, q: q, client: client, baseURL: base,
		clk: clk, store: fs,
		apiKey: plain, apiKeyID: apiKey.ID, principalID: p.ID,
	}
}

func mintKey(t *testing.T) (plain, hash string) {
	t.Helper()
	const k = "hk_unit_test_token_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	return k, protosend.HashAPIKey(k)
}

// doRequest issues a JSON request with optional Authorization.
func (h *sendHarness) doRequest(method, path, key string, body any) (*http.Response, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			rdr = strings.NewReader(v)
		case []byte:
			rdr = bytes.NewReader(v)
		default:
			b, err := json.Marshal(body)
			if err != nil {
				h.t.Fatalf("marshal: %v", err)
			}
			rdr = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequest(method, h.baseURL+path, rdr)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read: %v", err)
	}
	return res, buf
}

// -- Tests ----------------------------------------------------------

func TestSend_HappyPath_Structured(t *testing.T) {
	h := newSendHarness(t)
	body := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "Hello",
			"body":    map[string]any{"text": "Hi there"},
		},
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	var resp struct {
		MessageID    string `json:"messageId"`
		SubmissionID string `json:"submissionId"`
	}
	if err := json.Unmarshal(buf, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(resp.MessageID, "<") || !strings.HasSuffix(resp.MessageID, ">") {
		t.Fatalf("bad message-id: %q", resp.MessageID)
	}
	if resp.SubmissionID == "" {
		t.Fatalf("missing submissionId")
	}
	calls := h.q.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 submit, got %d", len(calls))
	}
	c := calls[0]
	if c.MailFrom != "alice@example.test" {
		t.Errorf("MailFrom=%q", c.MailFrom)
	}
	if len(c.Recipients) != 1 || c.Recipients[0] != "bob@dest.test" {
		t.Errorf("Recipients=%v", c.Recipients)
	}
	if !c.Sign {
		t.Errorf("Sign should be true")
	}
	if c.SigningDomain != "example.test" {
		t.Errorf("SigningDomain=%q", c.SigningDomain)
	}
	bodies := h.q.Bodies()
	if len(bodies) != 1 || !bytes.Contains(bodies[0], []byte("Subject: Hello")) {
		t.Errorf("body missing subject: %s", bodies[0])
	}
}

func TestSend_RFC5322Multipart_FromTextAndHTML(t *testing.T) {
	h := newSendHarness(t)
	body := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "Both",
			"body": map[string]any{
				"text": "plain text",
				"html": "<p>html body</p>",
			},
		},
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	bodies := h.q.Bodies()
	if len(bodies) != 1 {
		t.Fatalf("expected 1 body, got %d", len(bodies))
	}
	b := bodies[0]
	if !bytes.Contains(b, []byte("multipart/alternative")) {
		t.Errorf("body should be multipart/alternative: %s", b)
	}
	if !bytes.Contains(b, []byte("text/plain")) {
		t.Errorf("body should contain text/plain part: %s", b)
	}
	if !bytes.Contains(b, []byte("text/html")) {
		t.Errorf("body should contain text/html part: %s", b)
	}
}

func TestSendRaw_HappyPath(t *testing.T) {
	h := newSendHarness(t)
	raw := "From: alice@example.test\r\n" +
		"To: bob@dest.test\r\n" +
		"Subject: raw\r\n" +
		"\r\n" +
		"raw body\r\n"
	body := map[string]any{
		"destinations": []string{"bob@dest.test"},
		"rawMessage":   base64.StdEncoding.EncodeToString([]byte(raw)),
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send-raw", h.apiKey, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	calls := h.q.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].MailFrom != "alice@example.test" {
		t.Errorf("MailFrom=%q", calls[0].MailFrom)
	}
	bodies := h.q.Bodies()
	// Original body bytes should be present (with at least subject).
	if !bytes.Contains(bodies[0], []byte("Subject: raw")) {
		t.Errorf("body missing original subject: %s", bodies[0])
	}
	// Date should have been prepended (was missing in the raw input).
	if !bytes.Contains(bodies[0], []byte("Date:")) {
		t.Errorf("body missing prepended Date: %s", bodies[0])
	}
}

func TestSendBatch_PartialSuccess(t *testing.T) {
	h := newSendHarness(t)
	// Item 1: ok. Item 2: bad source (unowned domain). Item 3: ok.
	good := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "Hi",
			"body":    map[string]any{"text": "hello"},
		},
	}
	bad := map[string]any{
		"source": "alice@unowned.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "Bad",
			"body":    map[string]any{"text": "hello"},
		},
	}
	batch := []map[string]any{
		{"send": good},
		{"send": bad},
		{"send": good},
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send-batch", h.apiKey, batch)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	var out struct {
		Items []struct {
			MessageID    string                 `json:"messageId"`
			SubmissionID string                 `json:"submissionId"`
			Problem      map[string]interface{} `json:"problem"`
		} `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if len(out.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out.Items))
	}
	if out.Items[0].MessageID == "" || out.Items[0].Problem != nil {
		t.Errorf("item 0 should succeed: %+v", out.Items[0])
	}
	if out.Items[1].Problem == nil {
		t.Errorf("item 1 should have problem")
	}
	if out.Items[2].MessageID == "" || out.Items[2].Problem != nil {
		t.Errorf("item 2 should succeed: %+v", out.Items[2])
	}
	// Two queue calls.
	if len(h.q.Calls()) != 2 {
		t.Errorf("expected 2 queue calls, got %d", len(h.q.Calls()))
	}
}

func TestIdempotency_RepeatedKeyReturnsPrior(t *testing.T) {
	h := newSendHarness(t)
	body := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "Idem",
			"body":    map[string]any{"text": "hi"},
		},
		"idempotencyKey": "client-uuid-1",
	}
	res1, buf1 := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("first: status=%d body=%s", res1.StatusCode, buf1)
	}
	res2, buf2 := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("second: status=%d body=%s", res2.StatusCode, buf2)
	}
	var r1, r2 struct {
		SubmissionID string `json:"submissionId"`
	}
	_ = json.Unmarshal(buf1, &r1)
	_ = json.Unmarshal(buf2, &r2)
	if r1.SubmissionID == "" || r1.SubmissionID != r2.SubmissionID {
		t.Fatalf("expected same submission id; first=%q second=%q",
			r1.SubmissionID, r2.SubmissionID)
	}
}

func TestForbiddenSource_NonOwnedDomain(t *testing.T) {
	h := newSendHarness(t)
	body := map[string]any{
		"source": "alice@somewhere-else.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "X",
			"body":    map[string]any{"text": "hi"},
		},
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	if !bytes.Contains(buf, []byte("forbidden-source")) {
		t.Errorf("body should reference forbidden-source: %s", buf)
	}
	if len(h.q.Calls()) != 0 {
		t.Errorf("queue should not have been called: %d calls", len(h.q.Calls()))
	}
}

func TestRateLimit_PerKey_429(t *testing.T) {
	h := newSendHarnessWithRate(t, 2)
	body := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "X", "body": map[string]any{"text": "hi"},
		},
	}
	for i := 0; i < 2; i++ {
		res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("warmup %d: status=%d body=%s", i, res.StatusCode, buf)
		}
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 got %d body=%s", res.StatusCode, buf)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Errorf("expected Retry-After header")
	}
}

func newSendHarnessWithRate(t *testing.T, rate int) *sendHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs, Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "send", Protocol: "send"},
		},
	})
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	plain, hash := mintKey(t)
	apiKey, err := fs.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: p.ID, Hash: hash, Name: "alice-send",
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	q := newFakeQueue()
	srv := protosend.NewServer(fs, nil, q, nil, nil, clk, protosend.Options{
		RateLimitPerKey: rate,
		Hostname:        "mx.example.test",
	})
	if err := h.AttachSend("send", srv, protosend.ListenerModePlain); err != nil {
		t.Fatalf("AttachSend: %v", err)
	}
	client, base := h.DialSendByName(context.Background(), "send")
	return &sendHarness{
		t: t, h: h, srv: srv, q: q, client: client, baseURL: base,
		clk: clk, store: fs,
		apiKey: plain, apiKeyID: apiKey.ID, principalID: p.ID,
	}
}

func TestQuota_Returns_Snapshot(t *testing.T) {
	h := newSendHarness(t)
	// Send once so PerMinuteUsed is non-zero.
	body := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "X", "body": map[string]any{"text": "hi"},
		},
	}
	if res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body); res.StatusCode != http.StatusOK {
		t.Fatalf("send: status=%d body=%s", res.StatusCode, buf)
	}
	res, buf := h.doRequest("GET", "/api/v1/mail/quota", h.apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("quota: status=%d body=%s", res.StatusCode, buf)
	}
	var q struct {
		PerMinuteLimit  int   `json:"perMinuteLimit"`
		PerMinuteUsed   int   `json:"perMinuteUsed"`
		PerMinuteRemain int   `json:"perMinuteRemaining"`
		DailyUsed       int64 `json:"dailyUsed"`
	}
	if err := json.Unmarshal(buf, &q); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if q.PerMinuteUsed < 1 {
		t.Errorf("PerMinuteUsed should be >=1, got %d", q.PerMinuteUsed)
	}
	if q.DailyUsed < 1 {
		t.Errorf("DailyUsed should be >=1, got %d", q.DailyUsed)
	}
}

func TestStats_Returns_Aggregates(t *testing.T) {
	h := newSendHarness(t)
	// Insert a couple of QueueItem rows directly so the stats aggregator
	// has something to count.
	now := h.clk.Now()
	_, err := h.store.Meta().EnqueueMessage(context.Background(), store.QueueItem{
		PrincipalID:  h.principalID,
		MailFrom:     "alice@example.test",
		RcptTo:       "bob@dest.test",
		EnvelopeID:   "env-A",
		BodyBlobHash: "abc",
		State:        store.QueueStateDone,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("EnqueueMessage 1: %v", err)
	}
	_, err = h.store.Meta().EnqueueMessage(context.Background(), store.QueueItem{
		PrincipalID:  h.principalID,
		MailFrom:     "alice@example.test",
		RcptTo:       "carol@dest.test",
		EnvelopeID:   "env-B",
		BodyBlobHash: "def",
		State:        store.QueueStateFailed,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("EnqueueMessage 2: %v", err)
	}
	res, buf := h.doRequest("GET", "/api/v1/mail/stats", h.apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	var s struct {
		Submitted int `json:"submitted"`
		Delivered int `json:"delivered"`
		Failed    int `json:"failed"`
		Bounced   int `json:"bounced"`
	}
	if err := json.Unmarshal(buf, &s); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if s.Submitted < 2 {
		t.Errorf("Submitted should be >=2: %+v", s)
	}
	if s.Delivered < 1 {
		t.Errorf("Delivered should be >=1: %+v", s)
	}
	if s.Failed < 1 {
		t.Errorf("Failed should be >=1: %+v", s)
	}
}

// panicQueue is a Submitter that panics. Used to verify the panic
// recover middleware turns crashes into 500s.
type panicQueue struct{}

func (panicQueue) Submit(ctx context.Context, msg queue.Submission) (queue.EnvelopeID, error) {
	panic("boom")
}

func TestPanic_InHandler_Returns500_NotCrash(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs, Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "send", Protocol: "send"},
		},
	})
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	plain, hash := mintKey(t)
	if _, err := fs.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: p.ID, Hash: hash, Name: "k",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	srv := protosend.NewServer(fs, nil, panicQueue{}, nil, nil, clk, protosend.Options{
		Hostname: "mx.example.test",
	})
	if err := h.AttachSend("send", srv, protosend.ListenerModePlain); err != nil {
		t.Fatalf("AttachSend: %v", err)
	}
	client, base := h.DialSendByName(context.Background(), "send")
	body := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "X", "body": map[string]any{"text": "hi"},
		},
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+"/api/v1/mail/send", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+plain)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 got %d body=%s", res.StatusCode, buf)
	}
}
