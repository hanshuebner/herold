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

// MailparseExtractor is the production TextExtractor: it parses the
// blob through internal/mailparse, concatenates the text of every
// text/* leaf, then walks the message attachments and appends extracted
// text from supported binary formats (HTML, DOCX, XLSX, PPTX). PDF
// extraction is gated behind a separate optional extractor so the
// default build does not pull in a PDF parser.
//
// Architecture: docs/design/server/architecture/02-storage-architecture.md
// §FTS specifies per-attachment and per-message extraction caps with a
// counter on truncation; PerAttachmentMaxBytes and PerMessageMaxBytes
// implement those bounds.
type MailparseExtractor struct {
	// Options controls the mailparse strictness caps. Zero value uses
	// NewParseOptions defaults; operators may tighten or relax
	// per-deployment (e.g. tolerate StrictCharset failures that come
	// from the wild).
	Options mailparse.ParseOptions

	// PerAttachmentMaxBytes caps the extracted-text length for a single
	// attachment; the default is 5 MiB per the FTS architecture spec.
	// A zero value is treated as the default; pass a negative value
	// (or use the test seam SetCaps) to disable the cap.
	PerAttachmentMaxBytes int

	// PerMessageMaxBytes caps the total extracted-text length across
	// all attachments in a single message; default 20 MiB.
	PerMessageMaxBytes int
}

// NewMailparseExtractor returns a MailparseExtractor with production
// defaults. Callers override Options after construction if needed.
func NewMailparseExtractor() *MailparseExtractor {
	return &MailparseExtractor{
		Options:               mailparse.NewParseOptions(),
		PerAttachmentMaxBytes: defaultPerAttachmentMaxBytes,
		PerMessageMaxBytes:    defaultPerMessageMaxBytes,
	}
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
	var b strings.Builder
	for i, p := range mailparse.TextParts(parsed) {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(p.Text)
	}
	e.appendAttachmentText(&b, mailparse.Attachments(parsed))
	return b.String(), nil
}

// appendAttachmentText runs every attachment through the format
// dispatcher, capped by PerAttachmentMaxBytes (per item) and
// PerMessageMaxBytes (running total). Per-attachment errors are logged
// to the metric and skipped; a single malformed PDF or DOCX must not
// fail the whole index call.
func (e *MailparseExtractor) appendAttachmentText(b *strings.Builder, parts []mailparse.Part) {
	if len(parts) == 0 {
		return
	}
	perAttach := e.PerAttachmentMaxBytes
	if perAttach == 0 {
		perAttach = defaultPerAttachmentMaxBytes
	}
	perMsg := e.PerMessageMaxBytes
	if perMsg == 0 {
		perMsg = defaultPerMessageMaxBytes
	}
	remaining := perMsg
	for _, p := range parts {
		text, format, attachTrunc, err := extractAttachmentText(p, perAttach)
		if err != nil {
			recordExtraction(format, "error")
			continue
		}
		if format == "skipped" {
			// Unrecognised format: don't bump the counter so the metric
			// stays scoped to handler outcomes.
			continue
		}
		if text == "" {
			recordExtraction(format, "ok")
			continue
		}
		msgTrunc := false
		if remaining >= 0 && len(text) > remaining {
			text = text[:remaining]
			msgTrunc = true
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		if remaining >= 0 {
			remaining -= len(text)
		}
		switch {
		case msgTrunc:
			recordExtraction(format, "truncated_message")
		case attachTrunc:
			recordExtraction(format, "truncated_attachment")
		default:
			recordExtraction(format, "ok")
		}
		if remaining == 0 {
			return
		}
	}
}
