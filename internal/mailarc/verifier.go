package mailarc

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/store"
)

// maxHeaderSize is the upper bound for the header block buffered by Verify.
// Must match the DKIM verifier's constant; both are bounded well below any
// real MTA's working set.
const maxHeaderSize = 256 * 1024

// HeaderARCSeal is the ARC-Seal header name as defined in RFC 8617 §4.1.2.
const HeaderARCSeal = "ARC-Seal"

// HeaderARCMessageSignature is the ARC-Message-Signature header name
// (RFC 8617 §4.1.1).
const HeaderARCMessageSignature = "ARC-Message-Signature"

// HeaderARCAuthenticationResults is the ARC-Authentication-Results
// header name (RFC 8617 §4.1.3).
const HeaderARCAuthenticationResults = "ARC-Authentication-Results"

// MaxChainLength is RFC 8617 §5.1.1's ceiling on ARC chain depth.
// Messages with more sets than this produce a Fail verdict.
const MaxChainLength = 50

// Verifier verifies inbound ARC chains. It is safe for concurrent use.
type Verifier struct {
	resolver mailauth.Resolver
}

// New returns a Verifier. The resolver will be used by a future Phase to
// validate ARC-Seal signatures (which requires DKIM-style TXT lookups
// against the seal's d= / s= tags). Phase 1 performs structural chain
// validation without cryptographic verification of each seal; that lands
// alongside the sealing implementation in Phase 2 so signing and
// verification share one code path.
func New(resolver mailauth.Resolver) *Verifier {
	if resolver == nil {
		panic("mailarc: nil resolver")
	}
	return &Verifier{resolver: resolver}
}

