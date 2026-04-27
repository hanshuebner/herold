package push

import (
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/vapid"
)

// VerificationPinger is the narrow interface the JMAP push handlers
// consume to fire the RFC 8620 §7.2 verification ping after a
// successful PushSubscription/set { create }. The real implementation
// is *webpush.Dispatcher; tests pass a stub.
//
// The interface lives here (not in webpush) so the JMAP handler
// package does not import webpush directly — that would create a
// dependency cycle when admin/server.go wires both together.
type VerificationPinger interface {
	SendVerificationPing(ctx context.Context, sub store.PushSubscription) error
}

// handlerSet bundles the dependencies the per-method handlers reach
// for. One instance is constructed by Register and wrapped by each
// method-handler struct.
type handlerSet struct {
	store    store.Store
	logger   *slog.Logger
	clk      clock.Clock
	vapid    *vapid.Manager
	verifier VerificationPinger
}

// Register installs the JMAP PushSubscription handlers under
// CapabilityPush ("https://tabard.dev/jmap/push") and registers the
// per-server capability descriptor that carries the deployment's
// VAPID applicationServerKey. When vm is nil or unconfigured the
// capability is still advertised (so clients can detect that herold
// supports the surface) but applicationServerKey is omitted; the suite
// then surfaces a "push not available on this deployment" UI.
//
// Idempotent on the per-method axis: re-registering a method panics
// because that is a programmer bug per protojmap.CapabilityRegistry's
// contract.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, vm *vapid.Manager, verifier VerificationPinger, logger *slog.Logger, clk clock.Clock) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk, vapid: vm, verifier: verifier}
	reg.Register(protojmap.CapabilityPush, &getHandler{h: h})
	reg.Register(protojmap.CapabilityPush, &setHandler{h: h})
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityPush, buildCapabilityDescriptor(vm))
}

// pushCapability is the JSON-marshalable body of the JMAP push
// capability descriptor. RFC 8292 names the field
// "applicationServerKey" — the same name pushManager.subscribe()
// uses on the browser side, so a suite-shaped client can read it
// directly without translation.
//
// The field is omitted (omitempty) when no VAPID key is configured;
// clients then surface "push not available" rather than try to
// register against a missing key.
type pushCapability struct {
	ApplicationServerKey string `json:"applicationServerKey,omitempty"`
}

// buildCapabilityDescriptor returns the immutable descriptor object
// installed under capabilities["https://tabard.dev/jmap/push"]. The
// session endpoint marshals it verbatim. We snapshot the public key
// at server-construction time; rotation requires a server restart so
// the descriptor surfaces the new key.
func buildCapabilityDescriptor(vm *vapid.Manager) pushCapability {
	if vm == nil || !vm.Configured() {
		return pushCapability{}
	}
	pub, err := vm.PublicKeyB64URL()
	if err != nil {
		return pushCapability{}
	}
	return pushCapability{ApplicationServerKey: pub}
}
