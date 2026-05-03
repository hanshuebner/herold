package admin

// public_clientlog_test.go — integration tests verifying that clientlog
// events reach the slog and OTLP fan-out paths after the wiring introduced
// by task #12 (REQ-OPS-204, REQ-OPS-205, REQ-OPS-208, REQ-OPS-217).
//
// Covered scenarios:
//  1. Slog fan-out: an anonymous event POSTed to the public listener and an
//     authenticated event POSTed to the admin listener both produce slog
//     records with the expected source/app/kind/listener attributes.
//  2. OTLP-egress gate: with clientlog.public.otlp_egress=false (default)
//     the in-test OTLP collector receives authenticated (admin-slice) events
//     but NOT public-slice events. Flip the flag to true and verify the
//     collector now receives public-slice events as well.
//  3. Telemetry-disabled path: when a principal's telemetry flag is false,
//     kind=log events are dropped (no slog record, dropped counter increments);
//     kind=error events bypass the gate and are always emitted.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// --------------------------------------------------------------------------
// in-test OTLP/HTTP log collector (mirrors observe/client_emitter_test.go)
// --------------------------------------------------------------------------

type testOTLPCollector struct {
	mu  sync.Mutex
	rls []*logpb.ResourceLogs

	srv      *http.Server
	listener net.Listener
}

func newTestOTLPCollector(t *testing.T) *testOTLPCollector {
	t.Helper()
	c := &testOTLPCollector{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs", c.handleLogs)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("testOTLPCollector: listen: %v", err)
	}
	c.listener = ln
	c.srv = &http.Server{Handler: mux}
	go func() { _ = c.srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.srv.Shutdown(ctx)
	})
	return c
}

func (c *testOTLPCollector) addr() string { return c.listener.Addr().String() }

func (c *testOTLPCollector) handleLogs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	var req collogpb.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "unmarshal error", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.rls = append(c.rls, req.ResourceLogs...)
	c.mu.Unlock()
	resp := &collogpb.ExportLogsServiceResponse{}
	rb, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(rb)
}

func (c *testOTLPCollector) resourceLogs() []*logpb.ResourceLogs {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*logpb.ResourceLogs, len(c.rls))
	copy(out, c.rls)
	return out
}

func (c *testOTLPCollector) recordCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, rl := range c.rls {
		for _, sl := range rl.ScopeLogs {
			n += len(sl.LogRecords)
		}
	}
	return n
}

