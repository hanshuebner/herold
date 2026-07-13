// Package idpstalesweep registers the periodic sweep that enforces
// REQ-AC-63(b): a member who stops logging in entirely eventually loses
// the "idp:<provider>" grants a claim-mapping rule previously conferred,
// so a membership revoked at the IdP does not retain herold access
// indefinitely once the configured staleness window elapses. The store
// primitive (store.Metadata.SweepStaleIdPGrants) and the audited wrapper
// (authz.SweepStale) already existed (epic #188); this package is the
// missing scheduler that actually calls them on a running server, of the
// same Run(ctx)/Tick(ctx) shape as internal/trashretention and
// internal/archiveretention.
//
// Deprovisioning trigger (a) -- "a login without a previously-matched
// claim loses that grant immediately" -- is handled inline by
// authz.ReconcileIdP on every OIDC login and needs no sweeper. This
// package is trigger (b) only: the member who never logs in again.
package idpstalesweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Default option values applied when callers leave fields zero.
const (
	// DefaultStaleness is the age past which an idp: grant's
	// last_asserted_at makes it eligible for removal (REQ-AC-63b:
	// "default bounded, e.g. 30 days").
	DefaultStaleness = 30 * 24 * time.Hour

	// DefaultSweepInterval is the cadence at which the worker scans for
	// stale idp: grants. 1 hour matches trashretention/archiveretention's
	// own default; the staleness window itself is day-grained, so
	// sub-hour sweep precision is not needed.
	DefaultSweepInterval = 60 * time.Minute

	// MinSweepInterval is the floor below which configuration is treated
	// as the default.
	MinSweepInterval = 5 * time.Second
)

// staleSweepMeta is the slice of store.Metadata this worker depends on,
// matching authz's own (unexported) staleSweepStore contract structurally.
type staleSweepMeta interface {
	SweepStaleIdPGrants(ctx context.Context, olderThan time.Time) ([]store.Grant, error)
	AppendAuditLog(ctx context.Context, entry store.AuditLogEntry) error
}

// Options configures Worker. Zero fields fall back to defaults.
type Options struct {
	// Meta is the metadata store. Required.
	Meta staleSweepMeta
	// Logger is the structured logger; nil falls back to slog.Default.
	Logger *slog.Logger
	// Clock is the injected clock; nil falls back to clock.NewReal.
	Clock clock.Clock
	// Staleness is the age past which an idp: grant is swept. 0 / negative
	// -> DefaultStaleness.
	Staleness time.Duration
	// SweepInterval is the cadence between sweeps. Below MinSweepInterval
	// is treated as the default.
	SweepInterval time.Duration
}

// Worker is the idp: grant staleness sweeper loop (REQ-AC-63b). Construct
// with NewWorker; call Run(ctx) in a managed goroutine.
type Worker struct {
	meta      staleSweepMeta
	logger    *slog.Logger
	clock     clock.Clock
	staleness time.Duration
	interval  time.Duration
	running   atomic.Bool
	removed   atomic.Uint64
}

// NewWorker constructs a Worker with the supplied options. Defaults are
// applied at construction time so the worker exposes a stable Staleness /
// SweepInterval for tests.
func NewWorker(opts Options) *Worker {
	observe.RegisterIdpstalesweepMetrics()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	staleness := opts.Staleness
	if staleness <= 0 {
		staleness = DefaultStaleness
	}
	interval := opts.SweepInterval
	if interval < MinSweepInterval {
		interval = DefaultSweepInterval
	}
	return &Worker{
		meta:      opts.Meta,
		logger:    logger,
		clock:     clk,
		staleness: staleness,
		interval:  interval,
	}
}

// Removed returns the cumulative count of idp: grants swept by this
// worker. Used by tests and the metrics exporter.
func (w *Worker) Removed() uint64 { return w.removed.Load() }

// Staleness returns the resolved staleness window (post-defaulting).
func (w *Worker) Staleness() time.Duration { return w.staleness }

// SweepInterval returns the resolved sweep interval (post-defaulting).
func (w *Worker) SweepInterval() time.Duration { return w.interval }

// Run drives the sweep loop until ctx is cancelled. Each tick removes
// every idp:<provider> grant whose last_asserted_at predates the
// staleness window, auditing each removal (via authz.SweepStale).
//
// Returns nil on ctx cancellation; non-nil only on an unrecoverable store
// failure (the lifecycle errgroup logs and triggers shutdown). Run is
// single-goroutine; a second concurrent invocation returns an error.
func (w *Worker) Run(ctx context.Context) error {
	if w.meta == nil {
		return errors.New("idpstalesweep: nil Meta")
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("idpstalesweep: worker already running")
	}
	defer w.running.Store(false)

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if _, err := w.Tick(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.clock.After(w.interval):
		}
	}
}

// Tick performs one sweep, removing every idp: grant staler than the
// configured window. Returns the removed grants. Exported so tests can
// drive the sweeper deterministically without a real sleep.
func (w *Worker) Tick(ctx context.Context) ([]store.Grant, error) {
	start := w.clock.Now()
	defer func() {
		observe.IdpstalesweepSweepsTotal.Inc()
		observe.IdpstalesweepSweepDurationSeconds.Observe(w.clock.Now().Sub(start).Seconds())
	}()
	removed, err := authz.SweepStale(ctx, w.meta, w.clock, w.staleness)
	if err != nil {
		return nil, fmt.Errorf("idpstalesweep: sweep: %w", err)
	}
	if n := len(removed); n > 0 {
		w.removed.Add(uint64(n))
		observe.IdpstalesweepGrantsRemovedTotal.Add(float64(n))
		w.logger.LogAttrs(ctx, slog.LevelInfo, "idpstalesweep: swept",
			slog.Int("removed", n),
			slog.String("activity", "system"),
		)
	}
	return removed, nil
}
