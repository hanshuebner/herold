package protocall

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Credential is one TURN credential pair plus the metadata clients
// need to use it. The shape matches RFC 8489 §9.2's long-term
// credential mechanism as adapted by coturn's REST API:
// (https://github.com/coturn/coturn/blob/master/turndb/schema.userdb.redis).
type Credential struct {
	// Username encodes the credential's expiry and the principal
	// that minted it: "<unix-expiry-seconds>:<principal-id>".
	// coturn parses the colon-separated form to validate expiry.
	Username string `json:"username"`
	// Password is base64(HMAC-SHA1(SharedSecret, Username)). coturn
	// validates by recomputing this against its `static-auth-secret`
	// — no per-credential server state is needed.
	Password string `json:"password"`
	// URIs is the list of "turn:" / "turns:" URIs the client should
	// try, in operator-supplied preference order.
	URIs []string `json:"uris"`
	// ExpiresAt is the absolute time at which the credential stops
	// being valid. Clients refresh shortly before this.
	ExpiresAt time.Time `json:"expiresAt"`
	// TTLSeconds is ExpiresAt - now at mint time, surfaced for
	// client convenience (no need to recompute against the wall
	// clock if its skew is large).
	TTLSeconds int `json:"ttlSeconds"`
}

// errTURNDisabled is returned by MintCredential when the operator has
// not configured TURN URIs. Callers translate to 503.
var errTURNDisabled = errors.New("protocall: TURN not configured")

// errTURNNoSecret is returned by MintCredential when URIs are
// configured but the shared secret is empty. This is a configuration
// bug — the HTTP handler treats it as 503 with a structured error so
// operators see the misconfiguration in client logs.
var errTURNNoSecret = errors.New("protocall: TURN shared secret missing")

// MintCredential returns a fresh TURN credential for principal,
// computed against the configured TURN shared secret per coturn's
// REST API. The TTL is opts.TURN.CredentialTTL clamped to
// MaxCredentialTTL with the DefaultCredentialTTL fallback for zero.
//
// MintCredential does not consult the rate limiter; callers (the HTTP
// handler) gate higher up. The function is therefore safe to call
// from in-process code paths that mint credentials at server-side
// init for self-tests.
func (s *Server) MintCredential(ctx context.Context, principal store.PrincipalID) (Credential, error) {
	if len(s.turn.URIs) == 0 {
		return Credential{}, errTURNDisabled
	}
	if len(s.turn.SharedSecret) == 0 {
		return Credential{}, errTURNNoSecret
	}
	ttl := s.turn.CredentialTTL
	if ttl <= 0 {
		ttl = DefaultCredentialTTL
	}
	if ttl > MaxCredentialTTL {
		ttl = MaxCredentialTTL
	}
	expiry := s.clk.Now().Add(ttl)
	username := fmt.Sprintf("%d:%d", expiry.Unix(), uint64(principal))
	mac := hmac.New(sha1.New, s.turn.SharedSecret)
	mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	uris := make([]string, len(s.turn.URIs))
	copy(uris, s.turn.URIs)
	return Credential{
		Username:   username,
		Password:   password,
		URIs:       uris,
		ExpiresAt:  expiry,
		TTLSeconds: int(ttl / time.Second),
	}, nil
}
