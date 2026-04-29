package mailarc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultSealedHeaders is the message-header set covered by an
// ARC-Message-Signature (the AMS). It mirrors the DKIM-default set so
// modifications to identity-affecting headers invalidate the seal — which
// is what receivers use to attribute trust to a forwarder.
var DefaultSealedHeaders = []string{
	"From",
	"To",
	"Cc",
	"Subject",
	"Date",
	"Message-ID",
	"MIME-Version",
	"Content-Type",
	"Content-Transfer-Encoding",
	"In-Reply-To",
	"References",
}

// Sealer adds one ARC set (AAR + AMS + AS) to a message that is being
// forwarded with prior authentication results. The chain validation tag
// (cv=) is computed from the prior verification: messages whose chain
// already failed must propagate cv=fail; first-hop adds use cv=none.
type Sealer struct {
	keys       *keymgmt.Manager
	dkimSigner DKIMSigner
	logger     *slog.Logger
}

// DKIMSigner is the narrow contract Sealer uses to delegate
// hash-and-sign cycles to an existing maildkim.Signer-style implementation.
// In practice it is satisfied by *maildkim.Signer; callers may pass any
// implementation that returns a signed copy of message via DKIM. The
// sealer does not currently use this for its own crypto (the seal uses
// the keymgmt key directly), but it is held so future implementations
// that share the DKIM signing pipeline can be slotted in without changing
// the constructor signature.
type DKIMSigner interface {
	Sign(ctx context.Context, domain string, message []byte) ([]byte, error)
}

// NewSealer returns a Sealer keyed by km. dkimSigner may be nil; it is
// retained for future paths that want to chain DKIM signing into the
// sealing pipeline. logger must not be nil.
func NewSealer(km *keymgmt.Manager, dkimSigner DKIMSigner, logger *slog.Logger) *Sealer {
	if km == nil {
		panic("mailarc: nil key manager")
	}
	if logger == nil {
		panic("mailarc: nil logger")
	}
	return &Sealer{keys: km, dkimSigner: dkimSigner, logger: logger}
}

// Seal returns a fresh byte slice with an ARC set prepended.
//
// signingDomain selects the active DKIM key used to sign both the AMS and
// the AS. prior is the consolidated authentication verdict for the
// message at this hop; its chain status (Status) drives the cv= tag of
// the new ARC-Seal:
//
//   - prior.ARC.Status == AuthFail: cv=fail (taint propagates).
//   - prior.ARC.Status == AuthPass and chain length > 0: cv=pass.
//   - otherwise (no prior chain): cv=none, which is only valid at i=1.
func (s *Sealer) Seal(ctx context.Context, msg []byte, prior mailauth.AuthResults, signingDomain string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(msg) == 0 {
		return nil, errors.New("mailarc: empty message")
	}

	key, err := s.keys.ActiveKey(ctx, signingDomain)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("mailarc: no active DKIM key for %s", signingDomain)
		}
		return nil, fmt.Errorf("mailarc: lookup key: %w", err)
	}
	signer, err := keymgmt.LoadPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("mailarc: load key: %w", err)
	}
	algoTag, err := dkimAlgoTag(key.Algorithm)
	if err != nil {
		return nil, err
	}

	header, body := splitHeaderBody(msg)
	if header == nil {
		return nil, errors.New("mailarc: no header/body separator")
	}

	priorSets, err := extractARCSets(header)
	if err != nil {
		return nil, fmt.Errorf("mailarc: extract prior chain: %w", err)
	}
	if len(priorSets) >= MaxChainLength {
		return nil, fmt.Errorf("mailarc: chain at maximum length (%d)", MaxChainLength)
	}
	instance := len(priorSets) + 1

	cv := computeCV(prior, instance)

	// Build ARC-Authentication-Results. We use AuthResults.Raw verbatim
	// when present so we propagate the receiver's exact wording (it is
	// what the next hop sees on the wire); fall back to a synthesised
	// minimum string when the caller did not preserve a raw form.
	aar := buildARCAuthResults(instance, signingDomain, prior)

	// Compute the ARC-Message-Signature.
	ams, err := buildAMS(instance, key, signer, algoTag, header, body)
	if err != nil {
		return nil, fmt.Errorf("mailarc: build AMS: %w", err)
	}

	// Compute the ARC-Seal. The seal covers all prior ARC sets plus the
	// just-built AAR + AMS, in the canonical order specified by RFC 8617
	// §5.1.1: for each i=1..N: AAR(i), AMS(i), AS(i). The new AS is
	// itself excluded from the hash input (its own b= is empty during
	// the signing computation).
	as, err := buildAS(instance, key, signer, algoTag, cv, header, aar, ams)
	if err != nil {
		return nil, fmt.Errorf("mailarc: build AS: %w", err)
	}

	// Prepend headers in the order receivers parse them. RFC 8617 §5.1
	// recommends AAR, AMS, AS appearing as the topmost headers in that
	// order so the next hop sees the freshly added set first.
	var out bytes.Buffer
	out.Grow(len(msg) + len(aar) + len(ams) + len(as) + 16)
	out.WriteString(as)
	out.WriteString(ams)
	out.WriteString(aar)
	out.Write(header)
	out.WriteString("\r\n\r\n")
	out.Write(body)

	s.logger.DebugContext(ctx, "mailarc: sealed",
		slog.String("activity", "system"),
		slog.String("subsystem", "mailarc"),
		slog.String("domain", signingDomain),
		slog.String("selector", key.Selector),
		slog.Int("instance", instance),
		slog.String("cv", cv))
	return out.Bytes(), nil
}

