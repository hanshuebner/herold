package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

// fakeCloudflare is an in-process stub of the Cloudflare API v4. It
// records every inbound request and lets a per-test handler return a
// canned envelope. The wrapper around handler is responsible for
// emitting the cloudflare-shaped {success,errors,result} envelope.
type fakeCloudflare struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []capturedRequest
	handler  func(req capturedRequest) (status int, result any, errs []map[string]any)

	calls int64
}

type capturedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

func newFakeCloudflare(t *testing.T) *fakeCloudflare {
	t.Helper()
	f := &fakeCloudflare{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.calls, 1)
		body, _ := io.ReadAll(r.Body)
		cap := capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		}
		f.mu.Lock()
		f.requests = append(f.requests, cap)
		h := f.handler
		f.mu.Unlock()
		if h == nil {
			http.Error(w, `{"success":false,"errors":[{"code":1,"message":"no handler"}]}`, http.StatusInternalServerError)
			return
		}
		status, result, errs := h(cap)
		raw, _ := json.Marshal(result)
		env := map[string]any{
			"success": status >= 200 && status < 300,
			"errors":  errs,
			"result":  json.RawMessage(raw),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(env)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCloudflare) setHandler(h func(req capturedRequest) (int, any, []map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = h
}

func (f *fakeCloudflare) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
	atomic.StoreInt64(&f.calls, 0)
}

func (f *fakeCloudflare) snapshot() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// spawnPlugin builds the cloudflare plugin binary, starts it, drives
// initialize + configure, and returns a live Client wired to the child
// process. Cleanup tears the plugin down on test completion.
type spawnedPlugin struct {
	t      *testing.T
	cmd    *exec.Cmd
	client *plug.Client
	done   chan error
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func buildPluginBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "herold-dns-cloudflare-bin-")
		if err != nil {
			binErr = err
			return
		}
		bin := filepath.Join(dir, "herold-dns-cloudflare")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "github.com/hanshuebner/herold/plugins/herold-dns-cloudflare")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
			return
		}
		binPath = bin
	})
	if binErr != nil {
		t.Fatalf("build plugin: %v", binErr)
	}
	return binPath
}

func spawnPlugin(t *testing.T, configureOpts map[string]any) *spawnedPlugin {
	t.Helper()
	bin := buildPluginBinary(t)

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	client := plug.NewClient(stdout, stdin, plug.ClientOptions{
		Name:          "herold-dns-cloudflare",
		MaxConcurrent: 8,
	})
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()

	sp := &spawnedPlugin{t: t, cmd: cmd, client: client, done: done}

	// initialize
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res plug.InitializeResult
		if err := client.Call(ctx, plug.MethodInitialize, plug.InitializeParams{
			ServerVersion: "test",
			ABIVersion:    plug.ABIVersion,
		}, &res); err != nil {
			sp.close()
			t.Fatalf("initialize: %v", err)
		}
		if res.Manifest.Name != "herold-dns-cloudflare" {
			sp.close()
			t.Fatalf("manifest.Name = %q", res.Manifest.Name)
		}
	}

	// configure
	if configureOpts != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res plug.ConfigureResult
		if err := client.Call(ctx, plug.MethodConfigure, plug.ConfigureParams{Options: configureOpts}, &res); err != nil {
			sp.close()
			t.Fatalf("configure: %v", err)
		}
	}

	t.Cleanup(sp.close)
	return sp
}

func (s *spawnedPlugin) close() {
	if p, ok := s.cmd.Stdin.(io.Closer); ok {
		_ = p.Close()
	}
	waited := make(chan error, 1)
	go func() { waited <- s.cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-waited
	}
}

func (s *spawnedPlugin) present(t *testing.T, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res sdk.DNSPresentResult
	err := s.client.Call(ctx, sdk.MethodDNSPresent, in, &res)
	return res, err
}

