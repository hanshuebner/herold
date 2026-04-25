package contacts

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
	limits AccountLimits
}

// AccountLimits is the per-account capability descriptor body. The
// JMAP-Contacts binding draft mandates these three knobs; defaults are
// chosen conservatively and overridable by the caller of Register.
type AccountLimits struct {
	// MaxAddressBooksPerAccount caps AddressBook creation per principal.
	MaxAddressBooksPerAccount int `json:"maxAddressBooksPerAccount"`
	// MaxContactsPerAddressBook caps Contact creation per address book.
	MaxContactsPerAddressBook int `json:"maxContactsPerAddressBook"`
	// MaxSizePerContactBlob caps the JSContact body size in bytes.
	MaxSizePerContactBlob int `json:"maxSizePerContactBlob"`
}

// DefaultLimits returns the binding-draft conservative defaults.
func DefaultLimits() AccountLimits {
	return AccountLimits{
		MaxAddressBooksPerAccount: 50,
		MaxContactsPerAddressBook: 50000,
		MaxSizePerContactBlob:     256 * 1024,
	}
}

// Register installs the AddressBook/* and Contact/* method handlers
// under the JMAP Contacts capability (REQ-PROTO-55). It also installs
// the per-account capability descriptor (maxAddressBooksPerAccount,
// maxContactsPerAddressBook, maxSizePerContactBlob) per the binding
// draft. Called from internal/admin/server.go's StartServer alongside
// the other JMAP datatype Registers.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	RegisterWithLimits(reg, st, logger, clk, DefaultLimits())
}

// RegisterWithLimits is Register with explicit per-account limits;
// useful for tests and operator-tuned production deployments.
func RegisterWithLimits(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
	limits AccountLimits,
) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk, limits: limits}

	reg.Register(protojmap.CapabilityJMAPContacts, &abGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, &abChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, &abSetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, &abQueryHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, abQueryChangesHandler{h: h})

	reg.Register(protojmap.CapabilityJMAPContacts, &contactGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, &contactChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, &contactSetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, &contactQueryHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPContacts, contactQueryChangesHandler{h: h})

	// Per the JMAP-Contacts binding draft, the per-account capability
	// descriptor advertises the limits the server enforces. The
	// server-wide capability descriptor is the empty object — the
	// binding-draft's tunables all live on the per-account axis.
	reg.RegisterAccountCapability(protojmap.CapabilityJMAPContacts, contactsAccountCapability{limits: limits})
}

// contactsAccountCapability is the per-account capability descriptor
// provider. AccountCapability returns the limits struct verbatim per
// the binding draft.
type contactsAccountCapability struct {
	limits AccountLimits
}

func (c contactsAccountCapability) AccountCapability() any { return c.limits }
