package protojmap

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// CapabilityID is a JMAP capability URI as defined in RFC 8620 §1.7.
// Concrete values include "urn:ietf:params:jmap:core" (RFC 8620 §4.1)
// and "urn:ietf:params:jmap:mail" (RFC 8621 §1).
type CapabilityID string

// Well-known capability IDs.
const (
	// CapabilityCore is the JMAP Core capability (RFC 8620 §4.1).
	CapabilityCore CapabilityID = "urn:ietf:params:jmap:core"
	// CapabilityMail is the JMAP Mail capability (RFC 8621 §1).
	// Defined here as a string constant only; protojmap does not
	// register handlers for it. The Mail-implementor agent registers
	// (capability, handlers) at server construction.
	CapabilityMail CapabilityID = "urn:ietf:params:jmap:mail"
	// CapabilityMailSnooze is the JMAP Mail snooze extension
	// (REQ-PROTO-49 / IETF draft "JMAP Snooze"). The snooze surface
	// is property-only — snoozedUntil on Email and the "$snoozed"
	// keyword — so the capability advertises no methods of its own;
	// the dispatcher checks the descriptor for client capability
	// detection.
	CapabilityMailSnooze CapabilityID = "urn:ietf:params:jmap:mail:snooze"
	// CapabilityJMAPSieve is the JMAP Sieve datatype capability
	// (REQ-PROTO-53 / RFC 9007). Implemented under
	// internal/protojmap/mail/sieve; wraps the existing Sieve parser
	// + interpreter so JMAP clients can manage scripts without
	// speaking ManageSieve.
	CapabilityJMAPSieve CapabilityID = "urn:ietf:params:jmap:sieve"
)

// MethodHandler resolves and executes one method call within a JMAP
// request (e.g. "Email/get", "Mailbox/changes"). Implementations live
// in their datatype's package and are registered with the
// CapabilityRegistry at server construction time. The dispatch core
// does not enumerate concrete types.
type MethodHandler interface {
	// Method returns the JMAP method name this handler serves; e.g.
	// "Core/echo" or "Mailbox/changes". The string is the registry's
	// primary key.
	Method() string
	// Execute handles one method call. ctx carries the authenticated
	// principal (PrincipalFromContext) and the calling request's
	// deadline. args is the raw JSON object the client supplied; the
	// handler unmarshals into its own typed shape. The returned
	// response is JSON-marshaled and embedded in the response
	// envelope; an &MethodError causes the dispatch core to emit an
	// "error" entry in its place per RFC 8620 §3.6.2.
	Execute(ctx context.Context, args json.RawMessage) (response any, err *MethodError)
}

// AccountCapabilityProvider is implemented by capability handlers that
// need to expose a per-account capability descriptor in the session
// endpoint (RFC 8620 §2 "accounts.<id>.accountCapabilities"). The
// returned object is JSON-marshaled verbatim. Capabilities that do not
// implement this interface contribute an empty object {} to the
// per-account map. Wave 2.2 datatype packages implement this on a
// dedicated capability-meta struct and register it via
// Registry().RegisterAccountCapability(cap, provider).
type AccountCapabilityProvider interface {
	AccountCapability() any
}

// CapabilityRegistry tracks installed JMAP datatype handlers. The
// session descriptor and method dispatcher consult it; concrete types
// are not named in dispatch code paths. Per
// docs/architecture/03-protocol-architecture.md §JMAP "Capability and
// account registration" this is the forward-compat surface that future
// datatypes plug into.
//
// Safe for concurrent use; Register / Resolve / Capabilities /
// AccountCapabilities can be called from any goroutine. The dispatcher
// reads it on every POST /jmap request.
type CapabilityRegistry struct {
	mu sync.RWMutex

	// methods maps full method name ("Email/get") to handler.
	methods map[string]MethodHandler
	// methodToCap maps full method name to its owning capability so
	// the dispatcher can validate "using" against the actual set of
	// capabilities the request reached for.
	methodToCap map[string]CapabilityID
	// caps is the registered capability set.
	caps map[CapabilityID]struct{}
	// capDescriptors is the per-capability descriptor object (RFC 8620
	// §2 "capabilities.<id>"); populated by handlers via
	// installCapabilityDescriptor or RegisterCapabilityDescriptor.
	capDescriptors map[CapabilityID]any
	// acctDescriptors is the per-capability per-account descriptor
	// object (RFC 8620 §2 "accountCapabilities.<id>"); populated by
	// RegisterAccountCapability.
	acctDescriptors map[CapabilityID]AccountCapabilityProvider
}

// NewCapabilityRegistry returns an empty registry.
func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{
		methods:         make(map[string]MethodHandler),
		methodToCap:     make(map[string]CapabilityID),
		caps:            make(map[CapabilityID]struct{}),
		capDescriptors:  make(map[CapabilityID]any),
		acctDescriptors: make(map[CapabilityID]AccountCapabilityProvider),
	}
}

