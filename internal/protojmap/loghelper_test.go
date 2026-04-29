package protojmap_test

// loghelper_test.go provides a local activity-tagging test helper used
// by TestDispatch_LogsMethodActivity, TestRequestLog_AccessTagged, and
// TestEventSource_PingTaggedPoll.
//
// observe.AssertActivityTagged is being developed in parallel by the
// ops-observability-implementor; until it lands we ship an inline
// equivalent here per the task brief.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// capturedRecord holds a single slog record's message, level, and all
// top-level attributes, captured by recordingHandler.
type capturedRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]slog.Value
}

// recordStore is the shared, mutex-protected record accumulator. A single
// store is shared among all cloned recordingHandlers produced by WithAttrs
// so callers get one flat list regardless of logger branching.
type recordStore struct {
	mu      sync.Mutex
	records []capturedRecord
}

// snapshot returns a copy of the current record list. Safe to call from any
// goroutine; callers hold the copy for their own assertions without needing
// further synchronisation.
func (s *recordStore) snapshot() []capturedRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedRecord, len(s.records))
	copy(out, s.records)
	return out
}

// recordingHandler is a slog.Handler that collects every record emitted
// at any level into the shared recordStore. Thread-safe.
type recordingHandler struct {
	store *recordStore
	attrs []slog.Attr // pre-scoped attrs from WithAttrs
}

func newRecordingLogger() (*slog.Logger, *recordStore) {
	st := &recordStore{}
	h := &recordingHandler{store: st}
	return slog.New(h), st
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   make(map[string]slog.Value),
	}
	// Collect pre-scoped attrs first so per-record attrs can shadow them.
	for _, a := range h.attrs {
		rec.Attrs[a.Key] = a.Value
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value
		return true
	})
	h.store.mu.Lock()
	h.store.records = append(h.store.records, rec)
	h.store.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(merged, h.attrs)
	copy(merged[len(h.attrs):], attrs)
	return &recordingHandler{store: h.store, attrs: merged}
}

func (h *recordingHandler) WithGroup(name string) slog.Handler { return h }

// validActivityValues is the closed enum from REQ-OPS-86.
var validActivityValues = map[string]bool{
	"user":     true,
	"audit":    true,
	"system":   true,
	"poll":     true,
	"access":   true,
	"internal": true,
}

// assertActivityTagged asserts that every record in recs carries an
// "activity" attribute whose value is in the closed enum.
func assertActivityTagged(t *testing.T, recs []capturedRecord) {
	t.Helper()
	for _, rec := range recs {
		v, ok := rec.Attrs["activity"]
		if !ok {
			t.Errorf("record %q at level %v has no activity attribute (REQ-OPS-86a)", rec.Message, rec.Level)
			continue
		}
		if !validActivityValues[v.String()] {
			t.Errorf("record %q has activity=%q which is not in the closed enum", rec.Message, v.String())
		}
	}
}

// -- Tests ---------------------------------------------------------------