// Verify inspects r (a single-forward RFC 5322 message stream) for an ARC
// chain and returns its status.
//
// Verify buffers only the header block (up to maxHeaderSize bytes) to extract
// the ARC sets; the body is streamed through a single SHA-256 pass to verify
// each AMS body hash. Nothing beyond the header block is held in RAM.
//
// When no ARC headers are present the result is Status == AuthNone with
// Chain == 0 and a nil error. When the chain is structurally valid the
// status is AuthPass; structural failures produce AuthFail with a
// reason. A non-nil error indicates an internal failure (context
// cancel, allocator limits).
func (v *Verifier) Verify(ctx context.Context, r io.Reader) (mailauth.ARCResult, error) {
	if err := ctx.Err(); err != nil {
		return mailauth.ARCResult{}, err
	}

	// Buffer the header block (bounded) for ARC-set extraction; leave the
	// body as a streaming reader for the body-hash computation below.
	headerBuf, bodyReader, err := readARCHeaderBlock(r)
	if err != nil {
		return mailauth.ARCResult{Status: mailauth.AuthFail, Reason: "header read: " + err.Error()}, nil
	}

	sets, err := extractARCSets(headerBuf)
	if err != nil {
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Reason: err.Error(),
		}, nil
	}
	if len(sets) == 0 {
		// Drain the body so the underlying reader is not left mid-stream.
		_, _ = io.Copy(io.Discard, bodyReader)
		return mailauth.ARCResult{Status: mailauth.AuthNone}, nil
	}
	if len(sets) > MaxChainLength {
		_, _ = io.Copy(io.Discard, bodyReader)
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Chain:  len(sets),
			Reason: fmt.Sprintf("chain length %d exceeds max %d", len(sets), MaxChainLength),
		}, nil
	}

	// Verify instance indexes form a contiguous 1..N sequence; the spec
	// forbids gaps and duplicates (RFC 8617 §5.1.1).
	sort.Slice(sets, func(i, j int) bool { return sets[i].instance < sets[j].instance })
	for i, s := range sets {
		want := i + 1
		if s.instance != want {
			_, _ = io.Copy(io.Discard, bodyReader)
			return mailauth.ARCResult{
				Status: mailauth.AuthFail,
				Chain:  len(sets),
				Reason: fmt.Sprintf("missing or duplicate instance %d", want),
			}, nil
		}
		if !s.hasSeal || !s.hasMsgSig || !s.hasAAR {
			_, _ = io.Copy(io.Discard, bodyReader)
			return mailauth.ARCResult{
				Status: mailauth.AuthFail,
				Chain:  len(sets),
				Reason: fmt.Sprintf("instance %d: incomplete set", s.instance),
			}, nil
		}
	}

	// RFC 8617 §5.1.1: the latest ARC-Seal carries the cv= tag that
	// summarises the chain's cumulative status. cv=fail on any hop
	// taints the whole chain. Structural cv= rules are enforced FIRST;
	// only after a structural pass do we perform per-set cryptographic
	// verification, so the (cheap) structural failures do not require
	// any DNS lookups.
	last := sets[len(sets)-1]
	cv := strings.ToLower(last.cv)
	switch cv {
	case "pass":
		// Continue to crypto-verify below.
	case "none":
		if last.instance != 1 {
			_, _ = io.Copy(io.Discard, bodyReader)
			return mailauth.ARCResult{
				Status: mailauth.AuthFail,
				Chain:  len(sets),
				Reason: "cv=none at i>1",
			}, nil
		}
		// Continue to crypto-verify below.
	case "fail":
		// RFC 8617 §5.1.1: cv=fail is permanent; no need to crypto-
		// verify the chain — the seal's own author declared it broken.
		_, _ = io.Copy(io.Discard, bodyReader)
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Chain:  len(sets),
			Reason: "cv=fail",
		}, nil
	default:
		_, _ = io.Copy(io.Discard, bodyReader)
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Chain:  len(sets),
			Reason: fmt.Sprintf("unknown cv=%q", last.cv),
		}, nil
	}

	// Compute the relaxed-body SHA-256 hash once by streaming the body;
	// all AMS instances check the same body hash so we do it here before
	// the per-set loop and pass the result in.
	bodyHash, herr := streamBodyHash(bodyReader)
	if herr != nil {
		return mailauth.ARCResult{}, fmt.Errorf("mailarc: body hash: %w", herr)
	}

	// Per-set cryptographic verification. We require every prior AS to
	// be cryptographically valid AND every prior AMS to be valid; the
	// final cv= tag's claim ("pass"/"none") is only honoured if the
	// crypto agrees.
	if cryptoStatus, reason, err := v.verifyChainCrypto(ctx, sets, headerBuf, bodyHash); err != nil {
		// Internal error (context cancel) propagates.
		return mailauth.ARCResult{}, err
	} else if cryptoStatus != mailauth.AuthPass {
		return mailauth.ARCResult{
			Status: cryptoStatus,
			Chain:  len(sets),
			Reason: reason,
		}, nil
	}

	// All structural and cryptographic checks pass.
	return mailauth.ARCResult{Status: mailauth.AuthPass, Chain: len(sets)}, nil
}

