package admin

// Integration tests for the queue-to-loopback-deliverer seam (re #43).
//
// The bug: the queue's signing step ran before the loopback deliverer's
// locality check, so Sign=true on a domain with no DKIM key permanently
// failed delivery even for recipients on this server (same process).
//
// The fix: a LocalRecipientChecker injected into queue.Options lets the
// queue skip signing for local recipients before the signer is invoked.
// These tests exercise that seam end-to-end: real *queue.Queue +
// real loopbackDeliverer + real localityChecker + stubIngester and
// failingSigner, backed by an in-memory SQLite store. A Postgres variant
// runs when HEROLD_PG_DSN is set (STANDARDS §8.8 / CI matrix).

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// loopbackQueueFixture wires the queue + loopbackDeliverer +
// localityChecker for integration tests. It seeds a local domain
// "example.local" with one principal "alice@example.local" in st.
//
// The injected signer is always a failingSigner so tests exercise the
// path where signing would fail for external recipients; local
// recipients must succeed regardless.
//
// The stubIngester records IngestBytes calls; tests assert it was (or
// was not) called depending on the recipient's locality.
type loopbackQueueFixture struct {
	q        *queue.Queue
	ing      *stubIngester
	clk      *clock.FakeClock
	st       store.Store
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	t        *testing.T
	alicePID directory.PrincipalID
}

