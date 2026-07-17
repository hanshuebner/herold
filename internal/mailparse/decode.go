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
	dec, _ := resolveCharset(charset)
	return dec
}

// resolveCharset is the single charset registry shared by the body decoder
// (openBodyDecoder / convertCharset) and the RFC 2047 header decoder
// (headerCharsetReader, re #257): both agree on which charset names are
// known via golang.org/x/text/encoding/htmlindex, so a charset like
// ISO-8859-15 that decodes correctly in a body part also decodes in a
// Subject/From/etc. header.
//
// utf8Compatible is true when charset requires no conversion (empty,
// UTF-8, US-ASCII, or 7bit): the caller should pass the bytes through
// unchanged rather than treat dec == nil as "unknown".
func resolveCharset(charset string) (dec *encoding.Decoder, utf8Compatible bool) {
	norm := strings.ToLower(strings.TrimSpace(charset))
	switch norm {
	case "", "utf-8", "utf8", "us-ascii", "ascii", "7bit":
		return nil, true
	}
	enc, err := htmlindex.Get(norm)
	if err != nil {
		return nil, false
	}
	return enc.NewDecoder(), false
}

// headerCharsetReader is installed as mime.WordDecoder.CharsetReader for all
// RFC 2047 header decoding (Subject, From/To/Cc/Bcc/Reply-To/Sender display
// names, In-Reply-To/References) so headers resolve charsets via the same
// htmlindex-backed registry the body decoder uses (re #257).
//
// It never returns an error. mime.WordDecoder.DecodeHeader discards the
// ENTIRE header -- not just the failing encoded-word -- when CharsetReader
// returns an error, which is exactly how an ISO-8859-15 Subject came out as
// "". For a charset unknown even to htmlindex, the raw bytes are decoded
// byte-for-byte as Latin-1 (every byte maps to a Unicode code point, so this
// can never fail) instead of erroring, keeping the header non-empty.
func headerCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	dec, utf8Compatible := resolveCharset(charset)
	if utf8Compatible {
		return input, nil
	}
	if dec != nil {
		return transform.NewReader(input, dec), nil
	}
	return latin1Reader(input)
}

// latin1Reader reads all of r and returns a reader over the Latin-1
// (byte-for-byte, ISO-8859-1) decoding of those bytes as UTF-8. Used as the
// last-resort fallback in headerCharsetReader for charset names htmlindex
// does not recognize.
func latin1Reader(r io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.Grow(len(raw) * 2)
	for _, c := range raw {
		sb.WriteRune(rune(c))
	}
	return strings.NewReader(sb.String()), nil
}
