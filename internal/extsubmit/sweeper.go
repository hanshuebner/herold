package extsubmit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

const (
	// defaultSweeperInterval is the tick interval for the sweeper goroutine.
	// The sweeper queries the store for OAuth identities whose refresh_due_us
	// is <= now and dispatches refresh attempts to the worker pool.
	defaultSweeperInterval = 60 * time.Second

	// defaultWorkerCount is the bounded worker-pool size when SweeperWorkers
	// is zero (Phase-6 architectural decision 1).
	defaultWorkerCount = 4

	// refreshWindow is how far before token expiry the sweeper schedules the
	// next refresh (Phase-6 architectural decision 3).
	refreshWindow = 60 * time.Second
)

// SweeperStore is the minimal store surface the Sweeper needs: list rows due
// for refresh, count all OAuth rows, and persist updated rows after a
// successful or failed refresh.
type SweeperStore interface {
	// ListIdentitySubmissionsDue returns rows whose refresh_due_us is
	// non-null and <= before, ordered by refresh_due_us ascending.
	ListIdentitySubmissionsDue(ctx context.Context, before time.Time) ([]store.IdentitySubmission, error)
	// CountOAuthIdentitySubmissions returns the total number of
	// identity_submission rows with submit_auth_method = 'oauth2', regardless
	// of refresh_due_us. Used to keep the active-identities gauge accurate
	// between refresh windows.
	CountOAuthIdentitySubmissions(ctx context.Context) (int, error)
	// UpsertIdentitySubmission persists an updated IdentitySubmission row.
	UpsertIdentitySubmission(ctx context.Context, sub store.IdentitySubmission) error
}

// TokenRefresher is the interface the Sweeper uses to refresh one OAuth token.
// The concrete implementation is *Refresher; tests inject a stub.
type TokenRefresher interface {
	// Refresh obtains a fresh access token for sub using creds, persists the
	// new token in the store, and returns the plaintext access token.
	// Returns ErrAuthFailed for 4xx token-endpoint responses.
	Refresh(ctx context.Context, sub store.IdentitySubmission, creds OAuthClientCredentials) (accessToken string, err error)
}

// SweeperAuditLogger is an optional interface for emitting structured audit
// entries from the sweeper. When nil, failures are logged only via slog.
type SweeperAuditLogger interface {
	// AppendAudit records a sweeper-triggered state transition.
	// action is "submission.external.refresh_failure".
	// principalID and identityID identify the affected row.
	// category is the failure class ("auth", "network", "unknown").
	// correlationID is an opaque trace token.
	AppendAudit(ctx context.Context, action, principalID, identityID, category, correlationID string)
}

// Sweeper is the background goroutine that refreshes OAuth access tokens for
// identity_submission rows before they expire (Phase-6 architectural decision 1).
//
// The Sweeper runs a single dispatcher goroutine that ticks every Interval
// (default 60 s). On each tick it queries SweeperStore.ListIdentitySubmissionsDue
// and dispatches each row to a bounded pool of Workers goroutines. Per-row
// panics are caught by the worker so one misbehaving row cannot crash the
// dispatcher.
type Sweeper struct {
	// Store is the data access layer. Required.
	Store SweeperStore
	// TokenRefresh performs the OAuth token refresh. Required when any identity
	// uses oauth2 auth method. Nil causes sweeper to log an error and skip
	// refresh for those rows. Accepts *Refresher or a test stub that implements
	// the TokenRefresher interface.
	TokenRefresh TokenRefresher
	// DataKey is the 32-byte AEAD key used to decrypt per-row client secrets.
	// Required when TokenRefresh is non-nil.
	DataKey []byte
	// Logger is the structured logger. Defaults to slog.Default.
	Logger *slog.Logger
	// AuditLog is the optional audit emitter. When nil, audit events are
	// omitted (acceptable in test environments).
	AuditLog SweeperAuditLogger
	// Interval is the sweep tick period. Defaults to 60 s.
	Interval time.Duration
	// Workers is the bounded pool size. Defaults to 4.
	Workers int
	// Now is a clock function for deterministic testing. Defaults to
	// time.Now when nil.
	Now func() time.Time
}