func newLoopbackQueueFixture(t *testing.T, st store.Store) *loopbackQueueFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())

	// Seed domain + principal.
	if err := st.Meta().InsertDomain(ctx, store.Domain{
		Name: "example.local", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(st.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.local", "correct-horse-battery-1")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	ing := &stubIngester{}
	inner := &stubInner{outcome: queue.DeliveryOutcome{Status: queue.DeliveryStatusPermanent,
		Detail: "external: signer refused"}}

	lbDel := loopbackDeliverer{
		inner: inner,
		smtp:  ing,
		meta:  st.Meta(),
		dir:   dir,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	lc := localityChecker{
		meta: st.Meta(),
		dir:  dir,
	}

	signer := &loopbackQueueFailingSigner{err: errors.New("no active DKIM key for domain: example.local")}

	q := queue.New(queue.Options{
		Store:                 st,
		Deliverer:             lbDel,
		Signer:                signer,
		LocalRecipientChecker: lc,
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:                 clk,
		Concurrency:           4,
		PerHostMax:            2,
		PollInterval:          30 * time.Millisecond,
		Hostname:              "example.local",
		DSNFromAddress:        "postmaster@example.local",
		ShutdownGrace:         2 * time.Second,
		// One-retry schedule so exhaustion tests complete quickly.
		Retry: queue.RetryPolicy{Schedule: []time.Duration{50 * time.Millisecond}},
	})

	f := &loopbackQueueFixture{
		q:        q,
		ing:      ing,
		clk:      clk,
		st:       st,
		ctx:      ctx,
		cancel:   cancel,
		t:        t,
		alicePID: pid,
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		_ = q.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		f.wg.Wait()
	})
	return f
}

// loopbackQueueFailingSigner is a queue.Signer that always returns an
// error. Named distinctly from the queue_test package's failingSigner
// to avoid any cross-package confusion.
type loopbackQueueFailingSigner struct {
	err error
}

func (s *loopbackQueueFailingSigner) SignStream(_ context.Context, _ string, _ io.ReadSeeker) (io.Reader, error) {
	return nil, s.err
}

// loopbackQueueWaitFor polls pred every 5 ms until it returns true or
// timeout elapses.
func loopbackQueueWaitFor(t *testing.T, timeout time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}

// openSQLiteStore opens a fresh SQLite store for the integration tests.
func openSQLiteStore(t *testing.T) store.Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return sqlitetest.Open(t, clk)
}

// openPostgresStoreForLoopback opens a Postgres store for the
// loopback-queue integration tests. Skips when HEROLD_PG_DSN is unset.
func openPostgresStoreForLoopback(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	// HEROLD_PG_DSN is a single shared throwaway database; reset row state
	// before each test so seeds (the local domain + principal) do not
	// collide with a prior test in the same run. Mirrors the storepg test
	// harness's TruncateAll-between-tests pattern.
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			_ = st.Close()
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// runLoopbackQueueLocalOnly exercises the core re #43 fix: a submission
// with Sign=true addressed to a local recipient is delivered via
// loopback even though the signer fails. This is the test that would
// have caught the bug before it shipped.
func runLoopbackQueueLocalOnly(t *testing.T, st store.Store) {
	t.Helper()
	f := newLoopbackQueueFixture(t, st)

	envID, err := f.q.Submit(f.ctx, queue.Submission{
		MailFrom:      "postmaster@example.local",
		Recipients:    []string{"alice@example.local"},
		Body:          strings.NewReader("Subject: local-only\r\n\r\nbody\r\n"),
		Sign:          true,
		SigningDomain: "example.local",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The queue row must reach Done (not Failed) because the recipient
	// is local: the signer is bypassed and the message is handed to the
	// loopback ingester.
	if !loopbackQueueWaitFor(t, 5*time.Second, func() bool {
		rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		return len(rows) == 1 && rows[0].State == store.QueueStateDone
	}) {
		rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		t.Fatalf("local recipient row did not reach Done; rows=%+v", rows)
	}

	// IngestBytes must have been called exactly once with the local recipient.
	if f.ing.calls != 1 {
		t.Fatalf("IngestBytes call count: got %d, want 1", f.ing.calls)
	}
	if got := f.ing.last.Recipients[0].Addr; !strings.EqualFold(got, "alice@example.local") {
		t.Fatalf("IngestBytes recipient addr: got %q, want alice@example.local", got)
	}
}

// TestQueueLoopback_LocalRecipient_SignTrue_NoKey_Delivers is the
// SQLite backend run of the re #43 regression test.
func TestQueueLoopback_LocalRecipient_SignTrue_NoKey_Delivers(t *testing.T) {
	runLoopbackQueueLocalOnly(t, openSQLiteStore(t))
}

// TestQueueLoopback_LocalRecipient_SignTrue_NoKey_Delivers_Postgres is
// the Postgres backend run of the same test. Skips when HEROLD_PG_DSN
// is unset.
func TestQueueLoopback_LocalRecipient_SignTrue_NoKey_Delivers_Postgres(t *testing.T) {
	runLoopbackQueueLocalOnly(t, openPostgresStoreForLoopback(t))
}

// runLoopbackQueueMixed exercises the mixed-recipient case: one local
// recipient (should be delivered via loopback, signer bypassed) and
// one external recipient (should fail permanently because the signer
// fails and Sign=true — re #20 intact).
func runLoopbackQueueMixed(t *testing.T, st store.Store) {
	t.Helper()
	f := newLoopbackQueueFixture(t, st)

	envID, err := f.q.Submit(f.ctx, queue.Submission{
		MailFrom:      "postmaster@example.local",
		Recipients:    []string{"alice@example.local", "bob@external.test"},
		Body:          strings.NewReader("Subject: mixed\r\n\r\nbody\r\n"),
		Sign:          true,
		SigningDomain: "example.local",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait until BOTH rows have reached a terminal state (Done or Failed).
	// The local row must be Done; the external row must be Failed (signer
	// refused, re #20).
	if !loopbackQueueWaitFor(t, 10*time.Second, func() bool {
		rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		if len(rows) != 2 {
			return false
		}
		for _, r := range rows {
			if r.State != store.QueueStateDone && r.State != store.QueueStateFailed {
				return false
			}
		}
		return true
	}) {
		rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		t.Fatalf("rows did not reach terminal state; rows=%+v", rows)
	}

	rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	for _, r := range rows {
		switch {
		case strings.EqualFold(r.RcptTo, "alice@example.local"):
			if r.State != store.QueueStateDone {
				t.Errorf("local recipient alice: got state %s, want Done", r.State)
			}
		case strings.EqualFold(r.RcptTo, "bob@external.test"):
			if r.State != store.QueueStateFailed {
				t.Errorf("external recipient bob: got state %s, want Failed (re #20)", r.State)
			}
			if !strings.Contains(r.LastError, "signer failure") {
				t.Errorf("external recipient bob: LastError %q must mention signer failure", r.LastError)
			}
		default:
			t.Errorf("unexpected row rcpt_to %q", r.RcptTo)
		}
	}

	// IngestBytes must have been called for alice only.
	if f.ing.calls != 1 {
		t.Fatalf("IngestBytes call count: got %d, want 1 (alice only)", f.ing.calls)
	}
}

// TestQueueLoopback_Mixed_LocalSucceeds_ExternalFails is the SQLite run.
func TestQueueLoopback_Mixed_LocalSucceeds_ExternalFails(t *testing.T) {
	runLoopbackQueueMixed(t, openSQLiteStore(t))
}

// TestQueueLoopback_Mixed_LocalSucceeds_ExternalFails_Postgres is the
// Postgres run. Skips when HEROLD_PG_DSN is unset.
func TestQueueLoopback_Mixed_LocalSucceeds_ExternalFails_Postgres(t *testing.T) {
	runLoopbackQueueMixed(t, openPostgresStoreForLoopback(t))
}

// runLoopbackQueueExternalOnly is the re #20 regression guard: an
// external-only recipient with Sign=true and a failing signer still
// permanently fails even after the re #43 fix.
func runLoopbackQueueExternalOnly(t *testing.T, st store.Store) {
	t.Helper()
	f := newLoopbackQueueFixture(t, st)

	envID, err := f.q.Submit(f.ctx, queue.Submission{
		MailFrom:      "postmaster@example.local",
		Recipients:    []string{"bob@external.test"},
		Body:          strings.NewReader("Subject: external-only\r\n\r\nbody\r\n"),
		Sign:          true,
		SigningDomain: "example.local",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The row must reach Failed — signing is required for external
	// recipients and the signer always fails in this fixture.
	if !loopbackQueueWaitFor(t, 5*time.Second, func() bool {
		rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		return len(rows) == 1 && rows[0].State == store.QueueStateFailed
	}) {
		rows, _ := f.st.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		t.Fatalf("external row never reached Failed; rows=%+v", rows)
	}

	// IngestBytes must NOT have been called (no local delivery attempted).
	if f.ing.calls != 0 {
		t.Fatalf("IngestBytes must not be called for external-only submission; got %d call(s)", f.ing.calls)
	}
}

// TestQueueLoopback_External_SignTrue_FailingSigner_PermanentFail is
// the SQLite re #20 regression run.
func TestQueueLoopback_External_SignTrue_FailingSigner_PermanentFail(t *testing.T) {
	runLoopbackQueueExternalOnly(t, openSQLiteStore(t))
}

// TestQueueLoopback_External_SignTrue_FailingSigner_PermanentFail_Postgres
// is the Postgres re #20 regression run. Skips when HEROLD_PG_DSN is unset.
func TestQueueLoopback_External_SignTrue_FailingSigner_PermanentFail_Postgres(t *testing.T) {
	runLoopbackQueueExternalOnly(t, openPostgresStoreForLoopback(t))
}
