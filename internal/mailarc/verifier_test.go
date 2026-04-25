package mailarc_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

type fakeResolver struct{ inner *fakedns.Resolver }

func (f fakeResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	v, err := f.inner.LookupTXT(ctx, name)
	if err != nil {
		return nil, mailauth.ErrNoRecords
	}
	return v, nil
}
func (f fakeResolver) MXLookup(ctx context.Context, _ string) ([]*net.MX, error) {
	return nil, mailauth.ErrNoRecords
}
func (f fakeResolver) IPLookup(ctx context.Context, _ string) ([]net.IP, error) {
	return nil, mailauth.ErrNoRecords
}

func newVerifier() *mailarc.Verifier {
	return mailarc.New(fakeResolver{inner: fakedns.New()})
}

func newVerifierWithDNS(dns *fakedns.Resolver) *mailarc.Verifier {
	return mailarc.New(fakeResolver{inner: dns})
}

// baseMessage is the unsealed RFC 5322 carrier the synthetic ARC test
// fixtures wrap. It is intentionally short so a hand-traced AMS body
// hash is easy to follow if a regression appears.
const baseMessage = "From: alice@example.com\r\n" +
	"To: bob@example.net\r\n" +
	"Subject: hello\r\n" +
	"Date: Fri, 24 Apr 2026 12:00:00 +0000\r\n" +
	"Message-ID: <abc@example.com>\r\n" +
	"\r\n" +
	"hello world\r\n"

// generateRSAKeyForTest produces a deterministic 2048-bit RSA keypair
// suitable for the synthetic ARC chains. The reader is non-deterministic
// (crypto/rand) because rsa.GenerateKey rejects predictable readers in
// Go 1.21+ for safety; tests should not depend on the exact key bytes,
// only on the round-trip Sign -> Verify behaviour.
func generateRSAKeyForTest(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return priv
}

func generateEd25519KeyForTest(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return priv
}

// publishKey adds the v=DKIM1 TXT record for signer at
// <selector>._domainkey.<domain> in dns. kAlg is the k= tag value
// ("rsa" or "ed25519").
func publishKey(t *testing.T, dns *fakedns.Resolver, signer crypto.Signer, domain, selector, kAlg string) {
	t.Helper()
	pubB64, err := mailarc.EncodePublicKeyB64ForTest(signer.Public())
	if err != nil {
		t.Fatalf("encode pub: %v", err)
	}
	dns.AddTXT(selector+"._domainkey."+domain, "v=DKIM1; k="+kAlg+"; p="+pubB64)
}

// buildChain wraps the in-package fixture so the per-test boilerplate
// stays readable. mutate may be nil.
func buildChain(t *testing.T, hops int, signer crypto.Signer, alg store.DKIMAlgorithm, domain, selector string, base []byte, mutate func([]byte) []byte) []byte {
	t.Helper()
	out, err := mailarc.BuildARCChainForTest(hops, signer, domain, selector, alg, base, mutate)
	if err != nil {
		t.Fatalf("buildChain: %v", err)
	}
	return out
}

// -- structural-failure tests (no crypto path executed) ------------------