func (sw *Sweeper) now() time.Time {
	if sw.Now != nil {
		return sw.Now()
	}
	return time.Now()
}

func (sw *Sweeper) logger() *slog.Logger {
	if sw.Logger != nil {
		return sw.Logger
	}
	return slog.Default()
}

func (sw *Sweeper) interval() time.Duration {
	if sw.Interval > 0 {
		return sw.Interval
	}
	return defaultSweeperInterval
}

func (sw *Sweeper) workers() int {
	if sw.Workers > 0 {
		return sw.Workers
	}
	return defaultWorkerCount
}

// Run starts the sweeper loop. It blocks until ctx is cancelled. The
// errgroup pattern in admin/server.go starts this in a goroutine and relies
// on ctx cancellation for shutdown.
//
// The function returns nil on clean context cancellation. Any other error
// (e.g., initial store query failure on the first tick) is returned to the
// caller but does not terminate the loop — sweeper failures are transient
// and the next tick will retry.
func (sw *Sweeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(sw.interval())
	defer ticker.Stop()

	sw.logger().LogAttrs(ctx, slog.LevelInfo, "extsubmit.sweeper: started",
		slog.Duration("interval", sw.interval()),
		slog.Int("workers", sw.workers()))

	for {
		select {
		case <-ctx.Done():
			sw.logger().LogAttrs(context.Background(), slog.LevelInfo,
				"extsubmit.sweeper: shutting down")
			return nil
		case <-ticker.C:
			sw.tick(ctx)
		}
	}
}

// tick executes one sweep: queries due rows and dispatches them to the worker
// pool. It blocks until all dispatched workers have returned.
func (sw *Sweeper) tick(ctx context.Context) {
	rows, err := sw.Store.ListIdentitySubmissionsDue(ctx, sw.now())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		sw.logger().LogAttrs(ctx, slog.LevelError, "extsubmit.sweeper: list due",
			slog.String("err", err.Error()))
		return
	}

	// Update the active-identities gauge with the count of ALL OAuth-configured
	// identities, not just those due for refresh in this window. This keeps the
	// gauge accurate between refresh windows (it would otherwise read 0 when no
	// rows are due, misleading operators into thinking no OAuth identities exist).
	if n, err := sw.Store.CountOAuthIdentitySubmissions(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			sw.logger().LogAttrs(ctx, slog.LevelWarn, "extsubmit.sweeper: count oauth identities",
				slog.String("err", err.Error()))
		}
	} else {
		observe.SetExtSubActiveIdentities(n)
	}

	if len(rows) == 0 {
		return
	}

	// Dispatch rows to a bounded worker pool. The channel acts as a semaphore:
	// at most Workers goroutines run concurrently.
	sem := make(chan struct{}, sw.workers())
	var wg sync.WaitGroup
	for _, row := range rows {
		row := row // capture
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() {
				<-sem
				wg.Done()
				// Catch per-row panics so a misbehaving row cannot crash the
				// dispatcher goroutine (Phase-6 sweeper requirement).
				if r := recover(); r != nil {
					sw.logger().LogAttrs(ctx, slog.LevelError,
						"extsubmit.sweeper: worker panic (recovered)",
						slog.String("identity_id", row.IdentityID),
						slog.Any("panic", r))
				}
			}()
			sw.refreshRow(ctx, row)
		}()
	}
	wg.Wait()
}

