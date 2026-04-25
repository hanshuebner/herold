package mailarc

import (
	"crypto"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// BuildARCChainForTest is the in-package fixture used by verifier_test
// to build N-deep ARC chains without spinning up a full keymgmt.Manager
// or store.Metadata. It calls into sealer.go's primitives directly so
// the round-trip Seal -> Verify path is end-to-end exercised.
//
// hops is the number of ARC sets to add; signer / pubKey / domain /
// selector / alg control the keypair used for every set (one keypair
// per hop is sufficient for verification testing — using the same
// keypair across hops keeps the test simple while still exercising the
// instance-ordered hashing logic). The first hop always seals with
// cv=none; subsequent hops seal with cv=pass to mirror a normal
// downstream forwarder.
//
// mutate, if non-nil, is invoked on the message bytes between hops; it
// returns the bytes the next hop should seal. Tests use this to
// simulate tampering between hops.
func BuildARCChainForTest(
	hops int,
	signer crypto.Signer,
	domain, selector string,
	alg store.DKIMAlgorithm,
	initial []byte,
	mutate func([]byte) []byte,
) ([]byte, error) {
	if hops < 1 {
		return nil, fmt.Errorf("mailarc test: hops must be >= 1")
	}
	algoTag, err := dkimAlgoTag(alg)
	if err != nil {
		return nil, err
	}
	key := store.DKIMKey{
		Domain:    domain,
		Selector:  selector,
		Algorithm: alg,
	}

	msg := initial
	for i := 1; i <= hops; i++ {
		header, body := splitHeaderBody(msg)
		if header == nil {
			return nil, fmt.Errorf("mailarc test: no header/body separator at hop %d", i)
		}

		cv := "none"
		if i > 1 {
			cv = "pass"
		}

		// Synthesise an Authentication-Results-style body for the AAR
		// header; for tests, plain "spf=pass" is sufficient — what
		// matters is that the AS at the next hop hashes a stable line.
		aar := fmt.Sprintf("%s: i=%d; %s; spf=pass\r\n", HeaderARCAuthenticationResults, i, domain)

		ams, err := buildAMS(i, key, signer, algoTag, header, body)
		if err != nil {
			return nil, fmt.Errorf("hop %d AMS: %w", i, err)
		}
		as, err := buildAS(i, key, signer, algoTag, cv, header, aar, ams)
		if err != nil {
			return nil, fmt.Errorf("hop %d AS: %w", i, err)
		}

		// Prepend AS, AMS, AAR to the existing header block so the
		// new set is the topmost — same wire ordering as Sealer.Seal.
		var out []byte
		out = append(out, []byte(as)...)
		out = append(out, []byte(ams)...)
		out = append(out, []byte(aar)...)
		out = append(out, header...)
		out = append(out, []byte("\r\n\r\n")...)
		out = append(out, body...)
		msg = out

		if mutate != nil && i < hops {
			msg = mutate(msg)
		}
	}
	return msg, nil
}

// EncodePublicKeyB64ForTest is the in-package helper used by
// verifier_test to render a public key in the SubjectPublicKeyInfo
// base64 form expected at <selector>._domainkey.<domain>.
func EncodePublicKeyB64ForTest(pub crypto.PublicKey) (string, error) {
	return encodePublicKeyB64(pub)
}
