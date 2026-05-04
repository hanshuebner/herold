package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// stubInner records calls and returns a configured outcome.
type stubInner struct {
	calls   int
	last    queue.DeliveryRequest
	outcome queue.DeliveryOutcome
}

func (s *stubInner) Deliver(_ context.Context, req queue.DeliveryRequest) (queue.DeliveryOutcome, error) {
	s.calls++
	s.last = req
	return s.outcome, nil
}

// stubIngester records IngestBytes calls.
type stubIngester struct {
	calls int
	last  protosmtp.IngestRequest
	err   error
}

func (s *stubIngester) IngestBytes(_ context.Context, req protosmtp.IngestRequest) error {
	s.calls++
	s.last = req
	return s.err
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loopbackFixture seeds an in-memory SQLite store with one local principal
// on example.local and returns the adapter handles required to construct
// a loopbackDeliverer.
func loopbackFixture(t *testing.T) (store.Store, *directory.Directory, store.PrincipalID) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "example.local", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(context.Background(), "alice@example.local", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	return fs, dir, pid
}

// TestLoopback_LocalRecipient_RoutesToIngest: a recipient on a local
// domain bypasses the outbound deliverer and hands the bytes to
// IngestBytes with the resolved principal id.
func TestLoopback_LocalRecipient_RoutesToIngest(t *testing.T) {
	fs, dir, pid := loopbackFixture(t)
	inner := &stubInner{}
	ing := &stubIngester{}

	d := loopbackDeliverer{
		inner: inner,
		smtp:  ing,
		meta:  fs.Meta(),
		dir:   dir,
		log:   discardLog(),
	}
	out, err := d.Deliver(context.Background(), queue.DeliveryRequest{
		MailFrom:  "alice@example.local",
		Recipient: "alice@example.local",
		Message:   []byte("body"),
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out.Status != queue.DeliveryStatusSuccess {
		t.Fatalf("status = %v, want Success", out.Status)
	}
	if inner.calls != 0 {
		t.Errorf("inner deliverer called %d times, expected 0 for local recipient", inner.calls)
	}
	if ing.calls != 1 {
		t.Fatalf("ingest calls = %d, want 1", ing.calls)
	}
	if got := ing.last.IngestSource; got != "loopback" {
		t.Errorf("ingest source = %q, want loopback", got)
	}
	if len(ing.last.Recipients) != 1 || ing.last.Recipients[0].PrincipalID != directory.PrincipalID(pid) {
		t.Errorf("recipient resolution = %+v, want one entry with pid %d",
			ing.last.Recipients, pid)
	}
}

// TestLoopback_NonLocalRecipient_FallsThrough: a recipient on a domain
// not in the store flows straight to the wrapped outbound deliverer.
func TestLoopback_NonLocalRecipient_FallsThrough(t *testing.T) {
	fs, dir, _ := loopbackFixture(t)
	inner := &stubInner{outcome: queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}}
	ing := &stubIngester{}

	d := loopbackDeliverer{
		inner: inner, smtp: ing, meta: fs.Meta(), dir: dir, log: discardLog(),
	}
	if _, err := d.Deliver(context.Background(), queue.DeliveryRequest{
		MailFrom: "alice@example.local", Recipient: "bob@example.com", Message: []byte("body"),
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner calls = %d, want 1", inner.calls)
	}
	if ing.calls != 0 {
		t.Errorf("ingest calls = %d, want 0", ing.calls)
	}
}

// TestLoopback_LocalDomain_UnknownRecipient_PermanentFail: a recipient
// on a hosted local domain that doesn't resolve to a principal returns
// a permanent failure (so the queue emits a bounce DSN) instead of
// retrying outbound MX lookups against a domain that has none.
func TestLoopback_LocalDomain_UnknownRecipient_PermanentFail(t *testing.T) {
	fs, dir, _ := loopbackFixture(t)
	inner := &stubInner{}
	ing := &stubIngester{}

	d := loopbackDeliverer{
		inner: inner, smtp: ing, meta: fs.Meta(), dir: dir, log: discardLog(),
	}
	out, err := d.Deliver(context.Background(), queue.DeliveryRequest{
		MailFrom: "alice@example.local", Recipient: "ghost@example.local", Message: []byte("body"),
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out.Status != queue.DeliveryStatusPermanent {
		t.Fatalf("status = %v, want Permanent", out.Status)
	}
	if inner.calls != 0 || ing.calls != 0 {
		t.Errorf("nothing else should run on permanent fail (inner=%d ingest=%d)",
			inner.calls, ing.calls)
	}
}

// TestLoopback_IngestError_Transient: when the local ingest pipeline
// fails the deliverer surfaces a transient outcome so the queue retries.
func TestLoopback_IngestError_Transient(t *testing.T) {
	fs, dir, _ := loopbackFixture(t)
	inner := &stubInner{}
	ing := &stubIngester{err: errors.New("blob put exploded")}

	d := loopbackDeliverer{
		inner: inner, smtp: ing, meta: fs.Meta(), dir: dir, log: discardLog(),
	}
	out, _ := d.Deliver(context.Background(), queue.DeliveryRequest{
		MailFrom: "alice@example.local", Recipient: "alice@example.local", Message: []byte("body"),
	})
	if out.Status != queue.DeliveryStatusTransient {
		t.Fatalf("status = %v, want Transient", out.Status)
	}
}

// TestLoopback_NilSMTP_NoOp: defensive — when the loopback dependencies
// aren't wired the wrapper must still pass through to the inner
// deliverer without panicking.
func TestLoopback_NilSMTP_NoOp(t *testing.T) {
	inner := &stubInner{outcome: queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}}
	d := loopbackDeliverer{inner: inner, log: discardLog()}
	if _, err := d.Deliver(context.Background(), queue.DeliveryRequest{
		Recipient: "alice@example.local",
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner calls = %d, want 1", inner.calls)
	}
}
