package identityverify

// sweeper.go implements the verification-token GC pass (REQ-IDENT-35).
//
// Two cadences run inside one sweep tick:
//
//  1. Expired verification tokens. Identity rows whose
//     verification_token_expires_at_us is older than the current clock
//     have their trio cleared via ClearIdentityVerificationToken. The
//     Identity row itself survives -- the SPA renders it as
//     "verification pending" and the user can resend.
//
//  2. Stale unverified Identity rows. Rows with verified_at_us IS NULL
//     and created_at_us older than the operator-configured purge
//     window (default 7d) are destroyed via DeleteJMAPIdentity. This
//     covers Identities the user abandoned mid-flow.
//
// The sweeper is a managed goroutine started from admin/server.go.
// Stops cleanly on context cancellation. Uses the injected clock so
// FakeClock-driven tests can drive ticks deterministically.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// Default option values applied when SweeperOptions fields are zero.
const (
	// DefaultGCInterval is the cadence at which the sweeper runs. 6h
	// matches the spec wording ("default every 6 hours").
	DefaultGCInterval = 6 * time.Hour

	// MinGCInterval bounds the floor: a sub-minute sweep cadence is a
	// configuration mistake (the sweep itself is cheap but a
	// pathological loop pins the writer transaction).
	MinGCInterval = time.Minute

	// DefaultUnverifiedPurgeAfter is the unverified-row retention
	// window when SweeperOptions.UnverifiedPurgeAfter is zero. 7d
	// matches the spec default.
	DefaultUnverifiedPurgeAfter = 7 * 24 * time.Hour
)

// SweeperOptions configures Sweeper. The dispatcher's own options are
// distinct so test code can build a sweeper without standing up the
// full dispatch surface.
type SweeperOptions struct {
	// Store is the persistent metadata sink. Required.
	Store store.Store
	// Logger is the structured logger. Nil defaults to slog.Default.
	Logger *slog.Logger
	// Clock is the injected clock. Nil defaults to clock.NewReal.
	Clock clock.Clock
	// Auditor receives identity.purge entries when an unverified row
	// is destroyed (REQ-IDENT-90). Nil disables audit logging; the
	// slog line still survives.
	Auditor Auditor
	// Interval is the cadence between sweeps. Zero / sub-minute values
	// resolve to DefaultGCInterval at construction time.
	Interval time.Duration
	// UnverifiedPurgeAfter is the maximum age of an unverified row.
	// Zero resolves to DefaultUnverifiedPurgeAfter.
	UnverifiedPurgeAfter time.Duration
}

// Sweeper is the GC loop. Construct with NewSweeper, then drive Run in
// a managed goroutine. Tick is exported so tests can drive ticks
// without spinning Run.
type Sweeper struct {
	store                store.Store
	logger               *slog.Logger
	clock                clock.Clock
	auditor              Auditor
	interval             time.Duration
	unverifiedPurgeAfter time.Duration
	running              atomic.Bool
	tokensCleared        atomic.Uint64
	identitiesPurged     atomic.Uint64
}

// NewSweeper applies defaults and returns a sweeper ready for Run /
// Tick. opts.Store is required; the rest carry sensible defaults.
func NewSweeper(opts SweeperOptions) *Sweeper {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	interval := opts.Interval
	if interval < MinGCInterval {
		interval = DefaultGCInterval
	}
	purge := opts.UnverifiedPurgeAfter
	if purge <= 0 {
		purge = DefaultUnverifiedPurgeAfter
	}
	return &Sweeper{
		store:                opts.Store,
		logger:               logger,
		clock:                clk,
		auditor:              opts.Auditor,
		interval:             interval,
		unverifiedPurgeAfter: purge,
	}
}

// Interval returns the resolved sweep cadence. Exposed for tests.
func (s *Sweeper) Interval() time.Duration { return s.interval }

// UnverifiedPurgeAfter returns the resolved purge window. Exposed for
// tests.
func (s *Sweeper) UnverifiedPurgeAfter() time.Duration { return s.unverifiedPurgeAfter }

// TokensCleared returns the cumulative count of verification trios the
// sweeper has cleared (Identity rows survived). Exposed for tests and
// metrics.
func (s *Sweeper) TokensCleared() uint64 { return s.tokensCleared.Load() }

// IdentitiesPurged returns the cumulative count of stale unverified
// Identity rows the sweeper has destroyed. Exposed for tests and
// metrics.
func (s *Sweeper) IdentitiesPurged() uint64 { return s.identitiesPurged.Load() }

