package managedrule

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// CapabilityManagedRules is the JMAP capability URI for the ManagedRule
// datatype (Wave 3.15 / REQ-FLT-01..31). Vendor URI per the suite
// server-contract.
const CapabilityManagedRules protojmap.CapabilityID = "https://netzhansa.com/jmap/managed-rules"

// Register installs the ManagedRule/* method handlers on reg.
// Called from internal/protojmap/mail/register.go alongside other mail
// datatypes.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handlerSet{store: st, logger: logger}
	reg.Register(CapabilityManagedRules, getHandler{h: h})
	reg.Register(CapabilityManagedRules, queryHandler{h: h})
	reg.Register(CapabilityManagedRules, setHandler{h: h})
	reg.Register(CapabilityManagedRules, changesHandler{h: h})
	reg.Register(CapabilityManagedRules, threadMuteHandler{h: h})
	reg.Register(CapabilityManagedRules, threadUnmuteHandler{h: h})
	reg.Register(CapabilityManagedRules, blockedSenderSetHandler{h: h})
	reg.RegisterCapabilityDescriptor(CapabilityManagedRules, struct{}{})
}
