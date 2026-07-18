// Package reparseenvelopes implements the once-off maintenance pass that
// re-derives each message's cached Envelope columns (env_subject,
// env_from, env_message_id, env_date_us, etc.) from its raw blob.
//
// It exists to repair rows ingested before the mailparse header-charset
// fix (commit 2a8ae001, re #257) whose Subject -- and, by the same
// stored-envelope trap documented for the pre-fix oversized-message
// defect, From / Message-ID / Date (re #244) -- were decoded to empty
// and persisted that way. Nothing recomputes those columns after
// ingest except the opportunistic repair-on-read path in Email/get
// (internal/protojmap/mail/email), which only fires for the narrower
// "oversized message" trigger condition and only on the next read of
// that one row. This package walks every row exactly once.
//
// Safety model: for each message, only the fields that are currently
// empty in the stored Envelope are filled from the reparsed value, and
// only when the reparsed value is non-empty. A currently non-empty
// stored field is never overwritten. This makes a run idempotent (a
// second pass changes nothing) and safe against races with concurrent
// application writes (worst case, a concurrent write to a field this
// pass also wants to fill is overwritten by BackfillMessageEnvelope,
// exactly as already happens with the Email/get repair-on-read path
// today).
package reparseenvelopes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/store"
)

// defaultPageSize is the number of messages fetched per
// ListMessageIDsBefore call. Kept modest so a run against a large,
// live production mailbox table does not hold a wide result set or
// starve other queries.
const defaultPageSize = 200

// defaultSampleSize caps the number of before/after subject pairs kept
// for the dry-run / apply report.
const defaultSampleSize = 20

// maxBlobBytes bounds the amount of a single blob read into memory,
// matching the bound used by the existing repair-on-read and
// bodymeta-worker blob-read paths.
const maxBlobBytes = 64 << 20

// SampleChange records one message whose merged Envelope differs from
// the currently stored one, for the human-readable report.
type SampleChange struct {
	ID         store.MessageID
	OldSubject string
	NewSubject string
}

// Summary totals a run.
type Summary struct {
	// Scanned counts every message id yielded by the pager.
	Scanned int
	// Changed counts messages whose merged Envelope differs from the
	// stored one. When Options.Apply is false, this is the count of
	// changes that WOULD be written.
	Changed int
	// Applied counts messages actually written via
	// BackfillMessageEnvelope (0 when Options.Apply is false).
	Applied int
	// SkippedParseError counts messages whose blob failed to parse
	// with an error other than mailparse.ErrTooLarge.
	SkippedParseError int
	// SkippedNoBlob counts messages whose row or blob could not be
	// read (message deleted concurrently, blob missing, or a read
	// error), including the store.ErrNotFound race where the message
	// existed when listed but was gone by the time it was fetched.
	SkippedNoBlob int
	// WriteErrors counts messages where the merged Envelope differed
	// from the stored one, Options.Apply was true, and
	// BackfillMessageEnvelope returned an error. The run continues
	// past these; they are reported so the operator can re-run.
	WriteErrors int
	// Samples holds up to Options.SampleSize (default 20) of the
	// changed messages, in the order encountered.
	Samples []SampleChange
}

// Options configures a run.
type Options struct {
	// Apply performs the BackfillMessageEnvelope writes. When false
	// (the default the CLI wires), the run is entirely read-only: no
	// store mutation of any kind occurs.
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
// merges any newly-recovered envelope fields into the stored row. See
// the package doc for the merge/safety rules.
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
			return sum, fmt.Errorf("reparseenvelopes: list message ids: %w", err)
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

	opts := mailparse.NewParseOptions()
	parsed, perr := mailparse.Parse(bytes.NewReader(raw), opts)
	if perr != nil && !errors.Is(perr, mailparse.ErrTooLarge) {
		sum.SkippedParseError++
		return
	}
	// On ErrTooLarge, mailparse.Parse still recovers the headers (re
	// #244) and returns a non-zero Message when it can, so parsed is
	// usable for envelope purposes even though its body was not walked.

	reparsed := email.BuildStoreEnvelope(parsed)
	merged, changed := mergeEnvelope(m.Envelope, reparsed)
	if !changed {
		return
	}

	sum.Changed++
	if len(sum.Samples) < sampleSize {
		sum.Samples = append(sum.Samples, SampleChange{
			ID:         id,
			OldSubject: m.Envelope.Subject,
			NewSubject: merged.Subject,
		})
	}
	if !apply {
		return
	}
	if err := st.Meta().BackfillMessageEnvelope(ctx, id, merged); err != nil {
		sum.WriteErrors++
		return
	}
	sum.Applied++
}

// mergeEnvelope starts from stored and fills each field from reparsed
// only when the stored field is empty/zero and the reparsed value is
// non-empty. It never overwrites a non-empty stored field. Returns the
// merged Envelope and whether it differs from stored.
func mergeEnvelope(stored, reparsed store.Envelope) (store.Envelope, bool) {
	merged := stored
	changed := false
	fill := func(dst *string, src string) {
		if *dst == "" && src != "" {
			*dst = src
			changed = true
		}
	}
	fill(&merged.Subject, reparsed.Subject)
	fill(&merged.From, reparsed.From)
	fill(&merged.To, reparsed.To)
	fill(&merged.Cc, reparsed.Cc)
	fill(&merged.Bcc, reparsed.Bcc)
	fill(&merged.ReplyTo, reparsed.ReplyTo)
	fill(&merged.MessageID, reparsed.MessageID)
	fill(&merged.InReplyTo, reparsed.InReplyTo)
	fill(&merged.References, reparsed.References)
	if merged.Date.IsZero() && !reparsed.Date.IsZero() {
		merged.Date = reparsed.Date
		changed = true
	}
	return merged, changed
}
