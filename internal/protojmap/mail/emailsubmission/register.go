package emailsubmission

import (
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/identity"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// capabilitySubmission is the JMAP capability URI for EmailSubmission
// (RFC 8621 §1.1: "urn:ietf:params:jmap:submission").
const capabilitySubmission protojmap.CapabilityID = "urn:ietf:params:jmap:submission"

// Register installs the EmailSubmission/* handlers on reg under the
// dedicated submission capability. The submitter argument is typically
// a *queue.Queue from the boot path; tests inject a fake.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	q *queue.Queue,
	idStore *identity.Store,
	logger *slog.Logger,
	clk clock.Clock,
) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	registerWith(reg, st, queueAsSubmitter{q: q}, identityAdapter{store: idStore}, clk)
}

// RegisterWith is the test-friendly variant that accepts injected
// Submitter and IdentityResolver. Production code calls Register.
func RegisterWith(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	sub Submitter,
	idr IdentityResolver,
	clk clock.Clock,
) {
	registerWith(reg, st, sub, idr, clk)
}

func registerWith(reg *protojmap.CapabilityRegistry, st store.Store, sub Submitter, idr IdentityResolver, clk clock.Clock) {
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{
		store:    st,
		queue:    sub,
		clk:      clk,
		identity: idr,
	}
	reg.Register(capabilitySubmission, getHandler{h: h})
	reg.Register(capabilitySubmission, changesHandler{h: h})
	reg.Register(capabilitySubmission, queryHandler{h: h})
	reg.Register(capabilitySubmission, queryChangesHandler{h: h})
	reg.Register(capabilitySubmission, setHandler{h: h})
}

// identityAdapter wraps the identity package's Store to fit the local
// IdentityResolver interface. Defining the adapter here keeps the
// emailsubmission package's import surface narrow — the identity
// package only depends on store/clock, not on the queue.
type identityAdapter struct {
	store *identity.Store
}

func (a identityAdapter) IdentityEmail(ctx context.Context, p store.Principal, id string) (string, bool) {
	if a.store == nil {
		return "", false
	}
	for _, rec := range a.store.Snapshot(ctx, p) {
		if rec.JMAPID() == id {
			return rec.Email(), true
		}
	}
	return "", false
}