// computeCV picks the cv= tag for the new ARC-Seal. RFC 8617 §5.1.1:
// cv=none only at i=1; cv=fail propagates a tainted chain; cv=pass when
// the prior verdict was clean.
func computeCV(prior mailauth.AuthResults, instance int) string {
	switch {
	case instance == 1:
		return "none"
	case prior.ARC.Status == mailauth.AuthFail:
		return "fail"
	case prior.ARC.Status == mailauth.AuthPass:
		return "pass"
	default:
		// Conservative: any indeterminate prior state taints the chain.
		return "fail"
	}
}

// buildARCAuthResults synthesises the ARC-Authentication-Results header.
// The instance i= tag is mandatory; the rest is the AuthResults raw
// content when supplied, else a minimal authserv-id placeholder.
func buildARCAuthResults(instance int, authservID string, r mailauth.AuthResults) string {
	body := strings.TrimSpace(r.Raw)
	if body == "" {
		body = authservID + "; arc=" + r.ARC.Status.String()
	}
	header := fmt.Sprintf("%s: i=%d; %s\r\n", HeaderARCAuthenticationResults, instance, body)
	return foldHeader(header)
}

// dkimAlgoTag returns the ARC a= token for the algorithm. Both ARC-Seal
// and ARC-Message-Signature share this vocabulary with DKIM.
func dkimAlgoTag(alg store.DKIMAlgorithm) (string, error) {
	switch alg {
	case store.DKIMAlgorithmRSASHA256:
		return "rsa-sha256", nil
	case store.DKIMAlgorithmEd25519SHA256:
		return "ed25519-sha256", nil
	default:
		return "", errors.New("mailarc: unsupported DKIM algorithm")
	}
}