func (s *spawnedPlugin) cleanup(t *testing.T, id string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res map[string]any
	return s.client.Call(ctx, sdk.MethodDNSCleanup, sdk.DNSCleanupParams{ID: id}, &res)
}

func (s *spawnedPlugin) list(t *testing.T, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res []sdk.DNSRecord
	err := s.client.Call(ctx, sdk.MethodDNSList, in, &res)
	return res, err
}

// configuredOpts returns the standard option map for tests with the
// fake server and zero propagation/timeouts so tests do not spend
// real wall time waiting.
func configuredOpts(envVar, baseURL string) map[string]any {
	return map[string]any{
		"api_token_env":            envVar,
		"zone_id":                  "zone-abc",
		"base_url":                 baseURL,
		"propagation_wait_seconds": 0,
		"request_timeout_seconds":  5,
		"retry_attempts":           2,
	}
}

// setEnv sets envVar to value, registering a cleanup that unsets it.
// Use a unique envVar per test so parallel tests do not race.
func setEnv(t *testing.T, envVar, value string) {
	t.Helper()
	old, hadOld := os.LookupEnv(envVar)
	if err := os.Setenv(envVar, value); err != nil {
		t.Fatalf("setenv %s: %v", envVar, err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(envVar, old)
		} else {
			_ = os.Unsetenv(envVar)
		}
	})
}

// TestPresent_RecordTypes exercises dns.present for each record type
// the plugin supports. The fake recognises POST /zones/<zone>/dns_records
// and returns a record with a synthetic id.
func TestPresent_RecordTypes(t *testing.T) {
	const env = "HEROLD_DNS_CF_TEST_TOKEN_RECORDS"
	setEnv(t, env, "tok-records")

	fake := newFakeCloudflare(t)
	fake.setHandler(func(req capturedRequest) (int, any, []map[string]any) {
		if req.Method != http.MethodPost {
			return http.StatusBadRequest, nil, []map[string]any{{"code": 1, "message": "bad method"}}
		}
		var body map[string]any
		_ = json.Unmarshal(req.Body, &body)
		body["id"] = "rec-" + body["type"].(string)
		return http.StatusOK, body, nil
	})

	p := spawnPlugin(t, configuredOpts(env, fake.server.URL))

	cases := []struct {
		recordType string
		name       string
		value      string
	}{
		{"TXT", "_acme-challenge.example.com", "abc123"},
		{"A", "host.example.com", "192.0.2.1"},
		{"AAAA", "host.example.com", "2001:db8::1"},
		{"MX", "example.com", "10 mail.example.com"},
		{"TLSA", "_25._tcp.mail.example.com", "3 1 1 abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.recordType, func(t *testing.T) {
			fake.reset()
			res, err := p.present(t, sdk.DNSPresentParams{
				Zone:       "example.com",
				RecordType: tc.recordType,
				Name:       tc.name,
				Value:      tc.value,
				TTL:        300,
			})
			if err != nil {
				t.Fatalf("present(%s): %v", tc.recordType, err)
			}
			if res.ID != "rec-"+tc.recordType {
				t.Fatalf("present(%s): id=%q, want rec-%s", tc.recordType, res.ID, tc.recordType)
			}
			reqs := fake.snapshot()
			if len(reqs) != 1 {
				t.Fatalf("present(%s): %d requests, want 1", tc.recordType, len(reqs))
			}
			r := reqs[0]
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if r.Path != "/zones/zone-abc/dns_records" {
				t.Fatalf("path = %s", r.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tok-records" {
				t.Fatalf("Authorization = %q", got)
			}
			var body map[string]any
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["type"] != tc.recordType {
				t.Fatalf("body.type = %v, want %s", body["type"], tc.recordType)
			}
			if body["name"] != tc.name {
				t.Fatalf("body.name = %v, want %s", body["name"], tc.name)
			}
			if body["content"] != tc.value {
				t.Fatalf("body.content = %v, want %s", body["content"], tc.value)
			}
		})
	}
}

