package maildmarc

import (
	"context"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/store"
)

// Aggregator surfaces DMARC aggregate-report data to admin REST + serves
// as the back-compat entry point that the Phase 1 stub returned an error
// from. Construction is cheap; the Aggregator owns no goroutines and is
// safe for concurrent use across requests.
//
// The ingestion pipeline lives in Ingestor (internal/maildmarc/ingest.go).
// Aggregator delegates Ingest to a lazily-constructed Ingestor so callers
// that only want the read-side aggregate path do not pay the parse-side
// cost.
type Aggregator struct {
	meta     store.Metadata
	ingestor *Ingestor
}

// NewAggregator returns an Aggregator backed by meta. clock is forwarded
// to the lazy Ingestor; callers wanting deterministic ingestion timestamps
// pass a FakeClock. A nil clock falls back to a real wall clock.
func NewAggregator(meta store.Metadata, clk clock.Clock) *Aggregator {
	if meta == nil {
		panic("maildmarc: NewAggregator with nil store.Metadata")
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	return &Aggregator{
		meta:     meta,
		ingestor: NewIngestor(meta, nil, clk),
	}
}

// Ingest is the alias the Phase 1 stub returned an error from. It now
// delegates to Ingestor.IngestMessage. raw is an RFC 5322 message; the
// caller does not need to pre-detect that the message is a DMARC report
// (the ingestor returns recognised=false on non-DMARC mail).
func (a *Aggregator) Ingest(ctx context.Context, raw []byte) error {
	if a == nil || a.ingestor == nil {
		return fmt.Errorf("maildmarc: Aggregator not initialised")
	}
	_, err := a.ingestor.IngestMessage(ctx, raw)
	return err
}

// AggregateRow is one per-domain rollup row exposed to the admin REST
// surface. It mirrors store.DMARCAggregateRow but carries operator-
// friendly disposition strings instead of the on-the-wire integers.
type AggregateRow struct {
	// Domain is the policy domain the report covers (echoed for callers
	// that pass a wildcard/empty domain in future revisions).
	Domain string
	// SourceIP is the row's source IP. Not currently aggregated by
	// store.DMARCAggregate (which groups by HeaderFrom + Disposition);
	// kept on the type so a future store-side aggregation that adds the
	// source IP grouping does not need a wire-format change.
	SourceIP string
	// Count is the message count rolled up for this group.
	Count int
	// Disposition is the action the reporter applied (none / quarantine /
	// reject), as a wire-form lowercase token.
	Disposition string
	// SPFAligned reports whether at least one message in the group passed
	// SPF alignment (PassedSPF > 0).
	SPFAligned bool
	// DKIMAligned reports whether at least one message in the group
	// passed DKIM alignment (PassedDKIM > 0).
	DKIMAligned bool
	// HeaderFrom is the RFC 5322.From domain the rollup groups by.
	HeaderFrom string
}

// Aggregate returns a per-domain rollup over [since, until]. Backs on
// store.Metadata.DMARCAggregate; the returned rows are deterministic
// (sorted by HeaderFrom then Disposition) so admin tooling can render
// stable tables without an extra sort pass.
func (a *Aggregator) Aggregate(ctx context.Context, domain string, since, until time.Time) ([]AggregateRow, error) {
	if a == nil || a.meta == nil {
		return nil, fmt.Errorf("maildmarc: Aggregator not initialised")
	}
	rows, err := a.meta.DMARCAggregate(ctx, domain, since, until)
	if err != nil {
		return nil, fmt.Errorf("maildmarc: aggregate: %w", err)
	}
	out := make([]AggregateRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AggregateRow{
			Domain:      domain,
			Count:       int(r.Count),
			Disposition: dispositionToken(r.Disposition),
			SPFAligned:  r.PassedSPF > 0,
			DKIMAligned: r.PassedDKIM > 0,
			HeaderFrom:  r.HeaderFrom,
		})
	}
	return out, nil
}

// dispositionToken renders a numeric disposition (encoded by
// mailauth.DMARCDisposition) into the wire token used by the DMARC XML
// schema and the admin REST surface.
func dispositionToken(d int32) string {
	switch mailauth.DMARCDisposition(d) {
	case mailauth.DispositionQuarantine:
		return "quarantine"
	case mailauth.DispositionReject:
		return "reject"
	default:
		return "none"
	}
}