// Run drives the sweep loop until ctx is cancelled. The first tick
// fires immediately so a server restart catches up on tokens that
// expired during downtime. Returns nil on ctx cancellation; non-nil
// only on an unrecoverable store failure (the lifecycle errgroup
// triggers shutdown).
func (s *Sweeper) Run(ctx context.Context) error {
	if s.store == nil {
		return errors.New("identityverify.Sweeper: nil Store")
	}
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("identityverify.Sweeper: already running")
	}
	defer s.running.Store(false)

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := s.Tick(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			s.logger.LogAttrs(ctx, slog.LevelError, "identityverify.sweeper.tick_failed",
				slog.String("subsystem", "identityverify"),
				slog.String("err", err.Error()),
			)
			// Do not return: a single sweep failure should not abort
			// the loop. Sleep for one interval and try again.
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.clock.After(s.interval):
		}
	}
}

// Tick performs one sweep pass: clears expired token trios, then
// destroys stale unverified Identity rows. Returns the first error
// encountered; partial progress is preserved.
func (s *Sweeper) Tick(ctx context.Context) error {
	now := s.clock.Now().UTC()
	if err := s.sweepExpiredTokens(ctx, now); err != nil {
		return err
	}
	if err := s.sweepUnverifiedRows(ctx, now); err != nil {
		return err
	}
	return nil
}

// sweepExpiredTokens lists every Identity row whose token expired
// before now, clears the trio, and audits. The "before" cutoff is now
// itself: tokens that expire exactly at this micro-second instant are
// still considered live (the comparison is strict-less-than at the SQL
// level).
func (s *Sweeper) sweepExpiredTokens(ctx context.Context, now time.Time) error {
	rows, err := s.store.Meta().ListExpiredVerificationTokens(ctx, now)
	if err != nil {
		return fmt.Errorf("identityverify.sweeper: ListExpiredVerificationTokens: %w", err)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The row may be a verified-and-cleared row picked up between
		// the listing and the clear (the listing is by
		// expires_at_us; MarkIdentityVerified clears it). Treat
		// ErrNotFound on Clear as benign.
		err := s.store.Meta().ClearIdentityVerificationToken(ctx, row.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("identityverify.sweeper: ClearIdentityVerificationToken(%s): %w",
				row.ID, err)
		}
		s.tokensCleared.Add(1)
		s.logger.LogAttrs(ctx, slog.LevelInfo, "identityverify.sweeper.token_cleared",
			slog.String("subsystem", "identityverify"),
			slog.String("identity_id", row.ID),
			slog.Int64("expired_at_us", row.VerificationTokenExpiresAtUs),
			slog.String("activity", "system"),
		)
		s.auditPurge(ctx, row, "token_cleared",
			map[string]string{
				"identity_id":    row.ID,
				"identity_email": row.Email,
				"kind":           "token_cleared",
			})
	}
	return nil
}

// sweepUnverifiedRows lists every unverified Identity row older than
// the purge window, destroys it, and audits. DeleteJMAPIdentity is
// idempotent against ErrNotFound; concurrent deletion (e.g. user
// destroyed the row from the SPA) is treated as benign.
func (s *Sweeper) sweepUnverifiedRows(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-s.unverifiedPurgeAfter)
	rows, err := s.store.Meta().ListUnverifiedIdentitiesOlderThan(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("identityverify.sweeper: ListUnverifiedIdentitiesOlderThan: %w", err)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.store.Meta().DeleteJMAPIdentity(ctx, row.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("identityverify.sweeper: DeleteJMAPIdentity(%s): %w",
				row.ID, err)
		}
		s.identitiesPurged.Add(1)
		s.logger.LogAttrs(ctx, slog.LevelInfo, "identityverify.sweeper.identity_purged",
			slog.String("subsystem", "identityverify"),
			slog.String("identity_id", row.ID),
			slog.String("identity_email", row.Email),
			slog.Int64("created_at_us", row.CreatedAtUs),
			slog.String("activity", "system"),
		)
		s.auditPurge(ctx, row, "identity_purged",
			map[string]string{
				"identity_id":    row.ID,
				"identity_email": row.Email,
				"kind":           "identity_purged",
			})
	}
	return nil
}

// auditPurge writes an identity.purge entry (REQ-IDENT-90). When the
// auditor is nil the entry is silently dropped; the slog line still
// survives. The actor is the system since this is a background GC
// pass, not user-initiated.
func (s *Sweeper) auditPurge(ctx context.Context, row store.JMAPIdentity, message string, meta map[string]string) {
	if s.auditor == nil {
		return
	}
	if meta == nil {
		meta = map[string]string{}
	}
	if row.PrincipalID != 0 {
		meta["principal_id"] = fmt.Sprintf("%d", row.PrincipalID)
	}
	entry := store.AuditLogEntry{
		At:        s.clock.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "system",
		Action:    "identity.purge",
		Subject:   fmt.Sprintf("identity:%s", row.ID),
		Outcome:   store.OutcomeSuccess,
		Message:   message,
		Metadata:  meta,
	}
	if err := s.auditor.Append(ctx, entry); err != nil {
		s.logger.WarnContext(ctx, "identityverify.sweeper.audit_append_failed",
			slog.String("subsystem", "identityverify"),
			slog.String("identity_id", row.ID),
			slog.String("err", err.Error()))
	}
}
