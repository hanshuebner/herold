package mailparse

import (
	"errors"
	"fmt"
)

// Reason classifies a parse failure so callers can react without matching on strings.
type Reason int

const (
	// ReasonUnknown is the zero value; callers should treat it as an internal bug.
	ReasonUnknown Reason = iota
	// ReasonTooLarge indicates the input exceeded ParseOptions.MaxSize.
	ReasonTooLarge
	// ReasonDepthExceeded indicates the MIME tree nested deeper than ParseOptions.MaxDepth.
	ReasonDepthExceeded
	// ReasonTooManyParts indicates the part count exceeded ParseOptions.MaxParts.
	ReasonTooManyParts
	// ReasonMalformedBase64 indicates a base64 body failed structural validation.
	ReasonMalformedBase64
	// ReasonMalformedQP indicates a quoted-printable body contained illegal sequences.
	ReasonMalformedQP
	// ReasonUnknownCharset indicates the declared charset is unknown or invalid for the bytes.
	ReasonUnknownCharset
	// ReasonTruncated indicates the message ended without a closing boundary.
	ReasonTruncated
	// ReasonMalformed indicates a general structural RFC 5322 / MIME violation.
	ReasonMalformed
	// ReasonReaderError indicates an error reading from the input stream.
	ReasonReaderError
)

// String returns a short identifier suitable for logs and test assertions.
func (r Reason) String() string {
	switch r {
	case ReasonTooLarge:
		return "too_large"
	case ReasonDepthExceeded:
		return "depth_exceeded"
	case ReasonTooManyParts:
		return "too_many_parts"
	case ReasonMalformedBase64:
		return "malformed_base64"
	case ReasonMalformedQP:
		return "malformed_quoted_printable"
	case ReasonUnknownCharset:
		return "unknown_charset"
	case ReasonTruncated:
		return "truncated"
	case ReasonMalformed:
		return "malformed"
	case ReasonReaderError:
		return "reader_error"
	default:
		return "unknown"
	}
}

// ParseError is the error type returned by Parse. Use errors.As to inspect it.
type ParseError struct {
	Reason  Reason
	Message string
	// PartIndex is the zero-based sequential index of the offending part as
	// discovered by the walker, or -1 if the error is not associated with a part.
	PartIndex int
	// HeaderLine is the 1-based line number inside the message headers where the
	// fault was detected, or 0 if not applicable.
	HeaderLine int
	// Cause wraps the underlying error, if any.
	Cause error
}

// Error formats a ParseError for display.
func (e *ParseError) Error() string {
	loc := ""
	if e.PartIndex >= 0 {
		loc = fmt.Sprintf(" part=%d", e.PartIndex)
	}
	if e.HeaderLine > 0 {
		loc += fmt.Sprintf(" header_line=%d", e.HeaderLine)
	}
	if e.Cause != nil {
		return fmt.Sprintf("mailparse: %s%s: %s: %v", e.Reason, loc, e.Message, e.Cause)
	}
	return fmt.Sprintf("mailparse: %s%s: %s", e.Reason, loc, e.Message)
}

// Unwrap returns the wrapped cause.
func (e *ParseError) Unwrap() error { return e.Cause }

// Is reports whether the error matches a sentinel registered below.
func (e *ParseError) Is(target error) bool {
	var s *sentinelError
	if errors.As(target, &s) {
		return s.reason == e.Reason
	}
	return false
}

// sentinelError is the concrete type behind the exported Err* sentinels so that
// errors.Is works without pulling callers into a full switch on Reason.
type sentinelError struct {
	reason Reason
}

func (s *sentinelError) Error() string { return "mailparse: " + s.reason.String() }

func sentinel(r Reason) error { return &sentinelError{reason: r} }

// Sentinel values usable with errors.Is against a ParseError.
var (
	ErrTooLarge        = sentinel(ReasonTooLarge)
	ErrDepthExceeded   = sentinel(ReasonDepthExceeded)
	ErrTooManyParts    = sentinel(ReasonTooManyParts)
	ErrMalformedBase64 = sentinel(ReasonMalformedBase64)
	ErrMalformedQP     = sentinel(ReasonMalformedQP)
	ErrUnknownCharset  = sentinel(ReasonUnknownCharset)
	ErrTruncated       = sentinel(ReasonTruncated)
	ErrMalformed       = sentinel(ReasonMalformed)
	ErrReader          = sentinel(ReasonReaderError)
)

func newError(r Reason, partIndex int, msg string, cause error) *ParseError {
	return &ParseError{Reason: r, Message: msg, PartIndex: partIndex, Cause: cause}
}