// buildAMS assembles the ARC-Message-Signature for instance. The body
// hash uses relaxed body canonicalization; the signed headers use the
// relaxed header canonicalization. The signature input format is the
// DKIM input format but with the AMS header itself inserted as the final
// "to-be-signed" header (with its own b= empty).
func buildAMS(instance int, key store.DKIMKey, signer crypto.Signer, algoTag string, header, body []byte) (string, error) {
	bodyHash := sha256.Sum256(canonicaliseBodyRelaxed(body))
	bh := base64.StdEncoding.EncodeToString(bodyHash[:])

	headerKeys := DefaultSealedHeaders
	headerHash, err := hashHeadersRelaxed(header, headerKeys)
	if err != nil {
		return "", err
	}

	// Build the AMS skeleton with b= empty; canonicalise; hash it onto
	// the running hash; sign.
	tags := []tag{
		{"i", strconv.Itoa(instance)},
		{"a", algoTag},
		{"c", "relaxed/relaxed"},
		{"d", key.Domain},
		{"s", key.Selector},
		{"h", strings.Join(headerKeys, ":")},
		{"bh", bh},
		{"b", ""},
	}
	skeleton := HeaderARCMessageSignature + ": " + formatTags(tags)
	canonSkeleton := relaxHeader(skeleton)
	canonSkeleton = strings.TrimRight(canonSkeleton, "\r\n")

	hasher := sha256.New()
	hasher.Write(headerHash)
	hasher.Write([]byte(canonSkeleton))
	digest := hasher.Sum(nil)

	sig, err := rawSign(signer, digest, key.Algorithm)
	if err != nil {
		return "", err
	}
	tags[len(tags)-1].v = base64.StdEncoding.EncodeToString(sig)
	out := HeaderARCMessageSignature + ": " + formatTags(tags) + "\r\n"
	return foldHeader(out), nil
}

// buildAS assembles the ARC-Seal. Per RFC 8617 §5.1.1 the seal covers,
// in order, every prior set's AAR, AMS, AS (in instance order) followed
// by the freshly built AAR and AMS for this instance, and finally the
// AS skeleton (with its own b= empty). No body is covered.
func buildAS(instance int, key store.DKIMKey, signer crypto.Signer, algoTag, cv string, header []byte, aar, ams string) (string, error) {
	// Collect prior ARC headers in instance order.
	priorLines := orderedPriorARCHeaders(header)

	hasher := sha256.New()
	for _, line := range priorLines {
		hasher.Write([]byte(relaxHeader(line)))
	}
	// New AAR and AMS for this instance: relax them and feed in.
	hasher.Write([]byte(relaxHeader(strings.TrimRight(aar, "\r\n"))))
	hasher.Write([]byte(relaxHeader(strings.TrimRight(ams, "\r\n"))))

	tags := []tag{
		{"i", strconv.Itoa(instance)},
		{"a", algoTag},
		{"cv", cv},
		{"d", key.Domain},
		{"s", key.Selector},
		{"b", ""},
	}
	skeleton := HeaderARCSeal + ": " + formatTags(tags)
	canonSkeleton := relaxHeader(skeleton)
	canonSkeleton = strings.TrimRight(canonSkeleton, "\r\n")
	hasher.Write([]byte(canonSkeleton))
	digest := hasher.Sum(nil)

	sig, err := rawSign(signer, digest, key.Algorithm)
	if err != nil {
		return "", err
	}
	tags[len(tags)-1].v = base64.StdEncoding.EncodeToString(sig)
	out := HeaderARCSeal + ": " + formatTags(tags) + "\r\n"
	return foldHeader(out), nil
}

// arcBucket carries the three header lines of one logical ARC set
// while orderedPriorARCHeaders is grouping them by instance.
type arcBucket struct{ aar, ams, as string }

// orderedPriorARCHeaders walks the existing header block and returns
// every ARC-Authentication-Results / ARC-Message-Signature / ARC-Seal
// header in their instance order, with within-instance order
// AAR -> AMS -> AS, ready to be relax-canonicalised and hashed.
func orderedPriorARCHeaders(header []byte) []string {
	buckets := map[int]*arcBucket{}
	for _, line := range foldedHeaderLines(header) {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		nm := strings.TrimSpace(name)
		switch {
		case strings.EqualFold(nm, HeaderARCAuthenticationResults):
			i, _ := parseInstanceTag(value)
			b := getBucket(buckets, i)
			b.aar = line
		case strings.EqualFold(nm, HeaderARCMessageSignature):
			i, _ := parseInstanceTag(value)
			b := getBucket(buckets, i)
			b.ams = line
		case strings.EqualFold(nm, HeaderARCSeal):
			i, _, _ := parseSealTags(value)
			b := getBucket(buckets, i)
			b.as = line
		}
	}
	indices := make([]int, 0, len(buckets))
	for i := range buckets {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]string, 0, len(indices)*3)
	for _, i := range indices {
		b := buckets[i]
		if b.aar != "" {
			out = append(out, b.aar)
		}
		if b.ams != "" {
			out = append(out, b.ams)
		}
		if b.as != "" {
			out = append(out, b.as)
		}
	}
	return out
}

