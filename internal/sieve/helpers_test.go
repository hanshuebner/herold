package sieve

import (
	"bytes"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// parseSafe is a test helper that runs mailparse.Parse with lenient
// options and swallows the error for fuzz targets that need to accept
// arbitrary input without exploding.
func parseSafe(raw []byte) (mailparse.Message, error) {
	opts := mailparse.ParseOptions{
		MaxSize:          512 * 1024,
		MaxDepth:         8,
		MaxParts:         256,
		MaxHeaderLine:    2048,
		StrictCharset:    false,
		StrictBase64:     false,
		StrictQP:         false,
		StrictHeaderLine: false,
		StrictBoundary:   false,
	}
	return mailparse.Parse(bytes.NewReader(raw), opts)
}
