package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/noop"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
)

// --- in-test OTLP/HTTP log collector ---

// otlpLogCollector is a minimal OTLP/HTTP log receiver for tests.
// It records every ResourceLogs batch received on POST /v1/logs.
type otlpLogCollector struct {
	mu  sync.Mutex
	rls []*logpb.ResourceLogs

	srv      *http.Server
	listener net.Listener
}

// newOTLPLogCollector starts an OTLP/HTTP log collector on a random port.
func newOTLPLogCollector(t *testing.T) *otlpLogCollector {
	t.Helper()
	c := &otlpLogCollector{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs", c.handleLogs)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("otlpLogCollector: listen: %v", err)
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

// addr returns the host:port the collector is listening on.
func (c *otlpLogCollector) addr() string {
	return c.listener.Addr().String()
}

func (c *otlpLogCollector) handleLogs(w http.ResponseWriter, r *http.Request) {
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

// resourceLogs returns all collected ResourceLogs slices (copy).
func (c *otlpLogCollector) resourceLogs() []*logpb.ResourceLogs {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*logpb.ResourceLogs, len(c.rls))
	copy(out, c.rls)
	return out
}

// waitForRecords blocks until at least n ResourceLogs entries have been
// received or the deadline elapses. Returns true if n entries arrived.
func (c *otlpLogCollector) waitForRecords(n int, deadline time.Duration) bool {
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		c.mu.Lock()
		got := len(c.rls)
		c.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// --- helpers ---

func makeTestEvent(kind, level string, authenticated bool) ClientEvent {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	return ClientEvent{
		Kind:          kind,
		Level:         level,
		Msg:           "test message",
		ClientTS:      now,
		ServerRecvTS:  now.Add(50 * time.Millisecond),
		ClockSkewMS:   50,
		PageID:        "page-abc",
		SessionID:     "sess-xyz",
		App:           "suite",
		BuildSHA:      "deadbeef",
		Route:         "/mail/inbox",
		RequestID:     "req-123",
		UA:            "TestBrowser/1.0",
		UserID:        "user-42",
		Listener:      "public",
		Endpoint:      "auth",
		Authenticated: authenticated,
	}
}

// captureLogRecords runs fn with a slog.Logger that captures all emitted
// records as JSON. Returns the decoded JSON objects.
func captureLogRecords(t *testing.T, fn func(log *slog.Logger)) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: LevelTrace}))
	fn(l)
	var records []map[string]any
	dec := json.NewDecoder(&buf)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		records = append(records, m)
	}
	return records
}

// --- tests ---

// TestClientEmitter_SlogFields verifies that Emit writes the required slog
// attributes (REQ-OPS-204).
func TestClientEmitter_SlogFields(t *testing.T) {
	ev := makeTestEvent("log", "info", true)

	records := captureLogRecords(t, func(l *slog.Logger) {
		e := NewClientEmitter(ClientEmitterConfig{
			Logger:      l,
			LogProvider: noopLogProvider(),
		})
		e.Emit(context.Background(), ev)
	})

	if len(records) != 1 {
		t.Fatalf("expected 1 slog record, got %d", len(records))
	}
	r := records[0]

	assertField := func(key, want string) {
		t.Helper()
		got, ok := r[key]
		if !ok {
			t.Errorf("slog record missing key %q", key)
			return
		}
		if got.(string) != want {
			t.Errorf("slog record %q: got %q, want %q", key, got, want)
		}
	}

	assertField("source", "client")
	assertField("app", "suite")
	assertField("kind", "log")
	assertField("route", "/mail/inbox")
	assertField("build", "deadbeef")
	assertField("activity", ActivityUser) // authenticated kind=log -> user
	assertField("client_session", "sess-xyz")
	assertField("request_id", "req-123")

	if _, ok := r["client_ts"]; !ok {
		t.Error("slog record missing client_ts")
	}
	// level
	if r["level"] != "INFO" {
		t.Errorf("expected level=INFO, got %v", r["level"])
	}
}