func getBucket(m map[int]*arcBucket, i int) *arcBucket {
	b, ok := m[i]
	if !ok {
		b = &arcBucket{}
		m[i] = b
	}
	return b
}

// rawSign feeds a pre-hashed digest into signer using the right hash
// constant for alg. Ed25519 demands crypto.Hash(0) (it hashes internally
// via Ed25519ph or a raw signature over the digest); the DKIM and ARC
// specs both define the input as raw SHA-256 output for ed25519-sha256.
func rawSign(signer crypto.Signer, digest []byte, alg store.DKIMAlgorithm) ([]byte, error) {
	switch alg {
	case store.DKIMAlgorithmRSASHA256:
		return signer.Sign(noEntropyReader{}, digest, crypto.SHA256)
	case store.DKIMAlgorithmEd25519SHA256:
		// DKIM RFC 8463 §3: ed25519 signs the SHA-256 digest of the
		// canonicalised data directly. Ed25519's stdlib Sign expects
		// the message itself; we use the EdDSA primitive's Sign path
		// by passing crypto.Hash(0) so it treats the input as the
		// message bytes (here, the 32-byte digest).
		return signer.Sign(noEntropyReader{}, digest, crypto.Hash(0))
	default:
		return nil, errors.New("mailarc: unsupported algorithm")
	}
}

// noEntropyReader is supplied to crypto.Signer.Sign for callers whose
// algorithm does not consume randomness. RSA-PSS would, but RSA-PKCS1v15
// (the DKIM mode) and Ed25519 do not. Using a deterministic empty reader
// makes test runs reproducible without compromising security: neither
// algorithm reads from this source on the production path.
type noEntropyReader struct{}

func (noEntropyReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// VerifySealKey allows the verifier to confirm a parsed signature against
// a public key, used by the chain-crypto verifier added below. Kept here
// so signing and verifying share one place.
func verifySignature(pub crypto.PublicKey, alg store.DKIMAlgorithm, digest, sig []byte) error {
	switch alg {
	case store.DKIMAlgorithmRSASHA256:
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("mailarc: key/algorithm mismatch (want RSA)")
		}
		return rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, digest, sig)
	case store.DKIMAlgorithmEd25519SHA256:
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return errors.New("mailarc: key/algorithm mismatch (want Ed25519)")
		}
		if !ed25519.Verify(edPub, digest, sig) {
			return errors.New("mailarc: ed25519 signature mismatch")
		}
		return nil
	default:
		return errors.New("mailarc: unsupported algorithm")
	}
}

// canonicaliseBodyRelaxed implements RFC 6376 §3.4.4: collapse runs of
// whitespace inside lines to a single space, strip trailing whitespace at
// end of line, and strip trailing empty lines, then ensure the body ends
// with exactly one CRLF. Empty input yields an empty body; receivers
// produce the same hash either way per the RFC.
func canonicaliseBodyRelaxed(body []byte) []byte {
	// Normalise to LF for splitting; reattach CRLF on the way out.
	s := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		// Trim trailing whitespace.
		ln = strings.TrimRight(ln, " \t")
		// Collapse runs of WSP.
		ln = collapseWSP(ln)
		lines[i] = ln
	}
	// Strip trailing empty lines.
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	lines = lines[:end]
	if len(lines) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, ln := range lines {
		buf.WriteString(ln)
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

func collapseWSP(s string) string {
	var b strings.Builder
	prevWS := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if prevWS {
				continue
			}
			prevWS = true
			b.WriteByte(' ')
			continue
		}
		prevWS = false
		b.WriteRune(r)
	}
	return b.String()
}