// Register installs h under cap. The method name h.Method() becomes
// the dispatcher's lookup key. Registering twice with the same method
// name is a programmer error and panics — JMAP method names are
// globally unique across capabilities.
func (r *CapabilityRegistry) Register(cap CapabilityID, h MethodHandler) {
	if h == nil {
		panic("protojmap: Register nil handler")
	}
	method := h.Method()
	if method == "" {
		panic("protojmap: handler returned empty Method()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.methods[method]; dup {
		panic("protojmap: duplicate method handler: " + method)
	}
	r.methods[method] = h
	r.methodToCap[method] = cap
	r.caps[cap] = struct{}{}
}

// RegisterCapabilityDescriptor associates a JSON-marshalable descriptor
// with cap; the session endpoint embeds it under capabilities.<cap>.
// The Core capability registers its own descriptor automatically.
// Datatype packages call this with their per-capability metadata
// struct (e.g. the Mail capability's emailQuerySortOptions).
func (r *CapabilityRegistry) RegisterCapabilityDescriptor(cap CapabilityID, descriptor any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caps[cap] = struct{}{}
	r.capDescriptors[cap] = descriptor
}

// installCapabilityDescriptor is the package-internal alias for
// RegisterCapabilityDescriptor used by NewServer. It exists to keep
// the public surface read-only with respect to Core's own descriptor.
func (r *CapabilityRegistry) installCapabilityDescriptor(cap CapabilityID, descriptor any) {
	r.RegisterCapabilityDescriptor(cap, descriptor)
}

// RegisterAccountCapability associates a per-account descriptor
// provider with cap. The session endpoint calls AccountCapability()
// for each account when assembling the descriptor. Datatype packages
// register a provider whose AccountCapability returns the
// per-principal capability object (e.g. mayCreateTopLevelMailbox for
// JMAP Mail).
func (r *CapabilityRegistry) RegisterAccountCapability(cap CapabilityID, provider AccountCapabilityProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caps[cap] = struct{}{}
	r.acctDescriptors[cap] = provider
}

// Resolve returns the handler registered for method, or (nil, false)
// when no such handler exists.
func (r *CapabilityRegistry) Resolve(method string) (MethodHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.methods[method]
	return h, ok
}

// CapabilityFor returns the CapabilityID that owns method, or
// ("", false) when method is unregistered.
func (r *CapabilityRegistry) CapabilityFor(method string) (CapabilityID, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cap, ok := r.methodToCap[method]
	return cap, ok
}

// Capabilities returns the session-descriptor "capabilities" object: a
// map from capability URI to its descriptor (or the empty object {}
// when the capability has no per-server descriptor). Safe to call
// concurrently with Register; the returned map is a fresh copy.
func (r *CapabilityRegistry) Capabilities() map[CapabilityID]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[CapabilityID]any, len(r.caps))
	for cap := range r.caps {
		if d, ok := r.capDescriptors[cap]; ok {
			out[cap] = d
		} else {
			out[cap] = struct{}{}
		}
	}
	return out
}

// AccountCapabilities returns the per-account capability descriptor
// map for one account. Capabilities without a registered provider
// contribute the empty object {}. Safe to call concurrently with
// Register; the returned map is a fresh copy. The per-account
// capability provider is invoked once per call so providers may
// inspect ctx (for example to read principal-scoped flags).
func (r *CapabilityRegistry) AccountCapabilities() map[CapabilityID]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[CapabilityID]any, len(r.caps))
	for cap := range r.caps {
		if p, ok := r.acctDescriptors[cap]; ok {
			out[cap] = p.AccountCapability()
		} else {
			out[cap] = struct{}{}
		}
	}
	return out
}

// HasCapability reports whether cap is registered.
func (r *CapabilityRegistry) HasCapability(cap CapabilityID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.caps[cap]
	return ok
}

// SortedCapabilityIDs returns the registered capability URIs in
// ascending lexical order. Used by the session-descriptor "state"
// hash to make the hash stable across request orderings.
func (r *CapabilityRegistry) SortedCapabilityIDs() []CapabilityID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CapabilityID, 0, len(r.caps))
	for cap := range r.caps {
		out = append(out, cap)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

// coreEchoHandler implements RFC 8620 §4 Core/echo: the canonical
// no-op method that returns its arguments verbatim. We register one
// instance from NewServer.
type coreEchoHandler struct{}

func (coreEchoHandler) Method() string { return "Core/echo" }

func (coreEchoHandler) Execute(_ context.Context, args json.RawMessage) (any, *MethodError) {
	// RFC 8620 §4: "echoes back any data sent". We return the args
	// JSON object unchanged. json.RawMessage marshals as the raw bytes
	// (a valid JSON value), so the response embedding is a verbatim
	// echo.
	if len(strings.TrimSpace(string(args))) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return args, nil
}