// TestClientEmitter_SeverityMapping exercises the full level mapping table.
func TestClientEmitter_SeverityMapping(t *testing.T) {
	cases := []struct {
		kind      string
		level     string
		wantLevel string
	}{
		{"log", "trace", "DEBUG-4"}, // slog renders LevelTrace (-8) as DEBUG-4 (offset from slog.LevelDebug=-4)
		{"log", "debug", "DEBUG"},
		{"log", "info", "INFO"},
		{"log", "warn", "WARN"},
		{"log", "error", "ERROR"},
		// kind=error forces ERROR regardless of level
		{"error", "debug", "ERROR"},
		{"error", "info", "ERROR"},
		{"error", "trace", "ERROR"},
		// kind=vital -> internal activity, level as given
		{"vital", "info", "INFO"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.kind+"/"+tc.level, func(t *testing.T) {
			ev := makeTestEvent(tc.kind, tc.level, true)
			records := captureLogRecords(t, func(l *slog.Logger) {
				e := NewClientEmitter(ClientEmitterConfig{
					Logger:      l,
					LogProvider: noopLogProvider(),
				})
				e.Emit(context.Background(), ev)
			})
			if len(records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(records))
			}
			got := records[0]["level"].(string)
			if got != tc.wantLevel {
				t.Errorf("kind=%q level=%q: slog level = %q, want %q", tc.kind, tc.level, got, tc.wantLevel)
			}
		})
	}
}

// TestClientEmitter_ActivityMapping verifies the activity field mapping
// (REQ-OPS-204).
func TestClientEmitter_ActivityMapping(t *testing.T) {
	cases := []struct {
		kind          string
		authenticated bool
		wantActivity  string
	}{
		{"error", true, ActivityAudit},
		{"error", false, ActivityAudit},
		{"log", true, ActivityUser},
		{"log", false, ActivityInternal},
		{"vital", true, ActivityInternal},
		{"vital", false, ActivityInternal},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.kind, func(t *testing.T) {
			ev := makeTestEvent(tc.kind, "info", tc.authenticated)
			records := captureLogRecords(t, func(l *slog.Logger) {
				e := NewClientEmitter(ClientEmitterConfig{
					Logger:      l,
					LogProvider: noopLogProvider(),
				})
				e.Emit(context.Background(), ev)
			})
			if len(records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(records))
			}
			got := records[0]["activity"].(string)
			if got != tc.wantActivity {
				t.Errorf("kind=%q auth=%v: activity=%q, want %q", tc.kind, tc.authenticated, got, tc.wantActivity)
			}
		})
	}
}

// TestClientEmitter_AnonymousOTLPGate verifies that anonymous events are not
// sent to OTLP unless publicOTLPEgress is true (REQ-OPS-205).
func TestClientEmitter_AnonymousOTLPGate(t *testing.T) {
	t.Run("gate_off", func(t *testing.T) {
		coll := newOTLPLogCollector(t)
		ctx := context.Background()
		lp, shutdown, err := NewOTLPLogProvider(ctx, OTLPLoggerConfig{Endpoint: coll.addr()})
		if err != nil {
			t.Fatalf("NewOTLPLogProvider: %v", err)
		}
		defer func() { _ = shutdown(context.Background()) }()

		var logBuf bytes.Buffer
		l := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		e := NewClientEmitter(ClientEmitterConfig{
			Logger:           l,
			LogProvider:      lp,
			PublicOTLPEgress: false, // gate off
		})

		ev := makeTestEvent("log", "info", false)
		ev.Endpoint = "public"
		e.Emit(ctx, ev)

		// Flush and give the BatchProcessor time to export.
		sdkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_ = shutdown(sdkCtx)

		rls := coll.resourceLogs()
		if len(rls) != 0 {
			t.Errorf("anonymous event with gate=off should not reach OTLP; got %d ResourceLogs", len(rls))
		}
		// But slog must still have the record.
		if !strings.Contains(logBuf.String(), "test message") {
			t.Errorf("slog should still have the event even when OTLP gate is off; got: %q", logBuf.String())
		}
	})

	t.Run("gate_on", func(t *testing.T) {
		coll := newOTLPLogCollector(t)
		ctx := context.Background()
		lp, shutdown, err := NewOTLPLogProvider(ctx, OTLPLoggerConfig{Endpoint: coll.addr()})
		if err != nil {
			t.Fatalf("NewOTLPLogProvider: %v", err)
		}
		defer func() { _ = shutdown(context.Background()) }()

		var logBuf bytes.Buffer
		l := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		e := NewClientEmitter(ClientEmitterConfig{
			Logger:           l,
			LogProvider:      lp,
			PublicOTLPEgress: true, // gate on
		})

		ev := makeTestEvent("log", "info", false)
		ev.Endpoint = "public"
		e.Emit(ctx, ev)

		// Flush.
		sdkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = shutdown(sdkCtx)

		if !coll.waitForRecords(1, 2*time.Second) {
			t.Errorf("anonymous event with gate=on should reach OTLP, but no records received")
		}
	})
}

