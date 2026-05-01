package directory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// EffectiveTelemetry resolves the effective clientlog telemetry flag for a
// principal. The principal's own ClientlogTelemetryEnabled field takes
// precedence when non-nil; otherwise defaultEnabled (the value from
// [clientlog.defaults].telemetry_enabled in system.toml, defaulting to true
// until task #8 wires the sysconfig block) is returned.
//
// Errors (`kind=error` events) always flow regardless of this flag — it gates
// only `kind=log` and `kind=vital` (REQ-OPS-208, REQ-CLOG-06).
func EffectiveTelemetry(p store.Principal, defaultEnabled bool) bool {
	if p.ClientlogTelemetryEnabled != nil {
		return *p.ClientlogTelemetryEnabled
	}
	return defaultEnabled
}

// SetTelemetry persists the per-user clientlog telemetry override for
// principal pid. A nil enabled clears the override (NULL column), causing
// EffectiveTelemetry to fall back to the system default on the next
// resolution. The call is audit-logged per REQ-ADM-300.
//
// The associated session row (if sessionID is non-empty) is also updated so
// TelemetryGate.IsEnabled returns the new value immediately without waiting
// for the next session creation or refresh.
func (d *Directory) SetTelemetry(ctx context.Context, pid PrincipalID, enabled *bool, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: principal %d", ErrNotFound, pid)
		}
		return fmt.Errorf("directory: load principal: %w", err)
	}

	// Record before-value for the audit log.
	var beforeStr string
	if p.ClientlogTelemetryEnabled != nil {
		if *p.ClientlogTelemetryEnabled {
			beforeStr = "true"
		} else {
			beforeStr = "false"
		}
	} else {
		beforeStr = "null"
	}

	p.ClientlogTelemetryEnabled = enabled
	if err := d.meta.UpdatePrincipal(ctx, p); err != nil {
		return fmt.Errorf("directory: update principal telemetry: %w", err)
	}

	var afterStr string
	if enabled != nil {
		if *enabled {
			afterStr = "true"
		} else {
			afterStr = "false"
		}
	} else {
		afterStr = "null"
	}
	d.audit(ctx, pid, "principal.clientlog_telemetry.set",
		slog.String("before", beforeStr),
		slog.String("after", afterStr),
	)

	// If a session ID is provided, propagate the resolved flag to the session
	// row so the ingest handler sees the new value immediately.
	if sessionID != "" && enabled != nil {
		if err := d.meta.UpdateSessionTelemetry(ctx, sessionID, *enabled); err != nil {
			// Non-fatal: the session may have expired or not yet been
			// created. Log at debug and continue.
			d.logger.LogAttrs(ctx, slog.LevelDebug, "directory.telemetry.session_update_skipped",
				slog.String("activity", observe.ActivityInternal),
				slog.Uint64("principal_id", uint64(pid)),
				slog.String("session_id", sessionID),
				slog.String("err", err.Error()),
			)
		}
	}

	return nil
}

// TelemetryGate provides a zero-dependency way for the clientlog ingest
// handler (task #4) to ask "is telemetry enabled for session S?" without
// importing the directory package's full surface or performing a principal
// lookup on every request.
//
// The gate is backed by the sessions table (migration 0039): the effective
// flag is stamped there at session creation / refresh, so IsEnabled is a
// single indexed lookup.
type TelemetryGate struct {
	meta store.Metadata
}

// NewTelemetryGate returns a TelemetryGate backed by meta.
func NewTelemetryGate(meta store.Metadata) *TelemetryGate {
	return &TelemetryGate{meta: meta}
}

// IsEnabled reports whether clientlog behavioural telemetry is enabled for
// the session identified by sessionID. It returns (false, ErrNotFound) when
// the session row does not exist (the caller should treat missing sessions as
// having telemetry disabled by default for defence-in-depth).
func (g *TelemetryGate) IsEnabled(ctx context.Context, sessionID string) (bool, error) {
	row, err := g.meta.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, fmt.Errorf("directory: TelemetryGate.IsEnabled: %w", ErrNotFound)
		}
		return false, fmt.Errorf("directory: TelemetryGate.IsEnabled: %w", err)
	}
	return row.ClientlogTelemetryEnabled, nil
}