// TestCleanup_DeletesRecord asserts cleanup issues a DELETE against the
// records endpoint with the configured zone.
func TestCleanup_DeletesRecord(t *testing.T) {
	const env = "HEROLD_DNS_CF_TEST_TOKEN_CLEANUP"
	setEnv(t, env, "tok-cleanup")

	fake := newFakeCloudflare(t)
	fake.setHandler(func(req capturedRequest) (int, any, []map[string]any) {
		if req.Method == http.MethodDelete {
			return http.StatusOK, map[string]any{"id": "rec-1"}, nil
		}
		return http.StatusBadRequest, nil, []map[string]any{{"code": 1, "message": "unexpected"}}
	})

	p := spawnPlugin(t, configuredOpts(env, fake.server.URL))

	if err := p.cleanup(t, "rec-1"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	reqs := fake.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	if reqs[0].Method != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", reqs[0].Method)
	}
	if reqs[0].Path != "/zones/zone-abc/dns_records/rec-1" {
		t.Fatalf("path = %s", reqs[0].Path)
	}
}

// TestList_EnumeratesRecords pre-populates the fake with two records
// and asserts both come back from dns.list.
func TestList_EnumeratesRecords(t *testing.T) {
	const env = "HEROLD_DNS_CF_TEST_TOKEN_LIST"
	setEnv(t, env, "tok-list")

	fake := newFakeCloudflare(t)
	fake.setHandler(func(req capturedRequest) (int, any, []map[string]any) {
		if req.Method != http.MethodGet {
			return http.StatusBadRequest, nil, nil
		}
		// Cloudflare envelope's `result` is the array of records.
		return http.StatusOK, []map[string]any{
			{"id": "rec-a", "type": "A", "name": "host.example.com", "content": "192.0.2.1", "ttl": 300},
			{"id": "rec-b", "type": "A", "name": "host.example.com", "content": "192.0.2.2", "ttl": 300},
		}, nil
	})

	p := spawnPlugin(t, configuredOpts(env, fake.server.URL))

	recs, err := p.list(t, sdk.DNSListParams{Zone: "example.com", RecordType: "A", Name: "host.example.com"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	got := map[string]string{recs[0].ID: recs[0].Value, recs[1].ID: recs[1].Value}
	if got["rec-a"] != "192.0.2.1" || got["rec-b"] != "192.0.2.2" {
		t.Fatalf("records = %+v", recs)
	}
}

// TestPresent_AuthFailure asserts a 401 response surfaces as an RPC
// error. The plugin must not retry on 4xx.
func TestPresent_AuthFailure(t *testing.T) {
	const env = "HEROLD_DNS_CF_TEST_TOKEN_AUTH"
	setEnv(t, env, "tok-bad")

	fake := newFakeCloudflare(t)
	fake.setHandler(func(req capturedRequest) (int, any, []map[string]any) {
		return http.StatusUnauthorized, nil, []map[string]any{{"code": 10000, "message": "Authentication error"}}
	})

	p := spawnPlugin(t, configuredOpts(env, fake.server.URL))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err type = %T, want *plug.Error: %v", err, err)
	}
	if !strings.Contains(strings.ToLower(rpcErr.Message), "auth") && !strings.Contains(rpcErr.Message, "401") {
		t.Fatalf("msg = %q, want auth error indicator", rpcErr.Message)
	}
	if n := atomic.LoadInt64(&fake.calls); n != 1 {
		t.Fatalf("got %d calls, want 1 (no retry on 4xx)", n)
	}
}

// TestPresent_RetryOn5xx asserts the plugin retries once on 503 and
// then succeeds on the 200.
func TestPresent_RetryOn5xx(t *testing.T) {
	const env = "HEROLD_DNS_CF_TEST_TOKEN_RETRY"
	setEnv(t, env, "tok-retry")

	var step int64
	fake := newFakeCloudflare(t)
	fake.setHandler(func(req capturedRequest) (int, any, []map[string]any) {
		n := atomic.AddInt64(&step, 1)
		if n == 1 {
			return http.StatusServiceUnavailable, nil, []map[string]any{{"code": 1, "message": "boom"}}
		}
		var body map[string]any
		_ = json.Unmarshal(req.Body, &body)
		body["id"] = "rec-after-retry"
		return http.StatusOK, body, nil
	})

	p := spawnPlugin(t, configuredOpts(env, fake.server.URL))

	res, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	})
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if res.ID != "rec-after-retry" {
		t.Fatalf("id = %q", res.ID)
	}
	if n := atomic.LoadInt64(&fake.calls); n != 2 {
		t.Fatalf("got %d calls, want 2 (retry once)", n)
	}
}

