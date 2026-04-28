package identity

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// capabilitySubmission is the JMAP capability URI Identity is defined under
// per RFC 8621 §1.1: "When the urn:ietf:params:jmap:submission capability is
// included [...] the JMAP Identity object (Section 6) and EmailSubmission
// (Section 7) objects are made available." Identity belongs to Submission,
// not Mail; clients calling Identity/get with using=[submission] are
// correct.
const capabilitySubmission protojmap.CapabilityID = "urn:ietf:params:jmap:submission"

// Register installs the Identity datatype's method handlers on reg under
// the JMAP Submission capability per RFC 8621 §1.1. The returned *Store is
// the in-process overlay; tests use it to assert state directly.
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
	reg.Register(capabilitySubmission, getHandler{h: h})
	reg.Register(capabilitySubmission, changesHandler{h: h})
	reg.Register(capabilitySubmission, setHandler{h: h})
	return identityStore
}
