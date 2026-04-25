package sieve

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the JMAP Sieve datatype handlers
// (urn:ietf:params:jmap:sieve / RFC 9007). Called from
// internal/protojmap/mail/register.go alongside the other Wave 2.5
// datatypes. The capability is advertised as an empty descriptor —
// RFC 9007 reserves no per-server fields beyond the handler set.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk}
	reg.Register(protojmap.CapabilityJMAPSieve, getHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPSieve, setHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPSieve, validateHandler{h: h})
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityJMAPSieve, struct{}{})
}
