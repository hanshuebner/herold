package vapid

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// MaxJWTExpiry is the upper bound on the "exp" claim relative to "now"
// per common push-gateway implementations (FCM, Mozilla autopush,
// Apple Web Push). RFC 8292 §2 leaves the cap up to operators; 24 h
// is the documented maximum every gateway accepts.
const MaxJWTExpiry = 24 * time.Hour

// SignVAPIDJWT mints an RFC 8292 §2 VAPID JWT for the given audience
// (the origin of the push endpoint URL, e.g. "https://fcm.googleapis.com")
// using the manager's loaded P-256 ECDSA private key. expiresAt is
// capped at now+24h per push-gateway practice; subject must be a
// "mailto:operator@example" or "https://operator.example" URI per
// RFC 8292 §2.1.
//
// Returns the dot-separated header.payload.signature JWT string.
//
// "now" is supplied by the caller (so tests pass a deterministic
// instant). Production callers pass clock.Now().
func (m *Manager) SignVAPIDJWT(audience string, now, expiresAt time.Time, subject string) (string, error) {
	if m == nil || m.kp == nil {
		return "", ErrNotConfigured
	}
	return signVAPIDJWT(m.kp.Private, audience, now, expiresAt, subject)
}

// signVAPIDJWT is the underlying implementation; exported via the
// Manager method so callers reach it through the keymgmt API rather
// than re-parsing the private key.
func signVAPIDJWT(priv *ecdsa.PrivateKey, audience string, now, expiresAt time.Time, subject string) (string, error) {
	if priv == nil {
		return "", ErrNotConfigured
	}
	if err := validateAudience(audience); err != nil {
		return "", err
	}
	if err := validateSubject(subject); err != nil {
		return "", err
	}
	// Cap expiry at now+MaxJWTExpiry. RFC 8292 §2 does not specify a
	// ceiling but every shipping push gateway rejects tokens whose
	// exp - iat exceeds 24h.
	if expiresAt.IsZero() {
		expiresAt = now.Add(MaxJWTExpiry)
	}
	if expiresAt.Sub(now) > MaxJWTExpiry {
		expiresAt = now.Add(MaxJWTExpiry)
	}
	if !expiresAt.After(now) {
		return "", fmt.Errorf("vapid: expiresAt %s must be after now %s",
			expiresAt.Format(time.RFC3339), now.Format(time.RFC3339))
	}

	header := struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
	}{Typ: "JWT", Alg: "ES256"}

	claims := struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{Aud: audience, Exp: expiresAt.Unix(), Sub: subject}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("vapid: marshal header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("vapid: marshal claims: %w", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) +
		"." + base64.RawURLEncoding.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", fmt.Errorf("vapid: ecdsa sign: %w", err)
	}
	// JWS ES256 (RFC 7518 §3.4): the signature is the fixed-length
	// concatenation R || S, each padded to 32 bytes (the P-256 group
	// order length). math/big.FillBytes pads to the requested length.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// AudienceFromEndpoint extracts the RFC 8292 §2 "aud" value from a
// push-endpoint URL: scheme + "://" + host (no path, no query, no
// fragment). Returns an error on malformed URLs.
func AudienceFromEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("vapid: parse endpoint: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("vapid: endpoint %q missing scheme or host", endpoint)
	}
	return u.Scheme + "://" + u.Host, nil
}

// VerifyVAPIDJWT is the inverse of SignVAPIDJWT used by tests (and by
// the dispatcher's e2e fake gateway): it parses token, checks the
// signature against pub, and returns the decoded claims plus the
// alg-checked header. Production code never verifies its own
// signatures — push gateways do.
func VerifyVAPIDJWT(token string, pub *ecdsa.PublicKey) (header, claims map[string]any, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil, fmt.Errorf("vapid: jwt has %d parts (want 3)", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("vapid: header b64: %w", err)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("vapid: claims b64: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, nil, fmt.Errorf("vapid: sig b64: %w", err)
	}
	if len(sig) != 64 {
		return nil, nil, fmt.Errorf("vapid: signature length %d (want 64)", len(sig))
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, nil, fmt.Errorf("vapid: parse header: %w", err)
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, nil, fmt.Errorf("vapid: parse claims: %w", err)
	}
	if alg, _ := header["alg"].(string); alg != "ES256" {
		return nil, nil, fmt.Errorf("vapid: alg %q (want ES256)", alg)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return nil, nil, fmt.Errorf("vapid: signature invalid")
	}
	return header, claims, nil
}

func validateAudience(aud string) error {
	if aud == "" {
		return fmt.Errorf("vapid: audience must not be empty")
	}
	u, err := url.Parse(aud)
	if err != nil {
		return fmt.Errorf("vapid: audience parse: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("vapid: audience scheme %q (want https or http)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("vapid: audience missing host")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("vapid: audience must be origin-only (got path %q)", u.Path)
	}
	return nil
}

func validateSubject(sub string) error {
	if sub == "" {
		return fmt.Errorf("vapid: subject must not be empty")
	}
	if strings.HasPrefix(sub, "mailto:") {
		return nil
	}
	if strings.HasPrefix(sub, "https://") || strings.HasPrefix(sub, "http://") {
		return nil
	}
	return fmt.Errorf("vapid: subject %q must be a mailto: or https:// URI", sub)
}