// hashHeadersRelaxed walks the message header block and returns the
// running SHA-256 update buffer for headers in headerKeys, with
// relaxed-header canonicalisation. Headers that do not appear in the
// message are skipped (RFC 6376 §5.4.2).
func hashHeadersRelaxed(header []byte, headerKeys []string) ([]byte, error) {
	picker := newHeaderPicker(header)
	hasher := sha256.New()
	for _, k := range headerKeys {
		v := picker.Pick(k)
		if v == "" {
			continue
		}
		hasher.Write([]byte(relaxHeader(v)))
	}
	return hasher.Sum(nil), nil
}

// relaxHeader implements RFC 6376 §3.4.2 relaxed header canonicalisation:
// lowercase the field name, strip whitespace around ":", collapse runs of
// WSP to a single space, strip trailing whitespace, terminate with CRLF.
func relaxHeader(line string) string {
	// Strip trailing CRLF / LF first; we re-add a single CRLF below.
	line = strings.TrimRight(line, "\r\n")
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return strings.ToLower(strings.TrimSpace(line)) + ":\r\n"
	}
	name = strings.ToLower(strings.TrimSpace(name))
	value = collapseFolds(value)
	value = strings.TrimSpace(value)
	return name + ":" + value + "\r\n"
}

// collapseFolds replaces any sequence of CRLF/LF/CR/space/tab with a
// single space. RFC 6376 §3.4.2 specifies replacing each run of WSP
// (including line continuations) with one ASCII SP.
func collapseFolds(s string) string {
	var b strings.Builder
	prevWS := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if prevWS {
				continue
			}
			prevWS = true
			b.WriteByte(' ')
			continue
		}
		prevWS = false
		b.WriteRune(r)
	}
	return b.String()
}

// headerPicker walks a raw header block and picks headers by name in
// bottom-up order, the order DKIM mandates (last-occurrence first when a
// header appears multiple times). It mirrors the go-msgauth picker but
// keeps mailarc free of the dkim package's unexported helpers.
type headerPicker struct {
	header []string
	used   []bool
}

func newHeaderPicker(headerBytes []byte) *headerPicker {
	lines := foldedHeaderLines(headerBytes)
	return &headerPicker{header: lines, used: make([]bool, len(lines))}
}

func (p *headerPicker) Pick(name string) string {
	for i := len(p.header) - 1; i >= 0; i-- {
		if p.used[i] {
			continue
		}
		k, _, ok := strings.Cut(p.header[i], ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), name) {
			p.used[i] = true
			return p.header[i]
		}
	}
	return ""
}

// tag is one DKIM tag (key=value pair) in declared order. We carry tags
// as a slice rather than a map so the on-wire ordering is stable and the
// signing surface is reproducible across runs.
type tag struct {
	k string
	v string
}

func formatTags(tags []tag) string {
	var b strings.Builder
	for i, t := range tags {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(t.k)
		b.WriteByte('=')
		b.WriteString(t.v)
	}
	return b.String()
}

// foldHeader inserts soft line breaks inside a header value if the line
// would otherwise exceed 78 characters. The input is expected to be a
// single logical line ending in CRLF; the output preserves CRLF.
func foldHeader(line string) string {
	if len(line) <= 78 {
		return line
	}
	// Trim trailing CRLF for splitting, re-add at end.
	body := strings.TrimRight(line, "\r\n")
	parts := strings.Split(body, " ")
	var out strings.Builder
	col := 0
	for i, p := range parts {
		if i > 0 {
			if col+1+len(p) > 78 {
				out.WriteString("\r\n\t")
				col = 1
			} else {
				out.WriteByte(' ')
				col++
			}
		}
		out.WriteString(p)
		col += len(p)
	}
	out.WriteString("\r\n")
	return out.String()
}

// io.Discard alias for places where we want to throw away one writer end
// without import-cycling on the standard library indirectly.
var _ = io.Discard
