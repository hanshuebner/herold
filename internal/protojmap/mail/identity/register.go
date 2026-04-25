package identity

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the Identity datatype's method handlers on reg
// under the JMAP Mail capability (RFC 8621 §1: identities live under
// `urn:ietf:params:jmap:mail`). The returned *Store is the in-process
// overlay; tests use it to assert state directly.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
) *Store {
	_ = logger // Identity handlers do not log today; parameter kept
	// for signature parity with sibling Register entry points.
	identityStore := NewStoreWith(st, clk)
	h := &handlerSet{
		store:    st,
		identity: identityStore,
		domains:  makeDomainsFn(st),
	}
	reg.Register(protojmap.CapabilityMail, getHandler{h: h})
	reg.Register(protojmap.CapabilityMail, changesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, setHandler{h: h})
	return identityStore
}
