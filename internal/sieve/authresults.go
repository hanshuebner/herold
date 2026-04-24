package sieve

// AuthStatus is the normalized per-method verdict the Sieve spamtest
// mapping reads from the final mail-authentication result.
//
// The real type used at composition time is mailauth.AuthStatus; we do not
// import it here because internal/mailauth lands in a parallel implementation
// wave and we want internal/sieve to build independently. The enum values
// below mirror the RFC 8601 "ptype.property=result" vocabulary so the
// concrete mailauth implementation can satisfy this interface by shape.
type AuthStatus int

// Authentication outcomes that map to spamtest. Anything other than AuthPass
// counts against the message.
const (
	AuthNone AuthStatus = iota
	AuthPass
	AuthFail
	AuthSoftFail
	AuthNeutral
	AuthTempError
	AuthPermError
	AuthPolicy
)

// String returns a lower-case token suitable for logs and Sieve traces.
func (s AuthStatus) String() string {
	switch s {
	case AuthPass:
		return "pass"
	case AuthFail:
		return "fail"
	case AuthSoftFail:
		return "softfail"
	case AuthNeutral:
		return "neutral"
	case AuthTempError:
		return "temperror"
	case AuthPermError:
		return "permerror"
	case AuthPolicy:
		return "policy"
	default:
		return "none"
	}
}

// AuthResultsReader is the minimum mail-authentication result shape the
// Sieve spamtest/spamtestplus mapping needs. The concrete
// mailauth.AuthResults type (parallel wave) satisfies this interface
// naturally.
//
// Design note: the interface lives at the consumer (this package), not at
// the producer. The mail-auth-implementor does not depend on sieve, and
// sieve does not import mailauth. Wiring happens in cmd/herold.
type AuthResultsReader interface {
	// SPF returns the SPF verdict.
	SPF() AuthStatus
	// DKIM returns the best DKIM verdict across signatures present.
	DKIM() AuthStatus
	// DMARC returns the DMARC alignment verdict.
	DMARC() AuthStatus
	// ARC returns the most recent ARC-seal chain verdict.
	ARC() AuthStatus
	// FromDomain returns the bare domain of the From header's first
	// address, lower-cased. Empty string if not parseable.
	FromDomain() string
}

// nilAuthResults is used when no AuthResultsReader was supplied to the
// interpreter; every method returns the zero value (AuthNone / "") so
// spamtest treats the message as unauthenticated.
type nilAuthResults struct{}

func (nilAuthResults) SPF() AuthStatus    { return AuthNone }
func (nilAuthResults) DKIM() AuthStatus   { return AuthNone }
func (nilAuthResults) DMARC() AuthStatus  { return AuthNone }
func (nilAuthResults) ARC() AuthStatus    { return AuthNone }
func (nilAuthResults) FromDomain() string { return "" }
