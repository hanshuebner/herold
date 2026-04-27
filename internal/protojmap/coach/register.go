package coach

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles the dependencies all per-method handlers need.
type handlerSet struct {
	store  store.Store
	logger *slog.Logger
	clk    clock.Clock
}

// Register installs the JMAP ShortcutCoachStat handlers under
// CapabilityShortcutCoach ("https://netzhansa.com/jmap/shortcut-coach")
// and registers the per-server capability descriptor.
//
// Idempotent on the per-method axis: re-registering a method panics
// because that is a programmer bug per protojmap.CapabilityRegistry's
// contract.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk}
	reg.Register(protojmap.CapabilityShortcutCoach, &getHandler{h: h})
	reg.Register(protojmap.CapabilityShortcutCoach, &queryHandler{h: h})
	reg.Register(protojmap.CapabilityShortcutCoach, &changesHandler{h: h})
	reg.Register(protojmap.CapabilityShortcutCoach, &setHandler{h: h})
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityShortcutCoach, struct{}{})
}