// refreshRow performs one OAuth token refresh for a single IdentitySubmission
// row. On success it updates refresh_due_us to now+expiry-60s (architectural
// decision 3). On failure it flips state to auth-failed and emits an audit
// event; refresh_due_us is left unchanged so the sweeper retries on the next
// tick (architectural decision 3 — idempotent on failure).
func (sw *Sweeper) refreshRow(ctx context.Context, sub store.IdentitySubmission) {
	if sw.TokenRefresh == nil {
		sw.logger().LogAttrs(ctx, slog.LevelWarn,
			"extsubmit.sweeper: no TokenRefresh configured; skipping",
			slog.String("identity_id", sub.IdentityID))
		return
	}

	// Build per-row OAuthClientCredentials. In v1 the client secret is sealed
	// in OAuthClientSecretCT on the row (may be nil for public clients).
	creds := OAuthClientCredentials{
		ClientID:      sub.OAuthClientID,
		TokenEndpoint: sub.OAuthTokenEndpoint,
	}
	if len(sub.OAuthClientSecretCT) > 0 {
		cs, err := secrets.Open(sw.DataKey, sub.OAuthClientSecretCT)
		if err != nil {
			sw.recordRefreshFailure(ctx, sub, "unknown",
				fmt.Sprintf("open client secret: %s", err.Error()))
			return
		}
		creds.ClientSecret = string(cs)
		for i := range cs {
			cs[i] = 0
		}
	}

	correlationID := fmt.Sprintf("sweep:%s:%d", sub.IdentityID, sw.now().UnixMicro())

	_, err := sw.TokenRefresh.Refresh(ctx, sub, creds)
	if err != nil {
		category := classifyRefreshError(err)
		sw.recordRefreshFailure(ctx, sub, category,
			fmt.Sprintf("refresh: %s (correlation_id=%s)", err.Error(), correlationID))

		// Flip state to auth-failed, leave refresh_due_us unchanged so
		// the sweeper retries on the next tick (architectural decision 3).
		updated := sub
		updated.State = store.IdentitySubmissionStateAuthFailed
		updated.StateAt = sw.now()
		// Do not modify RefreshDue so the row reappears on the next tick.
		if err2 := sw.Store.UpsertIdentitySubmission(ctx, updated); err2 != nil {
			sw.logger().LogAttrs(ctx, slog.LevelError,
				"extsubmit.sweeper: upsert auth-failed state",
				slog.String("identity_id", sub.IdentityID),
				slog.String("err", err2.Error()))
		}

		// Audit event for sweeper-triggered state transition.
		if sw.AuditLog != nil {
			sw.AuditLog.AppendAudit(ctx,
				"submission.external.refresh_failure",
				"", // principalID: not tracked on the sub row in v1
				sub.IdentityID,
				category,
				correlationID,
			)
		}
		return
	}

	// Success: Refresher.Refresh already updated the store row with the new
	// token and refresh_due_us. We only log for operational visibility.
	sw.logger().LogAttrs(ctx, slog.LevelDebug,
		"extsubmit.sweeper: refreshed token",
		slog.String("identity_id", sub.IdentityID),
		slog.String("correlation_id", correlationID))
}

// recordRefreshFailure logs a refresh failure without credential material.
func (sw *Sweeper) recordRefreshFailure(ctx context.Context, sub store.IdentitySubmission, category, diagnostic string) {
	sw.logger().LogAttrs(ctx, slog.LevelWarn,
		"extsubmit.sweeper: token refresh failed",
		slog.String("identity_id", sub.IdentityID),
		slog.String("category", category),
		slog.String("diagnostic", diagnostic))
}

// classifyRefreshError maps a Refresher.Refresh error to a short failure
// category string for audit events. The category is safe to log — it never
// contains the token value.
func classifyRefreshError(err error) string {
	if errors.Is(err, ErrAuthFailed) {
		return "auth"
	}
	// Network errors and other transient failures.
	msg := err.Error()
	if containsAny(msg, "network", "connection refused", "no such host", "dial", "timeout", "i/o timeout") {
		return "network"
	}
	return "unknown"
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 {
			// Manual contains check (strings.Contains is fine here; avoiding
			// import of "strings" at package level to keep the file lean).
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
