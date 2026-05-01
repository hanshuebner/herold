package protojmap

// clientlog_meta.go injects the urn:netzhansa:params:jmap:clientlog
// capability into every JMAP session descriptor (REQ-OPS-211,
// REQ-CLOG-05, REQ-CLOG-12). The capability carries two fields the
// Suite SPA reads on every JMAP response:
//
//   - telemetry_enabled: resolved per-session boolean from the sessions
//     table (see directory.EffectiveTelemetry). The SPA uses this to gate
//     kind=log and kind=vital event emission (REQ-CLOG-06).
//
//   - livetail_until: RFC 3339 with milliseconds, present only when
//     clientlog_livetail_until_us is non-null and in the future. The SPA
//     observes it and switches to synchronous 100 ms flush mode while
//     the timestamp is in the future (REQ-CLOG-05).
//
// The lookup goes to the sessions table by session_id (ctxKeySessionID),
// not to the principals table, so the hot path is one indexed row scan.
// When no session row is present (Bearer / Basic auth, or cookie auth
// without a persisted row) the capability is still included with
// telemetry_enabled=false and no livetail_until field.
//
// The background sweeper (RunLivetailSweeper) clears expired
// clientlog_livetail_until_us values every 60 s as cosmetic hygiene;
// the real gate is the at-read-time comparison above.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// CapabilityClientLog is the URN for the Herold client-log capability.
// It is a private extension capability (not an IANA-registered one), so
// we use the urn:netzhansa: prefix per RFC 8620 §2 §8.
const CapabilityClientLog CapabilityID = "urn:netzhansa:params:jmap:clientlog"

// livetailSweeperInterval is how often the sweeper wakes to clear
// expired clientlog_livetail_until_us column values. Cosmetic only —
// the session descriptor comparison happens at read time.
const livetailSweeperInterval = 60 * time.Second

// clientLogCapability is the wire body of the clientlog capability
// descriptor in the session object. Fields named to match the SPA
// accessor names documented in REQ-CLOG-05 / REQ-CLOG-12.
type clientLogCapability struct {
	// TelemetryEnabled reflects the resolved per-session flag (from the
	// sessions row). False when the session row is absent (Bearer / Basic
	// auth with no persisted session row).
	TelemetryEnabled bool `json:"telemetry_enabled"`
	// LivetailUntil is omitted when nil. When set it is the RFC 3339
	// timestamp (with millisecond precision) after which the SPA should
	// stop synchronous flush mode. The comparison is done at read time;
	// the sweeper clears the column asynchronously for cosmetic hygiene.
	LivetailUntil *string `json:"livetail_until,omitempty"`
}

// buildClientLogCapability reads the session row identified by sessionID
// and returns the capability descriptor. The sessionID is the CSRF token
// from the suite-session cookie (ctxKeySessionID). When the session row
// is absent or the lookup fails the returned descriptor has
// TelemetryEnabled=false and no LivetailUntil.
func (s *Server) buildClientLogCapability(ctx context.Context, sessionID string) clientLogCapability {
	if sessionID == "" {
		return clientLogCapability{}
	}
	row, err := s.store.Meta().GetSession(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			loggerFromContext(ctx, s.log).Warn(
				"clientlog_meta.session_lookup_failed",
				"activity", observe.ActivityInternal,
				"session_id", sessionID,
				"err", err,
			)
		}
		return clientLogCapability{}
	}
	cap := clientLogCapability{
		TelemetryEnabled: row.ClientlogTelemetryEnabled,
	}
	if row.ClientlogLivetailUntil != nil && row.ClientlogLivetailUntil.After(s.clk.Now()) {
		ts := formatRFC3339Millis(*row.ClientlogLivetailUntil)
		cap.LivetailUntil = &ts
	}
	return cap
}

// formatRFC3339Millis formats t as RFC 3339 with millisecond precision
// (e.g. "2026-05-01T12:34:56.789Z"). The SPA expects this format per
// REQ-OPS-211.
func formatRFC3339Millis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// RunLivetailSweeper is a long-running goroutine that periodically clears
// expired clientlog_livetail_until_us values from the sessions table. It
// runs until ctx is cancelled. The interval is livetailSweeperInterval
// (60 s). Callers add this to the server lifecycle errgroup:
//
//	g.Go(func() error { return jmapSrv.RunLivetailSweeper(gctx) })
//
// The sweeper is purely cosmetic: the at-read-time comparison in
// buildClientLogCapability already gates the wire value. Clearing the
// column reduces index noise and keeps the sessions table tidy.
//
// Errors from ClearExpiredLivetail are logged and do not stop the sweeper.
func (s *Server) RunLivetailSweeper(ctx context.Context) error {
	ticker := time.NewTicker(livetailSweeperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			now := s.clk.Now()
			cleared, err := s.store.Meta().ClearExpiredLivetail(ctx, now.UnixMicro())
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.log.LogAttrs(ctx, slog.LevelWarn,
						"clientlog_meta.livetail_sweep_failed",
						slog.String("activity", observe.ActivityInternal),
						slog.String("err", err.Error()),
					)
				}
				continue
			}
			if cleared > 0 {
				s.log.LogAttrs(ctx, slog.LevelDebug,
					"clientlog_meta.livetail_sweep",
					slog.String("activity", observe.ActivityInternal),
					slog.Int("cleared", cleared),
				)
			}
		}
	}
}
