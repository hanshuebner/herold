package protosmtp

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/store"
)

// tlsDecision captures the negotiated TLS posture for one delivery
// attempt. policy is one of "none", "opportunistic", "mta_sts", "dane";
// danAuthentic indicates DNSSEC validation was confirmed (so a TLSA
// match was load-bearing rather than informational).
type tlsDecision struct {
	used         bool
	policy       string
	danAuthentic bool
}

// upgradeTLS performs the STARTTLS dance against an open SMTP session.
// On success it returns the upgraded *tls.Conn and the tlsDecision;
// the caller (deliverToMX) must EHLO again on the upgraded connection.
//
// Policy precedence (docs/design/server/architecture/04 §MTA-STS / DANE):
//
//  1. DANE: TLSA records present + DNSSEC-validated → require TLS,
//     verify the leaf cert against the TLSA RRset. Mismatch ⇒ permanent.
//  2. MTA-STS enforce: TLS required, cert must validate against the
//     recipient MX hostname. Failure ⇒ permanent.
//  3. MTA-STS testing or no policy → opportunistic TLS; failures
//     downgrade to plaintext (or permanent when REQUIRETLS=true).
func (c *Client) upgradeTLS(
	ctx context.Context,
	sess *outboundSession,
	mxHost string,
	policy *MTASTSPolicy,
	requireTLS bool,
) (*tls.Conn, tlsDecision, error) {
	supportsSTARTTLS := sess.hasExtension("STARTTLS")

	// DANE first: only kicks in when configured AND the resolver can
	// authenticate the answer.
	tlsaRecs, daneAuthentic := c.lookupDANE(ctx, mxHost)
	daneApplies := c.dane && daneAuthentic && len(tlsaRecs) > 0

	// Policy gate: REQUIRETLS / DANE / MTA-STS enforce all require
	// STARTTLS to be advertised. Otherwise the only legal posture is
	// either opportunistic-or-plaintext.
	if !supportsSTARTTLS {
		if daneApplies {
			c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureSTARTTLSNotOffered, "starttls-not-offered",
				"remote does not advertise STARTTLS but DANE TLSA is published")
			return nil, tlsDecision{}, errPolicyNoSTARTTLS
		}
		if policy != nil && policy.Mode == MTASTSModeEnforce {
			c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureSTARTTLSNotOffered, "starttls-not-offered",
				"remote does not advertise STARTTLS but MTA-STS enforce is in effect")
			return nil, tlsDecision{}, errPolicyNoSTARTTLS
		}
		if requireTLS {
			c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureSTARTTLSNotOffered, "starttls-not-offered",
				"REQUIRETLS=true but remote does not advertise STARTTLS")
			return nil, tlsDecision{}, errPolicyNoSTARTTLS
		}
		return nil, tlsDecision{used: false, policy: "none"}, nil
	}

	if err := sess.command(ctx, "STARTTLS"); err != nil {
		return nil, tlsDecision{}, fmt.Errorf("starttls write: %w", err)
	}
	code, _, line, err := sess.readReply(ctx)
	if err != nil {
		return nil, tlsDecision{}, fmt.Errorf("starttls read: %w", err)
	}
	if code != 220 {
		// The remote refused the upgrade. Treat as a TLS-negotiation
		// failure under DANE / enforce; otherwise fall back.
		if daneApplies || (policy != nil && policy.Mode == MTASTSModeEnforce) || requireTLS {
			c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureSTARTTLSNegotiation, "starttls-rejected", line)
			return nil, tlsDecision{}, fmt.Errorf("%w: STARTTLS rejected: %d %s", errTLSNegotiation, code, line)
		}
		return nil, tlsDecision{used: false, policy: "none"}, nil
	}

	// Wrap the underlying conn. ServerName drives SNI; for opportunistic
	// TLS we use the MX host even when MTA-STS doesn't apply because
	// SNI selection on the remote side keys off it.
	cfg := &tls.Config{
		ServerName: mxHost,
		MinVersion: tls.VersionTLS12,
	}
	// For DANE and opportunistic / testing posture we accept any
	// presented cert — DANE supplies its own cert validation, and
	// opportunistic TLS by definition tolerates self-signed peers
	// (RFC 7672 §1). Enforce mode keeps the stdlib's PKIX validation
	// against the MX hostname.
	if !(policy != nil && policy.Mode == MTASTSModeEnforce) {
		cfg.InsecureSkipVerify = true // policy validation happens below
	}

	tlsConn := tls.Client(sess.conn, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureSTARTTLSNegotiation, "handshake-failed", err.Error())
		if daneApplies || (policy != nil && policy.Mode == MTASTSModeEnforce) || requireTLS {
			return nil, tlsDecision{}, fmt.Errorf("%w: handshake: %w", errTLSNegotiation, err)
		}
		return nil, tlsDecision{used: false, policy: "none"}, nil
	}
	state := tlsConn.ConnectionState()

	// DANE validation against the leaf certificate.
	if daneApplies {
		if len(state.PeerCertificates) == 0 {
			c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureDANE, "no-peer-cert",
				"DANE configured but peer presented no certificate")
			_ = tlsConn.Close()
			return nil, tlsDecision{}, errDANEMismatch
		}
		if !matchTLSA(state.PeerCertificates, tlsaRecs) {
			c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureDANE, "tlsa-mismatch",
				fmt.Sprintf("peer cert did not match any of %d TLSA records", len(tlsaRecs)))
			_ = tlsConn.Close()
			return nil, tlsDecision{}, errDANEMismatch
		}
		c.log.DebugContext(ctx, "outbound: dane tlsa match",
			slog.String("mx", mxHost), slog.Int("tlsa_records", len(tlsaRecs)))
		return tlsConn, tlsDecision{used: true, policy: "dane", danAuthentic: true}, nil
	}

	// MTA-STS enforce: PKIX-verify the leaf and confirm the cert covers
	// the MX hostname (the stdlib already does this when InsecureSkipVerify
	// is false, so a successful handshake implies validity).
	if policy != nil && policy.Mode == MTASTSModeEnforce {
		// Re-verify using the chains the stdlib already constructed —
		// HandshakeContext guarantees the chain is non-empty here.
		if len(state.VerifiedChains) == 0 {
			c.recordTLSRPT(ctx, sess.policyDomain, mxHost, store.TLSRPTFailureValidation, "no-verified-chain",
				"MTA-STS enforce: no verified chain")
			_ = tlsConn.Close()
			return nil, tlsDecision{}, fmt.Errorf("%w: no verified chain", errTLSNegotiation)
		}
		return tlsConn, tlsDecision{used: true, policy: "mta_sts"}, nil
	}

	// Opportunistic TLS / testing — handshake succeeded, no further checks.
	pol := "opportunistic"
	if policy != nil && policy.Mode == MTASTSModeTesting {
		pol = "opportunistic" // testing falls under opportunistic on success
	}
	return tlsConn, tlsDecision{used: true, policy: pol}, nil
}