// TestClientEmitter_OTLPAttributes verifies that the OTLP record carries the
// expected per-record attributes (architecture §OTLP shape).
func TestClientEmitter_OTLPAttributes(t *testing.T) {
	coll := newOTLPLogCollector(t)
	ctx := context.Background()
	lp, shutdown, err := NewOTLPLogProvider(ctx, OTLPLoggerConfig{
		Endpoint:              coll.addr(),
		DeploymentEnvironment: "test",
		ServiceInstanceID:     "test-host",
	})
	if err != nil {
		t.Fatalf("NewOTLPLogProvider: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	l := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewClientEmitter(ClientEmitterConfig{
		Logger:           l,
		LogProvider:      lp,
		PublicOTLPEgress: false,
	})

	ev := makeTestEvent("log", "warn", true)
	e.Emit(ctx, ev)

	// Flush.
	sdkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = shutdown(sdkCtx)

	if !coll.waitForRecords(1, 2*time.Second) {
		t.Fatal("no OTLP records received")
	}

	rls := coll.resourceLogs()
	if len(rls) == 0 {
		t.Fatal("no ResourceLogs in OTLP output")
	}
	rl := rls[0]
	if len(rl.ScopeLogs) == 0 {
		t.Fatal("no ScopeLogs in ResourceLogs")
	}
	sl := rl.ScopeLogs[0]
	if sl.Scope == nil {
		t.Fatal("nil instrumentation scope")
	}
	// service.name is the scope name.
	if sl.Scope.Name != "herold-suite" {
		t.Errorf("scope name = %q, want herold-suite", sl.Scope.Name)
	}
	// build_sha is the scope version.
	if sl.Scope.Version != "deadbeef" {
		t.Errorf("scope version = %q, want deadbeef", sl.Scope.Version)
	}
	if len(sl.LogRecords) == 0 {
		t.Fatal("no LogRecords in ScopeLogs")
	}
	lr := sl.LogRecords[0]

	// Find attribute by key helper.
	findAttr := func(attrs []*logpb.LogRecord, key string) string {
		for _, lrec := range attrs {
			for _, kv := range lrec.Attributes {
				if kv.Key == key {
					if sv := kv.Value.GetStringValue(); sv != "" {
						return sv
					}
				}
			}
		}
		return ""
	}

	// Check select attributes on the single record.
	wantAttrs := map[string]string{
		"client.session_id": "sess-xyz",
		"client.page_id":    "page-abc",
		"client.route":      "/mail/inbox",
		"client.ua":         "TestBrowser/1.0",
		"client.kind":       "log",
		"client.build_sha":  "deadbeef",
		"client.endpoint":   "auth",
		"client.listener":   "public",
		"user.id":           "user-42",
		"request_id":        "req-123",
	}
	for key, want := range wantAttrs {
		got := findAttr([]*logpb.LogRecord{lr}, key)
		if got != want {
			t.Errorf("OTLP attribute %q = %q, want %q", key, got, want)
		}
	}

	// Check resource attributes.
	if rl.Resource != nil {
		findResAttr := func(key string) string {
			for _, kv := range rl.Resource.Attributes {
				if kv.Key == key {
					return kv.Value.GetStringValue()
				}
			}
			return ""
		}
		if env := findResAttr("deployment.environment"); env != "test" {
			t.Errorf("resource deployment.environment = %q, want test", env)
		}
		if id := findResAttr("service.instance.id"); id != "test-host" {
			t.Errorf("resource service.instance.id = %q, want test-host", id)
		}
	}
}

