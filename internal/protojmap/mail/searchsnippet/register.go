package searchsnippet

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the SearchSnippet/get handler under the JMAP Mail
// capability.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
) {
	if logger == nil {
		logger = slog.Default()
	}
	_ = clk
	h := &handlerSet{store: st}
	reg.Register(protojmap.CapabilityMail, getHandler{h: h})
}
