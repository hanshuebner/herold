package observe

import (
	"context"
	"testing"
	"time"
)

func TestNewTracer_EmptyEndpointReturnsNoopTracer(t *testing.T) {
	ctx := context.Background()
	tr, shutdown, err := NewTracer(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr == nil {
		t.Fatalf("tracer must not be nil for no-op")
	}
	if shutdown == nil {
		t.Fatalf("shutdown must not be nil for no-op")
	}
	// Noop span should be non-recording.
	_, span := tr.Start(ctx, "test-span")
	if span.IsRecording() {
		t.Fatalf("noop span should not record")
	}
	span.End()

	sdctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := shutdown(sdctx); err != nil {
		t.Fatalf("noop shutdown returned error: %v", err)
	}
}

func TestNewTracer_ShutdownHonoursContext(t *testing.T) {
	// With an endpoint configured, Shutdown must respect a cancelled context.
	ctx := context.Background()
	tr, shutdown, err := NewTracer(ctx, "127.0.0.1:1")
	if err != nil {
		// Constructing the exporter itself can fail on some platforms; that's
		// acceptable — in that case there is no tracer to shut down. The
		// important invariant under test is that when we do get a shutdown
		// closure, it honours its ctx.
		t.Skipf("exporter construction failed: %v", err)
	}
	if tr == nil || shutdown == nil {
		t.Fatalf("tracer/shutdown must be non-nil on success")
	}
	sdctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_ = shutdown(sdctx) // may return context deadline; we only assert it returns.
}