// readARCHeaderBlock reads from r until the RFC 5322 header/body separator
// (CRLF CRLF or lone LF LF) is found, bounded at maxHeaderSize. The returned
// header slice includes the separator bytes so it can be fed directly to
// extractARCSets (which calls splitHeaderBody internally). The returned
// bodyReader is the rest of the stream starting at the first body byte.
func readARCHeaderBlock(r io.Reader) (header []byte, bodyReader io.Reader, err error) {
	br := bufio.NewReaderSize(r, 4096)
	var buf bytes.Buffer
	for buf.Len() < maxHeaderSize {
		line, readErr := br.ReadBytes('\n')
		buf.Write(line)
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 && len(line) > 0 {
			// Blank line found: header/body separator.
			remaining := io.MultiReader(br, r)
			return buf.Bytes(), remaining, nil
		}
		if readErr == io.EOF {
			return buf.Bytes(), bytes.NewReader(nil), nil
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	remaining := io.MultiReader(br, r)
	return buf.Bytes(), remaining, nil
}

// streamBodyHash streams body through the relaxed body canonicaliser and
// returns the SHA-256 digest. It does not buffer body; it exists so the ARC
// verifier computes the body hash without holding the entire body in RAM.
func streamBodyHash(body io.Reader) ([]byte, error) {
	h := sha256.New()
	if err := canonicaliseBodyRelaxedStream(body, h); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// canonicaliseBodyRelaxedStream applies RFC 6376 §3.4.4 relaxed body
// canonicalisation by reading from body and writing the canonical form into
// w. It:
//   - collapses runs of whitespace inside each line to a single SP,
//   - strips trailing whitespace at end of each line,
//   - strips trailing empty lines,
//   - ensures the final output ends with exactly one CRLF.
//
// This is the streaming equivalent of canonicaliseBodyRelaxed([]byte) in
// sealer.go; the sealer keeps its []byte path for signing (where it assembles
// the full canonical body into a buffer anyway).
func canonicaliseBodyRelaxedStream(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 4096)
	// We maintain a queue of "pending empty lines" to implement the
	// strip-trailing-blank-lines rule: we only emit them when a non-empty
	// line follows. At EOF we discard the pending empties.
	var pendingCRLFs int
	writeCRLF := func() error {
		_, err := w.Write([]byte("\r\n"))
		return err
	}
	writePending := func() error {
		for pendingCRLFs > 0 {
			if err := writeCRLF(); err != nil {
				return err
			}
			pendingCRLFs--
		}
		return nil
	}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			// Strip trailing CRLF / LF before processing.
			raw := string(bytes.TrimRight(line, "\r\n"))
			// Collapse runs of whitespace (SP/TAB) per RFC 6376 §3.4.4.
			canonical := collapseWSP(strings.TrimRight(raw, " \t"))
			if canonical == "" {
				// Empty line after stripping: defer until we see a
				// non-empty line (or EOF to discard).
				pendingCRLFs++
			} else {
				// Non-empty line: flush any deferred empty lines first.
				if err2 := writePending(); err2 != nil {
					return err2
				}
				if _, werr := fmt.Fprintf(w, "%s\r\n", canonical); werr != nil {
					return werr
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	// discard pendingCRLFs — they are trailing empty lines per RFC 6376.
	// If we wrote zero non-empty lines the body is effectively empty; the
	// RFC says an empty body produces the hash of an empty string (no CRLF).
	return nil
}

// verifyChainCrypto walks the ARC chain in instance order and verifies
// each set's AMS and AS signatures using public keys retrieved from DNS.
// Per RFC 8617 §5.1.2 the chain is valid iff every AMS verifies AND
// every AS verifies; a failure at any instance taints the chain.
//
// headerBuf is the pre-buffered header block (including the separator line).
// bodyHash is the pre-computed SHA-256 of the relaxed-canonicalised body,
// computed once from the streaming body reader before this call.
//
// Returns AuthPass when every set verifies. Returns AuthFail with a
// machine-readable reason when a signature does not verify, the key is
// revoked, or the body has been tampered with. Returns AuthTempError
// for transient DNS conditions (no records yet) so callers can retry
// without poisoning the verdict.
func (v *Verifier) verifyChainCrypto(ctx context.Context, sets []arcSet, headerBuf, bodyHash []byte) (mailauth.AuthStatus, string, error) {
	// Extract the raw header block (without separator) from the buffered
	// header for header-hash computation. splitHeaderBody handles both
	// CRLF and LF separators.
	header, _ := splitHeaderBody(headerBuf)
	if header == nil {
		// No separator found — treat the full buffer as the header.
		header = headerBuf
	}
	for _, s := range sets {
		// AMS first: it covers headers + body, so any tampering of the
		// payload surfaces here regardless of how the chain looks.
		if status, reason, err := v.verifyAMS(ctx, s, header, bodyHash); err != nil {
			return 0, "", err
		} else if status != mailauth.AuthPass {
			return status, fmt.Sprintf("AMS i=%d: %s", s.instance, reason), nil
		}
		// AS covers all prior ARC sets plus the current AAR+AMS+cv
		// (with its own b= empty). Per RFC 8617 §5.1.2 step 4 a fail
		// at any instance taints the chain.
		if status, reason, err := v.verifyAS(ctx, s, sets, header); err != nil {
			return 0, "", err
		} else if status != mailauth.AuthPass {
			return status, fmt.Sprintf("AS i=%d: %s", s.instance, reason), nil
		}
	}
	return mailauth.AuthPass, "", nil
}

// verifyAMS reconstructs the ARC-Message-Signature signing input for
// instance s.instance and confirms the b= tag's signature using the
// public key advertised at <selector>._domainkey.<domain>. The
// reconstructed input mirrors buildAMS in sealer.go: sealed-header hash
// followed by the canonicalised AMS skeleton with b= emptied.
//
// bodyHash is the pre-computed SHA-256 of the relaxed-canonicalised message
// body, supplied by the caller (verifyChainCrypto) to avoid re-reading the
// body for each ARC instance in a multi-hop chain.
func (v *Verifier) verifyAMS(ctx context.Context, s arcSet, header, bodyHash []byte) (mailauth.AuthStatus, string, error) {
	domain := s.msgSigParams["d"]
	selector := s.msgSigParams["s"]
	algoTag := s.msgSigParams["a"]
	bhTag := s.msgSigParams["bh"]
	hTag := s.msgSigParams["h"]
	bSig := stripWhitespaceB64(s.msgSigParams["b"])
	if domain == "" || selector == "" || algoTag == "" || bhTag == "" || hTag == "" || bSig == "" {
		return mailauth.AuthFail, "missing required tag(s)", nil
	}
	alg, err := algoFromTag(algoTag)
	if err != nil {
		return mailauth.AuthPermError, err.Error(), nil
	}

	// Body hash MUST match bh=. Done before the signature check so a
	// modified body produces a clear "body hash mismatch" reason rather
	// than a generic signature error. bodyHash is the pre-computed
	// SHA-256 of the relaxed-canonical body (same digest for every set
	// in the chain — ARC signs the original body at every hop).
	wantBH, err := base64.StdEncoding.DecodeString(stripWhitespaceB64(bhTag))
	if err != nil {
		return mailauth.AuthPermError, "bad bh= base64", nil
	}
	if subtle.ConstantTimeCompare(bodyHash, wantBH) != 1 {
		return mailauth.AuthFail, "body hash mismatch", nil
	}

	// Hash the sealed headers per the h= list.
	headerKeys := splitHeaderList(hTag)
	headerHash, err := hashHeadersRelaxed(header, headerKeys)
	if err != nil {
		return mailauth.AuthFail, err.Error(), nil
	}

	// Canonicalise the AMS itself with b= empty (RFC 8617 §5.1.1's
	// "AMS skeleton" — the verifier mirrors what buildAMS produced).
	canonAMS := canonicaliseSignatureHeader(s.msgSigLine)
	hasher := sha256.New()
	hasher.Write(headerHash)
	hasher.Write([]byte(canonAMS))
	digest := hasher.Sum(nil)

	sigBytes, err := base64.StdEncoding.DecodeString(bSig)
	if err != nil {
		return mailauth.AuthPermError, "bad b= base64", nil
	}

	pub, status, reason, err := v.fetchKey(ctx, domain, selector)
	if err != nil {
		return 0, "", err
	}
	if status != mailauth.AuthPass {
		return status, reason, nil
	}
	if err := verifySignature(pub, alg, digest, sigBytes); err != nil {
		return mailauth.AuthFail, "signature did not verify with key from " + selector + "._domainkey." + domain, nil
	}
	return mailauth.AuthPass, "", nil
}

// verifyAS reconstructs the ARC-Seal signing input for instance
// s.instance per RFC 8617 §5.1.2 step 4: for every prior set k=1..i-1,
// AAR(k) || AMS(k) || AS(k); then AAR(i) || AMS(i); finally the AS(i)
// skeleton with b= emptied.
func (v *Verifier) verifyAS(ctx context.Context, s arcSet, allSets []arcSet, header []byte) (mailauth.AuthStatus, string, error) {
	domain := s.sealParams["d"]
	selector := s.sealParams["s"]
	algoTag := s.sealParams["a"]
	bSig := stripWhitespaceB64(s.sealParams["b"])
	if domain == "" || selector == "" || algoTag == "" || bSig == "" {
		return mailauth.AuthFail, "missing required tag(s)", nil
	}
	alg, err := algoFromTag(algoTag)
	if err != nil {
		return mailauth.AuthPermError, err.Error(), nil
	}

	hasher := sha256.New()
	for _, k := range allSets {
		if k.instance > s.instance {
			break
		}
		// For prior sets (k < s.instance) the seal covers AAR, AMS, AS
		// in that order; for the current set (k == s.instance) it
		// covers AAR and AMS only — the new AS goes in last as the
		// b=-empty skeleton.
		hasher.Write([]byte(relaxHeader(k.aarLine)))
		hasher.Write([]byte(relaxHeader(k.msgSigLine)))
		if k.instance < s.instance {
			hasher.Write([]byte(relaxHeader(k.sealLine)))
		}
	}
	hasher.Write([]byte(canonicaliseSignatureHeader(s.sealLine)))
	digest := hasher.Sum(nil)

	sigBytes, err := base64.StdEncoding.DecodeString(bSig)
	if err != nil {
		return mailauth.AuthPermError, "bad b= base64", nil
	}

	pub, status, reason, err := v.fetchKey(ctx, domain, selector)
	if err != nil {
		return 0, "", err
	}
	if status != mailauth.AuthPass {
		return status, reason, nil
	}
	if err := verifySignature(pub, alg, digest, sigBytes); err != nil {
		return mailauth.AuthFail, "signature did not verify with key from " + selector + "._domainkey." + domain, nil
	}
	return mailauth.AuthPass, "", nil
}

// fetchKey resolves <selector>._domainkey.<domain> via the verifier's
// resolver and parses the resulting v=DKIM1 TXT record. Returns:
//
//   - (pub, AuthPass, "", nil) on success.
//   - (nil, AuthTempError, reason, nil) when DNS is transiently unavailable.
//   - (nil, AuthPermError, reason, nil) when the key is missing, revoked
//     (p= empty), or unparseable.
//   - (nil, 0, "", err) on context cancel or other internal failure.
func (v *Verifier) fetchKey(ctx context.Context, domain, selector string) (crypto.PublicKey, mailauth.AuthStatus, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, "", err
	}
	name := selector + "._domainkey." + domain
	txts, err := v.resolver.TXTLookup(ctx, name)
	if err != nil {
		if errors.Is(err, mailauth.ErrNoRecords) {
			// Treat absent key as TempError per RFC 6376 §6.1.2: a
			// receiver SHOULD allow the message to be retried; the
			// caller's spam scoring decides what to do with the
			// indeterminate verdict.
			return nil, mailauth.AuthTempError, "no TXT for " + name, nil
		}
		if mailauth.IsTemporary(err) {
			return nil, mailauth.AuthTempError, "DNS temp failure: " + err.Error(), nil
		}
		return nil, mailauth.AuthPermError, "DNS lookup failed: " + err.Error(), nil
	}
	if len(txts) == 0 {
		return nil, mailauth.AuthTempError, "empty TXT for " + name, nil
	}
	if len(txts) > 1 {
		// RFC 6376 §3.6.2.2: multiple TXT records is undefined; we
		// reject permanently rather than guess.
		return nil, mailauth.AuthPermError, "multiple TXT records for " + name, nil
	}
	pub, err := parseDKIMRecord(txts[0])
	if err != nil {
		return nil, mailauth.AuthPermError, err.Error(), nil
	}
	return pub, mailauth.AuthPass, "", nil
}

// parseDKIMRecord parses a v=DKIM1 TXT record and returns the public
// key. Mirrors emersion/go-msgauth/dkim's parsePublicKey but kept here
// so we are not reaching into the dkim package's unexported helpers and
// so the ARC verifier owns its parse path end-to-end.
func parseDKIMRecord(s string) (crypto.PublicKey, error) {
	params := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		params[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	if v, ok := params["v"]; ok && v != "DKIM1" {
		return nil, fmt.Errorf("incompatible public key version %q", v)
	}
	p, ok := params["p"]
	if !ok {
		return nil, errors.New("missing p= tag")
	}
	if p == "" {
		return nil, errors.New("key revoked (empty p=)")
	}
	p = strings.ReplaceAll(p, " ", "")
	der, err := base64.StdEncoding.DecodeString(p)
	if err != nil {
		return nil, fmt.Errorf("bad p= base64: %w", err)
	}
	switch params["k"] {
	case "rsa", "":
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			// RFC 6376 erratum 3017: callers may have used the bare
			// PKCS#1 form. Try that as a fallback before declaring
			// the key syntactically broken.
			pub2, err2 := x509.ParsePKCS1PublicKey(der)
			if err2 != nil {
				return nil, fmt.Errorf("bad RSA key: %w", err)
			}
			return pub2, nil
		}
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("p= is not an RSA key")
		}
		// RFC 8301 §3.2: refuse RSA keys shorter than 1024 bits.
		if rsaPub.Size()*8 < 1024 {
			return nil, fmt.Errorf("RSA key too short (%d bits)", rsaPub.Size()*8)
		}
		return rsaPub, nil
	case "ed25519":
		// RFC 8463 §3 mandates the raw 32-byte form; some publishers
		// (including older keymgmt revisions in this repo) emit the
		// SPKI form, which x509.ParsePKIXPublicKey decodes back into a
		// 32-byte ed25519.PublicKey. Accept either to interoperate.
		if len(der) == ed25519.PublicKeySize {
			return ed25519.PublicKey(der), nil
		}
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, fmt.Errorf("bad Ed25519 key: %w", err)
		}
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("p= is not an Ed25519 key")
		}
		return edPub, nil
	default:
		return nil, fmt.Errorf("unsupported k=%q", params["k"])
	}
}

