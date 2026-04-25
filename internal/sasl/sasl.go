package sasl

import (
	"context"
	"errors"

	"github.com/hanshuebner/herold/internal/store"
)

// PrincipalID re-exports store.PrincipalID so callers that only need
// SASL do not pull in internal/store.
type PrincipalID = store.PrincipalID

// Mechanism is the per-session SASL state machine. Callers:
//
//  1. Build a mechanism via the appropriate New* constructor.
//  2. Call Start once with the client's first response (may be nil).
//  3. If done == false, keep calling Next with each subsequent client
//     response until done == true.
//  4. On done == true && err == nil, Principal() returns the
//     authenticated subject.
//
// Implementations must reject oversized client messages with
// ErrInvalidMessage and never allocate unbounded state from untrusted
// input. After a non-nil error Principal() returns ErrAuthFailed.
type Mechanism interface {
	// Name returns the wire-protocol name (e.g. "PLAIN", "SCRAM-SHA-256").
	Name() string

	// Start consumes the client's initial response. A zero-length
	// initialResponse indicates no IR was provided (the client sent only
	// the mechanism name); implementations must then emit an empty
	// challenge and wait for Next.
	Start(ctx context.Context, initialResponse []byte) (serverChallenge []byte, done bool, err error)

	// Next consumes the client's next response. Only call after Start
	// returned done == false.
	Next(ctx context.Context, clientResponse []byte) (serverChallenge []byte, done bool, err error)

	// Principal returns the authenticated subject after a successful
	// run. Returns ErrAuthFailed if Start/Next has not completed
	// successfully.
	Principal() (PrincipalID, error)
}

// Authenticator is satisfied by *directory.Directory. It is the
// password-authentication surface PLAIN, LOGIN, and SCRAM consume.
type Authenticator interface {
	Authenticate(ctx context.Context, email, password string) (PrincipalID, error)
}

// PasswordLookup is an optional adjunct to Authenticator used by SCRAM:
// SCRAM cannot operate over a plain password oracle because the server
// must know the salted password to compute ServerSignature. The
// directory-auth-implementor exposes a PasswordLookup on the directory
// side for this purpose. When a SCRAM mechanism is constructed with a
// nil lookup it returns ErrMechanismUnsupported on Start.
type PasswordLookup interface {
	// LookupSCRAMCredentials returns the stored SCRAM credentials for
	// the canonical authcid. Implementations MAY derive these from an
	// Argon2id password hash (by storing the cleartext on initial
	// password-set and discarding it) or from a dedicated SCRAM store.
	// Returns ErrAuthFailed if the user does not exist; callers must
	// treat any non-nil error as an auth failure (constant-time).
	LookupSCRAMCredentials(ctx context.Context, authcid string) (SCRAMCredentials, PrincipalID, error)
}

// SCRAMCredentials is the server-side SCRAM credential envelope:
// SaltedPassword = Hi(password, salt, iter); StoredKey = H(ClientKey);
// ServerKey = HMAC(SaltedPassword, "Server Key"). The client proves
// knowledge of ClientKey without ever sending it; we verify with
// StoredKey. See RFC 5802 §3.
type SCRAMCredentials struct {
	Salt       []byte
	Iterations int
	StoredKey  []byte
	ServerKey  []byte
}

// TokenVerifier is satisfied by *directoryoidc.RP; OAUTHBEARER and
// XOAUTH2 consume it. The access token is the Bearer value the client
// supplies in the SASL exchange; the verifier validates signature,
// issuer, audience, and expiry and returns the linked local principal.
//
// providerHint identifies which configured OIDC provider the token came
// from. The hint is mandatory in production (Phase 1 finding 9 fix:
// without it, a token issued by provider A whose `sub` happens to match
// a different principal's link to provider B would resolve to that
// principal). For OAUTHBEARER the hint comes from the gs2 host=
// advertisement; for XOAUTH2 the user= field can be paired with a
// per-deployment provider mapping. Implementations that receive an
// empty hint must reject the token.
type TokenVerifier interface {
	VerifyAccessToken(ctx context.Context, providerHint string, token string) (PrincipalID, error)
}

// Sentinel errors.
var (
	// ErrTLSRequired is returned by Start on plain-text mechanisms when
	// the caller has not marked the transport as TLS via WithTLS.
	ErrTLSRequired = errors.New("sasl: plain-text mechanism requires TLS")

	// ErrAuthFailed is returned when the supplied credential does not
	// authenticate. Plain-text paths map directory errors to this.
	ErrAuthFailed = errors.New("sasl: authentication failed")

	// ErrInvalidMessage is returned when the client response is
	// structurally invalid (bad base64, wrong number of fields, etc.).
	ErrInvalidMessage = errors.New("sasl: invalid client message")

	// ErrProtocolError is returned when the state machine is used out
	// of sequence (Next before Start, Start twice, etc.).
	ErrProtocolError = errors.New("sasl: protocol error")

	// ErrMechanismUnsupported is returned by SCRAM constructors when
	// essential collaborators (PasswordLookup) are missing.
	ErrMechanismUnsupported = errors.New("sasl: mechanism unsupported")

	// ErrChannelBindingMismatch is returned by SCRAM-*-PLUS when the
	// client-supplied channel binding does not match the server-side
	// value.
	ErrChannelBindingMismatch = errors.New("sasl: channel binding mismatch")
)

// tlsKey and channelBindingKey are context keys for carrying
// connection properties through to Start.
type tlsKey struct{}
type channelBindingKey struct{}

// WithTLS marks ctx as representing a connection that has completed TLS
// negotiation. Plain-text mechanisms refuse to run when this is absent.
// SCRAM ignores this flag but reads the channel-binding value via
// WithTLSServerEndpoint for -PLUS variants.
func WithTLS(ctx context.Context, tls bool) context.Context {
	return context.WithValue(ctx, tlsKey{}, tls)
}

// tlsPresent reports whether ctx has been marked TLS-present.
func tlsPresent(ctx context.Context) bool {
	v, ok := ctx.Value(tlsKey{}).(bool)
	return ok && v
}

// WithTLSServerEndpoint attaches the tls-server-end-point channel
// binding value (RFC 5929 §4) to ctx. SCRAM-*-PLUS requires it. The
// value is the hash of the server's TLS certificate as described in
// RFC 5929.
func WithTLSServerEndpoint(ctx context.Context, hash []byte) context.Context {
	// Copy to detach from caller's buffer.
	cb := make([]byte, len(hash))
	copy(cb, hash)
	return context.WithValue(ctx, channelBindingKey{}, cb)
}

// channelBinding returns the stored tls-server-end-point hash, or nil
// if none has been attached.
func channelBinding(ctx context.Context) []byte {
	v, _ := ctx.Value(channelBindingKey{}).([]byte)
	return v
}
