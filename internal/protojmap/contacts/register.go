package contacts

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
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
	observe.RegisterProtojmapContactsMetrics()
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, logger: logger, clk: clk, limits: limits}

	register := func(handler protojmap.MethodHandler) {
		reg.Register(protojmap.CapabilityJMAPContacts, instrumentContactsHandler(handler))
	}

	register(&abGetHandler{h: h})
	register(&abChangesHandler{h: h})
	register(&abSetHandler{h: h})
	register(&abQueryHandler{h: h})
	register(abQueryChangesHandler{h: h})

	register(&contactGetHandler{h: h})
	register(&contactChangesHandler{h: h})
	register(&contactSetHandler{h: h})
	register(&contactQueryHandler{h: h})
	register(contactQueryChangesHandler{h: h})

	// Per the JMAP-Contacts binding draft, the per-account capability
	// descriptor advertises the limits the server enforces. The
	// server-wide capability descriptor is the empty object — the
	// binding-draft's tunables all live on the per-account axis.
	reg.RegisterAccountCapability(protojmap.CapabilityJMAPContacts, contactsAccountCapability{limits: limits})
}

// instrumentContactsHandler wraps a MethodHandler so every Execute call
// increments the contacts-JMAP method counter and (on *MethodError
// return) the per-error-code counter. Names are bounded by the closed
// Register set above; error codes collapse to "unknown" if outside the
// closed JMAP error vocabulary.
func instrumentContactsHandler(inner protojmap.MethodHandler) protojmap.MethodHandler {
	return &contactsMetricHandler{inner: inner}
}

type contactsMetricHandler struct {
	inner protojmap.MethodHandler
}

func (h *contactsMetricHandler) Method() string { return h.inner.Method() }

func (h *contactsMetricHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	method := h.inner.Method()
	observe.ProtojmapContactsMethodsTotal.WithLabelValues(method).Inc()
	resp, mErr := h.inner.Execute(ctx, args)
	if mErr != nil {
		observe.ProtojmapContactsMethodErrorsTotal.WithLabelValues(method, jmapErrorCodeLabel(mErr.Type)).Inc()
	}
	return resp, mErr
}

// jmapErrorCodeLabel maps a JMAP MethodError.Type to the closed-enum
// label used by the protojmap error counters. Mirrors the chat
// package's helper; values outside the closed subset collapse to
// "unknown" so cardinality stays bounded.
func jmapErrorCodeLabel(typ string) string {
	switch typ {
	case "forbidden", "invalidArguments", "invalidProperties",
		"notFound", "serverFail", "serverPartialFail",
		"serverUnavailable", "tooLarge", "rateLimit",
		"stateMismatch", "unsupportedFilter", "unsupportedSort",
		"unknownMethod", "accountNotFound", "accountNotSupportedByMethod",
		"accountReadOnly", "anchorNotFound", "cannotCalculateChanges",
		"requestTooLarge", "willDestroy":
		return typ
	default:
		return "unknown"
	}
}

// contactsAccountCapability is the per-account capability descriptor
// provider. AccountCapability returns the limits struct verbatim per
// the binding draft.
type contactsAccountCapability struct {
	limits AccountLimits
}

func (c contactsAccountCapability) AccountCapability() any { return c.limits }
