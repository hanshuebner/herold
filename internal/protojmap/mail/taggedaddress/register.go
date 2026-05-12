package taggedaddress

import (
	"io"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the TaggedAddressFilter datatype's method handlers
// under the CapabilityTaggedAddresses ("https://netzhansa.com/jmap/
// tagged-addresses") capability and registers the per-server capability
// descriptor. The caller MUST gate this Register call behind the
// [server.tagged_addresses].enabled sysconfig flag (REQ-TAG-70); the
// admin server is the single wiring point.
//
// The caps closure is read on every TaggedAddressFilter/set request so
// a SIGHUP-driven cap adjustment takes effect without a restart.
// Passing nil falls back to the documented defaults
// (MaxTaggedAddressFiltersPerPrincipal / MaxTaggedAddressDismissalsPerPrincipal).
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
	caps CapsProvider,
) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{
		store:  st,
		clk:    clk,
		logger: logger,
		caps:   caps,
	}
	reg.Register(protojmap.CapabilityTaggedAddresses, getHandler{h: h})
	reg.Register(protojmap.CapabilityTaggedAddresses, changesHandler{h: h})
	reg.Register(protojmap.CapabilityTaggedAddresses, setHandler{h: h})
	// The capability descriptor is the empty object; per-principal caps
	// live in sysconfig and are read at request time.
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityTaggedAddresses, struct{}{})
}
