package vacation

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the VacationResponse/* method handlers on reg.
// Per RFC 8621 §1.1 VacationResponse lives under
// `urn:ietf:params:jmap:vacationresponse`; we register that capability
// in addition to the JMAP Mail capability so a session descriptor
// reflects support distinctly.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	sieveInterp *sieve.Interpreter,
	logger *slog.Logger,
	clk clock.Clock,
) {
	if logger == nil {
		logger = slog.Default()
	}
	_ = clk
	_ = sieveInterp // Interpreter is consulted at delivery time, not at JMAP read time.
	h := &handlerSet{store: st}
	reg.Register(capabilityVacation, getHandler{h: h})
	reg.Register(capabilityVacation, setHandler{h: h})
}

// capabilityVacation is the JMAP capability URI for VacationResponse
// (RFC 8621 §1.1: "urn:ietf:params:jmap:vacationresponse").
const capabilityVacation protojmap.CapabilityID = "urn:ietf:params:jmap:vacationresponse"
