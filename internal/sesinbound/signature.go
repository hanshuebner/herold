package sesinbound

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SNS v1 signatures mandate SHA-1 per the AWS spec.
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/netguard"
)

// certCacheEntry caches a fetched and chain-verified leaf certificate.
type certCacheEntry struct {
	cert      *x509.Certificate
	fetchedAt time.Time
}

// certCache is a bounded in-memory cache of parsed SNS signing certs keyed
// by their URL.  Entries expire after certCacheTTL to avoid re-fetching on
// every notification while still rotating periodically.
type certCache struct {
	mu      sync.Mutex
	entries map[string]*certCacheEntry
}

const certCacheTTL = 30 * time.Minute

func newCertCache() *certCache {
	return &certCache{entries: make(map[string]*certCacheEntry)}
}

func (c *certCache) get(u string) (*x509.Certificate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[u]
	if !ok {
		return nil, false
	}
	if time.Since(e.fetchedAt) > certCacheTTL {
		delete(c.entries, u)
		return nil, false
	}
	return e.cert, true
}

func (c *certCache) put(u string, cert *x509.Certificate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Bound at 256 entries — one per SNS region plus generous headroom.
	if len(c.entries) >= 256 {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[u] = &certCacheEntry{cert: cert, fetchedAt: time.Now()}
}

// verifier holds the cert cache and host allowlist.
type verifier struct {
	certHostAllowlist []string
	cache             *certCache
	httpClient        *http.Client
	// skipChainVerify disables x509 chain verification in fetchCert.
	// Only set to true in tests where a self-signed certificate is used.
	skipChainVerify bool
}

func newVerifier(certHostAllowlist []string) *verifier {
	// The http.Transport uses netguard.ControlContext so an attacker-
	// controlled SigningCertURL that resolves to an RFC 1918 / loopback
	// address is refused before connect(2) (REQ-HOOK-SES-06).
	dialer := &net.Dialer{
		Timeout:        10 * time.Second,
		ControlContext: netguard.ControlContext(),
	}
	return &verifier{
		certHostAllowlist: certHostAllowlist,
		cache:             newCertCache(),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext:     dialer.DialContext,
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
	}
}

// newVerifierForTest builds a verifier that skips TLS chain verification
// (accepting self-signed certs).  For unit tests only.
func newVerifierForTest(certHostAllowlist []string) *verifier {
	return &verifier{
		certHostAllowlist: certHostAllowlist,
		cache:             newCertCache(),
		// Plain http.Client — cert servers in tests use plain HTTP.
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, //nolint:gosec // test only
				},
			},
		},
		skipChainVerify: true,
	}
}

// VerifyOutcome is the closed label set for
// herold_hook_ses_signature_verify_total (REQ-HOOK-SES-05, STANDARDS §7).
type VerifyOutcome string

const (
	VerifyOutcomeValid              VerifyOutcome = "valid"
	VerifyOutcomeInvalidSig         VerifyOutcome = "invalid_signature"
	VerifyOutcomeCertFetchFailed    VerifyOutcome = "cert_fetch_failed"
	VerifyOutcomeCertChainInvalid   VerifyOutcome = "cert_chain_invalid"
	VerifyOutcomeCertHostDisallowed VerifyOutcome = "cert_host_disallowed"
)

// verifySNSSignature validates the RSA signature on a parsed SNS message.
// Returns the VerifyOutcome so the caller can increment the matching counter.
func (v *verifier) verifySNSSignature(ctx context.Context, m *snsMessage) VerifyOutcome {
	// Step 1: validate the SigningCertURL host against the allowlist.
	u, err := url.Parse(m.SigningCertURL)
	if err != nil || u.Host == "" {
		return VerifyOutcomeCertHostDisallowed
	}
	host := u.Hostname() // strips port, handles IPv6 brackets
	if !v.hostAllowed(host) {
		return VerifyOutcomeCertHostDisallowed
	}

	// Step 2: obtain the signing certificate (cached or fresh).
	cert, outcome := v.fetchCert(ctx, m.SigningCertURL)
	if outcome != "" {
		return outcome
	}

	// Step 3: build the canonical signing string per the SNS
	// HTTPS-subscriber contract.
	signingStr := canonicalSigningString(m)

	// Step 4: decode the base64 signature.
	sigBytes, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return VerifyOutcomeInvalidSig
	}

	// Step 5: verify RSA signature.
	rsaKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return VerifyOutcomeInvalidSig
	}

	var hashID crypto.Hash
	var digest []byte
	switch m.SignatureVersion {
	case "", "1":
		hashID = crypto.SHA1
		//nolint:gosec // SNS v1 mandates SHA-1.
		h := sha1.New()
		h.Write([]byte(signingStr))
		digest = h.Sum(nil)
	case "2":
		hashID = crypto.SHA256
		h := sha256.New()
		h.Write([]byte(signingStr))
		digest = h.Sum(nil)
	default:
		return VerifyOutcomeInvalidSig
	}

	if err := rsa.VerifyPKCS1v15(rsaKey, hashID, digest, sigBytes); err != nil {
		return VerifyOutcomeInvalidSig
	}
	return VerifyOutcomeValid
}

// hostAllowed returns true if host is in certHostAllowlist.
func (v *verifier) hostAllowed(host string) bool {
	for _, h := range v.certHostAllowlist {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// fetchCert fetches, parses, and chain-verifies the PEM certificate at
// certURL.  Returns the cert on success, or an empty cert and a non-empty
// VerifyOutcome on failure.
func (v *verifier) fetchCert(ctx context.Context, certURL string) (*x509.Certificate, VerifyOutcome) {
	if cert, ok := v.cache.get(certURL); ok {
		return cert, ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certURL, nil)
	if err != nil {
		return nil, VerifyOutcomeCertFetchFailed
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, VerifyOutcomeCertFetchFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, VerifyOutcomeCertFetchFailed
	}

	rawPEM, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16)) // 64 KiB cap
	if err != nil {
		return nil, VerifyOutcomeCertFetchFailed
	}

	block, _ := pem.Decode(rawPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, VerifyOutcomeCertChainInvalid
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, VerifyOutcomeCertChainInvalid
	}

	if !v.skipChainVerify {
		// Verify chain to system roots (not pinned — system roots, per
		// REQ-HOOK-SES-02).
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
			return nil, VerifyOutcomeCertChainInvalid
		}
	}

	v.cache.put(certURL, cert)
	return cert, ""
}

// canonicalSigningString builds the SNS canonical signing string per
// https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message.html
// The field list and order differ by message type.
func canonicalSigningString(m *snsMessage) string {
	var b strings.Builder
	add := func(key, val string) {
		b.WriteString(key)
		b.WriteByte('\n')
		b.WriteString(val)
		b.WriteByte('\n')
	}

	switch m.Type {
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		add("Message", m.Message)
		add("MessageId", m.MessageID)
		add("SubscribeURL", m.SubscribeURL)
		add("Timestamp", m.Timestamp)
		add("Token", m.Token)
		add("TopicArn", m.TopicArn)
		add("Type", m.Type)
	default: // Notification
		add("Message", m.Message)
		add("MessageId", m.MessageID)
		if m.Subject != "" {
			add("Subject", m.Subject)
		}
		add("Timestamp", m.Timestamp)
		add("TopicArn", m.TopicArn)
		add("Type", m.Type)
	}
	return b.String()
}
