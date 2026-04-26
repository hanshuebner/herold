package sdk_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

type handler struct {
	mu          sync.Mutex
	configured  map[string]any
	healthErr   error
	shutdownHit bool
	customEcho  string
}

func (h *handler) OnConfigure(_ context.Context, opts map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.configured = opts
	return nil
}
func (h *handler) OnHealth(_ context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.healthErr
}
func (h *handler) OnShutdown(_ context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shutdownHit = true
	return nil
}
func (h *handler) HandleCustom(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method != "echo.Ping" {
		return nil, sdk.ErrMethodNotFound
	}
	var p struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.customEcho = p.Msg
	h.mu.Unlock()
	return map[string]any{"msg": p.Msg}, nil
}

func runHarness(t *testing.T, m sdk.Manifest, h sdk.Handler, frames []string) []plug.Response {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	sdk.SetInputReader(inR)
	sdk.SetOutputWriter(outW)
	t.Cleanup(func() {
		sdk.SetInputReader(nil)
		sdk.SetOutputWriter(nil)
	})

	done := make(chan error, 1)
	go func() {
		done <- sdk.Run(m, h)
		_ = outW.Close()
	}()

	// Feed frames.
	go func() {
		for _, f := range frames {
			_, _ = inW.Write([]byte(f))
			if !strings.HasSuffix(f, "\n") {
				_, _ = inW.Write([]byte{'\n'})
			}
		}
		_ = inW.Close()
	}()

	var out []plug.Response
	scanner := bufio.NewScanner(outR)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		// Skip notifications (no id field); only keep responses.
		if _, hasMethod := probe["method"]; hasMethod {
			continue
		}
		var r plug.Response
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		out = append(out, r)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5 seconds")
	}
	return out
}

func TestSDK_Initialize_ReturnsManifest(t *testing.T) {
	m := sdk.Manifest{
		Name: "testplug", Version: "0.1.0",
		Type: plug.TypeEcho, Lifecycle: plug.LifecycleLongRunning,
		ABIVersion: plug.ABIVersion,
	}
	h := &handler{}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"server_version":"test","abi_version":1}}`,
	}
	resps := runHarness(t, m, h, frames)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	var res plug.InitializeResult
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.Manifest.Name != "testplug" {
		t.Fatalf("manifest.Name = %q, want testplug", res.Manifest.Name)
	}
	if res.Manifest.ABIVersion != plug.ABIVersion {
		t.Fatalf("manifest.ABIVersion = %d, want %d", res.Manifest.ABIVersion, plug.ABIVersion)
	}
}

func TestSDK_ConfigureHealthShutdown(t *testing.T) {
	m := sdk.Manifest{Name: "t", Version: "v", Type: plug.TypeEcho, Lifecycle: plug.LifecycleLongRunning, ABIVersion: plug.ABIVersion}
	h := &handler{}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"configure","params":{"options":{"k":"v"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"health"}`,
		`{"jsonrpc":"2.0","id":3,"method":"shutdown","params":{"grace_sec":1}}`,
	}
	resps := runHarness(t, m, h, frames)
	if len(resps) != 3 {
		t.Fatalf("got %d responses, want 3", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("configure error: %v", resps[0].Error)
	}
	h.mu.Lock()
	if h.configured["k"] != "v" {
		t.Fatalf("configure did not reach handler: %+v", h.configured)
	}
	h.mu.Unlock()
	var hr plug.HealthResult
	if err := json.Unmarshal(resps[1].Result, &hr); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !hr.OK {
		t.Fatalf("health reported not OK: %+v", hr)
	}
	h.mu.Lock()
	if !h.shutdownHit {
		t.Fatalf("OnShutdown never called")
	}
	h.mu.Unlock()
}

func TestSDK_CustomMethodDispatch(t *testing.T) {
	m := sdk.Manifest{Name: "t", Version: "v", Type: plug.TypeEcho, Lifecycle: plug.LifecycleLongRunning, ABIVersion: plug.ABIVersion}
	h := &handler{}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"echo.Ping","params":{"msg":"hello"}}`,
	}
	resps := runHarness(t, m, h, frames)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("echo.Ping error: %v", resps[0].Error)
	}
	var m2 map[string]string
	if err := json.Unmarshal(resps[0].Result, &m2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m2["msg"] != "hello" {
		t.Fatalf("got %q, want hello", m2["msg"])
	}
}

func TestSDK_UnknownMethodErrors(t *testing.T) {
	m := sdk.Manifest{Name: "t", Version: "v", Type: plug.TypeEcho, Lifecycle: plug.LifecycleLongRunning, ABIVersion: plug.ABIVersion}
	h := &handler{}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"bogus"}`,
	}
	resps := runHarness(t, m, h, frames)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].Error == nil || resps[0].Error.Code != plug.ErrCodeMethodNotFound {
		t.Fatalf("expected method-not-found, got %+v", resps[0].Error)
	}
}