// TestClientEmitter_ExceptionFields verifies that kind=error events get
// exception.type and exception.stacktrace attributes in OTLP.
func TestClientEmitter_ExceptionFields(t *testing.T) {
	coll := newOTLPLogCollector(t)
	ctx := context.Background()
	lp, shutdown, err := NewOTLPLogProvider(ctx, OTLPLoggerConfig{Endpoint: coll.addr()})
	if err != nil {
		t.Fatalf("NewOTLPLogProvider: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	l := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewClientEmitter(ClientEmitterConfig{
		Logger:      l,
		LogProvider: lp,
	})

	ev := makeTestEvent("error", "error", true)
	ev.Msg = "TypeError: Cannot read properties of undefined"
	ev.Stack = "TypeError: Cannot read properties of undefined\n    at foo (app.js:10:5)"
	e.Emit(ctx, ev)

	sdkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = shutdown(sdkCtx)

	if !coll.waitForRecords(1, 2*time.Second) {
		t.Fatal("no OTLP records received")
	}

	rls := coll.resourceLogs()
	if len(rls) == 0 || len(rls[0].ScopeLogs) == 0 || len(rls[0].ScopeLogs[0].LogRecords) == 0 {
		t.Fatal("missing OTLP log records")
	}
	lr := rls[0].ScopeLogs[0].LogRecords[0]

	findAttr := func(key string) string {
		for _, kv := range lr.Attributes {
			if kv.Key == key {
				return kv.Value.GetStringValue()
			}
		}
		return ""
	}

	if et := findAttr("exception.type"); et != "TypeError" {
		t.Errorf("exception.type = %q, want TypeError", et)
	}
	if st := findAttr("exception.stacktrace"); !strings.Contains(st, "TypeError") {
		t.Errorf("exception.stacktrace missing content: %q", st)
	}
}

// TestOTLPLogProvider_NoopWhenEndpointEmpty verifies the noop path.
func TestOTLPLogProvider_NoopWhenEndpointEmpty(t *testing.T) {
	ctx := context.Background()
	lp, shutdown, err := NewOTLPLogProvider(ctx, OTLPLoggerConfig{})
	if err != nil {
		t.Fatalf("NewOTLPLogProvider (empty endpoint): %v", err)
	}
	if lp == nil {
		t.Fatal("LoggerProvider must not be nil for noop")
	}
	if shutdown == nil {
		t.Fatal("shutdown must not be nil for noop")
	}
	sdctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := shutdown(sdctx); err != nil {
		t.Fatalf("noop shutdown: %v", err)
	}
}

// TestParseExceptionType exercises the best-effort exception type extractor.
func TestParseExceptionType(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"TypeError: Cannot read properties", "TypeError"},
		{"RangeError: Maximum call stack exceeded", "RangeError"},
		{"Error: something went wrong", "Error"},
		{"just a plain message", ""},
		{"has spaces before: colon", ""},
		{":no type prefix", ""},
		{"", ""},
		{"123Error: numeric prefix", ""},
	}
	for _, tc := range cases {
		got := parseExceptionType(tc.msg)
		if got != tc.want {
			t.Errorf("parseExceptionType(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

// TestClientEmitter_SessionIDOptional verifies that slog records with no
// SessionID omit the client_session key (don't emit empty string).
func TestClientEmitter_SessionIDOptional(t *testing.T) {
	ev := makeTestEvent("log", "info", false)
	ev.SessionID = ""
	ev.RequestID = ""

	records := captureLogRecords(t, func(l *slog.Logger) {
		e := NewClientEmitter(ClientEmitterConfig{
			Logger:      l,
			LogProvider: noopLogProvider(),
		})
		e.Emit(context.Background(), ev)
	})
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if _, ok := records[0]["client_session"]; ok {
		t.Error("client_session should be absent when SessionID is empty")
	}
	if _, ok := records[0]["request_id"]; ok {
		t.Error("request_id should be absent when RequestID is empty")
	}
}

// noopLogProvider returns a noop LoggerProvider for tests that don't need OTLP.
func noopLogProvider() otellog.LoggerProvider {
	return noop.NewLoggerProvider()
}
