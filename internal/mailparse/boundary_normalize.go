package mailparse

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"regexp"
	"sort"
)

// boundaryParamRE captures the value of any `boundary=` parameter on a
// Content-Type header. The match groups expose the value either as a
// double-quoted, single-quoted, or unquoted form. We are intentionally
// case-insensitive on the parameter name (RFC 2045 §5.1: parameter names are
// case-insensitive).
var boundaryParamRE = regexp.MustCompile(`(?i)boundary\s*=\s*(?:"([^"]+)"|'([^']+)'|([^\s;]+))`)

// normalizeTrailingDashBoundaries rewrites multipart boundary values that end
// in '-' to fresh unique values that do not, and updates every matching
// delimiter line in lockstep.
//
// Why: enmime's boundary reader uses bytes.Index — a substring search — to
// decide whether a line is a part delimiter. When a parent multipart's
// boundary ends in '-', the parent's start-delimiter "--<parentB>" can be a
// substring of a child's start-delimiter "--<childB>" (the leading dashes
// overlap). The parent reader then mistakes the child's "begin part" line for
// a parent delimiter and terminates the parent's content section
// prematurely, so the child sees EOF before it can find its own first part
// — producing a multipart with zero children, which surfaces as a blank
// "(no body)" in the UI.
//
// Real example seen in mail (2026-05-26): outer boundary
// "------=_NextPart_X--", inner "----------=_NextPart_X--". The outer
// start-delimiter "--------=_NextPart_X--" (8 dashes + name + 2 dashes)
// appears at offset 4 inside the inner start-delimiter
// "------------=_NextPart_X--" (12 dashes + name + 2 dashes), so the outer
// reader fires on the inner-start line.
//
// RFC 2046 §5.1.1 permits boundaries to contain '-' (bcharsnospace), so the
// MUA is technically valid even when picking trailing dashes. We work around
// the enmime defect by giving each offending boundary a fresh, unique tag.
//
// Strategy:
//  1. Find every distinct boundary parameter value in raw that ends in '-'.
//     The value may be double-quoted, single-quoted, or an unquoted bare token
//     — all three forms are legal per RFC 2046 §5.1.1 and DO appear in mail
//     from third-party MUAs.
//  2. Generate a fresh `herold-norm-<16-random-hex>` replacement for each.
//  3. Process pairs in longest-old-value-first order to avoid substring
//     collisions: a shorter old value's `--<old>` can be a substring of a
//     longer old value's `--<old>`, so the longer one is replaced first.
//  4. For each (old, new) pair (longest first): rewrite every `--<old>`
//     delimiter, then rewrite the matching `boundary=<old>` parameter in the
//     header. Both rewrites run in the same iteration so that later, shorter
//     pairs cannot corrupt a longer pair's header parameter via substring
//     match in the `--<old>` delimiter scan.
//
// The function is a no-op when no boundary values end with '-'.
func normalizeTrailingDashBoundaries(raw []byte) []byte {
	type pair struct {
		old, new []byte
	}
	var pairs []pair
	seen := make(map[string]string)

	for _, m := range boundaryParamRE.FindAllSubmatchIndex(raw, -1) {
		var val []byte
		switch {
		case m[2] >= 0:
			val = raw[m[2]:m[3]]
		case m[4] >= 0:
			val = raw[m[4]:m[5]]
		case m[6] >= 0:
			val = raw[m[6]:m[7]]
		}
		if len(val) == 0 || val[len(val)-1] != '-' {
			continue
		}
		s := string(val)
		if _, ok := seen[s]; ok {
			continue
		}
		var nbuf [16]byte
		if _, err := io.ReadFull(rand.Reader, nbuf[:]); err != nil {
			// On rand failure leave the input untouched; downstream parser
			// will surface whatever error it can.
			return raw
		}
		replacement := "herold-norm-" + hex.EncodeToString(nbuf[:])
		seen[s] = replacement
		pairs = append(pairs, pair{old: append([]byte(nil), val...), new: []byte(replacement)})
	}
	if len(pairs) == 0 {
		return raw
	}

	// Replace longest old value first: a shorter old value's `--<old>` bytes
	// can appear as a substring of a longer (still-pending) old value's
	// `--<old>`, so we must rewrite the longer one before the shorter one
	// would erase its leading bytes. The same ordering applies to header
	// parameter rewriting: when boundaries share a suffix (e.g. the outer
	// value "------B--" is a substring of the inner "----------B--"), we must
	// rewrite the inner header parameter before the outer delimiter pass
	// `bytes.ReplaceAll("--" + outerOld)` can inadvertently match inside the
	// still-unmodified inner parameter value. Therefore we keep the header
	// rewrite inside the loop, one pair per iteration, in longest-first order.
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].old) > len(pairs[j].old) })

	out := raw
	for _, p := range pairs {
		// Rewrite every delimiter occurrence. `--<old>` covers both the
		// start-delimiter `--<old>` and the close-delimiter `--<old>--`
		// because the latter is a strict suffix-extension of the former.
		// The header parameter line uses `boundary=<old>` with no leading
		// `--`, so this scan cannot accidentally match the parameter token
		// itself (confirmed for both quoted and unquoted forms).
		out = bytes.ReplaceAll(out, append([]byte("--"), p.old...), append([]byte("--"), p.new...))

		// Rewrite the boundary= parameter in the header for this pair.
		// Unquoted trailing-dash boundaries (e.g. boundary=abc123-) are
		// perfectly legal per RFC 2046 §5.1.1 (bcharsnospace includes '-')
		// and DO appear in mail from third-party MUAs, so anchoring on a
		// surrounding quote alone is not sufficient. We use boundaryParamRE
		// to locate each parameter occurrence and replace only the value
		// substring, preserving the original quoting style (double-quote,
		// single-quote, or unquoted bare token).
		oldVal := p.old
		newVal := p.new
		out = boundaryParamRE.ReplaceAllFunc(out, func(match []byte) []byte {
			// Re-run the regex on this (already-sliced) match to identify
			// which capture group holds the value and its byte extent.
			m := boundaryParamRE.FindSubmatchIndex(match)
			if m == nil {
				return match
			}
			// Groups: 1=double-quoted, 2=single-quoted, 3=unquoted.
			var start, end int
			switch {
			case m[2] >= 0:
				start, end = m[2], m[3]
			case m[4] >= 0:
				start, end = m[4], m[5]
			case m[6] >= 0:
				start, end = m[6], m[7]
			default:
				return match
			}
			if !bytes.Equal(match[start:end], oldVal) {
				return match
			}
			// Replace only the value portion, preserving surrounding quotes.
			return bytes.Replace(match, oldVal, newVal, 1)
		})
	}

	return out
}
