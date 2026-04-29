package maildmarc_test

// activity_test.go verifies that every log record emitted by maildmarc carries
// a valid activity attribute (REQ-OPS-86a).

import (
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// newIngestorForActivity returns an Ingestor wired to a recording logger so
// AssertActivityTagged can inspect emitted records.
func newIngestorForActivity(t *testing.T, log *slog.Logger) (*fakestore.Store, *maildmarc.Ingestor) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs, maildmarc.NewIngestor(fs.Meta(), log, clk)
}

// TestIngest_AggregateReport_ActivityTagged exercises the happy-path
// IngestMessage call so the "ingested DMARC aggregate report" info record
// (activity=system) is emitted.
func TestIngest_AggregateReport_ActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		_, ing := newIngestorForActivity(t, log)
		att := gzReportBytes(t, sampleReportXML)
		raw := buildReportEmail(
			"google.com!example.test!1700000000!1700086400.xml.gz",
			"application/gzip",
			att,
		)
		if _, err := ing.IngestMessage(t.Context(), raw); err != nil {
			t.Fatalf("IngestMessage: %v", err)
		}
	})
}

// TestIngest_DuplicateReport_ActivityTagged exercises the duplicate-report
// path so the "duplicate DMARC report ignored" info record (activity=system)
// is emitted.
func TestIngest_DuplicateReport_ActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		_, ing := newIngestorForActivity(t, log)
		att := gzReportBytes(t, sampleReportXML)
		raw := buildReportEmail(
			"google.com!example.test!1700000000!1700086400.xml.gz",
			"application/gzip",
			att,
		)
		// First ingest succeeds; second triggers the dedupe log record.
		if _, err := ing.IngestMessage(t.Context(), raw); err != nil {
			t.Fatalf("first IngestMessage: %v", err)
		}
		if _, err := ing.IngestMessage(t.Context(), raw); err != nil {
			t.Fatalf("second IngestMessage: %v", err)
		}
	})
}

// TestIngest_MalformedXML_ActivityTagged exercises the parse-failure path.
// The "maildmarc: persist parse failure" warn record is internal; the
// "ingested" path is not reached, but any record that is emitted must be tagged.
func TestIngest_MalformedXML_ActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		_, ing := newIngestorForActivity(t, log)
		att := gzReportBytes(t, "<not-valid-dmarc-xml/>")
		raw := buildReportEmail(
			"google.com!example.test!1700000000!1700086400.xml.gz",
			"application/gzip",
			att,
		)
		if _, err := ing.IngestMessage(t.Context(), raw); err != nil {
			t.Fatalf("IngestMessage: %v", err)
		}
	})
}
