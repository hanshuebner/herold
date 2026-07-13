package maillist

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// TokenPurpose scopes what a signed member token authorizes, so a token
// minted for one flow can never be replayed as another. Every herold
// mailing-list flow that must address one (list, member) pair without a
// session shares this one primitive (docs/design/server/architecture/
// 14-mailing-lists.md "VERP and the bounce processor": "these share the
// token machinery with the VERP bounce tokens"):
//
//   - TokenPurposeVERP: the per-member envelope MAIL FROM local-part
//     suffix (REQ-MLIST-50), minted here (VERPBounceAddress) and
//     verified by internal/maillist's bounce processor (REQ-MLIST-51/52).
//   - TokenPurposeUnsubscribe: the one-click List-Unsubscribe /
//     RFC 8058 token URL (REQ-MLIST-56/57). Not minted or verified by
//     this Stage-2-part-1 change; reserved for the next part of #184.
//   - TokenPurposeManage: the subscriber self-service management-page
//     token (REQ-MLIST-58/59). Reserved for a later part.
//   - TokenPurposeConfirm: the Stage-3 double opt-in subscribe/confirm
//     token (REQ-MLIST-61). Reserved for Stage 3.
type TokenPurpose byte

const (
	// tokenPurposeUnset is the zero value and never a valid Sign input;
	// guards against an accidentally-unset TokenPurpose forging a token
	// that verifies against every purpose check that also forgot to set
	// one.
	tokenPurposeUnset TokenPurpose = iota
	// TokenPurposeVERP is described above.
	TokenPurposeVERP
	// TokenPurposeUnsubscribe is described above.
	TokenPurposeUnsubscribe
	// TokenPurposeManage is described above.
	TokenPurposeManage
	// TokenPurposeConfirm is described above.
	TokenPurposeConfirm
)

// tokenKeyLabel domain-separates the maillist token-signing key derived
// by NewTokenSigner from the raw deployment data key
// (secrets.LoadDataKey): the data key's own stated purpose
// (internal/secrets' doc comment) is at-rest credential encryption, a
// different threat model than a token that travels in an email address
// or a URL. Deriving a distinct key means a future consumer of the raw
// data key cannot forge or decode maillist tokens, and vice versa.
const tokenKeyLabel = "herold-maillist-token-v1"

// ErrInvalidToken is returned by TokenSigner.Verify for any
// structurally invalid, tampered, purpose-mismatched, list-mismatched,
// or expired token. Per REQ-MLIST-52, callers MUST treat every failure
// uniformly as "no attribution": do not branch behavior on which
// specific check failed, since doing so could hand a probing attacker
// an oracle distinguishing "wrong list" from "tampered" from "expired".
var ErrInvalidToken = errors.New("maillist: invalid or expired token")

// TokenSigner mints and verifies member-scoped tokens. A token
// authenticates (purpose, list id, member id, optional expiry) via
// AEAD encryption (internal/secrets), so the member id is never visible
// in cleartext in the token (verifiable, but not member-enumerable --
// REQ-MLIST-50) and any tampering, including splicing a token minted
// for one (purpose, list, member) into a different context, is rejected
// by the authentication tag before any field is trusted.
type TokenSigner struct {
	key []byte
}

// NewTokenSigner derives a maillist-token signing key from dataKey (the
// deployment AEAD data key loaded via secrets.LoadDataKey) and returns a
// ready-to-use TokenSigner. dataKey must stay stable for a deployment's
// lifetime; rotating it invalidates every outstanding token exactly as
// it invalidates other secrets-sealed data.
func NewTokenSigner(dataKey []byte) *TokenSigner {
	mac := hmac.New(sha256.New, dataKey)
	mac.Write([]byte(tokenKeyLabel))
	return &TokenSigner{key: mac.Sum(nil)}
}

// tokenPayloadLen is the fixed-width plaintext AEAD-sealed inside a
// token: purpose (1 byte) + list id (8 bytes) + member id (8 bytes) +
// expiry as a Unix second count, 0 meaning "no expiry" (8 bytes).
const tokenPayloadLen = 1 + 8 + 8 + 8

func encodeTokenPayload(purpose TokenPurpose, listID store.MailingListID, memberID store.MailingListMemberID, expiresAt time.Time) []byte {
	buf := make([]byte, tokenPayloadLen)
	buf[0] = byte(purpose)
	binary.BigEndian.PutUint64(buf[1:9], uint64(listID))
	binary.BigEndian.PutUint64(buf[9:17], uint64(memberID))
	var exp uint64
	if !expiresAt.IsZero() {
		exp = uint64(expiresAt.Unix())
	}
	binary.BigEndian.PutUint64(buf[17:25], exp)
	return buf
}

func decodeTokenPayload(buf []byte) (purpose TokenPurpose, listID store.MailingListID, memberID store.MailingListMemberID, expiresAt time.Time, ok bool) {
	if len(buf) != tokenPayloadLen {
		return 0, 0, 0, time.Time{}, false
	}
	purpose = TokenPurpose(buf[0])
	listID = store.MailingListID(binary.BigEndian.Uint64(buf[1:9]))
	memberID = store.MailingListMemberID(binary.BigEndian.Uint64(buf[9:17]))
	if exp := binary.BigEndian.Uint64(buf[17:25]); exp != 0 {
		expiresAt = time.Unix(int64(exp), 0).UTC()
	}
	return purpose, listID, memberID, expiresAt, true
}

// Sign mints a token for (purpose, listID, memberID) that authenticates
// until expiresAt; the zero Time means no expiry, used by
// VERPBounceAddress since a VERP address must stay verifiable for as
// long as retry/backscatter traffic can still return a DSN -- an
// unbounded, unpredictable window -- unlike the bounded-TTL tokens
// TokenPurposeUnsubscribe/Manage/Confirm will use.
//
// The returned string is base64 URL-safe, unpadded: every character is
// RFC 5321 atext (safe to embed unquoted in an email local-part) and
// safe to embed in a URL path segment.
func (s *TokenSigner) Sign(purpose TokenPurpose, listID store.MailingListID, memberID store.MailingListMemberID, expiresAt time.Time) (string, error) {
	if purpose == tokenPurposeUnset {
		return "", fmt.Errorf("maillist: sign token: purpose must be set")
	}
	plain := encodeTokenPayload(purpose, listID, memberID, expiresAt)
	sealed, err := secrets.Seal(s.key, plain)
	if err != nil {
		return "", fmt.Errorf("maillist: sign token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Verify authenticates token, requiring it to have been minted for
// purpose and listID, and (when it carries an expiry) not to have
// expired as of now. Any failure -- bad encoding, failed AEAD
// authentication, a purpose or list-id mismatch, or an expired token --
// returns ErrInvalidToken and a zero memberID; callers MUST treat that
// uniformly as "no attribution" (REQ-MLIST-52) rather than inspecting
// which check failed.
func (s *TokenSigner) Verify(purpose TokenPurpose, listID store.MailingListID, token string, now time.Time) (store.MailingListMemberID, error) {
	if purpose == tokenPurposeUnset {
		return 0, ErrInvalidToken
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, ErrInvalidToken
	}
	plain, err := secrets.Open(s.key, sealed)
	if err != nil {
		return 0, ErrInvalidToken
	}
	gotPurpose, gotListID, memberID, expiresAt, ok := decodeTokenPayload(plain)
	if !ok || gotPurpose != purpose || gotListID != listID {
		return 0, ErrInvalidToken
	}
	if !expiresAt.IsZero() && now.After(expiresAt) {
		return 0, ErrInvalidToken
	}
	return memberID, nil
}