// algoFromTag maps the wire-form a= token (rsa-sha256 / ed25519-sha256)
// to the typed store.DKIMAlgorithm enum that verifySignature consumes.
func algoFromTag(tag string) (store.DKIMAlgorithm, error) {
	switch strings.TrimSpace(strings.ToLower(tag)) {
	case "rsa-sha256":
		return store.DKIMAlgorithmRSASHA256, nil
	case "ed25519-sha256":
		return store.DKIMAlgorithmEd25519SHA256, nil
	default:
		return store.DKIMAlgorithmUnknown, fmt.Errorf("unsupported a=%q", tag)
	}
}

// canonicaliseSignatureHeader returns the relaxed-canonical form of an
// ARC-Seal or ARC-Message-Signature header line with the b= tag value
// stripped to empty (b=). The result has no trailing CRLF. This mirrors
// the "skeleton" path in buildAMS / buildAS so verification feeds the
// same bytes into SHA-256 that signing did.
func canonicaliseSignatureHeader(line string) string {
	stripped := stripBValue(line)
	canon := relaxHeader(stripped)
	canon = strings.TrimRight(canon, "\r\n")
	return canon
}

// stripBValue replaces the b=<value> tag in a folded ARC-Seal /
// ARC-Message-Signature header line with an empty b= so the line can be
// canonicalised as the signing-input "skeleton". The line is taken in
// its un-folded form; tag separators are ';'. The function treats b= as
// always being the last semicolon-delimited tag value when matched but
// is robust to b= appearing earlier (the matched value extends to the
// next semicolon, end of line, or whitespace+next tag).
func stripBValue(line string) string {
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return line
	}
	parts := strings.Split(value, ";")
	for i, part := range parts {
		k, _, sep := strings.Cut(part, "=")
		if !sep {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "b") {
			// Preserve any leading whitespace before the key so the
			// relaxed canonicaliser collapses it consistently.
			leading := part[:len(part)-len(strings.TrimLeft(part, " \t"))]
			parts[i] = leading + "b="
		}
	}
	return name + ":" + strings.Join(parts, ";")
}

