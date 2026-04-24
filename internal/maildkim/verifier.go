package maildkim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
)

// DefaultMaxVerifications caps how many DKIM signatures the verifier will
// evaluate on a single message. RFC 6376 does not impose a limit; we pick
// a number that comfortably covers legitimate mailing-list / forwarder
// stacks while rejecting pathological sender-amplification attempts.
const DefaultMaxVerifications = 8

// Verifier verifies DKIM signatures on RFC 5322 messages. It is safe for
// concurrent use; constructor dependencies are read-only.
type Verifier struct {
	resolver mailauth.Resolver
	logger   *slog.Logger
	clock    clock.Clock
	// max bounds how many signatures are evaluated per message. Zero means
	// DefaultMaxVerifications.
	max int
}

// Option customises a Verifier. Callers rarely need these; they exist so
// tests can shrink the signature budget.
type Option func(*Verifier)

// WithMaxVerifications overrides DefaultMaxVerifications for this
// Verifier. A value <= 0 restores the default.
func WithMaxVerifications(n int) Option {
	return func(v *Verifier) {
		if n > 0 {
			v.max = n
		}
	}
}

// New returns a Verifier using resolver for DKIM key lookups. logger and
// clk must not be nil; callers building a production server wire
// clock.NewReal() and their slog.Logger; tests wire clock.NewFake and a
// test logger.
func New(resolver mailauth.Resolver, logger *slog.Logger, clk clock.Clock, opts ...Option) *Verifier {
	if resolver == nil {
		panic("maildkim: nil resolver")
	}
	if logger == nil {
		panic("maildkim: nil logger")
	}
	if clk == nil {
		panic("maildkim: nil clock")
	}
	v := &Verifier{
		resolver: resolver,
		logger:   logger,
		clock:    clk,
		max:      DefaultMaxVerifications,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Verify verifies every DKIM-Signature header present in raw (RFC 5322
// message bytes, headers + body) and returns one mailauth.DKIMResult per
// signature. An empty slice with a nil error means the message carried no
// DKIM signatures.
//
// Verify does not return an error for a bad signature: bad signatures
// surface as DKIMResult.Status == AuthFail with a reason. A non-nil error
// indicates an I/O or internal failure: the caller should treat the
// whole message's DKIM verdict as indeterminate (the caller will usually
// report a synthetic AuthTempError DKIMResult in that case).
func (v *Verifier) Verify(ctx context.Context, raw []byte) ([]mailauth.DKIMResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	opts := &dkim.VerifyOptions{
		// Bridge the dkim lookup callback through our Resolver. The
		// library invokes it synchronously from Verify, so the closure
		// can safely capture ctx.
		LookupTXT: func(domain string) ([]string, error) {
			return v.resolver.TXTLookup(ctx, domain)
		},
		MaxVerifications: v.max,
	}

	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), opts)
	// ErrTooManySignatures is returned alongside the first v.max
	// verifications; we accept that result and log a note but do not
	// propagate the error to the caller (we have already enforced the
	// cap).
	if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
		// Distinguish genuine I/O errors (context cancel, reader fail)
		// from the dkim library's typed permFail/tempFail errors: those
		// are attached to the per-signature Verification, not returned
		// at the top level, so any error here is unexpected.
		return nil, fmt.Errorf("maildkim: verify: %w", err)
	}
	if errors.Is(err, dkim.ErrTooManySignatures) {
		v.logger.InfoContext(ctx, "maildkim: truncated signatures above cap",
			slog.Int("cap", v.max))
	}

	results := make([]mailauth.DKIMResult, 0, len(verifs))
	for _, vr := range verifs {
		results = append(results, verificationToResult(vr))
	}
	// Overlay s= / a= tags from the raw signature headers: go-msgauth
	// does not surface those on its Verification struct but downstream
	// consumers (DMARC alignment, spam scoring, Authentication-Results
	// rendering) need them.
	return enrichWithTags(results, ExtractSignatureTags(raw)), nil
}

