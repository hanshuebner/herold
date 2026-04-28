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
// CapabilityCore (RFC 8620 §5.2) and registers the per-server
// capability descriptor under CapabilityPush that carries the
// deployment's VAPID applicationServerKey.
//
// RFC 8620 §5.2 places PushSubscription/get and PushSubscription/set
// in the core capability, not in a vendor-specific one. Registering
// the methods under CapabilityCore means a client that lists only
// "urn:ietf:params:jmap:core" in its "using" array can call them
// without knowing the herold-specific push URI. The vendor descriptor
// under CapabilityPush remains so the suite SPA can discover the
// deployment's VAPID applicationServerKey from the session resource.
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
	// PushSubscription/get and PushSubscription/set are Core methods per
	// RFC 8620 §5.2. Register them under CapabilityCore so the standard
	// "using": ["urn:ietf:params:jmap:core"] is sufficient.
	reg.Register(protojmap.CapabilityCore, &getHandler{h: h})
	reg.Register(protojmap.CapabilityCore, &setHandler{h: h})
	// The vendor descriptor is kept under CapabilityPush so the suite SPA
	// can read the deployment's VAPID applicationServerKey from the session
	// capabilities map without a separate endpoint.
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
// installed under capabilities["https://netzhansa.com/jmap/push"]. The
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
