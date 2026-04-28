package protojmap_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fixture holds the per-test server, store, fake clock, and a ready-
// to-use API key for the seeded principal. It is the single object
// every test in this file consumes; the boilerplate lives here so
// individual test functions stay focused on one assertion.
type fixture struct {
	t      *testing.T
	store  store.Store
	dir    *directory.Directory
	clk    *clock.FakeClock
	srv    *protojmap.Server
	httpd  *httptest.Server
	pid    store.PrincipalID
	apiKey string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "alice@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	apiKey, _, err := createAPIKey(context.Background(), fs, pid)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	srv := protojmap.NewServer(fs, dir, nil, nil, clk, protojmap.Options{
		MaxCallsInRequest:  4,
		PushPingInterval:   60 * time.Second,
		PushCoalesceWindow: 50 * time.Millisecond,
		// Disable rate limit by default; one test re-enables it.
		DownloadRatePerSec: -1,
	})
	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)
	return &fixture{
		t: t, store: fs, dir: dir, clk: clk,
		srv: srv, httpd: httpd, pid: pid, apiKey: apiKey,
	}
}

// createAPIKey mints a fresh API key for pid and returns the plaintext
// token. Mirrors what the protoadmin POST /api-keys handler does
// internally so tests do not need to drive that surface.
func createAPIKey(ctx context.Context, st store.Store, pid store.PrincipalID) (string, store.APIKey, error) {
	const plaintext = "hk_protojmap_test_key_alice_0001"
	hashed := protojmap.HashAPIKeyForTest(plaintext)
	row, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid,
		Hash:        hashed,
		Name:        "test",
		CreatedAt:   time.Now(),
	})
	if err != nil {
		return "", store.APIKey{}, err
	}
	return plaintext, row, nil
}

// fakeHandler is a MethodHandler stand-in. It records the args its
// Execute received so back-reference tests can assert on the resolved
// values, and it returns either a fixed response or a canned
// MethodError. Every Wave 2.2 test against the registry seam uses this
// shape so tests stay independent of any real datatype handler.
type fakeHandler struct {
	method string

	// Behaviour. Exactly one of (resp, errOut) is set.
	resp   any
	errOut *protojmap.MethodError

	// Recording. Append-only; readers take a snapshot.
	mu     sync.Mutex
	called []json.RawMessage
}

func (h *fakeHandler) Method() string { return h.method }

func (h *fakeHandler) Execute(_ context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	h.mu.Lock()
	cp := make(json.RawMessage, len(args))
	copy(cp, args)
	h.called = append(h.called, cp)
	h.mu.Unlock()
	if h.errOut != nil {
		return nil, h.errOut
	}
	return h.resp, nil
}

func (h *fakeHandler) lastArgs() json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.called) == 0 {
		return nil
	}
	return h.called[len(h.called)-1]
}

// fakeAccountCap is a stand-in for an AccountCapabilityProvider. The
// session-descriptor test asserts that its AccountCapability output
// lands in the per-account map.
type fakeAccountCap struct {
	maxFoo int
}

func (c fakeAccountCap) AccountCapability() any {
	return map[string]any{"maxFoo": c.maxFoo}
}

// doRequest issues an HTTP request against the test server. When key
// is non-empty it is sent as Bearer auth.
func (f *fixture) doRequest(method, path, key string, body any) (*http.Response, []byte) {
	f.t.Helper()
	var rdr io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			rdr = bytes.NewReader(v)
		case string:
			rdr = strings.NewReader(v)
		default:
			b, err := json.Marshal(v)
			if err != nil {
				f.t.Fatalf("marshal: %v", err)
			}
			rdr = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequest(method, f.httpd.URL+path, rdr)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := f.httpd.Client().Do(req)
	if err != nil {
		f.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("read: %v", err)
	}
	return res, buf
}

// -- Session endpoint -------------------------------------------------