// TestRequestLog_AccessTagged verifies that the per-HTTP-request access
// log line is emitted at debug and carries activity=access (REQ-OPS-86d).
func TestRequestLog_AccessTagged(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "bob@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	apiKey, _, err := createAPIKey(context.Background(), fs, pid)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	recLog, records := newRecordingLogger()
	srv := protojmap.NewServer(fs, dir, nil, recLog, clk, protojmap.Options{
		MaxCallsInRequest:  4,
		DownloadRatePerSec: -1,
	})
	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	// Issue a GET to the session endpoint — guaranteed to produce the
	// access log line.
	req, _ := http.NewRequest("GET", httpd.URL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	res, err := httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()

	// Find the access record.
	var accessRecs []capturedRecord
	for _, rec := range records.snapshot() {
		if rec.Message == "request" {
			accessRecs = append(accessRecs, rec)
		}
	}
	if len(accessRecs) == 0 {
		t.Fatalf("no protojmap.request record emitted")
	}
	for _, rec := range accessRecs {
		if rec.Level != slog.LevelDebug {
			t.Errorf("access record level = %v, want debug", rec.Level)
		}
		if v, ok := rec.Attrs["activity"]; !ok || v.String() != "access" {
			t.Errorf("access record activity = %v, want access", rec.Attrs["activity"])
		}
	}

	// All emitted records must carry a valid activity tag.
	assertActivityTagged(t, records.snapshot())
}

// TestDispatch_LogsMethodActivity verifies that a batch containing
// Email/set and Mailbox/get emits exactly two per-method records (whose
// message is the method name), both at info, both with activity=user,
// and that the set record carries created/updated/destroyed counts and
// the get record carries result_count.
func TestDispatch_LogsMethodActivity(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "charlie@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	apiKey, _, err := createAPIKey(context.Background(), fs, pid)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	recLog, records := newRecordingLogger()
	srv := protojmap.NewServer(fs, dir, nil, recLog, clk, protojmap.Options{
		MaxCallsInRequest:  4,
		DownloadRatePerSec: -1,
	})

	// Register fake handlers that return shapes matching the expected
	// response schemas so appendMethodCountAttrs can decode them.
	const cap = protojmap.CapabilityID("urn:test:activity")
	setResp := map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(pid),
		"oldState":   "1",
		"newState":   "2",
		"created":    map[string]any{"c1": map[string]any{"id": "new1"}},
		"updated":    map[string]any{},
		"destroyed":  []string{"old1"},
		"notCreated": map[string]any{},
		"notUpdated": map[string]any{},
		"notDestroyed": map[string]any{},
	}
	getResp := map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(pid),
		"state":     "2",
		"list":      []map[string]any{{"id": "new1"}, {"id": "existing1"}},
		"notFound":  []string{},
	}
	fakeSet := &fakeHandler{method: "FakeType/set", resp: setResp}
	fakeGet := &fakeHandler{method: "FakeType/get", resp: getResp}
	srv.Registry().Register(cap, fakeSet)
	srv.Registry().Register(cap, fakeGet)

	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	f := &fixture{t: t, srv: srv, httpd: httpd, apiKey: apiKey}
	accountID := protojmap.AccountIDForPrincipal(pid)

	_, _, raw := f.jmapPost(
		[]protojmap.CapabilityID{cap},
		[]protojmap.Invocation{
			{Name: "FakeType/set", Args: json.RawMessage(
				fmt.Sprintf(`{"accountId":%q,"create":{"c1":{"name":"x"}},"update":{},"destroy":["old1"]}`,
					accountID)),
				CallID: "s1"},
			{Name: "FakeType/get", Args: json.RawMessage(
				fmt.Sprintf(`{"accountId":%q,"ids":["new1","existing1"]}`, accountID)),
				CallID: "g1"},
		},
	)
	_ = raw

	// Collect the per-method records (message == method name).
	var methodRecs []capturedRecord
	for _, rec := range records.snapshot() {
		if rec.Message == "FakeType/set" || rec.Message == "FakeType/get" {
			methodRecs = append(methodRecs, rec)
		}
	}
	if len(methodRecs) != 2 {
		t.Fatalf("want 2 method records, got %d; all records: %v", len(methodRecs), records.snapshot())
	}

	// Both should be at info, activity=user, and carry the method name.
	for _, rec := range methodRecs {
		if rec.Level != slog.LevelInfo {
			t.Errorf("method record %q level = %v, want info", rec.Message, rec.Level)
		}
		if v, ok := rec.Attrs["activity"]; !ok || v.String() != "user" {
			t.Errorf("method record activity = %v, want user", rec.Attrs["activity"])
		}
	}

	// Find the set record and check count attrs.
	var setRec, getRec *capturedRecord
	for i := range methodRecs {
		switch methodRecs[i].Message {
		case "FakeType/set":
			setRec = &methodRecs[i]
		case "FakeType/get":
			getRec = &methodRecs[i]
		}
	}
	if setRec == nil {
		t.Fatalf("FakeType/set method record missing")
	}
	if getRec == nil {
		t.Fatalf("FakeType/get method record missing")
	}

	// set record must have created=1, updated=0, destroyed=1.
	if v := setRec.Attrs["created"].Int64(); v != 1 {
		t.Errorf("set created = %d, want 1", v)
	}
	if v := setRec.Attrs["updated"].Int64(); v != 0 {
		t.Errorf("set updated = %d, want 0", v)
	}
	if v := setRec.Attrs["destroyed"].Int64(); v != 1 {
		t.Errorf("set destroyed = %d, want 1", v)
	}

	// get record must have result_count=2.
	if v := getRec.Attrs["result_count"].Int64(); v != 2 {
		t.Errorf("get result_count = %d, want 2", v)
	}

	// All records must carry a valid activity tag.
	assertActivityTagged(t, records.snapshot())
}

