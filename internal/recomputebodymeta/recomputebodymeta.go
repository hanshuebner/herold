// Package recomputebodymeta implements the once-off maintenance pass
// that re-derives each message's persisted body metadata (the RFC
// 8621 Email.preview value plus has_attachment) from its raw blob.
//
// It exists to repair rows ingested before the mailparse.BodyPreview
// HTML-fallback fix (commit 8dffa63c, re #263): messages with an
// HTML-only body (no text/plain part) had their preview persisted as
// raw, untruncated HTML rather than extracted text. The background
// bodymeta worker (internal/bodymeta) only computes body meta once
// per message -- it skips any row where body_meta_computed is already
// true -- so it never revisits these already-computed-but-wrong rows.
// This package walks every row exactly once and repairs them.
//
// Safety model: for each message the preview and has_attachment are
// recomputed from the raw blob using the current (fixed)
// mailparse.BodyPreview / mailparse.HasAttachment logic, and the
// stored row is overwritten ONLY when the recomputed value differs
// from what is currently stored. This is deliberately
// overwrite-when-changed rather than fill-empty: the defect this pass
// repairs left a wrong, non-empty value in place, so an emptiness
// check would never trigger the fix. A row whose recomputed value
// already matches storage is left untouched, which makes a run
// idempotent (a second pass changes nothing).
package recomputebodymeta

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// defaultPageSize is the number of messages fetched per
// ListMessageIDsBefore call. Kept modest so a run against a large,
// live production mailbox table does not hold a wide result set or
// starve other queries.
const defaultPageSize = 200

// defaultSampleSize caps the number of before/after preview pairs
// kept for the dry-run / apply report.
const defaultSampleSize = 20

// defaultPreviewSampleChars bounds how much of the old/new preview is
// kept in a SampleChange, so a report against a message with an
// unusually long raw-HTML preview stays readable.
const defaultPreviewSampleChars = 80

// maxBlobBytes bounds the amount of a single blob read into memory,
// matching the bound used by the bodymeta worker's own blob-read path.
const maxBlobBytes = 64 << 20

// SampleChange records one message whose recomputed body meta differs
// from the currently stored one, for the human-readable report.
type SampleChange struct {
	ID         store.MessageID
	OldPreview string
	NewPreview string
	OldHasAtt  bool
	NewHasAtt  bool
}

// Summary totals a run.
type Summary struct {
	// Scanned counts every message id yielded by the pager.
	Scanned int
	// Changed counts messages whose recomputed (preview, has_attachment)
	// differs from the stored one. When Options.Apply is false, this is
	// the count of changes that WOULD be written.
	Changed int
	// Applied counts messages actually written via SetMessageBodyMeta
	// (0 when Options.Apply is false).
	Applied int
	// SkippedParseError counts messages whose blob failed to parse
	// with an error other than mailparse.ErrTooLarge.
	SkippedParseError int
	// SkippedNoBlob counts messages whose row or blob could not be
	// read (message deleted concurrently, blob missing, or a read
	// error), including the store.ErrNotFound race where the message
	// existed when listed but was gone by the time it was fetched.
	SkippedNoBlob int
	// WriteErrors counts messages where the recomputed body meta
	// differed from stored, Options.Apply was true, and
	// SetMessageBodyMeta returned an error. The run continues past
	// these; they are reported so the operator can re-run.
	WriteErrors int
	// Samples holds up to Options.SampleSize (default 20) of the
	// changed messages, in the order encountered.
	Samples []SampleChange
}

// Options configures a run.
type Options struct {
	// Apply performs the SetMessageBodyMeta writes. When false (the
	// default the CLI wires), the run is entirely read-only: no store
	// mutation of any kind occurs.
	Apply bool
	// PageSize overrides defaultPageSize when > 0.
	PageSize int
	// SampleSize overrides defaultSampleSize when > 0.
	SampleSize int
	// Progress, when non-nil, is called after each page of messages is
	// processed with the running Summary so far. Callers use this to
	// print progress on a long-running pass; Run does not log on its
	// own.
	Progress func(Summary)
}

// Run walks every message in st, oldest-page-last (the pager returns
// descending id order, newest first), reparses its raw blob, and
// overwrites the stored preview/has_attachment when the recomputed
// value differs. See the package doc for the overwrite-when-changed
// safety rule.
//
// Run always returns the accumulated Summary, even when it returns a
// non-nil error: a pager failure partway through still reports what
// was scanned before the failure.
func Run(ctx context.Context, st store.Store, opts Options) (Summary, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	sampleSize := opts.SampleSize
	if sampleSize <= 0 {
		sampleSize = defaultSampleSize
	}

	var sum Summary
	var beforeID store.MessageID
	for {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		ids, err := st.Meta().ListMessageIDsBefore(ctx, beforeID, pageSize)
		if err != nil {
			return sum, fmt.Errorf("recomputebodymeta: list message ids: %w", err)
		}
		if len(ids) == 0 {
			return sum, nil
		}
		for _, id := range ids {
			sum.Scanned++
			processOne(ctx, st, id, opts.Apply, sampleSize, &sum)
		}
		beforeID = ids[len(ids)-1]
		if opts.Progress != nil {
			opts.Progress(sum)
		}
		if len(ids) < pageSize {
			return sum, nil
		}
	}
}

// processOne handles a single message id, mutating sum in place.
// Errors reading the message or its blob, or a hard parse failure, are
// counted and swallowed -- the run never aborts on a single bad row.
func processOne(ctx context.Context, st store.Store, id store.MessageID, apply bool, sampleSize int, sum *Summary) {
	m, err := st.Meta().GetMessage(ctx, id)
	if err != nil {
		sum.SkippedNoBlob++
		return
	}
	rc, err := st.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		sum.SkippedNoBlob++
		return
	}
	raw, rerr := io.ReadAll(io.LimitReader(rc, maxBlobBytes))
	_ = rc.Close()
	if rerr != nil {
		sum.SkippedNoBlob++
		return
	}

	popts := mailparse.NewLenientParseOptions()
	parsed, perr := mailparse.Parse(bytes.NewReader(raw), popts)
	if perr != nil && !errors.Is(perr, mailparse.ErrTooLarge) {
		sum.SkippedParseError++
		return
	}
	// On ErrTooLarge, mailparse.Parse still recovers what it can of the
	// body structure it walked before hitting the limit, so parsed
	// remains usable for a best-effort preview/attachment recompute.

	newPreview := mailparse.BodyPreview(parsed, 256)
	newHasAtt := mailparse.HasAttachment(parsed)
	if newPreview == m.Preview && newHasAtt == m.HasAttachment {
		return
	}

	sum.Changed++
	if len(sum.Samples) < sampleSize {
		sum.Samples = append(sum.Samples, SampleChange{
			ID:         id,
			OldPreview: truncateSample(m.Preview),
			NewPreview: truncateSample(newPreview),
			OldHasAtt:  m.HasAttachment,
			NewHasAtt:  newHasAtt,
		})
	}
	if !apply {
		return
	}
	if err := st.Meta().SetMessageBodyMeta(ctx, id, newPreview, newHasAtt); err != nil {
		sum.WriteErrors++
		return
	}
	sum.Applied++
}

// truncateSample bounds a preview string to defaultPreviewSampleChars
// runes for the human-readable / JSON sample report, appending an
// ellipsis marker when truncated.
func truncateSample(s string) string {
	r := []rune(s)
	if len(r) <= defaultPreviewSampleChars {
		return s
	}
	return string(r[:defaultPreviewSampleChars]) + "..."
}
