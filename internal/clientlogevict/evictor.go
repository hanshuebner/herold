package clientlogevict

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

const (
	// DefaultTickInterval is the default time between eviction passes.
	DefaultTickInterval = 60 * time.Second

	// DefaultAuthCapRows is the maximum number of rows to retain in the
	// auth slice when no operator configuration is present (REQ-OPS-219).
	DefaultAuthCapRows = 100_000

	// DefaultAuthMaxAge is the default age limit for auth-slice rows
	// (7 days, REQ-OPS-219).
	DefaultAuthMaxAge = 168 * time.Hour

	// DefaultPublicCapRows is the maximum number of rows to retain in the
	// public slice when no operator configuration is present (REQ-OPS-219).
	DefaultPublicCapRows = 10_000

	// DefaultPublicMaxAge is the default age limit for public-slice rows
	// (24 h, REQ-OPS-219).
	DefaultPublicMaxAge = 24 * time.Hour

	// DefaultBatchSize is the maximum number of rows deleted per eviction
	// statement to bound write-lock duration on SQLite (REQ-OPS-219).
	DefaultBatchSize = 1000
)

// Evictor is the background goroutine that keeps the clientlog ring buffer
// within its configured bounds.  It satisfies the "background worker that
// watches ctx and returns on shutdown" contract used across the project
// (see STANDARDS.md §5 and the extsubmit sweeper for the canonical pattern).
type Evictor struct {
	// Store is the metadata repository surface.  Required.
	Store ClientlogStore

	// Logger receives progress and error events.  Defaults to slog.Default
	// when nil.
	Logger *slog.Logger

	// TickInterval overrides the default 60-second tick period.  Zero
	// uses DefaultTickInterval.
	TickInterval time.Duration

	// AuthCapRows overrides DefaultAuthCapRows.  Zero uses the default.
	AuthCapRows int

	// AuthMaxAge overrides DefaultAuthMaxAge.  Zero uses the default.
	AuthMaxAge time.Duration

	// PublicCapRows overrides DefaultPublicCapRows.  Zero uses the default.
	PublicCapRows int

	// PublicMaxAge overrides DefaultPublicMaxAge.  Zero uses the default.
	PublicMaxAge time.Duration

	// BatchSize overrides DefaultBatchSize.  Zero uses the default.
	BatchSize int
}

// ClientlogStore is the minimal store surface the Evictor needs.
type ClientlogStore interface {
	EvictClientLog(ctx context.Context, opts store.ClientLogEvictOptions) (deleted int, err error)
}

func (e *Evictor) tickInterval() time.Duration {
	if e.TickInterval > 0 {
		return e.TickInterval
	}
	return DefaultTickInterval
}

func (e *Evictor) authCapRows() int {
	if e.AuthCapRows > 0 {
		return e.AuthCapRows
	}
	return DefaultAuthCapRows
}

func (e *Evictor) authMaxAge() time.Duration {
	if e.AuthMaxAge > 0 {
		return e.AuthMaxAge
	}
	return DefaultAuthMaxAge
}

func (e *Evictor) publicCapRows() int {
	if e.PublicCapRows > 0 {
		return e.PublicCapRows
	}
	return DefaultPublicCapRows
}

func (e *Evictor) publicMaxAge() time.Duration {
	if e.PublicMaxAge > 0 {
		return e.PublicMaxAge
	}
	return DefaultPublicMaxAge
}

func (e *Evictor) batchSize() int {
	if e.BatchSize > 0 {
		return e.BatchSize
	}
	return DefaultBatchSize
}

func (e *Evictor) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// Run starts the eviction loop.  It blocks until ctx is cancelled and
// returns nil on clean shutdown.  Transient store errors are logged and
// do not terminate the loop; the next tick will retry.
//
// This function is designed to be launched in an errgroup goroutine:
//
//	g.Go(func() error { return ev.Run(ctx) })
func (e *Evictor) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.tickInterval())
	defer ticker.Stop()

	e.logger().LogAttrs(ctx, slog.LevelInfo,
		"clientlogevict: started",
		slog.Duration("interval", e.tickInterval()),
		slog.Int("auth_cap_rows", e.authCapRows()),
		slog.Duration("auth_max_age", e.authMaxAge()),
		slog.Int("public_cap_rows", e.publicCapRows()),
		slog.Duration("public_max_age", e.publicMaxAge()),
		slog.Int("batch_size", e.batchSize()),
	)

	for {
		select {
		case <-ctx.Done():
			e.logger().LogAttrs(context.Background(), slog.LevelInfo,
				"clientlogevict: shutting down")
			return nil
		case <-ticker.C:
			e.tick(ctx)
		}
	}
}

// tick executes one eviction pass for every slice.
func (e *Evictor) tick(ctx context.Context) {
	for _, cfg := range []struct {
		slice   store.ClientLogSlice
		capRows int
		maxAge  time.Duration
	}{
		{store.ClientLogSliceAuth, e.authCapRows(), e.authMaxAge()},
		{store.ClientLogSlicePublic, e.publicCapRows(), e.publicMaxAge()},
	} {
		n, err := e.Store.EvictClientLog(ctx, store.ClientLogEvictOptions{
			Slice:     cfg.slice,
			CapRows:   cfg.capRows,
			MaxAge:    cfg.maxAge,
			BatchSize: e.batchSize(),
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			e.logger().LogAttrs(ctx, slog.LevelError,
				"clientlogevict: evict failed",
				slog.String("slice", string(cfg.slice)),
				slog.String("err", err.Error()),
			)
			continue
		}
		if n > 0 {
			e.logger().LogAttrs(ctx, slog.LevelDebug,
				"clientlogevict: evicted rows",
				slog.String("slice", string(cfg.slice)),
				slog.Int("deleted", n),
			)
		}
	}
}