// TestDispatch_AuditActivityForIdentityMethods verifies that Identity/*
// method calls are tagged activity=audit (REQ-OPS-86 / brief).
func TestDispatch_AuditActivityForIdentityMethods(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "diana@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	apiKey, _, err := createAPIKey(context.Background(), fs, pid)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	recLog, records := newRecordingLogger()
	srv := protojmap.NewServer(fs, dir, nil, recLog, clk, protojmap.Options{
		MaxCallsInRequest:  4,
		DownloadRatePerSec: -1,
	})

	const cap = protojmap.CapabilityID("urn:test:identity")
	fakeIdentityGet := &fakeHandler{
		method: "Identity/get",
		resp:   map[string]any{"list": []any{}, "state": "0"},
	}
	srv.Registry().Register(cap, fakeIdentityGet)

	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	f := &fixture{t: t, srv: srv, httpd: httpd, apiKey: apiKey}
	accountID := protojmap.AccountIDForPrincipal(pid)

	f.jmapPost(
		[]protojmap.CapabilityID{cap},
		[]protojmap.Invocation{
			{Name: "Identity/get", Args: json.RawMessage(
				fmt.Sprintf(`{"accountId":%q,"ids":null}`, accountID)),
				CallID: "i1"},
		},
	)

	var identityRecs []capturedRecord
	for _, rec := range records.snapshot() {
		if rec.Message == "Identity/get" {
			identityRecs = append(identityRecs, rec)
		}
	}
	if len(identityRecs) == 0 {
		t.Fatalf("no Identity/get method record emitted")
	}
	for _, rec := range identityRecs {
		if v, ok := rec.Attrs["activity"]; !ok || v.String() != "audit" {
			t.Errorf("Identity/get activity = %v, want audit", rec.Attrs["activity"])
		}
	}

	assertActivityTagged(t, records.snapshot())
}

// TestEventSource_PingTaggedPoll verifies that EventSource keepalive
// records carry activity=poll at debug (REQ-OPS-86).
func TestEventSource_PingTaggedPoll(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "eve@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	apiKey, _, err := createAPIKey(context.Background(), fs, pid)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	recLog, records := newRecordingLogger()
	srv := protojmap.NewServer(fs, dir, nil, recLog, clk, protojmap.Options{
		PushPingInterval:   1 * time.Second,
		PushCoalesceWindow: 50 * time.Millisecond,
		DownloadRatePerSec: -1,
	})
	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	// Open an EventSource connection with a short ping interval.
	url := httpd.URL + "/jmap/eventsource?types=Email&ping=1"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)

	resCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := httpd.Client().Do(req)
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

	// Drive the clock to fire at least one ping.
	saw := make(chan struct{})
	tmp := make([]byte, 64)
	buf := new(bytes.Buffer)
	go func() {
		for {
			n, readErr := res.Body.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
				if strings.Contains(buf.String(), ": ping") {
					close(saw)
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		clk.Advance(2 * time.Second)
		select {
		case <-saw:
			goto pinged
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("did not observe SSE ping")

pinged:
	cancel()
	// Allow the eventsource close log to flush.
	time.Sleep(50 * time.Millisecond)

	// Find ping records.
	var pingRecs []capturedRecord
	for _, rec := range records.snapshot() {
		if rec.Message == "eventsource.ping" {
			pingRecs = append(pingRecs, rec)
		}
	}
	if len(pingRecs) == 0 {
		t.Fatalf("no protojmap.eventsource.ping records emitted")
	}
	for _, rec := range pingRecs {
		if rec.Level != slog.LevelDebug {
			t.Errorf("ping record level = %v, want debug", rec.Level)
		}
		if v, ok := rec.Attrs["activity"]; !ok || v.String() != "poll" {
			t.Errorf("ping record activity = %v, want poll", rec.Attrs["activity"])
		}
	}

	// Spot-check that the open record is at info + activity=user.
	for _, rec := range records.snapshot() {
		if rec.Message == "eventsource.open" {
			if rec.Level != slog.LevelInfo {
				t.Errorf("open record level = %v, want info", rec.Level)
			}
			if v, ok := rec.Attrs["activity"]; !ok || v.String() != "user" {
				t.Errorf("open record activity = %v, want user", rec.Attrs["activity"])
			}
		}
	}

	// All emitted records must carry a valid activity tag (REQ-OPS-86a).
	assertActivityTagged(t, records.snapshot())
}
