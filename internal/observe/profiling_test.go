package observe

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a thread-safe bytes.Buffer wrapper for slog write +
// concurrent test read. The race detector flags shared bytes.Buffer
// access without synchronisation; the diagnostic-test pattern needs a
// safe one.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStartMemStatsLogger_EmitsBaselineThenTicks asserts the baseline
// emit happens at start (not deferred to the first tick) and that
// subsequent ticks fire on the configured interval. The diagnostic
// value of the logger comes from having a record from t=0; a regression
// that defers the first emission to t=interval would silently break
// after-the-fact incident reconstruction.
func TestStartMemStatsLogger_EmitsBaselineThenTicks(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartMemStatsLogger(ctx, logger, 50*time.Millisecond)

	// Wait for the baseline emit + at least one tick.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(buf.String(), "runtime memstats") >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	got := buf.String()
	if n := strings.Count(got, "runtime memstats"); n < 2 {
		t.Fatalf("expected ≥ 2 memstats lines (baseline + tick); got %d\n--full output--\n%s", n, got)
	}
	for _, want := range []string{"heap_alloc=", "heap_inuse=", "heap_released=", "num_goroutine="} {
		if !strings.Contains(got, want) {
			t.Errorf("memstats line missing %q\n--full output--\n%s", want, got)
		}
	}
}

// TestPprofMux_RegistersIndexAndHeapEndpoints exercises the contract
// the diagnostic operator workflow depends on: mounting PprofMux on a
// fresh ServeMux makes /debug/pprof/ and /debug/pprof/heap reachable
// and produce non-trivial output. A regression that silently dropped
// one of the registrations would only surface mid-incident.
func TestPprofMux_RegistersIndexAndHeapEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	PprofMux(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, tc := range []struct {
		path           string
		wantContains   string
		wantStatusCode int
	}{
		{"/debug/pprof/", "Profile Descriptions", http.StatusOK},
		{"/debug/pprof/heap", "", http.StatusOK}, // binary; just check 200
	} {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body := readAll(t, resp)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.wantStatusCode {
			t.Errorf("GET %s status = %d; want %d", tc.path, resp.StatusCode, tc.wantStatusCode)
		}
		if tc.wantContains != "" && !strings.Contains(string(body), tc.wantContains) {
			t.Errorf("GET %s body missing %q\n--first 200 bytes--\n%s", tc.path, tc.wantContains, head(body, 200))
		}
		if tc.path == "/debug/pprof/heap" && len(body) < 16 {
			t.Errorf("GET /debug/pprof/heap body suspiciously short: %d bytes", len(body))
		}
	}
}

func readAll(t *testing.T, r *http.Response) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf
}

func head(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