// verificationToResult maps a dkim.Verification to mailauth.DKIMResult,
// translating the library's tempFail/permFail error shape into the
// status + reason pair the shared contract uses.
func verificationToResult(v *dkim.Verification) mailauth.DKIMResult {
	r := mailauth.DKIMResult{
		Domain:     v.Domain,
		Identifier: v.Identifier,
	}
	if v.Err == nil {
		r.Status = mailauth.AuthPass
	} else {
		switch {
		case dkim.IsTempFail(v.Err):
			r.Status = mailauth.AuthTempError
		case dkim.IsPermFail(v.Err):
			r.Status = mailauth.AuthPermError
		default:
			r.Status = mailauth.AuthFail
		}
		r.Reason = cleanReason(v.Err.Error())
	}
	// The dkim.Verification struct does not expose the selector or the
	// a= tag directly; parse them back out of the original signature
	// header so downstream consumers (spam, DMARC) have them.
	return r
}

// cleanReason strips the "dkim: " prefix the library puts on every error
// string so the reason returned through our contract stays compact.
func cleanReason(s string) string {
	return strings.TrimPrefix(s, "dkim: ")
}

// ExtractSignatureTags scans raw for every DKIM-Signature header and
// returns the parsed (selector, algorithm) pair in header order. It is
// used by Verify to enrich DKIMResult with s= / a= details the
// go-msgauth Verification struct does not expose.
//
// This is intentionally a lightweight tag scanner and not a full DKIM
// header parser: it tolerates missing tags and returns empty strings
// rather than failing, because the downstream Verify call already
// produces a PermError with a sharper reason when the tag is malformed.
func ExtractSignatureTags(raw []byte) []SignatureTags {
	header, _ := splitHeaderBody(raw)
	var out []SignatureTags
	for _, line := range foldedHeaderLines(header) {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(name), "DKIM-Signature") {
			continue
		}
		out = append(out, parseTags(value))
	}
	return out
}

// SignatureTags are the fields we expose from a parsed DKIM-Signature
// header beyond what go-msgauth surfaces on its Verification type.
type SignatureTags struct {
	Selector  string
	Algorithm string
	Domain    string
}

// parseTags extracts s=, a=, d= from a DKIM-Signature header value. Tags
// are ";"-separated key=value pairs with optional whitespace.
func parseTags(v string) SignatureTags {
	var out SignatureTags
	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val = strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, val)
		switch strings.ToLower(key) {
		case "s":
			out.Selector = val
		case "a":
			out.Algorithm = val
		case "d":
			out.Domain = val
		}
	}
	return out
}

// splitHeaderBody splits raw at the first CRLF CRLF (or LF LF) pair and
// returns the header and body sections. It tolerates either line ending.
func splitHeaderBody(raw []byte) (header, body []byte) {
	for _, sep := range [][]byte{{'\r', '\n', '\r', '\n'}, {'\n', '\n'}} {
		if i := bytes.Index(raw, sep); i >= 0 {
			return raw[:i], raw[i+len(sep):]
		}
	}
	return raw, nil
}

// foldedHeaderLines un-folds RFC 5322 §2.2.3 header lines: continuation
// lines (those starting with space or tab) are joined to the previous
// logical line with a single space.
func foldedHeaderLines(header []byte) []string {
	// Normalise CRLF to LF so splitting is simple; DKIM tag parsing
	// doesn't care about line endings in the un-folded form.
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

// enrichWithTags overlays selector / algorithm tags from ExtractSignatureTags
// onto a slice of results whose domain matches. It is invoked from Verify.
// Kept exported (unexported in tests) to make the enrichment visible in
// the fuzz corpus.
func enrichWithTags(results []mailauth.DKIMResult, tags []SignatureTags) []mailauth.DKIMResult {
	// Walk tags in order and apply them to the matching result; matching
	// is by domain+selector when we can establish it, else by index.
	n := len(results)
	for i, t := range tags {
		if i >= n {
			break
		}
		if results[i].Domain == "" || strings.EqualFold(results[i].Domain, t.Domain) {
			if results[i].Selector == "" {
				results[i].Selector = t.Selector
			}
			if results[i].Algorithm == "" {
				results[i].Algorithm = t.Algorithm
			}
		}
	}
	return results
}

// ReadAll is a helper for callers that have a streaming body. It reads
// the entire message into memory and delegates to Verify. The stdlib
// already does this for net/mail-sized messages; SMTP delivery paths
// cap the message at the configured max size before reaching here.
func (v *Verifier) ReadAll(ctx context.Context, r io.Reader) ([]mailauth.DKIMResult, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return v.Verify(ctx, raw)
}
