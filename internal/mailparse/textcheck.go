package mailparse

import (
	"strings"
	"unicode/utf8"
)

// binaryMagicPrefixes lists the leading byte sequences of common binary file
// formats. A text/plain leaf whose decoded content starts with one of these
// is not text regardless of its declared Content-Type -- it is a binary
// payload the sender (or an internal rewriter, per #324) mislabelled.
var binaryMagicPrefixes = []string{
	"\x89PNG\r\n\x1a\n", // PNG
	"\xff\xd8\xff",      // JPEG
	"GIF87a",            // GIF
	"GIF89a",            // GIF
	"%PDF-",             // PDF
	"PK\x03\x04",        // ZIP (local file header)
	"PK\x05\x06",        // ZIP (empty archive)
	"PK\x07\x08",        // ZIP (spanned archive)
}

// LooksLikeText reports whether s is plausibly genuine text rather than
// binary content mislabelled as text/plain: valid UTF-8, free of NUL bytes,
// and not prefixed by a known binary file signature. The empty string
// satisfies all three checks trivially, so a genuinely empty part is still
// "text" -- it is the caller's job (e.g. BodyPreview's empty-string check)
// to treat an empty result as "no content" and fall back accordingly.
//
// This is the shared guard behind the JMAP Email.preview computation
// (mailparse.BodyPreview and protojmap/mail/email.previewFromValues) so
// that a text/plain leaf whose decoded bytes are actually a binary payload
// (re #325 -- e.g. an inline image whose Content-Type was invalidated by
// the #324 extimg defect and defaulted to text/plain per RFC 2045) is
// skipped in favour of the next text/plain candidate, then HTML extraction,
// then an empty preview -- never raw binary bytes.
func LooksLikeText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	if strings.IndexByte(s, 0) >= 0 {
		return false
	}
	for _, magic := range binaryMagicPrefixes {
		if strings.HasPrefix(s, magic) {
			return false
		}
	}
	return true
}
