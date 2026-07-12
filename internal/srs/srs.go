// Package srs implements the Sender Rewriting Scheme for herold's mail
// forwarding paths (issue #204): external-target alias forwarding (#181)
// and Sieve redirect (#63), both of which re-inject the forwarded message
// through internal/protosmtp's shared queueForward path.
//
// Forwarding a message off this server preserves the envelope MAIL FROM,
// so the receiving MTA's SPF check runs against the ORIGINAL sender's
// domain -- which does not authorize this server's IP -- and forwarded
// mail fails SPF at strict-DMARC destinations (the motivating case:
// forward a local address to Gmail). SRS rewrites the envelope
// return-path to an address in one of this server's own domains that
// encodes the original sender, so the destination's SPF check passes
// against a domain this server IS authorized to send for, while a
// bounce sent back to that address decodes to the original sender and
// is relayed or delivered locally.
//
// Address forms (RFC-adjacent to the SRS convention popularised by
// libsrs2 / postsrsd; herold's encode and decode are both implemented
// here so wire-compatibility with a third-party SRS implementation is
// not required):
//
//	SRS0=HHHH=TT=origdomain=origlocal@srs-domain
//	SRS1=HHHH=prevdomain==prevlocal@srs-domain
//
// SRS0 wraps a plain (non-SRS) return-path: HHHH is a truncated keyed
// MAC over (TT, origdomain, origlocal); TT is a 2-character base32 day
// counter used for freshness checking on decode. SRS1 wraps a
// return-path that is ALREADY an SRS0 or SRS1 address (a message being
// forwarded a second time): rather than nesting SRS0 inside SRS0 --
// which would keep growing the local-part on every hop and require
// unwinding N layers to find the original sender -- SRS1 re-signs the
// existing (domain, local-part) pair as an opaque unit. Decoding one
// SRS1 layer recovers that pair unchanged; if it happens to itself be
// another SRS address on a domain this server hosts, the caller
// (internal/protosmtp) decodes again, bounded, until it reaches a
// plain address or a foreign domain.
package srs

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SRS truncates to 4 chars regardless; matches the libsrs2/postsrsd convention. Not a cryptographic-strength requirement -- see doc comment on hash4.
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxAgeDefault is the default freshness window for an SRS0 timestamp
// (postsrsd's default MAXAGE). An SRS0 address older than this is
// rejected on decode as stale, regardless of hash validity.
const MaxAgeDefault = 21 * 24 * time.Hour

// tsBase is the modulus of the 2-character base32 day counter (32^2).
// Timestamps wrap every tsBase days (~34 years), far beyond MaxAgeDefault,
// so the wraparound arithmetic in freshness checking never ambiguates a
// real address (see decodeTimestamp / isFresh).
const tsBase = 1024

// srs0Prefix and srs1Prefix are the local-part tags recognised on
// encode and decode.
const (
	srs0Prefix = "SRS0="
	srs1Prefix = "SRS1="
)

var (
	// ErrNotSRS means the given local-part carries neither the SRS0=
	// nor SRS1= tag; it is not an SRS address at all.
	ErrNotSRS = errors.New("srs: not an SRS address")
	// ErrMalformed means the local-part has the SRS0=/SRS1= tag but its
	// internal structure does not parse.
	ErrMalformed = errors.New("srs: malformed address")
	// ErrTamper means the embedded hash does not validate against any
	// configured secret -- the address was forged or signed with a
	// secret this server no longer has.
	ErrTamper = errors.New("srs: hash validation failed")
	// ErrStale means the embedded SRS0 timestamp is older than the
	// caller's maxAge.
	ErrStale = errors.New("srs: timestamp too old")
)

// b32Alphabet is a standard RFC 4648 base32 alphabet, used only for the
// 2-character day counter (10 bits); not used for the hash itself.
const b32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// encodeTimestamp returns the 2-character base32 day counter for t,
// wrapping modulo tsBase.
func encodeTimestamp(t time.Time) string {
	days := int(t.UTC().Unix()/86400) % tsBase
	if days < 0 {
		days += tsBase
	}
	return string([]byte{b32Alphabet[days/32], b32Alphabet[days%32]})
}

// decodeTimestamp parses a 2-character base32 day counter back to its
// integer value in [0, tsBase).
func decodeTimestamp(s string) (int, error) {
	if len(s) != 2 {
		return 0, ErrMalformed
	}
	hi := strings.IndexByte(b32Alphabet, s[0])
	lo := strings.IndexByte(b32Alphabet, s[1])
	if hi < 0 || lo < 0 {
		return 0, ErrMalformed
	}
	return hi*32 + lo, nil
}

// isFresh reports whether a timestamp encoded at day dayThen is still
// within maxAge of "now", accounting for the tsBase wraparound. dayThen
// and dayNow are both in [0, tsBase).
func isFresh(dayThen, dayNow int, maxAge time.Duration) bool {
	elapsedDays := (dayNow - dayThen + tsBase) % tsBase
	maxAgeDays := int(maxAge / (24 * time.Hour))
	return elapsedDays <= maxAgeDays
}

// hash4 computes a truncated keyed MAC over parts (joined with "=") and
// returns it as 4 URL-safe base64 characters (24 bits). This matches the
// short-hash convention of libsrs2/postsrsd: SRS's threat model is
// forgery of a bounce-routing address, not confidentiality or
// high-assurance authentication, so a compact hash keeps addresses
// short at an accepted, deliberate reduction in collision resistance
// (roughly 1-in-16-million per guess). SHA-1 is used, matching the same
// upstream convention, not for its collision resistance (irrelevant at
// this truncation) but for parity with the widely deployed reference
// implementations.
func hash4(secret []byte, parts ...string) string {
	mac := hmac.New(sha1.New, secret)
	mac.Write([]byte(strings.Join(parts, "=")))
	sum := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sum)[:4]
}