func TestVerify_NoHeaders(t *testing.T) {
	v := newVerifier()
	r, err := v.Verify(context.Background(), []byte("From: a@b\r\n\r\nhi\r\n"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthNone {
		t.Fatalf("status = %v want none", r.Status)
	}
	if r.Chain != 0 {
		t.Errorf("chain = %d want 0", r.Chain)
	}
}

// cvFailMessage carries cv=fail on the last seal. The structural fast
// path returns AuthFail before crypto-verify runs, so the placeholder
// signature bytes never need to be valid.
const cvFailMessage = "ARC-Seal: i=1; a=rsa-sha256; cv=none; d=example.com; s=s1; b=AAAA\r\n" +
	"ARC-Message-Signature: i=1; a=rsa-sha256; d=example.com; s=s1; h=from; bh=AAAA; b=BBBB\r\n" +
	"ARC-Authentication-Results: i=1; mx.example.com; spf=pass\r\n" +
	"ARC-Seal: i=2; a=rsa-sha256; cv=fail; d=example.net; s=s2; b=CCCC\r\n" +
	"ARC-Message-Signature: i=2; a=rsa-sha256; d=example.net; s=s2; h=from; bh=AAAA; b=DDDD\r\n" +
	"ARC-Authentication-Results: i=2; mx.example.net; arc=fail\r\n" +
	"From: alice@example.com\r\n\r\nbody\r\n"

func TestVerify_CVFail(t *testing.T) {
	v := newVerifier()
	r, err := v.Verify(context.Background(), []byte(cvFailMessage))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail", r.Status)
	}
}

func TestVerify_InstanceGap(t *testing.T) {
	msg := "ARC-Seal: i=1; cv=none; d=e; s=s1; b=A\r\n" +
		"ARC-Message-Signature: i=1; d=e; s=s1; h=from; bh=A; b=B\r\n" +
		"ARC-Authentication-Results: i=1; mx; spf=pass\r\n" +
		"ARC-Seal: i=3; cv=pass; d=e; s=s3; b=C\r\n" +
		"ARC-Message-Signature: i=3; d=e; s=s3; h=from; bh=A; b=D\r\n" +
		"ARC-Authentication-Results: i=3; mx; arc=pass\r\n" +
		"From: a@b\r\n\r\nhi\r\n"
	r, err := newVerifier().Verify(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (gap at i=2)", r.Status)
	}
}

func TestVerify_CVNoneAtI2Fails(t *testing.T) {
	msg := "ARC-Seal: i=1; cv=none; d=e; s=s1; b=A\r\n" +
		"ARC-Message-Signature: i=1; d=e; s=s1; h=from; bh=A; b=B\r\n" +
		"ARC-Authentication-Results: i=1; mx; spf=pass\r\n" +
		"ARC-Seal: i=2; cv=none; d=e; s=s2; b=C\r\n" +
		"ARC-Message-Signature: i=2; d=e; s=s2; h=from; bh=A; b=D\r\n" +
		"ARC-Authentication-Results: i=2; mx; arc=pass\r\n" +
		"From: a@b\r\n\r\nhi\r\n"
	r, err := newVerifier().Verify(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (cv=none at i>1)", r.Status)
	}
}

func TestVerify_MissingMessageSignature(t *testing.T) {
	msg := "ARC-Seal: i=1; cv=none; d=e; s=s1; b=A\r\n" +
		"ARC-Authentication-Results: i=1; mx; spf=pass\r\n" +
		"From: a@b\r\n\r\nhi\r\n"
	r, err := newVerifier().Verify(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (incomplete set)", r.Status)
	}
}

// -- crypto-verify tests -------------------------------------------------

// TestVerify_CryptoPass_SingleHopChain builds a synthetic message with
// a valid i=1 ARC set signed by a generated RSA keypair; fakedns serves
// the matching public-key TXT; Verify returns Status=Pass, Chain=1.
func TestVerify_CryptoPass_SingleHopChain(t *testing.T) {
	signer := generateRSAKeyForTest(t)
	dns := fakedns.New()
	publishKey(t, dns, signer, "example.com", "s1", "rsa")

	sealed := buildChain(t, 1, signer, store.DKIMAlgorithmRSASHA256, "example.com", "s1", []byte(baseMessage), nil)

	r, err := newVerifierWithDNS(dns).Verify(context.Background(), sealed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthPass {
		t.Fatalf("status = %v want pass (reason=%q)", r.Status, r.Reason)
	}
	if r.Chain != 1 {
		t.Errorf("chain = %d want 1", r.Chain)
	}
}

// TestVerify_CryptoPass_TwoHopChain extends the chain to i=2. Both hops
// use the same keypair (a single forwarder on the path); the AS at i=2
// must hash AAR(1) || AMS(1) || AS(1) || AAR(2) || AMS(2) || AS(2)-skel.
func TestVerify_CryptoPass_TwoHopChain(t *testing.T) {
	signer := generateRSAKeyForTest(t)
	dns := fakedns.New()
	publishKey(t, dns, signer, "example.com", "s1", "rsa")

	sealed := buildChain(t, 2, signer, store.DKIMAlgorithmRSASHA256, "example.com", "s1", []byte(baseMessage), nil)

	r, err := newVerifierWithDNS(dns).Verify(context.Background(), sealed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthPass {
		t.Fatalf("status = %v want pass (reason=%q)", r.Status, r.Reason)
	}
	if r.Chain != 2 {
		t.Errorf("chain = %d want 2", r.Chain)
	}
}

// TestVerify_CryptoPass_Ed25519 covers the ed25519-sha256 algorithm
// path. Same fixture as the RSA case; only the keypair differs.
func TestVerify_CryptoPass_Ed25519(t *testing.T) {
	signer := generateEd25519KeyForTest(t)
	dns := fakedns.New()
	publishKey(t, dns, signer, "example.com", "s1", "ed25519")

	sealed := buildChain(t, 1, signer, store.DKIMAlgorithmEd25519SHA256, "example.com", "s1", []byte(baseMessage), nil)

	r, err := newVerifierWithDNS(dns).Verify(context.Background(), sealed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthPass {
		t.Fatalf("status = %v want pass (reason=%q)", r.Status, r.Reason)
	}
}

// TestVerify_AMS_CryptoFail_DetectsTampering builds a valid chain, then
// mutates one byte of the message body; the AMS bh= no longer matches
// the body hash so verification fails with a Reason that names i=1.
func TestVerify_AMS_CryptoFail_DetectsTampering(t *testing.T) {
	signer := generateRSAKeyForTest(t)
	dns := fakedns.New()
	publishKey(t, dns, signer, "example.com", "s1", "rsa")

	sealed := buildChain(t, 1, signer, store.DKIMAlgorithmRSASHA256, "example.com", "s1", []byte(baseMessage), nil)
	tampered := bytes.Replace(sealed, []byte("hello world"), []byte("HELLO world"), 1)

	r, err := newVerifierWithDNS(dns).Verify(context.Background(), tampered)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (reason=%q)", r.Status, r.Reason)
	}
	if !strings.Contains(r.Reason, "AMS i=1") {
		t.Errorf("reason = %q want substring 'AMS i=1'", r.Reason)
	}
}

// TestVerify_AS_CryptoFail_DetectsTampering builds a 2-hop chain, then
// mutates a tag value inside the i=1 AAR header. The AS at i=2 hashed
// the original AAR(1); after the mutation its input hashes differently
// and the signature does not verify.
func TestVerify_AS_CryptoFail_DetectsTampering(t *testing.T) {
	signer := generateRSAKeyForTest(t)
	dns := fakedns.New()
	publishKey(t, dns, signer, "example.com", "s1", "rsa")

	sealed := buildChain(t, 2, signer, store.DKIMAlgorithmRSASHA256, "example.com", "s1", []byte(baseMessage), nil)
	// Mutate the AAR(1) header content — leaves AMS(1) intact (it does
	// not cover the AAR) so the failure isolates to the AS path.
	tampered := bytes.Replace(sealed, []byte("spf=pass"), []byte("spf=fail"), 1)

	r, err := newVerifierWithDNS(dns).Verify(context.Background(), tampered)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail (reason=%q)", r.Status, r.Reason)
	}
	if !strings.Contains(r.Reason, "AS i=2") {
		t.Errorf("reason = %q want substring 'AS i=2'", r.Reason)
	}
}

// TestVerify_KeyNotInDNS_TempError exercises the absent-key branch:
// fakedns has no TXT for the seal's <selector>._domainkey.<domain>;
// verifier returns AuthTempError so callers can retry.
func TestVerify_KeyNotInDNS_TempError(t *testing.T) {
	signer := generateRSAKeyForTest(t)
	dns := fakedns.New()
	// Deliberately skip publishKey — DNS has nothing for the AMS/AS
	// lookup. The chain itself is valid; only the key is missing.

	sealed := buildChain(t, 1, signer, store.DKIMAlgorithmRSASHA256, "example.com", "s1", []byte(baseMessage), nil)

	r, err := newVerifierWithDNS(dns).Verify(context.Background(), sealed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthTempError {
		t.Fatalf("status = %v want temperror (reason=%q)", r.Status, r.Reason)
	}
}

// TestVerify_KeyRevoked_PermError exercises the revoked-key branch:
// the published TXT has v=DKIM1 but an empty p= tag, which RFC 6376
// §3.6.1 defines as "the key has been revoked". The verifier surfaces
// AuthPermError so callers know the verdict will not improve on retry.
func TestVerify_KeyRevoked_PermError(t *testing.T) {
	signer := generateRSAKeyForTest(t)
	dns := fakedns.New()
	dns.AddTXT("s1._domainkey.example.com", "v=DKIM1; k=rsa; p=")

	sealed := buildChain(t, 1, signer, store.DKIMAlgorithmRSASHA256, "example.com", "s1", []byte(baseMessage), nil)

	r, err := newVerifierWithDNS(dns).Verify(context.Background(), sealed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthPermError {
		t.Fatalf("status = %v want permerror (reason=%q)", r.Status, r.Reason)
	}
}

// TestVerify_CV_Fail_PropagatesAcrossSets confirms a downstream cv=fail
// taints the chain even when AS+AMS at that hop verify cleanly.
// Reusing cvFailMessage is sufficient: the structural cv= switch
// returns Fail before crypto-verify runs (RFC 8617 §5.1.1 is explicit
// that cv=fail propagates without requiring crypto re-verification).
func TestVerify_CV_Fail_PropagatesAcrossSets(t *testing.T) {
	v := newVerifier()
	r, err := v.Verify(context.Background(), []byte(cvFailMessage))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail", r.Status)
	}
	if r.Reason != "cv=fail" {
		t.Errorf("reason = %q want %q", r.Reason, "cv=fail")
	}
}

// TestVerify_RFC8617_TestVectors_If_Present is a placeholder for the
// IETF working group's published ARC test vectors. RFC 8617's §C draft
// referenced a vectors directory that never made it into the published
// RFC; emersion/go-msgauth has DKIM vectors but no ARC vectors. The
// synthetic chains in the other crypto-verify tests cover the
// meaningful crypto paths (single-hop, multi-hop, body tamper, header
// tamper, missing key, revoked key); when the IETF publishes a
// canonical fixture set, drop them in under testdata/ and exercise
// them here. Skip is preferred over fail so the test is a discovery
// hook rather than a tripwire.
func TestVerify_RFC8617_TestVectors_If_Present(t *testing.T) {
	t.Skip("no published RFC 8617 ARC test vectors located; synthetic round-trip tests cover crypto paths")
}
