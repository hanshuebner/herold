package mailarc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailauth"
)

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

// Verify inspects raw (RFC 5322 message bytes) for an ARC chain and
// returns its status.
//
// When no ARC headers are present the result is Status == AuthNone with
// Chain == 0 and a nil error. When the chain is structurally valid the
// status is AuthPass; structural failures produce AuthFail with a
// reason. A non-nil error indicates an internal failure (context
// cancel, allocator limits).
func (v *Verifier) Verify(ctx context.Context, raw []byte) (mailauth.ARCResult, error) {
	if err := ctx.Err(); err != nil {
		return mailauth.ARCResult{}, err
	}

	sets, err := extractARCSets(raw)
	if err != nil {
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Reason: err.Error(),
		}, nil
	}
	if len(sets) == 0 {
		return mailauth.ARCResult{Status: mailauth.AuthNone}, nil
	}
	if len(sets) > MaxChainLength {
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
			return mailauth.ARCResult{
				Status: mailauth.AuthFail,
				Chain:  len(sets),
				Reason: fmt.Sprintf("missing or duplicate instance %d", want),
			}, nil
		}
		if !s.hasSeal || !s.hasMsgSig || !s.hasAAR {
			return mailauth.ARCResult{
				Status: mailauth.AuthFail,
				Chain:  len(sets),
				Reason: fmt.Sprintf("instance %d: incomplete set", s.instance),
			}, nil
		}
	}

	// RFC 8617 §5.1.1: the latest ARC-Seal carries the cv= tag that
	// summarises the chain's cumulative status. cv=fail on any hop
	// taints the whole chain.
	last := sets[len(sets)-1]
	cv := strings.ToLower(last.cv)
	switch cv {
	case "pass":
		return mailauth.ARCResult{Status: mailauth.AuthPass, Chain: len(sets)}, nil
	case "none":
		// Valid only at i=1 when no previous chain existed.
		if last.instance == 1 {
			return mailauth.ARCResult{Status: mailauth.AuthPass, Chain: len(sets)}, nil
		}
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Chain:  len(sets),
			Reason: "cv=none at i>1",
		}, nil
	case "fail":
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Chain:  len(sets),
			Reason: "cv=fail",
		}, nil
	default:
		return mailauth.ARCResult{
			Status: mailauth.AuthFail,
			Chain:  len(sets),
			Reason: fmt.Sprintf("unknown cv=%q", last.cv),
		}, nil
	}
}

// arcSet is one logical ARC set: the ARC-Seal, ARC-Message-Signature,
// and ARC-Authentication-Results sharing an i= instance.
type arcSet struct {
	instance  int
	cv        string
	hasSeal   bool
	hasMsgSig bool
	hasAAR    bool
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
		case strings.EqualFold(strings.TrimSpace(name), HeaderARCMessageSignature):
			i, err := parseInstanceTag(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", HeaderARCMessageSignature, err)
			}
			getOrCreate(sets, i).hasMsgSig = true
		case strings.EqualFold(strings.TrimSpace(name), HeaderARCAuthenticationResults):
			i, err := parseInstanceTag(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", HeaderARCAuthenticationResults, err)
			}
			getOrCreate(sets, i).hasAAR = true
		}
	}
	out := make([]arcSet, 0, len(sets))
	for _, s := range sets {
		out = append(out, *s)
	}
	return out, nil
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
