package maildmarc_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

const sampleReportXML = `<?xml version="1.0" encoding="UTF-8"?>
<feedback>
  <report_metadata>
    <org_name>google.com</org_name>
    <email>noreply-dmarc-support@google.com</email>
    <report_id>1234567890</report_id>
    <date_range>
      <begin>1700000000</begin>
      <end>1700086400</end>
    </date_range>
  </report_metadata>
  <policy_published>
    <domain>example.test</domain>
    <adkim>r</adkim>
    <aspf>r</aspf>
    <p>quarantine</p>
    <pct>100</pct>
  </policy_published>
  <record>
    <row>
      <source_ip>192.0.2.1</source_ip>
      <count>3</count>
      <policy_evaluated>
        <disposition>none</disposition>
        <dkim>pass</dkim>
        <spf>pass</spf>
      </policy_evaluated>
    </row>
    <identifiers>
      <header_from>example.test</header_from>
      <envelope_from>example.test</envelope_from>
    </identifiers>
    <auth_results>
      <dkim>
        <domain>example.test</domain>
        <result>pass</result>
        <selector>herold</selector>
      </dkim>
      <spf>
        <domain>example.test</domain>
        <result>pass</result>
      </spf>
    </auth_results>
  </record>
  <record>
    <row>
      <source_ip>198.51.100.4</source_ip>
      <count>1</count>
      <policy_evaluated>
        <disposition>quarantine</disposition>
        <dkim>fail</dkim>
        <spf>fail</spf>
      </policy_evaluated>
    </row>
    <identifiers>
      <header_from>example.test</header_from>
    </identifiers>
    <auth_results>
      <dkim>
        <domain>impostor.test</domain>
        <result>fail</result>
      </dkim>
      <spf>
        <domain>impostor.test</domain>
        <result>fail</result>
      </spf>
    </auth_results>
  </record>
</feedback>`

// gzReportBytes returns the gzipped XML bytes.
func gzReportBytes(t *testing.T, xml string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(xml)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// zipReportBytes returns a zip archive containing one .xml entry — the
// Yahoo-shaped wrapper.
func zipReportBytes(t *testing.T, xml string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("report.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(xml)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// buildReportEmail synthesises an inbound RFC 5322 message that carries
// the supplied attachment with the given filename and content type. The
// Subject matches the canonical DMARC report pattern so the textual
// detector recognises it independent of the filename.
func buildReportEmail(filename, contentType string, attachment []byte) []byte {
	const boundary = "BOUNDARY-DMARC"
	enc := encodeBase64Wrapped(attachment)
	return []byte(strings.Join([]string{
		"From: noreply-dmarc-support@google.com",
		"To: dmarc-reports@example.test",
		"Subject: Report Domain: example.test Submitter: google.com Report-ID: <1234567890>",
		"Date: Mon, 14 Nov 2023 00:00:00 +0000",
		"MIME-Version: 1.0",
		fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"", boundary),
		"",
		"--" + boundary,
		"Content-Type: text/plain; charset=utf-8",
		"",
		"This is a DMARC aggregate report.",
		"--" + boundary,
		fmt.Sprintf("Content-Type: %s", contentType),
		"Content-Transfer-Encoding: base64",
		fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"", filename),
		"",
		enc,
		"--" + boundary + "--",
		"",
	}, "\r\n"))
}

// encodeBase64Wrapped renders the byte slice as wrapped base64 so the
// inbound parser does not reject overlong lines.
func encodeBase64Wrapped(b []byte) string {
	const wrap = 76
	enc := []byte(base64Encode(b))
	var out strings.Builder
	for i := 0; i < len(enc); i += wrap {
		end := i + wrap
		if end > len(enc) {
			end = len(enc)
		}
		out.Write(enc[i:end])
		out.WriteString("\r\n")
	}
	return out.String()
}

// base64Encode is a thin wrapper that lets us avoid importing encoding/base64
// with the wrapping helper inline.
func base64Encode(b []byte) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	sb.Grow((len(b) + 2) / 3 * 4)
	i := 0
	for ; i+3 <= len(b); i += 3 {
		v := uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
		sb.WriteByte(tbl[(v>>18)&0x3f])
		sb.WriteByte(tbl[(v>>12)&0x3f])
		sb.WriteByte(tbl[(v>>6)&0x3f])
		sb.WriteByte(tbl[v&0x3f])
	}
	switch len(b) - i {
	case 1:
		v := uint32(b[i]) << 16
		sb.WriteByte(tbl[(v>>18)&0x3f])
		sb.WriteByte(tbl[(v>>12)&0x3f])
		sb.WriteString("==")
	case 2:
		v := uint32(b[i])<<16 | uint32(b[i+1])<<8
		sb.WriteByte(tbl[(v>>18)&0x3f])
		sb.WriteByte(tbl[(v>>12)&0x3f])
		sb.WriteByte(tbl[(v>>6)&0x3f])
		sb.WriteByte('=')
	}
	return sb.String()
}

// newIngestor returns a (store, ingestor) pair under a fixed clock.
func newIngestor(t *testing.T) (store.Store, *maildmarc.Ingestor) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return fs, maildmarc.NewIngestor(fs.Meta(), logger, clk)
}