// stripWhitespaceB64 strips spaces, tabs, CR, and LF from a base64-encoded
// tag value so the b= and bh= tags decode correctly even when the header
// has been line-folded.
func stripWhitespaceB64(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
}

// encodePublicKeyB64 returns the base64-encoded SubjectPublicKeyInfo
// form of pub, the value that lands in DNS at the p= tag of a
// v=DKIM1 TXT record. Used by tests to publish synthetic keys without
// reaching into keymgmt; production keys are encoded in keymgmt.
func encodePublicKeyB64(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// splitHeaderList splits an h= tag value on ':' and trims surrounding
// whitespace. Empty entries are dropped.
func splitHeaderList(s string) []string {
	parts := strings.Split(s, ":")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// arcSet is one logical ARC set: the ARC-Seal, ARC-Message-Signature,
// and ARC-Authentication-Results sharing an i= instance. The verifier
// keeps the raw folded header lines so the crypto-verify pass can
// canonicalise them without re-walking the message.
type arcSet struct {
	instance  int
	cv        string
	hasSeal   bool
	hasMsgSig bool
	hasAAR    bool

	sealLine   string
	msgSigLine string
	aarLine    string

	sealParams   map[string]string
	msgSigParams map[string]string
}

// extractARCSets walks the header block of raw and returns one arcSet
// per distinct instance. A malformed tag list returns an error; a
// missing i= tag does too.
func extractARCSets(raw []byte) ([]arcSet, error) {
	header, _ := splitHeaderBody(raw)
	lines := foldedHeaderLines(header)
	sets := map[int]*arcSet{}
	for _, line := range lines {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(name), HeaderARCSeal):
			i, cv, err := parseSealTags(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", HeaderARCSeal, err)
			}
			s := getOrCreate(sets, i)
			s.hasSeal = true
			s.cv = cv
			s.sealLine = line
			s.sealParams = parseAllTags(value)
		case strings.EqualFold(strings.TrimSpace(name), HeaderARCMessageSignature):
			i, err := parseInstanceTag(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", HeaderARCMessageSignature, err)
			}
			s := getOrCreate(sets, i)
			s.hasMsgSig = true
			s.msgSigLine = line
			s.msgSigParams = parseAllTags(value)
		case strings.EqualFold(strings.TrimSpace(name), HeaderARCAuthenticationResults):
			i, err := parseInstanceTag(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", HeaderARCAuthenticationResults, err)
			}
			s := getOrCreate(sets, i)
			s.hasAAR = true
			s.aarLine = line
		}
	}
	out := make([]arcSet, 0, len(sets))
	for _, s := range sets {
		out = append(out, *s)
	}
	return out, nil
}