// TestPresent_UnknownRecordType asserts an unsupported record type is
// rejected before any provider call.
func TestPresent_UnknownRecordType(t *testing.T) {
	const env = "HEROLD_DNS_CF_TEST_TOKEN_UNKNOWN"
	setEnv(t, env, "tok-unknown")

	fake := newFakeCloudflare(t)
	fake.setHandler(func(req capturedRequest) (int, any, []map[string]any) {
		t.Errorf("unexpected provider call: %s %s", req.Method, req.Path)
		return http.StatusOK, map[string]any{}, nil
	})

	p := spawnPlugin(t, configuredOpts(env, fake.server.URL))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "PTR", Name: "x.example.com", Value: "v", TTL: 60,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err type = %T", err)
	}
	if !strings.Contains(rpcErr.Message, "PTR") && !strings.Contains(rpcErr.Message, "unsupported") {
		t.Fatalf("msg = %q", rpcErr.Message)
	}
	if n := atomic.LoadInt64(&fake.calls); n != 0 {
		t.Fatalf("provider got %d calls, want 0", n)
	}
}

// TestZone_AutoDiscovery asserts that when zone_id is unset the plugin
// looks the zone up via /zones?name=<domain> and uses the returned id
// for subsequent calls.
func TestZone_AutoDiscovery(t *testing.T) {
	const env = "HEROLD_DNS_CF_TEST_TOKEN_AUTODISCO"
	setEnv(t, env, "tok-autodisco")

	fake := newFakeCloudflare(t)
	fake.setHandler(func(req capturedRequest) (int, any, []map[string]any) {
		switch {
		case req.Method == http.MethodGet && req.Path == "/zones":
			if got := req.Query.Get("name"); got != "example.com" {
				t.Errorf("zone lookup name=%q, want example.com", got)
			}
			return http.StatusOK, []map[string]any{
				{"id": "discovered-zone", "name": "example.com"},
			}, nil
		case req.Method == http.MethodPost && req.Path == "/zones/discovered-zone/dns_records":
			var body map[string]any
			_ = json.Unmarshal(req.Body, &body)
			body["id"] = "rec-disco"
			return http.StatusOK, body, nil
		}
		return http.StatusBadRequest, nil, []map[string]any{{"code": 1, "message": "unexpected " + req.Method + " " + req.Path}}
	})

	opts := map[string]any{
		"api_token_env":            env,
		"base_url":                 fake.server.URL,
		"propagation_wait_seconds": 0,
		"request_timeout_seconds":  5,
		"retry_attempts":           1,
	}
	p := spawnPlugin(t, opts)

	res, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	})
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if res.ID != "rec-disco" {
		t.Fatalf("id = %q", res.ID)
	}
	reqs := fake.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2 (zone lookup + create)", len(reqs))
	}
	if reqs[0].Path != "/zones" {
		t.Fatalf("first request path = %s, want /zones", reqs[0].Path)
	}
	if reqs[1].Path != "/zones/discovered-zone/dns_records" {
		t.Fatalf("second request path = %s", reqs[1].Path)
	}
}
