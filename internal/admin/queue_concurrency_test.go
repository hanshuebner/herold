package admin

import (
	"context"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestBuildOutboundQueue_HonorsConcurrencyKnobs verifies that
// buildOutboundQueue constructs a queue without error when non-default
// concurrency and per_host_max values are supplied via sysconfig.
// This is an end-to-end construction test: non-zero values that pass
// sysconfig.Validate must not cause errors in queue.New.
func TestBuildOutboundQueue_HonorsConcurrencyKnobs(t *testing.T) {
	ctx := context.Background()
	_, cfg := minimalConfigFixture(t)

	// Inject non-default concurrency via the config struct directly
	// (bypassing Parse since we already have a validated config fixture).
	cfg.Server.Queue = sysconfig.QueueConfig{
		Concurrency: 64,
		PerHostMax:  8,
	}

	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer st.Close()

	q, err := buildOutboundQueue(cfg, st, stubTLSRPTResolver{}, nil, discardLogger(), clk)
	if err != nil {
		t.Fatalf("buildOutboundQueue with concurrency=64: %v", err)
	}
	if q == nil {
		t.Fatalf("buildOutboundQueue returned nil queue")
	}
}

// TestBuildOutboundQueue_DefaultConcurrency verifies that concurrency=0
// (use built-in default) also succeeds.
func TestBuildOutboundQueue_DefaultConcurrency(t *testing.T) {
	ctx := context.Background()
	_, cfg := minimalConfigFixture(t)

	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer st.Close()

	q, err := buildOutboundQueue(cfg, st, stubTLSRPTResolver{}, nil, discardLogger(), clk)
	if err != nil {
		t.Fatalf("buildOutboundQueue with default concurrency: %v", err)
	}
	if q == nil {
		t.Fatalf("buildOutboundQueue returned nil queue")
	}
}