// TestSDK_NotificationsWriteToStdout pins that Logf/Metric/Notify produce
// newline-delimited JSON notifications on the outbound stream.
func TestSDK_NotificationsWriteToStdout(t *testing.T) {
	var buf bytes.Buffer
	sdk.SetOutputWriter(&buf)
	t.Cleanup(func() { sdk.SetOutputWriter(nil) })
	sdk.Logf("info", "hello %d", 42)
	sdk.Metric("herold_test_total", map[string]string{"k": "v"}, 1)
	sdk.Notify("smoke", map[string]any{"x": 1})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), buf.String())
	}
	for i, want := range []string{"log", "metric", "notify"} {
		var r plug.Request
		if err := json.Unmarshal([]byte(lines[i]), &r); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if r.Method != want {
			t.Fatalf("line %d: method=%q, want %q", i, r.Method, want)
		}
	}
}

// directoryHandler is a minimum-viable Directory + ResolveRcpt handler
// for the round-trip test below. The Lookup / Authenticate /
// ListAliases calls are no-ops; the test exercises ResolveRcpt only.
type directoryHandler struct {
	handler
	captured sdk.ResolveRcptRequest
	resp     sdk.ResolveRcptResponse
}

func (d *directoryHandler) DirectoryLookup(_ context.Context, _ sdk.DirectoryLookupParams) (sdk.DirectoryLookupResult, error) {
	return sdk.DirectoryLookupResult{}, nil
}
func (d *directoryHandler) DirectoryAuthenticate(_ context.Context, _ sdk.DirectoryAuthenticateParams) (sdk.DirectoryAuthenticateResult, error) {
	return sdk.DirectoryAuthenticateResult{}, nil
}
func (d *directoryHandler) DirectoryListAliases(_ context.Context, _ sdk.DirectoryListAliasesParams) ([]string, error) {
	return nil, nil
}
func (d *directoryHandler) ResolveRcpt(_ context.Context, in sdk.ResolveRcptRequest) (sdk.ResolveRcptResponse, error) {
	d.captured = in
	return d.resp, nil
}

func TestSDK_ResolveRcpt_RoundTrip(t *testing.T) {
	m := sdk.Manifest{
		Name: "dirplug", Version: "v",
		Type:       plug.TypeDirectory,
		Lifecycle:  plug.LifecycleLongRunning,
		ABIVersion: plug.ABIVersion,
		Supports:   []string{sdk.SupportsResolveRcpt},
	}
	pid := uint64(7)
	h := &directoryHandler{resp: sdk.ResolveRcptResponse{
		Action:      "accept",
		PrincipalID: &pid,
		RouteTag:    "ticket:7",
	}}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"directory.resolve_rcpt","params":{"recipient":"reply+7@app.example.com","envelope":{"mail_from":"a@b","source_ip":"1.1.1.1","listener":"inbound"},"context":{"plugin_name":"dirplug","request_id":"r1"}}}`,
	}
	resps := runHarness(t, m, h, frames)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("rpc error: %v", resps[0].Error)
	}
	var got sdk.ResolveRcptResponse
	if err := json.Unmarshal(resps[0].Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Action != "accept" || got.RouteTag != "ticket:7" || got.PrincipalID == nil || *got.PrincipalID != pid {
		t.Fatalf("response shape mismatch: %+v", got)
	}
	if h.captured.Recipient != "reply+7@app.example.com" {
		t.Fatalf("plugin did not see recipient: %+v", h.captured)
	}
	if h.captured.Envelope.SourceIP != "1.1.1.1" {
		t.Fatalf("plugin did not see source_ip: %+v", h.captured.Envelope)
	}
}

// TestSDK_ResolveRcpt_HandlerMissing verifies the SDK rejects a
// resolve_rcpt request when the plugin does not implement
// ResolveRcptHandler — even though it's a directory plugin.
func TestSDK_ResolveRcpt_HandlerMissing(t *testing.T) {
	m := sdk.Manifest{
		Name: "dirplug-no-resolve", Version: "v",
		Type: plug.TypeDirectory, Lifecycle: plug.LifecycleLongRunning, ABIVersion: plug.ABIVersion,
	}
	// directoryNoResolve only implements DirectoryHandler.
	h := &directoryNoResolveHandler{}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"directory.resolve_rcpt","params":{"recipient":"x@y","envelope":{"source_ip":"1.1.1.1","listener":"inbound"}}}`,
	}
	resps := runHarness(t, m, h, frames)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].Error == nil {
		t.Fatalf("expected method-not-found error, got result %s", string(resps[0].Result))
	}
	if resps[0].Error.Code != plug.ErrCodeMethodNotFound {
		t.Fatalf("err code: %d (want %d)", resps[0].Error.Code, plug.ErrCodeMethodNotFound)
	}
}

type directoryNoResolveHandler struct{ handler }

func (d *directoryNoResolveHandler) DirectoryLookup(_ context.Context, _ sdk.DirectoryLookupParams) (sdk.DirectoryLookupResult, error) {
	return sdk.DirectoryLookupResult{}, nil
}
func (d *directoryNoResolveHandler) DirectoryAuthenticate(_ context.Context, _ sdk.DirectoryAuthenticateParams) (sdk.DirectoryAuthenticateResult, error) {
	return sdk.DirectoryAuthenticateResult{}, nil
}
func (d *directoryNoResolveHandler) DirectoryListAliases(_ context.Context, _ sdk.DirectoryListAliasesParams) ([]string, error) {
	return nil, nil
}