// verifyHash4 reports whether want matches the hash computed with any
// of secrets over parts. Used on decode so a secret rotation does not
// invalidate addresses signed with an older (but still-known) secret.
func verifyHash4(secrets [][]byte, want string, parts ...string) bool {
	for _, secret := range secrets {
		if hmac.Equal([]byte(hash4(secret, parts...)), []byte(want)) {
			return true
		}
	}
	return false
}

// IsSRSAddress reports whether localPart carries the SRS0= or SRS1= tag.
func IsSRSAddress(localPart string) bool {
	return strings.HasPrefix(localPart, srs0Prefix) || strings.HasPrefix(localPart, srs1Prefix)
}

// EncodeSRS0 wraps a plain (non-SRS) return-path (origLocal, origDomain)
// as an SRS0 local-part, signed with signSecret and timestamped at now.
// The caller appends "@" + the chosen srs-domain (one of this server's
// own local domains).
func EncodeSRS0(signSecret []byte, now time.Time, origLocal, origDomain string) string {
	ts := encodeTimestamp(now)
	h := hash4(signSecret, ts, origDomain, origLocal)
	return fmt.Sprintf("%s%s=%s=%s=%s", srs0Prefix, h, ts, origDomain, origLocal)
}

// EncodeSRS1 wraps an existing SRS0 or SRS1 return-path (prevLocal,
// prevDomain -- the current MAIL FROM being forwarded a second time) as
// an SRS1 local-part, signed with signSecret. Re-forwarding a message
// whose return-path is already an SRS address uses this instead of
// EncodeSRS0, so the address does not grow an extra SRS0 layer on every
// hop (re #204 "double-forward" requirement).
func EncodeSRS1(signSecret []byte, prevLocal, prevDomain string) string {
	h := hash4(signSecret, prevDomain+"=="+prevLocal)
	return fmt.Sprintf("%s%s=%s==%s", srs1Prefix, h, prevDomain, prevLocal)
}

// Wrap produces the next return-path local-part for a message being
// forwarded, given the CURRENT MAIL FROM (curLocal, curDomain). When the
// current MAIL FROM is not already an SRS address, it is wrapped with
// EncodeSRS0; when it is already SRS0 or SRS1 (the message is being
// forwarded again), it is re-signed with EncodeSRS1 rather than nested.
func Wrap(signSecret []byte, now time.Time, curLocal, curDomain string) string {
	if IsSRSAddress(curLocal) {
		return EncodeSRS1(signSecret, curLocal, curDomain)
	}
	return EncodeSRS0(signSecret, now, curLocal, curDomain)
}

// Decode unwraps one layer of an SRS0 or SRS1 local-part, validating its
// hash against any of secrets and, for SRS0, its timestamp against now
// and maxAge. Returns ErrNotSRS when localPart carries neither tag.
//
// For SRS0 the returned (local, domain) is the ORIGINAL sender. For
// SRS1 the returned (local, domain) is the PREVIOUS hop's return-path,
// which may itself still be an SRS address (on this server's own
// domain, or a foreign one) -- the caller decides whether to decode
// again, deliver locally, or relay onward.
func Decode(secrets [][]byte, now time.Time, maxAge time.Duration, localPart string) (local, domain string, err error) {
	switch {
	case strings.HasPrefix(localPart, srs0Prefix):
		return decodeSRS0(secrets, now, maxAge, localPart)
	case strings.HasPrefix(localPart, srs1Prefix):
		return decodeSRS1(secrets, localPart)
	default:
		return "", "", ErrNotSRS
	}
}

func decodeSRS0(secrets [][]byte, now time.Time, maxAge time.Duration, localPart string) (local, domain string, err error) {
	rest := strings.TrimPrefix(localPart, srs0Prefix)
	// hash=ts=domain=local -- SplitN(4) so a "=" inside the original
	// local-part (rare but legal) stays attached to the local field.
	parts := strings.SplitN(rest, "=", 4)
	if len(parts) != 4 {
		return "", "", ErrMalformed
	}
	hash, ts, origDomain, origLocal := parts[0], parts[1], parts[2], parts[3]
	if origDomain == "" || origLocal == "" {
		return "", "", ErrMalformed
	}
	if !verifyHash4(secrets, hash, ts, origDomain, origLocal) {
		return "", "", ErrTamper
	}
	dayThen, terr := decodeTimestamp(ts)
	if terr != nil {
		return "", "", ErrMalformed
	}
	dayNow := int(now.UTC().Unix()/86400) % tsBase
	if !isFresh(dayThen, dayNow, maxAge) {
		return "", "", ErrStale
	}
	return origLocal, origDomain, nil
}

func decodeSRS1(secrets [][]byte, localPart string) (local, domain string, err error) {
	rest := strings.TrimPrefix(localPart, srs1Prefix)
	// hash=prevdomain==prevlocal
	eq := strings.IndexByte(rest, '=')
	if eq < 0 {
		return "", "", ErrMalformed
	}
	hash, rest2 := rest[:eq], rest[eq+1:]
	sep := strings.Index(rest2, "==")
	if sep < 0 {
		return "", "", ErrMalformed
	}
	prevDomain, prevLocal := rest2[:sep], rest2[sep+2:]
	if prevDomain == "" || prevLocal == "" {
		return "", "", ErrMalformed
	}
	if !verifyHash4(secrets, hash, prevDomain+"=="+prevLocal) {
		return "", "", ErrTamper
	}
	return prevLocal, prevDomain, nil
}