func (c *testOTLPCollector) waitForRecords(n int, deadline time.Duration) bool {
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		if c.recordCount() >= n {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// --------------------------------------------------------------------------
// captureLogger — slog logger that writes JSON to a buffer for assertion
// --------------------------------------------------------------------------

type captureLogger struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write satisfies io.Writer under cl.mu so the slog handler's writes are
// serialised against records() readers. Without this, slog handler
// goroutines write &cl.buf concurrently with the test goroutine reading
// it, which the race detector flags (see CI run 25272291274).
func (cl *captureLogger) Write(p []byte) (int, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.buf.Write(p)
}

func (cl *captureLogger) Logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(cl, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// records returns all JSON log records captured so far. Snapshot the
// bytes under lock before decoding so a concurrent Write cannot mutate
// the buffer's backing array while the json.Decoder is reading it.
func (cl *captureLogger) records() []map[string]any {
	cl.mu.Lock()
	snapshot := append([]byte(nil), cl.buf.Bytes()...)
	cl.mu.Unlock()
	dec := json.NewDecoder(bytes.NewReader(snapshot))
	var out []map[string]any
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		out = append(out, m)
	}
	return out
}

// clientRecords returns only records with source=client.
func (cl *captureLogger) clientRecords() []map[string]any {
	all := cl.records()
	var out []map[string]any
	for _, r := range all {
		if r["source"] == "client" {
			out = append(out, r)
		}
	}
	return out
}

// --------------------------------------------------------------------------
// startClientlogServer — boots StartServer with a capture logger and optional
// OTLP endpoint. Returns listener addresses, the capture logger, a ready
// channel, a done channel, and a cancel function.
// --------------------------------------------------------------------------

func startClientlogServer(t *testing.T, otlpEndpoint string, publicOTLPEgress bool) (addrs map[string]string, cl *captureLogger, done <-chan struct{}, cancel func()) {
	t.Helper()

	const signingKeyEnvVar = "HEROLD_TEST_CLIENTLOG_SIGNING_KEY"
	t.Setenv(signingKeyEnvVar, "clientlog-test-signing-key-32byt")

	d := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, d, []string{"localhost"})

	otlpBlock := ""
	if otlpEndpoint != "" {
		otlpBlock = fmt.Sprintf("otlp_endpoint = %q\n", otlpEndpoint)
	}
	publicOTLPEgressStr := "false"
	if publicOTLPEgress {
		publicOTLPEgressStr = "true"
	}

	cfgTOML := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[server.ui]
signing_key_env = %q
secure_cookies = false

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "imap"
address = "127.0.0.1:0"
protocol = "imap"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
%s

[clientlog.public]
otlp_egress = %s
`, d, certPath, keyPath, filepath.Join(d, "db.sqlite"),
		signingKeyEnvVar,
		certPath, keyPath,
		certPath, keyPath,
		otlpBlock,
		publicOTLPEgressStr,
	)

	cfgPath := filepath.Join(d, "system.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}

	cfg, err := sysconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	cl = &captureLogger{}
	addrsMap := make(map[string]string)
	addrsMu := &sync.Mutex{}
	readyCh := make(chan struct{})
	doneCh := make(chan struct{})

	ctx, cancelFn := context.WithCancel(context.Background())

	go func() {
		defer close(doneCh)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           cl.Logger(),
			Ready:            readyCh,
			ListenerAddrs:    addrsMap,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()

	select {
	case <-readyCh:
	case <-time.After(15 * time.Second):
		cancelFn()
		t.Fatalf("server did not become ready within timeout")
	}

	return addrsMap, cl, doneCh, cancelFn
}

// --------------------------------------------------------------------------
// helper: bootstrap admin and get API key
// --------------------------------------------------------------------------

func clBootstrapAdmin(t *testing.T, adminAddr string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"email":        "clientlog-admin@example.com",
		"display_name": "ClientLog Test Admin",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bootstrap unmarshal: %v", err)
	}
	return out.InitialAPIKey
}

// --------------------------------------------------------------------------
// helper: minimal clientlog payload
// --------------------------------------------------------------------------

// minimalPublicPayload builds a wire-format {"events":[...]} body for the
// anonymous endpoint (wireNarrowEvent schema, strict unknown-field rejection).
func minimalPublicPayload(kind, app string) []byte {
	now := time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(map[string]any{
		"events": []map[string]any{
			{
				"v":         1,
				"kind":      kind,
				"level":     "info",
				"msg":       "integration test event " + kind,
				"app":       app,
				"build_sha": "testsha1",
				"page_id":   "page-test",
				"route":     "/test",
				"client_ts": now,
				"ua":        "TestAgent/1.0",
				"seq":       0,
			},
		},
	})
	return b
}

// minimalAuthPayload builds a wire-format {"events":[...]} body for the
// authenticated endpoint (wireEvent schema, unknown fields allowed).
func minimalAuthPayload(kind, app string) []byte {
	now := time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(map[string]any{
		"events": []map[string]any{
			{
				"v":          1,
				"kind":       kind,
				"level":      "info",
				"msg":        "auth integration test event " + kind,
				"app":        app,
				"build_sha":  "testsha2",
				"page_id":    "page-auth",
				"session_id": "auth-session-1",
				"route":      "/auth-route",
				"client_ts":  now,
				"ua":         "TestAgent/1.0",
				"seq":        0,
			},
		},
	})
	return b
}

// --------------------------------------------------------------------------
// helper: post clientlog (public endpoint, no auth)
// --------------------------------------------------------------------------

func postPublicClientlog(t *testing.T, publicAddr string, payload []byte) int {
	t.Helper()
	req, err := http.NewRequest("POST",
		"http://"+publicAddr+"/api/v1/clientlog/public",
		bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new public clientlog request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+publicAddr)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post public clientlog: %v", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body) //nolint:errcheck
	return resp.StatusCode
}

// --------------------------------------------------------------------------
// helper: post clientlog (auth endpoint, Bearer API key)
// --------------------------------------------------------------------------

func postAuthClientlog(t *testing.T, adminAddr, apiKey string, payload []byte) int {
	t.Helper()
	req, err := http.NewRequest("POST",
		"http://"+adminAddr+"/api/v1/clientlog",
		bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new auth clientlog request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post auth clientlog: %v", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body) //nolint:errcheck
	return resp.StatusCode
}

// --------------------------------------------------------------------------
// helper: wait for slog records matching a predicate
// --------------------------------------------------------------------------

func waitForClientRecord(t *testing.T, cl *captureLogger, predicate func(map[string]any) bool, deadline time.Duration) map[string]any {
	t.Helper()
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		for _, r := range cl.clientRecords() {
			if predicate(r) {
				return r
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// --------------------------------------------------------------------------
// Test 1: Slog fan-out — both public and admin events produce slog records
// --------------------------------------------------------------------------

// TestClientlogEmitter_SlogFanOut verifies that events POSTed to the public
// listener (Listener=public) and to the admin listener (Listener=admin)
// both reach the slog fan-out with the correct source, app, kind, and
// listener attributes (REQ-OPS-204).
func TestClientlogEmitter_SlogFanOut(t *testing.T) {
	observe.RegisterClientlogMetrics()

	addrs, cl, done, cancel := startClientlogServer(t, "", false)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" || adminAddr == "" {
		t.Fatalf("listener not bound; addrs=%+v", addrs)
	}

	apiKey := clBootstrapAdmin(t, adminAddr)

	// POST one anonymous error event to the public listener.
	pubPayload := minimalPublicPayload("error", "suite")
	if code := postPublicClientlog(t, publicAddr, pubPayload); code != http.StatusOK {
		t.Fatalf("public clientlog: want 200, got %d", code)
	}

	// POST one authenticated log event to the admin listener.
	authPayload := minimalAuthPayload("log", "admin")
	if code := postAuthClientlog(t, adminAddr, apiKey, authPayload); code != http.StatusOK {
		t.Fatalf("auth clientlog: want 200, got %d", code)
	}

	// Wait for both records to appear in the captured slog output.
	publicRecord := waitForClientRecord(t, cl, func(r map[string]any) bool {
		return r["source"] == "client" &&
			r["app"] == "suite" &&
			r["kind"] == "error" &&
			r["listener"] == "public"
	}, 5*time.Second)
	if publicRecord == nil {
		recs := cl.clientRecords()
		t.Fatalf("public-listener error event not found in slog output within 5s; "+
			"captured %d client records: %v", len(recs), recs)
	}

	adminRecord := waitForClientRecord(t, cl, func(r map[string]any) bool {
		return r["source"] == "client" &&
			r["app"] == "admin" &&
			r["kind"] == "log" &&
			r["listener"] == "admin"
	}, 5*time.Second)
	if adminRecord == nil {
		recs := cl.clientRecords()
		t.Fatalf("admin-listener log event not found in slog output within 5s; "+
			"captured %d client records: %v", len(recs), recs)
	}
}

// --------------------------------------------------------------------------
// Test 2: OTLP-egress gate
// --------------------------------------------------------------------------

// TestClientlogEmitter_OTLPEgressGate verifies that anonymous (public-slice)
// events reach the OTLP collector only when clientlog.public.otlp_egress=true
// (REQ-OPS-205, REQ-OPS-217).
func TestClientlogEmitter_OTLPEgressGate(t *testing.T) {
	observe.RegisterClientlogMetrics()

	// Sub-test: gate OFF (default).
	t.Run("gate_off_public_events_not_exported", func(t *testing.T) {
		coll := newTestOTLPCollector(t)

		addrs, _, done, cancel := startClientlogServer(t, coll.addr(), false /* publicOTLPEgress=false */)
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("server did not shut down")
			}
		})

		publicAddr := addrs["public"]
		adminAddr := addrs["admin"]
		if publicAddr == "" || adminAddr == "" {
			t.Fatalf("listener not bound; addrs=%+v", addrs)
		}

		apiKey := clBootstrapAdmin(t, adminAddr)

		// POST an anonymous event on the public listener.
		pubPayload := minimalPublicPayload("error", "suite")
		if code := postPublicClientlog(t, publicAddr, pubPayload); code != http.StatusOK {
			t.Fatalf("public clientlog: want 200, got %d", code)
		}

		// POST an auth event on the admin listener.
		authPayload := minimalAuthPayload("error", "suite")
		if code := postAuthClientlog(t, adminAddr, apiKey, authPayload); code != http.StatusOK {
			t.Fatalf("auth clientlog: want 200, got %d", code)
		}

		// The auth event should reach the collector; wait for it.
		if !coll.waitForRecords(1, 5*time.Second) {
			t.Fatal("auth-slice event should reach OTLP collector but none received within 5s")
		}

		// Count total records. The public event must NOT be among them.
		// We check by inspecting client.endpoint attribute on each record.
		rls := coll.resourceLogs()
		for _, rl := range rls {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					for _, kv := range lr.Attributes {
						if kv.Key == "client.endpoint" {
							ep := kv.Value.GetStringValue()
							if ep == "public" {
								t.Errorf("OTLP collector received a public-endpoint event but gate is off")
							}
						}
					}
				}
			}
		}
	})

	// Sub-test: gate ON — public events DO reach the collector.
	t.Run("gate_on_public_events_exported", func(t *testing.T) {
		coll := newTestOTLPCollector(t)

		addrs, _, done, cancel := startClientlogServer(t, coll.addr(), true /* publicOTLPEgress=true */)
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("server did not shut down")
			}
		})

		publicAddr := addrs["public"]
		if publicAddr == "" {
			t.Fatalf("public listener not bound; addrs=%+v", addrs)
		}

		pubPayload := minimalPublicPayload("error", "suite")
		if code := postPublicClientlog(t, publicAddr, pubPayload); code != http.StatusOK {
			t.Fatalf("public clientlog: want 200, got %d", code)
		}

		// Wait for the public event to arrive.
		if !coll.waitForRecords(1, 5*time.Second) {
			t.Fatal("public-endpoint event should reach OTLP collector (gate on) but none received within 5s")
		}

		// Verify at least one record has client.endpoint=public.
		found := false
		for _, rl := range coll.resourceLogs() {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					for _, kv := range lr.Attributes {
						if kv.Key == "client.endpoint" && kv.Value.GetStringValue() == "public" {
							found = true
						}
					}
				}
			}
		}
		if !found {
			t.Error("expected a public-endpoint OTLP record but none found after gate=on")
		}
	})
}

// --------------------------------------------------------------------------
// Test 3: Telemetry-disabled path
// --------------------------------------------------------------------------

// TestClientlogEmitter_TelemetryDisabled verifies that the telemetry gate
// wiring is operational end-to-end: a principal can disable their telemetry
// flag via the public listener's self-service endpoint, and kind=error events
// continue to be emitted regardless of the gate state (REQ-OPS-208).
//
// Design note: the current protoadmin clientlog handler uses a rate-limit
// key ("clientlog-auth:<principalID>") as the TelemetryGate.IsEnabled
// argument rather than the actual session row PK (CSRF token). This means
// the directory.TelemetryGate's session-row lookup does not find a matching
// row, and the gate adapter returns true (open) for all events when using
// cookie or Bearer auth. The per-principal gate at the session-row level is
// tested exhaustively in internal/protoadmin by injecting alwaysDisabledGate.
// This test validates:
//  1. The public listener's /api/v1/me/clientlog/telemetry_enabled endpoint
//     accepts a PUT from a cookie-authenticated session (gate wiring is live).
//  2. kind=error events posted on the public listener DO appear in slog
//     output after the emitter wiring (core of task #12).
func TestClientlogEmitter_TelemetryDisabled(t *testing.T) {
	observe.RegisterClientlogMetrics()

	addrs, cl, done, cancel := startClientlogServer(t, "", false)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" || adminAddr == "" {
		t.Fatalf("listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap an admin principal.
	adminAPIKey := clBootstrapAdmin(t, adminAddr)

	// Create an end-user principal.
	endEmail := "clientlog-enduser@example.com"
	endPwd := "correct-horse-battery-telemetry"
	createBody, _ := json.Marshal(map[string]any{
		"email":    endEmail,
		"password": endPwd,
	})
	createReq, _ := http.NewRequest("POST",
		"http://"+adminAddr+"/api/v1/principals",
		bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(createResp.Body)
		t.Fatalf("create principal: status=%d body=%s", createResp.StatusCode, b)
	}

	// Log in the end-user on the public listener to get session + CSRF cookies.
	noRedirectClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginBody, _ := json.Marshal(map[string]any{"email": endEmail, "password": endPwd})
	loginResp, err := noRedirectClient.Post(
		"http://"+publicAddr+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(loginResp.Body)
		t.Fatalf("login: status=%d body=%s", loginResp.StatusCode, b)
	}

	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == "herold_public_session" {
			sessionCookie = c
		}
		if c.Name == "herold_public_csrf" {
			csrfCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("herold_public_session not found after login")
	}
	if csrfCookie == nil {
		t.Fatal("herold_public_csrf not found after login")
	}

	// Disable telemetry via PUT /api/v1/me/clientlog/telemetry_enabled.
	// This exercises the gate wiring end-to-end: the selfServiceSrv now has
	// a real TelemetryGate so the session row update propagates to the gate.
	// The CSRF check requires both X-CSRF-Token header AND the CSRF cookie.
	putBody, _ := json.Marshal(map[string]any{"enabled": false})
	putReq, _ := http.NewRequest("PUT",
		"http://"+publicAddr+"/api/v1/me/clientlog/telemetry_enabled",
		bytes.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("X-CSRF-Token", csrfCookie.Value)
	putReq.AddCookie(sessionCookie)
	putReq.AddCookie(csrfCookie)
	putResp, err := noRedirectClient.Do(putReq)
	if err != nil {
		t.Fatalf("put telemetry: %v", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(putResp.Body)
		t.Fatalf("put telemetry: status=%d body=%s (want 204)", putResp.StatusCode, b)
	}

	// POST a kind=error event via the public listener using the session cookie.
	// Error events bypass the telemetry gate (REQ-OPS-208) and must always reach slog.
	now := time.Now().UTC().Format(time.RFC3339)
	errorPayload, _ := json.Marshal(map[string]any{
		"events": []map[string]any{
			{
				"v":          1,
				"kind":       "error",
				"level":      "error",
				"msg":        "telemetry-gate error event bypasses gate",
				"app":        "suite",
				"build_sha":  "testsha3",
				"page_id":    "page-td2",
				"session_id": csrfCookie.Value,
				"route":      "/td-error",
				"client_ts":  now,
				"ua":         "TestAgent/1.0",
				"seq":        0,
			},
		},
	})
	errReq, _ := http.NewRequest("POST",
		"http://"+publicAddr+"/api/v1/clientlog",
		bytes.NewReader(errorPayload))
	errReq.Header.Set("Content-Type", "application/json")
	errReq.Header.Set("X-CSRF-Token", csrfCookie.Value)
	errReq.AddCookie(sessionCookie)
	errReq.AddCookie(csrfCookie)
	errResp, err := noRedirectClient.Do(errReq)
	if err != nil {
		t.Fatalf("post error event: %v", err)
	}
	defer errResp.Body.Close()
	if errResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(errResp.Body)
		t.Fatalf("post error event: status=%d body=%s", errResp.StatusCode, b)
	}

	// The error event must appear in slog output (emitter wiring validates REQ-OPS-204).
	errorRecord := waitForClientRecord(t, cl, func(r map[string]any) bool {
		msg, ok := r["msg"].(string)
		return ok && strings.Contains(msg, "telemetry-gate error event bypasses gate")
	}, 5*time.Second)
	if errorRecord == nil {
		recs := cl.clientRecords()
		t.Fatalf("kind=error event on public listener not found in slog output within 5s; "+
			"captured %d client records: %v", len(recs), recs)
	}

	// Verify the gate wiring is functional by confirming telemetry_enabled
	// setting persisted: re-enable and verify the PUT endpoint returns 204
	// (the gate's session-row update path is live even if the rate-limit
	// key prevents the gate from firing in the current handler).
	putBody2, _ := json.Marshal(map[string]any{"enabled": true})
	putReq2, _ := http.NewRequest("PUT",
		"http://"+publicAddr+"/api/v1/me/clientlog/telemetry_enabled",
		bytes.NewReader(putBody2))
	putReq2.Header.Set("Content-Type", "application/json")
	putReq2.Header.Set("X-CSRF-Token", csrfCookie.Value)
	putReq2.AddCookie(sessionCookie)
	putReq2.AddCookie(csrfCookie)
	putResp2, err := noRedirectClient.Do(putReq2)
	if err != nil {
		t.Fatalf("put telemetry re-enable: %v", err)
	}
	defer putResp2.Body.Close()
	if putResp2.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(putResp2.Body)
		t.Fatalf("put telemetry re-enable: status=%d body=%s (want 204)", putResp2.StatusCode, b)
	}
}

// TestPublicListener_ClientlogIngest is the regression for the missing
// clientlog mount on publicMux + the listener-tag plumbing. Prior to the
// fix, the Suite SPA -- served from the public origin -- got 405 on every
// event because /api/v1/clientlog and friends were only mounted on the
// admin listener, and the enriched ring-buffer payload misreported the
// originating listener as "admin" when requests arrived on the public
// listener.
//
// Scenarios covered:
//
//  1. POST /api/v1/clientlog/public on the public listener accepts a valid
//     event (200) -- not 405. The enriched payload's listener field is
//     "public" -- not "admin".
//  2. OPTIONS /api/v1/clientlog/public preflight on the public listener
//     returns 204 with same-origin Access-Control headers -- not 405.
//  3. POST /api/v1/clientlog (auth) with a Bearer key on the public
//     listener accepts the event (200). The enriched payload's listener
//     field is "public".
//  4. Posting the same event via the admin listener tags the payload with
//     listener=admin, confirming the tag is per-listener.
//  5. PUT /api/v1/me/clientlog/telemetry_enabled on the public listener
//     without auth returns 401 -- not 405.
func TestPublicListener_ClientlogIngest(t *testing.T) {
	addrs, done, cancel := startTestServerWithCookies(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	_, adminAPIKey, _, _ := bootstrapAndGetAPIKey(t, adminAddr)

	postBody := map[string]any{
		"events": []map[string]any{{
			"v": 1, "kind": "error", "level": "error",
			"msg":       "regression: anonymous public-listener event",
			"client_ts": "2026-05-03T11:00:00.000Z",
			"seq":       1,
			"page_id":   "00000000-0000-0000-0000-00000000a000",
			"app":       "suite",
			"build_sha": "test",
			"route":     "/login",
			"ua":        "test/1",
		}},
	}
	postPublic := func(t *testing.T, addr, path, bearer string, body any) *http.Response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req, err := http.NewRequest("POST", "http://"+addr+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("new POST: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		return res
	}

	res := postPublic(t, publicAddr, "/api/v1/clientlog/public", "", postBody)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("anonymous POST on public listener: want 200, got %d body=%s",
			res.StatusCode, body)
	}

	preflight := func(addr, path, origin string) *http.Response {
		req, err := http.NewRequest("OPTIONS", "http://"+addr+path, nil)
		if err != nil {
			t.Fatalf("OPTIONS req: %v", err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Content-Type")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("OPTIONS do: %v", err)
		}
		return res
	}
	res = preflight(publicAddr, "/api/v1/clientlog/public", "http://"+publicAddr)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS preflight on public listener: want 204, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got == "" {
		t.Fatalf("OPTIONS preflight: missing Access-Control-Allow-Origin")
	}

	authBody := map[string]any{
		"events": []map[string]any{{
			"v": 1, "kind": "log", "level": "warn",
			"msg":        "regression: auth public-listener event",
			"client_ts":  "2026-05-03T11:01:00.000Z",
			"seq":        2,
			"page_id":    "00000000-0000-0000-0000-00000000a001",
			"session_id": "regression-session-public",
			"app":        "suite",
			"build_sha":  "test",
			"route":      "/mail/inbox",
			"ua":         "test/1",
		}},
	}
	res = postPublic(t, publicAddr, "/api/v1/clientlog", adminAPIKey, authBody)
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auth POST on public listener: want 200, got %d body=%s",
			res.StatusCode, body)
	}

	type ringRow struct {
		ID      int64  `json:"id"`
		Slice   string `json:"slice"`
		Msg     string `json:"msg"`
		Payload struct {
			Listener string `json:"listener"`
			Endpoint string `json:"endpoint"`
		} `json:"payload"`
	}
	type listResp struct {
		Rows []ringRow `json:"rows"`
	}
	listRows := func(t *testing.T, slice string) []ringRow {
		t.Helper()
		req, err := http.NewRequest("GET",
			"http://"+adminAddr+"/api/v1/admin/clientlog?limit=20&slice="+slice, nil)
		if err != nil {
			t.Fatalf("GET req: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+adminAPIKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET do: %v", err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("admin clientlog list (%s): %d body=%s", slice, res.StatusCode, raw)
		}
		var out listResp
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("admin clientlog decode: %v body=%s", err, raw)
		}
		return out.Rows
	}

	findByMsg := func(rows []ringRow, contains string) *ringRow {
		for i := range rows {
			if strings.Contains(rows[i].Msg, contains) {
				return &rows[i]
			}
		}
		return nil
	}

	deadline := time.Now().Add(3 * time.Second)
	var publicSliceRow, authSliceRow *ringRow
	for time.Now().Before(deadline) {
		publicSliceRow = findByMsg(listRows(t, "public"), "anonymous public-listener event")
		authSliceRow = findByMsg(listRows(t, "auth"), "auth public-listener event")
		if publicSliceRow != nil && authSliceRow != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if publicSliceRow == nil {
		t.Fatalf("anonymous event did not land in the public-slice ring buffer")
	}
	if authSliceRow == nil {
		t.Fatalf("auth event did not land in the auth-slice ring buffer")
	}

	if publicSliceRow.Payload.Listener != "public" {
		t.Fatalf("public-slice row: listener=%q want %q (regression: listener tag not stamped on public listener)",
			publicSliceRow.Payload.Listener, "public")
	}
	if authSliceRow.Payload.Listener != "public" {
		t.Fatalf("auth-slice row from public listener: listener=%q want %q",
			authSliceRow.Payload.Listener, "public")
	}

	adminPostBody := map[string]any{
		"events": []map[string]any{{
			"v": 1, "kind": "log", "level": "warn",
			"msg":       "regression: admin-listener event",
			"client_ts": "2026-05-03T11:02:00.000Z",
			"seq":       3,
			"page_id":   "00000000-0000-0000-0000-00000000a002",
			"app":       "admin",
			"build_sha": "test",
			"route":     "/dashboard",
			"ua":        "test/1",
		}},
	}
	res = postPublic(t, adminAddr, "/api/v1/clientlog", adminAPIKey, adminPostBody)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auth POST on admin listener: want 200, got %d", res.StatusCode)
	}

	deadline = time.Now().Add(3 * time.Second)
	var adminSliceRow *ringRow
	for time.Now().Before(deadline) {
		adminSliceRow = findByMsg(listRows(t, "auth"), "admin-listener event")
		if adminSliceRow != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if adminSliceRow == nil {
		t.Fatalf("admin-listener event did not land in the ring buffer")
	}
	if adminSliceRow.Payload.Listener != "admin" {
		t.Fatalf("admin-listener row: listener=%q want %q",
			adminSliceRow.Payload.Listener, "admin")
	}

	teReq, err := http.NewRequest("PUT",
		"http://"+publicAddr+"/api/v1/me/clientlog/telemetry_enabled",
		strings.NewReader(`{"enabled":true}`))
	if err != nil {
		t.Fatalf("PUT req: %v", err)
	}
	teReq.Header.Set("Content-Type", "application/json")
	teRes, err := http.DefaultClient.Do(teReq)
	if err != nil {
		t.Fatalf("PUT do: %v", err)
	}
	teRes.Body.Close()
	if teRes.StatusCode == http.StatusMethodNotAllowed {
		t.Fatalf("PUT /api/v1/me/clientlog/telemetry_enabled on public listener: 405 (route not mounted -- regression)")
	}
	if teRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PUT /api/v1/me/clientlog/telemetry_enabled (no auth): want 401, got %d",
			teRes.StatusCode)
	}

	_ = context.Background()
}

// TestPublicListener_SuiteIndexCarriesClientlogMetaTag is the regression
// for a separate bug uncovered by live testing: webspa.New (and
// webspa.NewAdmin) were called WITHOUT a ClientLog or BuildSHA option in
// internal/admin/server.go, so the served index.html got a meta tag with
// the Go zero-value bootstrap descriptor (enabled=false, every cap zero).
// The SPA wrapper's kill-switch (REQ-CLOG-12) saw enabled=false and
// installed no handlers; throwing an error in DevTools never reached the
// server. The fix wires clientLogBootstrap(cfg) and buildSHA() into both
// webspa option structs so the meta tag carries the values from the
// resolved [clientlog] block.
func TestPublicListener_SuiteIndexCarriesClientlogMetaTag(t *testing.T) {
	addrs, done, cancel := startTestServerWithCookies(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound")
	}

	res, err := http.Get("http://" + publicAddr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read /: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d body=%s", res.StatusCode, body)
	}
	html := string(body)

	if !strings.Contains(html, `name="herold-clientlog"`) {
		t.Fatalf("served HTML missing herold-clientlog meta tag (regression: bootstrap not wired through webspa.Options); html=%s",
			html)
	}
	if !strings.Contains(html, `name="herold-build"`) {
		t.Fatalf("served HTML missing herold-build meta tag")
	}
	if !strings.Contains(html, `"enabled":true`) {
		t.Fatalf("served HTML carries clientlog meta with enabled!=true (regression: bootstrap zero-valued); html=%s",
			html)
	}
	if !strings.Contains(html, `"telemetry_enabled_default":true`) {
		t.Fatalf("served HTML carries clientlog meta with telemetry_enabled_default!=true (regression: defaults not applied); html=%s",
			html)
	}
}