func TestSession_RequiresAuth(t *testing.T) {
	f := newFixture(t)
	res, _ := f.doRequest("GET", "/.well-known/jmap", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	if w := res.Header.Get("WWW-Authenticate"); !strings.Contains(w, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want Bearer challenge", w)
	}
}

func TestSession_RejectsBadBearer(t *testing.T) {
	f := newFixture(t)
	res, _ := f.doRequest("GET", "/.well-known/jmap", "not_a_real_key_format", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestSession_AcceptsBasicAuth(t *testing.T) {
	f := newFixture(t)
	auth := base64.StdEncoding.EncodeToString([]byte("alice@example.com:correct-horse-battery-staple-1"))
	req, err := http.NewRequest("GET", f.httpd.URL+"/.well-known/jmap", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Basic "+auth)
	res, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
}

func TestSession_ReturnsCapabilitiesAndAccounts(t *testing.T) {
	f := newFixture(t)
	// Register a fake datatype capability + a per-account capability
	// provider; the response must surface both in the session
	// descriptor.
	const fakeCap = protojmap.CapabilityID("urn:test:fake")
	fh := &fakeHandler{method: "Fake/echo", resp: json.RawMessage(`{}`)}
	f.srv.Registry().Register(fakeCap, fh)
	f.srv.Registry().RegisterCapabilityDescriptor(fakeCap, map[string]any{"version": 1})
	f.srv.Registry().RegisterAccountCapability(fakeCap, fakeAccountCap{maxFoo: 7})

	res, body := f.doRequest("GET", "/.well-known/jmap", f.apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	var desc map[string]any
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	caps, ok := desc["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing or wrong type: %T", desc["capabilities"])
	}
	if _, ok := caps[string(protojmap.CapabilityCore)]; !ok {
		t.Fatalf("Core capability missing")
	}
	if _, ok := caps[string(fakeCap)]; !ok {
		t.Fatalf("registered fake capability missing")
	}
	accts, ok := desc["accounts"].(map[string]any)
	if !ok || len(accts) != 1 {
		t.Fatalf("accounts: %v", desc["accounts"])
	}
	for _, a := range accts {
		acct := a.(map[string]any)
		ac, ok := acct["accountCapabilities"].(map[string]any)
		if !ok {
			t.Fatalf("accountCapabilities missing")
		}
		fc, ok := ac[string(fakeCap)].(map[string]any)
		if !ok {
			t.Fatalf("fakeCap account descriptor missing: %v", ac)
		}
		if v, _ := fc["maxFoo"].(float64); v != 7 {
			t.Fatalf("maxFoo = %v, want 7", v)
		}
	}
	if desc["state"] == nil || desc["state"] == "" {
		t.Fatalf("state missing")
	}
	if desc["apiUrl"] == "" || desc["downloadUrl"] == "" || desc["uploadUrl"] == "" || desc["eventSourceUrl"] == "" {
		t.Fatalf("urls incomplete: %#v", desc)
	}
}

// -- Dispatcher -------------------------------------------------------

// jmapPost issues a POST /jmap request envelope. Tests pass a slice of
// method calls + a "using" list; the helper marshals it and decodes
// the response envelope.
func (f *fixture) jmapPost(using []protojmap.CapabilityID, calls []protojmap.Invocation) (*http.Response, protojmap.Response, []byte) {
	f.t.Helper()
	envelope := protojmap.Request{Using: using, MethodCalls: calls}
	body, err := json.Marshal(envelope)
	if err != nil {
		f.t.Fatalf("marshal: %v", err)
	}
	res, raw := f.doRequest("POST", "/jmap", f.apiKey, body)
	if res.StatusCode != http.StatusOK {
		return res, protojmap.Response{}, raw
	}
	var out protojmap.Response
	if err := json.Unmarshal(raw, &out); err != nil {
		f.t.Fatalf("decode response: %v: %s", err, raw)
	}
	return res, out, raw
}

func TestDispatch_CoreEcho_RoundTrip(t *testing.T) {
	f := newFixture(t)
	args := json.RawMessage(`{"hello":"world","n":42}`)
	res, env, raw := f.jmapPost(
		[]protojmap.CapabilityID{protojmap.CapabilityCore},
		[]protojmap.Invocation{
			{Name: "Core/echo", Args: args, CallID: "c0"},
		},
	)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, raw)
	}
	if len(env.MethodResponses) != 1 {
		t.Fatalf("methodResponses = %v", env.MethodResponses)
	}
	got := env.MethodResponses[0]
	if got.Name != "Core/echo" || got.CallID != "c0" {
		t.Fatalf("response head = %+v", got)
	}
	var echoed map[string]any
	if err := json.Unmarshal(got.Args, &echoed); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	if echoed["hello"] != "world" {
		t.Fatalf("echo lost field: %v", echoed)
	}
}

func TestDispatch_BackReferences_Resolved(t *testing.T) {
	f := newFixture(t)
	const cap = protojmap.CapabilityID("urn:test:back")
	// First handler returns {"list":[{"id":"X1"},{"id":"X2"}]}.
	first := &fakeHandler{
		method: "T/get",
		resp: map[string]any{
			"list": []map[string]any{{"id": "X1"}, {"id": "X2"}},
		},
	}
	// Second handler records its received args verbatim.
	second := &fakeHandler{
		method: "T/use",
		resp:   map[string]any{"ok": true},
	}
	f.srv.Registry().Register(cap, first)
	f.srv.Registry().Register(cap, second)

	res, env, raw := f.jmapPost(
		[]protojmap.CapabilityID{cap},
		[]protojmap.Invocation{
			{Name: "T/get", Args: json.RawMessage(`{"ids":["X1","X2"]}`), CallID: "a0"},
			{Name: "T/use", Args: json.RawMessage(`{"#ids":{"resultOf":"a0","name":"T/get","path":"/list/*/id"}}`), CallID: "a1"},
		},
	)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, raw)
	}
	if len(env.MethodResponses) != 2 {
		t.Fatalf("methodResponses len = %d", len(env.MethodResponses))
	}
	if env.MethodResponses[1].Name != "T/use" {
		t.Fatalf("second response name = %q", env.MethodResponses[1].Name)
	}
	gotArgs := second.lastArgs()
	if gotArgs == nil {
		t.Fatalf("second handler not called")
	}
	var resolved map[string]any
	if err := json.Unmarshal(gotArgs, &resolved); err != nil {
		t.Fatalf("decode resolved args: %v", err)
	}
	ids, ok := resolved["ids"].([]any)
	if !ok {
		t.Fatalf("ids missing or wrong type: %v (raw=%s)", resolved, gotArgs)
	}
	if len(ids) != 2 || ids[0] != "X1" || ids[1] != "X2" {
		t.Fatalf("ids = %v, want [X1 X2]", ids)
	}
}

