package storefts

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// TextExtractor turns a message body byte stream into the plain text the
// FTS index will consume. Phase 1 wraps internal/mailparse to pull text/*
// leaves; Phase 1.5 layers on attachment extractors (PDF, DOCX, XLSX)
// that wrap an existing TextExtractor and append extracted attachment
// text to its output.
//
// The extension point is deliberate: a new backend registers by
// implementing Extract and composing on top of MailparseExtractor; nothing
// in the worker assumes a specific implementation.
type TextExtractor interface {
	// Extract consumes body and returns the plain-text representation of
	// the message suitable for indexing. Implementations must not close
	// body; the caller manages the reader lifecycle.
	Extract(ctx context.Context, msg store.Message, body io.Reader) (string, error)
}

// MailparseExtractor is the Phase 1 TextExtractor: it parses the blob
// through internal/mailparse and concatenates the text of every text/*
// leaf, separated by blank lines. Attachment text extraction (PDF /
// DOCX / XLSX / PPTX / HTML-to-text / archive recursion) is Phase 1.5
// work; a separate extractor will wrap this one and append attachment
// text before returning.
//
// TODO(phase-1.5-fts): add AttachmentExtractor composition so PDF/DOCX/
// XLSX/PPTX/HTML text lands in the same indexed string. Gate extraction
// behind per-attachment and per-message caps from
// docs/design/architecture/02-storage-architecture.md §FTS.
type MailparseExtractor struct {
	// Options controls the mailparse strictness caps. Zero value uses
	// NewParseOptions defaults; operators may tighten or relax
	// per-deployment (e.g. tolerate StrictCharset failures that come
	// from the wild).
	Options mailparse.ParseOptions
}

// NewMailparseExtractor returns a MailparseExtractor with production
// defaults. Callers override Options after construction if needed.
func NewMailparseExtractor() *MailparseExtractor {
	return &MailparseExtractor{Options: mailparse.NewParseOptions()}
}

// Extract implements TextExtractor. The message argument is currently
// unused (the parser reads everything from body) but is retained in the
// signature so future extractors can tailor handling by flags, size, or
// MIME-from-cache without an API break.
func (e *MailparseExtractor) Extract(
	ctx context.Context,
	_ store.Message,
	body io.Reader,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parsed, err := mailparse.Parse(body, e.Options)
	if err != nil {
		return "", fmt.Errorf("storefts: parse: %w", err)
	}
	parts := mailparse.TextParts(parsed)
	if len(parts) == 0 {
		// Nothing textual in the message; returning the subject alone
		// would duplicate what IndexMessage already writes, so leave the
		// body field empty.
		return "", nil
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}
