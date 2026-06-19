package mailparse

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime/quotedprintable"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

// openBodyDecoder returns a streaming reader that CTE-decodes (and optionally
// charset-converts) a raw section reader. This is the back-end for Part.OpenBody.
//
// For base64: wraps src in a whitespace-stripping reader then a base64 decoder.
// For quoted-printable: wraps in a QP reader.
// For text parts with a non-UTF8 charset: chains through a charset decoder.
// For all other cases: passes src through unchanged.
func openBodyDecoder(src io.Reader, cte, charset string, isText bool) (io.ReadCloser, error) {
	var r io.Reader = src
	cteLower := strings.ToLower(strings.TrimSpace(cte))
	switch cteLower {
	case "base64":
		// base64.NewDecoder requires pure base64 (no whitespace).
		// Wrap with a whitespace-stripping reader first.
		r = base64.NewDecoder(base64.StdEncoding, &wsStripReader{r: src})
	case "quoted-printable":
		r = quotedprintable.NewReader(src)
	}

	// For text parts with a non-identity charset, chain through a charset decoder.
	if isText {
		norm := strings.ToLower(strings.TrimSpace(charset))
		switch norm {
		case "", "utf-8", "utf8", "us-ascii", "ascii", "7bit":
			// Already UTF-8 or ASCII-compatible; no conversion needed.
		default:
			enc, err := htmlindex.Get(norm)
			if err != nil {
				// Unknown charset: return as-is; caller can handle.
				return io.NopCloser(r), fmt.Errorf("mailparse: OpenBody: unknown charset %q: %w", charset, err)
			}
			r = transform.NewReader(r, enc.NewDecoder())
		}
	}

	return io.NopCloser(r), nil
}

// wsStripReader wraps an io.Reader and strips ASCII whitespace (SP HT CR LF)
// on the fly. Used for base64 decoding since net/mail messages wrap base64
// content every 76 characters.
type wsStripReader struct {
	r   io.Reader
	buf [512]byte
}

func (w *wsStripReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Read a chunk from the underlying reader, strip whitespace, fill p.
	// We may need multiple passes if all bytes are whitespace.
	out := 0
	for out < len(p) {
		chunk := w.buf[:]
		if len(chunk) > len(p)-out {
			chunk = chunk[:len(p)-out]
		}
		n, err := w.r.Read(chunk)
		for i := 0; i < n; i++ {
			c := chunk[i]
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				p[out] = c
				out++
			}
		}
		if err != nil {
			if out > 0 {
				return out, nil
			}
			return 0, err
		}
		if out > 0 {
			break
		}
	}
	return out, nil
}

// charsetDecoder returns an x/text encoding.Decoder for charset, or nil if the
// charset requires no conversion (UTF-8/ASCII/empty) or is unknown. Used by
// both openBodyDecoder (streaming) and convertCharset (batch).
func charsetDecoder(charset string) *encoding.Decoder {
	norm := strings.ToLower(strings.TrimSpace(charset))
	switch norm {
	case "", "utf-8", "utf8", "us-ascii", "ascii", "7bit":
		return nil
	}
	enc, err := htmlindex.Get(norm)
	if err != nil {
		return nil
	}
	return enc.NewDecoder()
}
