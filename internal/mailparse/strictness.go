package mailparse

import (
	"fmt"

	"github.com/jhillyerd/enmime"
)

// checkTruncation verifies that every multipart container in the tree shows evidence of a
// closing boundary via enmime's own error collection. The raw-byte scanner in parse.go is
// the authoritative check; this layer surfaces any severe boundary errors enmime recorded.
func checkTruncation(env *enmime.Envelope, partsSeen int) *ParseError {
	if env == nil || env.Root == nil {
		return nil
	}
	if e := firstSevereBoundaryError(env.Root); e != nil {
		return &ParseError{
			Reason:    ReasonTruncated,
			Message:   fmt.Sprintf("%s: %s", e.Name, e.Detail),
			PartIndex: partsSeen,
		}
	}
	return nil
}

// firstSevereBoundaryError returns the first enmime error whose Name explicitly categorizes
// the problem as a boundary issue. Matching on Detail substrings produces false positives
// against malformed Content-Type headers that mention the word "boundary" in passing.
func firstSevereBoundaryError(p *enmime.Part) *enmime.Error {
	if p == nil {
		return nil
	}
	for _, e := range p.Errors {
		if e.Name == enmime.ErrorMissingBoundary {
			return e
		}
	}
	for c := p.FirstChild; c != nil; c = c.NextSibling {
		if e := firstSevereBoundaryError(c); e != nil {
			return e
		}
	}
	return nil
}
