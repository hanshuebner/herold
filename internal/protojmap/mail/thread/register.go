package thread

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the Thread/* method handlers on reg under the JMAP
// Mail capability. The clock and logger arguments are accepted to match
// the parallel agents' Register signatures, even though Thread carries
// no clock-dependent state of its own.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
) {
	if logger == nil {
		logger = slog.Default()
	}
	_ = clk // unused; signature parity with sibling Registers
	h := &handlerSet{store: st}
	reg.Register(protojmap.CapabilityMail, getHandler{h: h})
	reg.Register(protojmap.CapabilityMail, changesHandler{h: h})
}
