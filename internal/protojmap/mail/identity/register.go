package identity

import (
	"context"
	"io"
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

// VerificationTrigger is the hook fired after Identity/set { create }
// commits a fresh unverified identity row (REQ-IDENT-12 / REQ-IDENT-30).
// Implementations issue the verification token, compose the verification
// email, and enqueue it. The hook runs in the JMAP request goroutine and
// is best-effort: any returned error is logged and the create call still
// succeeds — clients can re-trigger the verification round-trip via the
// REST resend endpoint (REQ-IDENT-41). Implementations MUST NOT block on
// outbound SMTP; the email composition should hand the message to the
// outbound queue and return.
//
// Task #14 owns the production composer; for task #12 the default
// trigger is a no-op so the JMAP wire ships independently of the mail
// composer.
type VerificationTrigger func(ctx context.Context, row store.JMAPIdentity) error

// DomainPolicy decides whether an Identity/set { create } may use a
// given (lowercased) external domain. The hosted-vs-external split is
// computed inside the handler from ListLocalDomains; the policy hook
// only sees external candidates (REQ-IDENT-31..). Returning true allows
// the create; returning false rejects with forbiddenFrom.
//
// Task #19 owns the [server.identity_creation].external_domains
// sysconfig schema; the default policy bound by Register permits every
// external domain ("allow_all") so the JMAP surface ships before the
// admin schema lands.
type DomainPolicy func(externalDomain string) bool

// Options bundles the optional dependencies wired by callers that have
// the full Wave-3 toolchain assembled. Zero-value Options keeps the
// historical behaviour: hosted-only domains, no verification trigger
// fires, and the logger is no-op.
type Options struct {
	// VerificationTrigger fires after Identity/set { create } commits
	// (REQ-IDENT-30). When nil, no email is enqueued; the row is still
	// committed unverified and the suite can call the REST resend
	// endpoint to start the round-trip later.
	VerificationTrigger VerificationTrigger
	// ExternalDomainPolicy reports whether the given external domain is
	// permitted by the operator's policy. When nil, external domains are
	// rejected (the historical hosted-only behaviour). Hosted domains
	// are always permitted; this hook only sees externals.
	ExternalDomainPolicy DomainPolicy
}

// Register installs the Identity datatype's method handlers on reg under
// the JMAP Submission capability per RFC 8621 §1.1. The returned *Store is
// the in-process overlay; tests use it to assert state directly.
//
// Register preserves the pre-verification call shape (no Options
// argument): the wire surface advertises verifiedAt as null on every
// row and no email composition is wired. Callers that want the full
// Wave-3 surface (verification trigger, external-domain policy) call
// RegisterWithOptions instead.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
) *Store {
	return RegisterWithOptions(reg, st, logger, clk, Options{})
}

// RegisterWithOptions is Register with the optional dependencies bound.
// The returned *Store is the same instance the package uses internally.
func RegisterWithOptions(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
	opts Options,
) *Store {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	identityStore := NewStoreWith(st, clk)
	h := &handlerSet{
		store:               st,
		identity:            identityStore,
		domains:             makeDomainsFn(st),
		logger:              logger,
		verificationTrigger: opts.VerificationTrigger,
		externalDomain:      opts.ExternalDomainPolicy,
	}
	reg.Register(capabilitySubmission, getHandler{h: h})
	reg.Register(capabilitySubmission, changesHandler{h: h})
	reg.Register(capabilitySubmission, setHandler{h: h})
	return identityStore
}