// lookupDANE resolves _25._tcp.<mxHost> TLSA. Returns (records, authentic).
// The boolean second value reports whether the DNS answer was DNSSEC-
// validated; without that we never enforce DANE per RFC 7672 §3.
func (c *Client) lookupDANE(ctx context.Context, mxHost string) ([]mailauth.TLSARecord, bool) {
	if c.tlsaResolver == nil {
		return nil, false
	}
	rrs, authentic, err := c.tlsaResolver.LookupTLSA(ctx, "_25._tcp."+mxHost)
	if err != nil {
		return nil, false
	}
	return rrs, authentic
}

// matchTLSA returns true when at least one TLSA record matches at least
// one peer certificate. We support the two common usages the operator
// guidance in docs/design/server/requirements/04 names: PKIX-EE (3) and DANE-EE (3).
// Other usages fall through to no-match — operators publishing CA-anchor
// usages (0/2) get a clear "tlsa-mismatch" diagnostic rather than a
// silent pass.
func matchTLSA(chain []*x509.Certificate, recs []mailauth.TLSARecord) bool {
	if len(chain) == 0 {
		return false
	}
	leaf := chain[0]
	for _, r := range recs {
		// Usage 3 = DANE-EE (RFC 7671): the leaf cert must match.
		if r.Usage != 3 {
			continue
		}
		if matchTLSARecord(leaf, r) {
			return true
		}
	}
	return false
}

// matchTLSARecord compares one TLSA record against one cert. Selector
// chooses the source (full cert vs SubjectPublicKeyInfo); MatchingType
// chooses the digest (raw / SHA-256 / SHA-512).
func matchTLSARecord(cert *x509.Certificate, r mailauth.TLSARecord) bool {
	var src []byte
	switch r.Selector {
	case 0: // Cert
		src = cert.Raw
	case 1: // SPKI
		src = cert.RawSubjectPublicKeyInfo
	default:
		return false
	}
	switch r.MatchingType {
	case 0: // Full
		return bytesEq(src, r.Data)
	case 1: // SHA-256
		h := sha256.Sum256(src)
		return bytesEq(h[:], r.Data)
	case 2: // SHA-512
		h := sha512.Sum512(src)
		return bytesEq(h[:], r.Data)
	default:
		return false
	}
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// errPolicyNoSTARTTLS is returned by upgradeTLS when DANE / MTA-STS enforce
// / REQUIRETLS demand STARTTLS but the remote does not advertise it.
// errTLSNegotiation covers TLS handshake / STARTTLS-refused failures
// under a strict policy. errDANEMismatch is the DANE TLSA mismatch case.
// All three map to DeliveryPermanent at the call site.
var (
	errPolicyNoSTARTTLS = errors.New("protosmtp: STARTTLS not offered but required by policy")
	errTLSNegotiation   = errors.New("protosmtp: TLS negotiation failed under strict policy")
	errDANEMismatch     = errors.New("protosmtp: DANE TLSA validation failed")
)