// parseAllTags returns every "k=v" pair in v as a map. Whitespace inside
// values is preserved (callers may strip explicitly per-tag).
func parseAllTags(v string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(strings.ToLower(k))] = strings.TrimSpace(val)
	}
	return out
}

func getOrCreate(m map[int]*arcSet, i int) *arcSet {
	s, ok := m[i]
	if !ok {
		s = &arcSet{instance: i}
		m[i] = s
	}
	return s
}

// parseSealTags extracts the i= and cv= tags from an ARC-Seal header
// value. Missing i= is an error; missing cv= returns an empty cv so the
// caller can report it.
func parseSealTags(v string) (int, string, error) {
	i := -1
	cv := ""
	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val = strings.TrimSpace(val)
		switch strings.ToLower(key) {
		case "i":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return 0, "", errors.New("bad i= tag")
			}
			i = n
		case "cv":
			cv = val
		}
	}
	if i < 0 {
		return 0, "", errors.New("missing i= tag")
	}
	return i, cv, nil
}

// parseInstanceTag extracts the i= tag from an ARC-Message-Signature or
// ARC-Authentication-Results header.
func parseInstanceTag(v string) (int, error) {
	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "i") {
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil || n <= 0 {
				return 0, errors.New("bad i= tag")
			}
			return n, nil
		}
	}
	return 0, errors.New("missing i= tag")
}

// splitHeaderBody splits raw at the first blank line and returns the
// header and body sections. It tolerates CRLF or LF line endings.
func splitHeaderBody(raw []byte) (header, body []byte) {
	for _, sep := range [][]byte{{'\r', '\n', '\r', '\n'}, {'\n', '\n'}} {
		if i := bytes.Index(raw, sep); i >= 0 {
			return raw[:i], raw[i+len(sep):]
		}
	}
	return raw, nil
}

// foldedHeaderLines un-folds RFC 5322 continuation lines into single
// logical lines.
func foldedHeaderLines(header []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(header), "\r\n", "\n"), "\n")
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		if ln[0] == ' ' || ln[0] == '\t' {
			cur.WriteByte(' ')
			cur.WriteString(strings.TrimLeft(ln, " \t"))
			continue
		}
		flush()
		cur.WriteString(ln)
	}
	flush()
	return out
}
