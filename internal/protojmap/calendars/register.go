package calendars

import (
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultMaxOccurrencesPerExpansion bounds the per-master occurrence
// count any one CalendarEvent/query expansion produces. The binding
// draft does not mandate a specific cap; we picked 1000 because it
// covers a daily event running for two-and-a-half years inside one
// /query call without imposing a noticeable memory cost.
const DefaultMaxOccurrencesPerExpansion = 1000

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
// JMAP-Calendars binding draft mandates these knobs; defaults are
// chosen conservatively and overridable by the caller of Register.
type AccountLimits struct {
	// MaxCalendarsPerAccount caps Calendar creation per principal.
	MaxCalendarsPerAccount int `json:"maxCalendarsPerAccount"`
	// MaxEventsPerCalendar caps CalendarEvent creation per calendar.
	MaxEventsPerCalendar int `json:"maxEventsPerCalendar"`
	// MaxSizePerEventBlob caps the JSCalendar body size in bytes.
	MaxSizePerEventBlob int `json:"maxSizePerEventBlob"`
	// MaxOccurrencesPerExpansion bounds the per-master occurrence
	// count a single CalendarEvent/query expansion produces. <= 0
	// uses DefaultMaxOccurrencesPerExpansion.
	MaxOccurrencesPerExpansion int `json:"maxOccurrencesPerExpansion"`
}

// DefaultLimits returns the binding-draft conservative defaults.
func DefaultLimits() AccountLimits {
	return AccountLimits{
		MaxCalendarsPerAccount:     50,
		MaxEventsPerCalendar:       50000,
		MaxSizePerEventBlob:        256 * 1024,
		MaxOccurrencesPerExpansion: DefaultMaxOccurrencesPerExpansion,
	}
}

// Register installs the Calendar/* and CalendarEvent/* method handlers
// under the JMAP Calendars capability (REQ-PROTO-56). It also installs
// the per-account capability descriptor (maxCalendarsPerAccount,
// maxEventsPerCalendar, maxSizePerEventBlob,
// maxOccurrencesPerExpansion) per the binding draft. Called from
// internal/admin/server.go's StartServer alongside the other JMAP
// datatype Registers.
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
	if limits.MaxOccurrencesPerExpansion <= 0 {
		limits.MaxOccurrencesPerExpansion = DefaultMaxOccurrencesPerExpansion
	}
	h := &handlerSet{store: st, logger: logger, clk: clk, limits: limits}

	reg.Register(protojmap.CapabilityJMAPCalendars, &calGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, &calChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, &calSetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, &calQueryHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, calQueryChangesHandler{h: h})

	reg.Register(protojmap.CapabilityJMAPCalendars, &evGetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, &evChangesHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, &evSetHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, &evQueryHandler{h: h})
	reg.Register(protojmap.CapabilityJMAPCalendars, evQueryChangesHandler{h: h})

	// Per the JMAP-Calendars binding draft, the per-account capability
	// descriptor advertises the limits the server enforces. The
	// server-wide capability descriptor is the empty object.
	reg.RegisterAccountCapability(protojmap.CapabilityJMAPCalendars, calendarsAccountCapability{limits: limits})
}

// calendarsAccountCapability is the per-account capability descriptor
// provider. AccountCapability returns the limits struct verbatim per
// the binding draft.
type calendarsAccountCapability struct {
	limits AccountLimits
}

func (c calendarsAccountCapability) AccountCapability() any { return c.limits }