func TestIngestMessage_Gzip_HappyPath(t *testing.T) {
	fs, ing := newIngestor(t)
	att := gzReportBytes(t, sampleReportXML)
	raw := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)

	rec, err := ing.IngestMessage(t.Context(), raw)
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if !rec {
		t.Fatalf("recognised = false; want true")
	}

	reports, err := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
	if err != nil {
		t.Fatalf("ListDMARCReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	r := reports[0]
	if r.ReporterOrg != "google.com" {
		t.Errorf("reporter_org = %q", r.ReporterOrg)
	}
	if r.ReportID != "1234567890" {
		t.Errorf("report_id = %q", r.ReportID)
	}
	if r.Domain != "example.test" {
		t.Errorf("domain = %q", r.Domain)
	}
	if !r.ParsedOK {
		t.Errorf("parsed_ok = false; want true (parse_error=%q)", r.ParseError)
	}
	_, rows, err := fs.Meta().GetDMARCReport(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("GetDMARCReport: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	var seenPass, seenFail bool
	for _, row := range rows {
		if row.SPFAligned && row.DKIMAligned {
			seenPass = true
		}
		if !row.SPFAligned && !row.DKIMAligned {
			seenFail = true
		}
	}
	if !seenPass || !seenFail {
		t.Errorf("rows did not capture both pass and fail records: %+v", rows)
	}
}

func TestIngestMessage_Zip_HappyPath(t *testing.T) {
	fs, ing := newIngestor(t)
	att := zipReportBytes(t, sampleReportXML)
	raw := buildReportEmail(
		"yahoo.com!example.test!1700000000!1700086400.xml.zip",
		"application/zip",
		att,
	)

	rec, err := ing.IngestMessage(t.Context(), raw)
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if !rec {
		t.Fatalf("recognised = false; want true")
	}
	reports, _ := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	if !reports[0].ParsedOK {
		t.Fatalf("parsed_ok = false (err=%q)", reports[0].ParseError)
	}
}

func TestIngestMessage_NotDMARC_ReturnsRecognisedFalse(t *testing.T) {
	fs, ing := newIngestor(t)
	raw := []byte(strings.Join([]string{
		"From: alice@example.test",
		"To: bob@example.test",
		"Subject: hello",
		"",
		"just a normal mail",
		"",
	}, "\r\n"))
	rec, err := ing.IngestMessage(t.Context(), raw)
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if rec {
		t.Errorf("recognised = true; want false on non-DMARC mail")
	}
	reports, _ := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
	if len(reports) != 0 {
		t.Errorf("rows persisted on non-DMARC: %+v", reports)
	}
}

func TestIngestMessage_DuplicateReport_DedupedSilently(t *testing.T) {
	fs, ing := newIngestor(t)
	att := gzReportBytes(t, sampleReportXML)
	raw := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)
	for i := 0; i < 3; i++ {
		rec, err := ing.IngestMessage(t.Context(), raw)
		if err != nil {
			t.Fatalf("IngestMessage attempt %d: %v", i, err)
		}
		if !rec {
			t.Fatalf("recognised = false on attempt %d", i)
		}
	}
	reports, _ := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
	if len(reports) != 1 {
		t.Fatalf("dedupe failed: reports = %d, want 1", len(reports))
	}
}

func TestIngestMessage_BadXML_PersistsRawWithParseError(t *testing.T) {
	fs, ing := newIngestor(t)
	att := gzReportBytes(t, "<not-valid-dmarc-xml/>")
	raw := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)
	rec, err := ing.IngestMessage(t.Context(), raw)
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if !rec {
		t.Fatalf("recognised = false on broken XML; want true so the operator can debug")
	}
	reports, _ := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
	if len(reports) != 1 {
		t.Fatalf("expected 1 row marking the parse failure; got %d", len(reports))
	}
	r := reports[0]
	if r.ParsedOK {
		t.Errorf("parsed_ok = true; want false for broken XML")
	}
	if r.ParseError == "" {
		t.Errorf("parse_error empty; want a populated diagnostic")
	}
}

func TestAggregator_Aggregate_GroupsByDomain(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	ing := maildmarc.NewIngestor(fs.Meta(), logger, clk)
	att := gzReportBytes(t, sampleReportXML)
	raw := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)
	if _, err := ing.IngestMessage(t.Context(), raw); err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}

	agg := maildmarc.NewAggregator(fs.Meta(), clk)
	rows, err := agg.Aggregate(t.Context(), "example.test", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("no rows; want at least one (header_from grouping)")
	}
	var totalCount int
	var sawQuarantine bool
	for _, row := range rows {
		totalCount += row.Count
		if row.Disposition == "quarantine" {
			sawQuarantine = true
		}
	}
	if totalCount != 4 {
		t.Errorf("total count = %d, want 4 (3 + 1)", totalCount)
	}
	if !sawQuarantine {
		t.Errorf("expected one row with quarantine disposition, got %+v", rows)
	}
}

// TestAggregator_Ingest_BackcompatDelegate verifies that the back-compat
// Ingest() shim now succeeds on a recognised report (Phase 1 returned an
// error unconditionally).
func TestAggregator_Ingest_BackcompatDelegate(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	agg := maildmarc.NewAggregator(fs.Meta(), clk)
	att := gzReportBytes(t, sampleReportXML)
	raw := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)
	if err := agg.Ingest(t.Context(), raw); err != nil {
		t.Fatalf("Ingest delegate: %v", err)
	}
	reports, _ := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
	if len(reports) != 1 {
		t.Fatalf("expected one persisted report after Ingest, got %d", len(reports))
	}
}

func TestIngestMessage_NilContext_NoCrash(t *testing.T) {
	_, ing := newIngestor(t)
	// Passing a cancelled context exercises the early-return branch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ing.IngestMessage(ctx, []byte("From: a@b.test\r\n\r\n\r\n")); err == nil {
		t.Errorf("expected error from cancelled context")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
}
