package categorysettings

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the CategorySettings/* method handlers under
// CapabilityJMAPCategorise ("https://netzhansa.com/jmap/categorise") and
// registers the per-server capability descriptor.
//
// cat may be nil when the operator has not configured an LLM endpoint; in
// that case the capability is still registered so clients can read and
// write the category set and prompt even without a functional LLM (the
// recategorise method will return a serverFail in that scenario).
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	cat *categorise.Categoriser,
	reg2 *categorise.JobRegistry,
	logger *slog.Logger,
	clk clock.Clock,
) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{
		store:       st,
		categoriser: cat,
		jobs:        reg2,
		logger:      logger,
		clk:         clk,
	}
	reg.Register(protojmap.CapabilityJMAPCategorise, &getHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCategorise, &setHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCategorise, &recategoriseHandler{h: h})
	// The capability descriptor is the empty object; the active category set
	// is surfaced as per-account capability metadata so clients can read it
	// from the session endpoint without a separate CategorySettings/get call.
	reg.RegisterAccountCapability(protojmap.CapabilityJMAPCategorise, h)
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityJMAPCategorise, struct{}{})
}
