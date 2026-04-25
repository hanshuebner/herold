package mailbox

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles the dependencies the per-method handlers reach
// for. One instance is constructed by Register and wrapped by each
// method-handler struct.
type handlerSet struct {
	store  store.Store
	logger *slog.Logger
	clk    clock.Clock
}

// Register installs the Mailbox/* handlers under the JMAP Mail
// capability. Called from internal/admin/server.go's StartServer
// alongside the parallel agent's Core registration. Registration is
// idempotent on the per-method axis (re-registering the same method
// panics — duplicate registration is a programmer bug).
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk}
	reg.Register(protojmap.CapabilityMail, &getHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &changesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &queryHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &queryChangesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &setHandler{h: h})
}
