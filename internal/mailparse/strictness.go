package mailparse

// strictness.go — strictness checks for the MIME parser.
//
// The previous enmime-based implementation had checkTruncation / firstSevereBoundaryError
// here. Those functions are now integrated into the mimeWalker in parse.go:
// boundary truncation is detected by mimeWalker.walkMultipart checking for the
// --boundary-- close marker and the StrictBoundary option.
//
// looksLikeQPError is no longer needed because the new parser detects quoted-
// printable errors directly via mime/quotedprintable.
