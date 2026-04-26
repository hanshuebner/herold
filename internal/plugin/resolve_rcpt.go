package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
)

// MethodDirectoryResolveRcpt is the JSON-RPC method name dispatched to
// directory plugins that opt into RCPT-time validation
// (REQ-DIR-RCPT-01).
const MethodDirectoryResolveRcpt = "directory.resolve_rcpt"

// ResolveRcptDefaultTimeout is the default per-method timeout from
// REQ-PLUG-32 (2s). Operators may override per-plugin via
// Spec.CallTimeout.
const ResolveRcptDefaultTimeout = 2 * time.Second

// ResolveRcptHardCapTimeout is the upper bound on the per-method
// timeout. The SMTP RCPT phase cannot wait longer than this; sysconfig
// Validate refuses values above the cap (REQ-PLUG-32 / REQ-DIR-RCPT-04).
const ResolveRcptHardCapTimeout = 5 * time.Second

// InvokeResolveRcpt dispatches a directory.resolve_rcpt JSON-RPC call
// to the named plugin. It enforces the REQ-PLUG-32 timeouts on top of
// any deadline already on ctx and classifies failures so the SMTP
// layer can attribute outcomes (timeout / unavailable / transport
// error) to the right metric and audit-log bucket.
//
// The returned error wraps directory.ErrResolveRcptTimeout on deadline
// overrun and directory.ErrResolveRcptUnavailable on every other
// failure path (plugin disabled, transport error, unknown plugin,
// breaker-opened upstream). Callers test with errors.Is.
func (m *Manager) InvokeResolveRcpt(ctx context.Context, name string, req directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
	plug := m.Get(name)
	if plug == nil {
		return directory.ResolveRcptResponse{}, fmt.Errorf("%w: plugin %q not registered", directory.ErrResolveRcptUnavailable, name)
	}
	state := plug.State()
	switch state {
	case StateHealthy:
		// proceed.
	case StateDisabled, StateExited, StateStopping, StateUnhealthy:
		return directory.ResolveRcptResponse{}, fmt.Errorf("%w: plugin %q in state %s", directory.ErrResolveRcptUnavailable, name, state)
	default:
		return directory.ResolveRcptResponse{}, fmt.Errorf("%w: plugin %q not ready (state %s)", directory.ErrResolveRcptUnavailable, name, state)
	}
	mf := plug.Manifest()
	if mf == nil || !mf.HasSupport(SupportsResolveRcpt) {
		return directory.ResolveRcptResponse{}, fmt.Errorf("%w: plugin %q does not declare supports: [%q]", directory.ErrResolveRcptUnavailable, name, SupportsResolveRcpt)
	}
	timeout := plug.spec.CallTimeout
	if timeout <= 0 {
		timeout = ResolveRcptDefaultTimeout
	}
	if timeout > ResolveRcptHardCapTimeout {
		timeout = ResolveRcptHardCapTimeout
	}
	// Honour the caller's existing deadline if it's tighter.
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var resp directory.ResolveRcptResponse
	err := plug.Call(callCtx, MethodDirectoryResolveRcpt, req, &resp)
	if err != nil {
		return directory.ResolveRcptResponse{}, classifyResolveRcptErr(err)
	}
	return resp, nil
}

// classifyResolveRcptErr maps a JSON-RPC failure into the resolver's
// timeout / unavailable taxonomy. Errors from the plugin (verdict
// fields, unrecognised JSON) flow through unchanged so the resolver
// can decide whether to escalate or treat them as transport failures
// — currently any non-nil error becomes "unavailable" since the SMTP
// layer fails closed (REQ-DIR-RCPT-04).
func classifyResolveRcptErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", directory.ErrResolveRcptTimeout, err)
	}
	var pErr *Error
	if errors.As(err, &pErr) {
		switch pErr.Code {
		case ErrCodeTimeout:
			return fmt.Errorf("%w: %v", directory.ErrResolveRcptTimeout, err)
		case ErrCodeUnavailable, ErrCodeOverloaded, ErrCodeABIMismatch, ErrCodeCancelled:
			return fmt.Errorf("%w: %v", directory.ErrResolveRcptUnavailable, err)
		}
	}
	return fmt.Errorf("%w: %v", directory.ErrResolveRcptUnavailable, err)
}

// Compile-time assertion that *Manager satisfies the directory
// resolver's invoker interface.
var _ directory.ResolveRcptInvoker = (*Manager)(nil)