func TestDispatch_UnknownCapability_Errors(t *testing.T) {
	f := newFixture(t)
	res, _ := f.doRequest("POST", "/jmap", f.apiKey, protojmap.Request{
		Using: []protojmap.CapabilityID{"urn:test:nope"},
		MethodCalls: []protojmap.Invocation{
			{Name: "Core/echo", Args: json.RawMessage(`{}`), CallID: "c0"},
		},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

// TestDispatch_WrongContentType verifies that POST /jmap rejects
// requests whose Content-Type is not "application/json" with a 4xx
// error before any body parsing occurs (RFC 8620 §3.4).
func TestDispatch_WrongContentType(t *testing.T) {
	f := newFixture(t)
	body := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Core/echo",{},"c0"]]}`
	req, err := http.NewRequest(http.MethodPost, f.httpd.URL+"/jmap", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Content-Type", "text/plain")
	res, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 400 || res.StatusCode >= 500 {
		t.Fatalf("status = %d, want 4xx for wrong Content-Type", res.StatusCode)
	}
}

// TestDispatch_MissingContentType verifies that POST /jmap rejects
// requests with no Content-Type header (RFC 8620 §3.4).
func TestDispatch_MissingContentType(t *testing.T) {
	f := newFixture(t)
	body := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Core/echo",{},"c0"]]}`
	req, err := http.NewRequest(http.MethodPost, f.httpd.URL+"/jmap", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	// deliberately omit Content-Type
	res, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 400 || res.StatusCode >= 500 {
		t.Fatalf("status = %d, want 4xx for missing Content-Type", res.StatusCode)
	}
}

// TestDispatch_ContentTypeWithCharset verifies that "application/json;
// charset=utf-8" (with a parameter) is accepted (RFC 8620 §3.4).
func TestDispatch_ContentTypeWithCharset(t *testing.T) {
	f := newFixture(t)
	body := `{"using":["urn:ietf:params:jmap:core"],"methodCalls":[["Core/echo",{},"c0"]]}`
	req, err := http.NewRequest(http.MethodPost, f.httpd.URL+"/jmap", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, want 200 for application/json; charset=utf-8: %s", res.StatusCode, buf)
	}
}

// TestDispatch_MissingAccountID verifies that a method call that
// requires an accountId returns a method-level "invalidArguments"
// error when accountId is absent from the args (RFC 8620 §5.1).
// We register a minimal fake handler that calls requireAccount via its
// Execute so the test exercises the dispatch → handler path without
// depending on a real mail datatype.
func TestDispatch_MissingAccountID(t *testing.T) {
	f := newFixture(t)
	const cap = protojmap.CapabilityID("urn:test:acct")
	// fakeAccountHandler rejects calls with missing accountId, just like
	// all real datatype handlers do via their requireAccount helper.
	fakeAcct := &fakeAccountIDHandler{method: "Acct/get"}
	f.srv.Registry().Register(cap, fakeAcct)

	res, env, raw := f.jmapPost(
		[]protojmap.CapabilityID{cap},
		[]protojmap.Invocation{
			// no accountId in args — must produce invalidArguments
			{Name: "Acct/get", Args: json.RawMessage(`{}`), CallID: "c1"},
		},
	)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", res.StatusCode, raw)
	}
	if len(env.MethodResponses) != 1 {
		t.Fatalf("len = %d", len(env.MethodResponses))
	}
	got := env.MethodResponses[0]
	if got.Name != "error" {
		t.Fatalf("response name = %q, want \"error\" (accountId omitted)", got.Name)
	}
	var merr struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(got.Args, &merr); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if merr.Type != "invalidArguments" {
		t.Fatalf("error type = %q, want invalidArguments", merr.Type)
	}
}

// fakeAccountIDHandler simulates any real JMAP datatype handler that
// validates accountId, without importing a concrete datatype package.
type fakeAccountIDHandler struct{ method string }

func (h *fakeAccountIDHandler) Method() string { return h.method }

func (h *fakeAccountIDHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req struct {
		AccountID string `json:"accountId"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &req)
	}
	p, ok := protojmap.PrincipalFromContext(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no principal")
	}
	if req.AccountID == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if req.AccountID != protojmap.AccountIDForPrincipal(p.ID) {
		return nil, protojmap.NewMethodError("accountNotFound", "wrong account")
	}
	return map[string]any{"ok": true}, nil
}

func TestDispatch_MethodError_PartialResponse_OtherCallsExecute(t *testing.T) {
	f := newFixture(t)
	const cap = protojmap.CapabilityID("urn:test:partial")
	fail := &fakeHandler{
		method: "P/fail",
		errOut: protojmap.NewMethodError("invalidArguments", "bad"),
	}
	ok := &fakeHandler{method: "P/ok", resp: map[string]any{"yes": true}}
	f.srv.Registry().Register(cap, fail)
	f.srv.Registry().Register(cap, ok)

	res, env, raw := f.jmapPost(
		[]protojmap.CapabilityID{cap},
		[]protojmap.Invocation{
			{Name: "P/fail", Args: json.RawMessage(`{}`), CallID: "x"},
			{Name: "P/ok", Args: json.RawMessage(`{}`), CallID: "y"},
		},
	)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", res.StatusCode, raw)
	}
	if len(env.MethodResponses) != 2 {
		t.Fatalf("len = %d", len(env.MethodResponses))
	}
	if env.MethodResponses[0].Name != "error" {
		t.Fatalf("first = %q", env.MethodResponses[0].Name)
	}
	if env.MethodResponses[0].CallID != "x" {
		t.Fatalf("first callId = %q", env.MethodResponses[0].CallID)
	}
	if env.MethodResponses[1].Name != "P/ok" {
		t.Fatalf("second = %q", env.MethodResponses[1].Name)
	}
	if env.MethodResponses[1].CallID != "y" {
		t.Fatalf("second callId = %q", env.MethodResponses[1].CallID)
	}
}

func TestDispatch_MaxCallsInRequest_RejectsAtLimit(t *testing.T) {
	f := newFixture(t)
	// fixture's MaxCallsInRequest is 4. Build 5 calls.
	calls := make([]protojmap.Invocation, 5)
	for i := range calls {
		calls[i] = protojmap.Invocation{Name: "Core/echo", Args: json.RawMessage(`{}`), CallID: fmt.Sprintf("c%d", i)}
	}
	res, _ := f.doRequest("POST", "/jmap", f.apiKey, protojmap.Request{
		Using:       []protojmap.CapabilityID{protojmap.CapabilityCore},
		MethodCalls: calls,
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

func TestDispatch_RequiresAuth(t *testing.T) {
	f := newFixture(t)
	res, _ := f.doRequest("POST", "/jmap", "", protojmap.Request{
		Using: []protojmap.CapabilityID{protojmap.CapabilityCore},
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

// -- Upload / Download -----------------------------------------------

func TestUpload_RoundTrip_BlobInStore(t *testing.T) {
	f := newFixture(t)
	accountID := protojmap.AccountIDForPrincipal(f.pid)
	body := []byte("hello jmap upload payload")
	req, err := http.NewRequest("POST", f.httpd.URL+"/jmap/upload/"+accountID, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	res, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		buf, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d: %s", res.StatusCode, buf)
	}
	var out struct {
		AccountID string `json:"accountId"`
		BlobID    string `json:"blobId"`
		Type      string `json:"type"`
		Size      int64  `json:"size"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.BlobID == "" {
		t.Fatalf("blobId empty")
	}
	if out.Size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", out.Size, len(body))
	}
	// Stat the blob to confirm it landed in the blob store.
	size, _, err := f.store.Blobs().Stat(context.Background(), out.BlobID)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size != int64(len(body)) {
		t.Fatalf("stored size = %d, want %d", size, len(body))
	}
}

func TestUpload_TooLarge_413(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "alice@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	apiKey, _, err := createAPIKey(context.Background(), fs, pid)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	srv := protojmap.NewServer(fs, dir, nil, nil, clk, protojmap.Options{
		MaxSizeUpload:      32, // tiny
		DownloadRatePerSec: -1,
	})
	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	body := bytes.Repeat([]byte("x"), 1024)
	accountID := protojmap.AccountIDForPrincipal(pid)
	req, err := http.NewRequest("POST", httpd.URL+"/jmap/upload/"+accountID, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	res, err := httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.StatusCode)
	}
}

func TestDownload_NotFound_404(t *testing.T) {
	f := newFixture(t)
	accountID := protojmap.AccountIDForPrincipal(f.pid)
	// JMAP downloadUrl interpolates {type} as a single path segment, so
	// "text/plain" arrives URL-encoded as "text%2Fplain".
	url := fmt.Sprintf("/jmap/download/%s/missingblobhash/text%%2Fplain/file.txt", accountID)
	res, _ := f.doRequest("GET", url, f.apiKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestDownload_RoundTrip(t *testing.T) {
	f := newFixture(t)
	body := []byte("download body bytes")
	ref, err := f.store.Blobs().Put(context.Background(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	accountID := protojmap.AccountIDForPrincipal(f.pid)
	url := fmt.Sprintf("/jmap/download/%s/%s/text%%2Fplain/file.txt", accountID, ref.Hash)
	res, raw := f.doRequest("GET", url, f.apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if !bytes.Equal(raw, body) {
		t.Fatalf("body mismatch: got %q want %q", raw, body)
	}
	if got := res.Header.Get("Content-Disposition"); !strings.Contains(got, "file.txt") {
		t.Fatalf("Content-Disposition = %q", got)
	}
}

func TestDownload_RateLimit_Throttles(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "alice@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	apiKey, _, err := createAPIKey(context.Background(), fs, pid)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	srv := protojmap.NewServer(fs, dir, nil, nil, clk, protojmap.Options{
		DownloadRatePerSec: 1024,
		DownloadBurstBytes: 32,
	})
	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	// Insert a blob bigger than the burst so the second download
	// trips the limiter.
	body := bytes.Repeat([]byte("0123456789"), 4) // 40 bytes > burst 32
	ref, err := fs.Blobs().Put(context.Background(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	accountID := protojmap.AccountIDForPrincipal(pid)
	url := fmt.Sprintf("%s/jmap/download/%s/%s/application%%2Foctet-stream/blob.bin",
		httpd.URL, accountID, ref.Hash)

	// First request consumes the burst (size > burst, so the
	// pre-check capped to burst depletes the bucket on the first
	// call).
	req1, _ := http.NewRequest("GET", url, nil)
	req1.Header.Set("Authorization", "Bearer "+apiKey)
	res1, err := httpd.Client().Do(req1)
	if err != nil {
		t.Fatalf("do1: %v", err)
	}
	res1.Body.Close()
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", res1.StatusCode)
	}
	// Second request: bucket is empty, expect 429.
	req2, _ := http.NewRequest("GET", url, nil)
	req2.Header.Set("Authorization", "Bearer "+apiKey)
	res2, err := httpd.Client().Do(req2)
	if err != nil {
		t.Fatalf("do2: %v", err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", res2.StatusCode)
	}
	if res2.Header.Get("Retry-After") == "" {
		t.Fatalf("missing Retry-After")
	}
}

// -- EventSource push ------------------------------------------------

func TestEventSource_StateChange_OnFakeChangeFeed(t *testing.T) {
	f := newFixture(t)
	// Fetch the INBOX that CreatePrincipal auto-provisioned. We do not
	// re-insert it because directory.provisionDefaultMailboxes already
	// created it when the principal was created.
	mboxes, err := f.store.Meta().ListMailboxes(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("list mailboxes: %v", err)
	}
	var mb store.Mailbox
	for _, m := range mboxes {
		if m.Name == "INBOX" {
			mb = m
			break
		}
	}
	if mb.ID == 0 {
		t.Fatalf("INBOX not found after principal creation")
	}

	url := f.httpd.URL + "/jmap/eventsource?types=Email&closeafter=state&ping=300"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := f.httpd.Client().Do(req)
		if err != nil {
			errCh <- err
			return
		}
		resCh <- res
	}()

	// Bump the Email state and append a change-feed entry the push
	// loop will observe. The mailbox change is enough — we requested
	// types=Email so we need an Email change to fire.
	if _, err := f.store.Meta().IncrementJMAPState(context.Background(), f.pid, store.JMAPStateKindEmail); err != nil {
		t.Fatalf("increment state: %v", err)
	}
	if _, _, err := f.store.Meta().UpdateMailboxModseqAndAppendChange(context.Background(), mb.ID, store.StateChange{
		PrincipalID: f.pid,
		Kind:        store.EntityKindEmail,
		EntityID:    1,
		Op:          store.ChangeOpCreated,
	}); err != nil {
		t.Fatalf("append change: %v", err)
	}

	// Drive the FakeClock forward so the SSE poll timer fires and
	// the coalesce window expires. We tick a few times so the
	// post-poll flushTimer's deadline is also crossed.
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		for i := 0; i < 200; i++ {
			f.clk.Advance(50 * time.Millisecond)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	defer func() { <-tickDone }()

	var res *http.Response
	select {
	case res = <-resCh:
	case err := <-errCh:
		t.Fatalf("client.Do: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("eventsource did not respond")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	// Read up to and through the first state event. closeafter=state
	// terminates the stream after the first event so io.ReadAll
	// returns once the loop has flushed.
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read sse body: %v", err)
	}
	if !bytes.Contains(buf, []byte(`"@type":"StateChange"`)) {
		t.Fatalf("no StateChange event observed; got: %q", buf)
	}
	if !bytes.Contains(buf, []byte(`"Email":`)) {
		t.Fatalf("StateChange did not include Email type; got: %q", buf)
	}
}

func TestEventSource_Heartbeat(t *testing.T) {
	f := newFixture(t)
	url := f.httpd.URL + "/jmap/eventsource?types=Email&ping=1"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)

	resCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := f.httpd.Client().Do(req)
		if err != nil {
			errCh <- err
			return
		}
		resCh <- res
	}()
	var res *http.Response
	select {
	case res = <-resCh:
	case err := <-errCh:
		t.Fatalf("client.Do: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatalf("eventsource did not respond")
	}
	defer res.Body.Close()
	// Wait for ping to land. We pass real wall-time here because the
	// SSE loop uses the injected FakeClock for its timers, but the
	// FakeClock doesn't advance from outside without an Advance call.
	// Drive the clock forward until we observe a ping or time out.
	deadline := time.Now().Add(3 * time.Second)
	tmp := make([]byte, 64)
	buf := make([]byte, 0, 256)
	saw := make(chan struct{})
	go func() {
		for {
			n, err := res.Body.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				if bytes.Contains(buf, []byte(": ping")) {
					close(saw)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for time.Now().Before(deadline) {
		f.clk.Advance(2 * time.Second)
		select {
		case <-saw:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("did not observe heartbeat; buf=%q", buf)
}
