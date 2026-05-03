package autodns_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/autodns"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fakeQueueSubmitter records every Submit call.
type fakeQueueSubmitter struct {
	mu          sync.Mutex
	submissions []autodns.ReportSubmission
}

func (f *fakeQueueSubmitter) Submit(ctx context.Context, msg autodns.ReportSubmission) (string, error) {
	f.mu.Lock()
	f.submissions = append(f.submissions, msg)
	f.mu.Unlock()
	return "envid", nil
}

func (f *fakeQueueSubmitter) snapshot() []autodns.ReportSubmission {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]autodns.ReportSubmission, len(f.submissions))
	copy(out, f.submissions)
	return out
}

// seedReporter builds a SQLite-backed reporter under a fixed FakeClock
// and returns the bundle.
func seedReporter(t *testing.T, queue *fakeQueueSubmitter, http *http.Client) (*autodns.Reporter, store.Store, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	if err := fs.Meta().InsertDomain(t.Context(), store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	rep := autodns.NewReporter(autodns.ReporterOptions{
		Store:            fs,
		Logger:           logger,
		Clock:            clk,
		HTTPClient:       http,
		Queue:            queue,
		EmissionInterval: 24 * time.Hour,
		ReporterDomain:   "example.test",
		ReporterContact:  "tlsrpt-noreply@example.test",
		Hostname:         "mx.example.test",
	})
	return rep, fs, clk
}

// appendFailure inserts one row.
func appendFailure(t *testing.T, fs store.Store, when time.Time) {
	t.Helper()
	f := store.TLSRPTFailure{
		RecordedAt:           when,
		PolicyDomain:         "example.test",
		ReceivingMTAHostname: "mx.example.test",
		FailureType:          store.TLSRPTFailureSTARTTLSNegotiation,
		FailureCode:          "starttls-fail",
		FailureDetailJSON:    `{"reason":"handshake"}`,
	}
	if err := fs.Meta().AppendTLSRPTFailure(t.Context(), f); err != nil {
		t.Fatalf("AppendTLSRPTFailure: %v", err)
	}
}

func TestRunDailyEmission_BuildsAndQueuesMailtoReport(t *testing.T) {
	queue := &fakeQueueSubmitter{}
	rep, fs, clk := seedReporter(t, queue, nil)

	// Seed one failure inside the next emission window.
	appendFailure(t, fs, clk.Now())

	resolver := func(ctx context.Context, domain string) []string {
		if domain != "example.test" {
			return nil
		}
		return []string{"mailto:tlsrpt@receiver.test"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- rep.RunDailyEmission(ctx, resolver) }()

	// Advance the clock past the 24h interval so the loop's first tick fires.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		clk.Advance(25 * time.Hour)
		time.Sleep(20 * time.Millisecond)
		if len(queue.snapshot()) > 0 {
			break
		}
	}
	cancel()
	<-done

	subs := queue.snapshot()
	if len(subs) == 0 {
		t.Fatalf("no queue submissions; emission did not run")
	}
	got := subs[0]
	if len(got.Recipients) != 1 || got.Recipients[0] != "tlsrpt@receiver.test" {
		t.Errorf("recipient = %v", got.Recipients)
	}
	if !got.Sign {
		t.Errorf("Sign = false; want true (RFC 8460 §5.3)")
	}
	if got.SigningDomain != "example.test" {
		t.Errorf("SigningDomain = %q", got.SigningDomain)
	}
	if !bytes.Contains(got.Body, []byte("multipart/report")) {
		t.Errorf("body missing multipart/report; got first 200 bytes:\n%s",
			got.Body[:min(len(got.Body), 200)])
	}
	if !bytes.Contains(got.Body, []byte("application/tlsrpt+gzip")) {
		t.Errorf("body missing application/tlsrpt+gzip part; first 400 bytes:\n%s",
			got.Body[:min(len(got.Body), 400)])
	}
	if !bytes.Contains(got.Body, []byte("Report Domain: example.test")) {
		t.Errorf("subject missing 'Report Domain'")
	}
}

func TestRunDailyEmission_HTTPSRua_PUTsTLSRPT(t *testing.T) {
	var (
		got   atomic.Value // store *receivedRequest
		count atomic.Int32
	)
	type receivedRequest struct {
		ContentType     string
		ContentEncoding string
		Body            []byte
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Store(&receivedRequest{
			ContentType:     r.Header.Get("Content-Type"),
			ContentEncoding: r.Header.Get("Content-Encoding"),
			Body:            body,
		})
		count.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	rep, fs, clk := seedReporter(t, nil, srv.Client())
	appendFailure(t, fs, clk.Now())

	resolver := func(ctx context.Context, domain string) []string {
		return []string{srv.URL + "/tlsrpt"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- rep.RunDailyEmission(ctx, resolver) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		clk.Advance(25 * time.Hour)
		time.Sleep(20 * time.Millisecond)
		if count.Load() > 0 {
			break
		}
	}
	cancel()
	<-done

	if count.Load() == 0 {
		t.Fatalf("HTTP endpoint never received a request")
	}
	rec := got.Load().(*receivedRequest)
	if rec.ContentType != "application/tlsrpt+gzip" {
		t.Errorf("content-type = %q; want application/tlsrpt+gzip", rec.ContentType)
	}
	// Decompress and verify it is well-formed JSON with the expected
	// top-level fields.
	gr, err := gzip.NewReader(bytes.NewReader(rec.Body))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	raw, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gunzipped: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", err, raw)
	}
	if _, ok := doc["report-id"]; !ok {
		t.Errorf("JSON missing report-id: %s", raw)
	}
	if _, ok := doc["policies"]; !ok {
		t.Errorf("JSON missing policies: %s", raw)
	}
}

func TestRunDailyEmission_NoFailures_NoEmission(t *testing.T) {
	queue := &fakeQueueSubmitter{}
	rep, _, clk := seedReporter(t, queue, nil)

	resolverCalls := atomic.Int32{}
	resolver := func(ctx context.Context, domain string) []string {
		resolverCalls.Add(1)
		return []string{"mailto:tlsrpt@receiver.test"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- rep.RunDailyEmission(ctx, resolver) }()

	// Advance two full intervals.
	for i := 0; i < 3; i++ {
		clk.Advance(25 * time.Hour)
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if subs := queue.snapshot(); len(subs) != 0 {
		t.Fatalf("submissions = %d; want 0 (no failures recorded)", len(subs))
	}
	if resolverCalls.Load() != 0 {
		t.Errorf("resolver calls = %d; want 0 (no domains with failures)", resolverCalls.Load())
	}
}

func TestRunDailyEmission_RuaUnavailable_LogsAndContinues(t *testing.T) {
	queue := &fakeQueueSubmitter{}
	rep, fs, clk := seedReporter(t, queue, nil)
	appendFailure(t, fs, clk.Now())

	// Resolver returns nothing — emulates a rua TXT fetch that failed.
	resolver := func(ctx context.Context, domain string) []string { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- rep.RunDailyEmission(ctx, resolver) }()

	for i := 0; i < 3; i++ {
		clk.Advance(25 * time.Hour)
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	// No queue submissions; loop did not panic; the failure row remains
	// for a future tick.
	if subs := queue.snapshot(); len(subs) != 0 {
		t.Fatalf("submissions = %d; want 0 (resolver returned no rua)", len(subs))
	}
	rows, err := fs.Meta().ListTLSRPTFailures(t.Context(), "example.test", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ListTLSRPTFailures: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("rows after emission = %d; want 1 (reporter does not delete on rua failure)", len(rows))
	}
}

// TestReporter_BuildAggregateReport_Shape verifies the JSON the reporter
// emits round-trips through encoding/json.
func TestReporter_BuildAggregateReport_Shape(t *testing.T) {
	rep, fs, clk := seedReporter(t, nil, nil)
	appendFailure(t, fs, clk.Now())

	body, n, err := rep.BuildAggregateReport(t.Context(), "example.test", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("BuildAggregateReport: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
	var doc struct {
		ReportID  string `json:"report-id"`
		DateRange struct {
			StartDateTime string `json:"start-datetime"`
			EndDateTime   string `json:"end-datetime"`
		} `json:"date-range"`
		Policies []map[string]any `json:"policies"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}
	if doc.ReportID == "" {
		t.Errorf("report-id empty; full body: %s", body)
	}
	if !strings.Contains(string(body), "starttls-negotiation-failure") {
		t.Errorf("expected starttls-negotiation-failure result-type in body; got: %s", body)
	}
	if len(doc.Policies) != 1 {
		t.Errorf("policies = %d, want 1", len(doc.Policies))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
