package maillist_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

func testDataKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

// TestTokenSigner_RoundTrip exercises the basic sign/verify contract:
// what Sign produces, Verify recovers exactly.
func TestTokenSigner_RoundTrip(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	tok, err := ts.Sign(maillist.TokenPurposeVERP, 7, 42, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if tok == "" {
		t.Fatalf("Sign returned empty token")
	}
	got, err := ts.Verify(maillist.TokenPurposeVERP, 7, tok, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != 42 {
		t.Fatalf("Verify member id = %d, want 42", got)
	}
}

// TestTokenSigner_MemberIDNotInCleartext exercises REQ-MLIST-50: the
// token is verifiable but not member-enumerable -- the decimal member
// id must not appear anywhere in the token string.
func TestTokenSigner_MemberIDNotInCleartext(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	tok, err := ts.Sign(maillist.TokenPurposeVERP, 1, 123456789, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if strings.Contains(tok, "123456789") {
		t.Fatalf("token %q leaks the raw member id in cleartext", tok)
	}
}

// TestTokenSigner_TamperedTokenFails exercises the AEAD authentication
// guarantee: flipping any byte of a valid token must make Verify fail.
func TestTokenSigner_TamperedTokenFails(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	tok, err := ts.Sign(maillist.TokenPurposeVERP, 3, 9, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	tampered := []byte(tok)
	// Flip a bit in the middle of the token (inside the ciphertext, not
	// just base64 padding, since RawURLEncoding has none).
	mid := len(tampered) / 2
	tampered[mid] ^= 0x01
	if _, err := ts.Verify(maillist.TokenPurposeVERP, 3, string(tampered), time.Now()); err == nil {
		t.Fatalf("Verify accepted a tampered token")
	}
}

// TestTokenSigner_TruncatedTokenFails exercises malformed-input
// handling: a truncated token must fail closed, not panic.
func TestTokenSigner_TruncatedTokenFails(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	tok, err := ts.Sign(maillist.TokenPurposeVERP, 3, 9, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	truncated := tok[:len(tok)/2]
	if _, err := ts.Verify(maillist.TokenPurposeVERP, 3, truncated, time.Now()); err == nil {
		t.Fatalf("Verify accepted a truncated token")
	}
}

// TestTokenSigner_MemberANeverVerifiesAsMemberB exercises the
// cross-member isolation the bounce processor's attribution depends on:
// member A's token always recovers exactly member A's id, never member
// B's, and using member B's token to "confirm" member A is rejected
// outright by TamperedTokenFails-style substitution being meaningless
// here (each member's token is independently signed).
func TestTokenSigner_MemberANeverVerifiesAsMemberB(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	const listID store.MailingListID = 1
	const memberA store.MailingListMemberID = 100
	const memberB store.MailingListMemberID = 200

	tokA, err := ts.Sign(maillist.TokenPurposeVERP, listID, memberA, time.Time{})
	if err != nil {
		t.Fatalf("Sign(A): %v", err)
	}
	tokB, err := ts.Sign(maillist.TokenPurposeVERP, listID, memberB, time.Time{})
	if err != nil {
		t.Fatalf("Sign(B): %v", err)
	}

	gotA, err := ts.Verify(maillist.TokenPurposeVERP, listID, tokA, time.Now())
	if err != nil {
		t.Fatalf("Verify(A): %v", err)
	}
	if gotA != memberA {
		t.Fatalf("Verify(tokA) = %d, want %d (member A)", gotA, memberA)
	}
	if gotA == memberB {
		t.Fatalf("member A's token verified as member B's id")
	}

	gotB, err := ts.Verify(maillist.TokenPurposeVERP, listID, tokB, time.Now())
	if err != nil {
		t.Fatalf("Verify(B): %v", err)
	}
	if gotB != memberB {
		t.Fatalf("Verify(tokB) = %d, want %d (member B)", gotB, memberB)
	}
}

// TestTokenSigner_CrossListRejected exercises the defense-in-depth
// list-id binding: a token minted for one list must not verify against
// a different list, even with the same member id.
func TestTokenSigner_CrossListRejected(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	tok, err := ts.Sign(maillist.TokenPurposeVERP, 1, 55, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := ts.Verify(maillist.TokenPurposeVERP, 2, tok, time.Now()); err == nil {
		t.Fatalf("Verify accepted a token minted for a different list")
	}
}

// TestTokenSigner_CrossPurposeRejected exercises purpose isolation: a
// VERP token must not verify as (e.g.) an unsubscribe token.
func TestTokenSigner_CrossPurposeRejected(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	tok, err := ts.Sign(maillist.TokenPurposeVERP, 1, 55, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := ts.Verify(maillist.TokenPurposeUnsubscribe, 1, tok, time.Now()); err == nil {
		t.Fatalf("Verify accepted a VERP token under a different purpose")
	}
}

// TestTokenSigner_ExpiryEnforced exercises the bounded-TTL half of the
// primitive (used by the non-VERP purposes): an expired token fails,
// and a not-yet-expired one still succeeds.
func TestTokenSigner_ExpiryEnforced(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tok, err := ts.Sign(maillist.TokenPurposeUnsubscribe, 1, 5, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := ts.Verify(maillist.TokenPurposeUnsubscribe, 1, tok, base.Add(30*time.Minute)); err != nil {
		t.Fatalf("Verify before expiry failed: %v", err)
	}
	if _, err := ts.Verify(maillist.TokenPurposeUnsubscribe, 1, tok, base.Add(2*time.Hour)); err == nil {
		t.Fatalf("Verify after expiry succeeded, want failure")
	}
}

// TestTokenSigner_NoExpiryNeverExpires exercises the VERP case: a
// zero-Time expiry never expires, however far "now" advances.
func TestTokenSigner_NoExpiryNeverExpires(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	tok, err := ts.Sign(maillist.TokenPurposeVERP, 1, 5, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	farFuture := time.Now().AddDate(50, 0, 0)
	if _, err := ts.Verify(maillist.TokenPurposeVERP, 1, tok, farFuture); err != nil {
		t.Fatalf("Verify 50 years later failed: %v", err)
	}
}

// TestTokenSigner_GarbageStringRejected exercises fail-closed handling
// of a token string that is not even valid base64.
func TestTokenSigner_GarbageStringRejected(t *testing.T) {
	ts := maillist.NewTokenSigner(testDataKey())
	if _, err := ts.Verify(maillist.TokenPurposeVERP, 1, "not-a-valid-token!!! ***", time.Now()); err == nil {
		t.Fatalf("Verify accepted a garbage string")
	}
}
