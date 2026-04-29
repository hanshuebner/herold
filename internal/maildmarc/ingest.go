package maildmarc

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// reportSizeCap bounds the bytes the ingestor will read from a report
// payload (gzip / zip entry). Production aggregate reports rarely exceed
// a few hundred KiB; we cap at 8 MiB to refuse pathological compression
// bombs while staying well above any field-observed report size.
const reportSizeCap = 8 << 20

// Ingestor parses inbound DMARC aggregate reports and persists the
// header + parsed rows via store.Metadata. Construct one per server; the
// Ingestor owns no goroutines and is safe for concurrent use.
type Ingestor struct {
	meta   store.Metadata
	logger *slog.Logger
	clock  clock.Clock
}

// NewIngestor returns an Ingestor backed by meta. logger is structured
// (a nil logger falls back to slog.Default); clock supplies the
// ReceivedAt column on the persisted report header.
func NewIngestor(meta store.Metadata, logger *slog.Logger, clk clock.Clock) *Ingestor {
	if meta == nil {
		panic("maildmarc: NewIngestor with nil store.Metadata")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	return &Ingestor{meta: meta, logger: logger, clock: clk}
}

// IngestMessage processes an inbound RFC 5322 message and, if it carries
// a DMARC aggregate report, parses + persists it. recognised is true
// when the heuristic detector classified the message as a DMARC report;
// non-DMARC messages return (false, nil) so the caller can treat the
// call as advisory and fall through to other intake handlers.
//
// Persisted state is idempotent on (ReporterOrg, ReportID): a duplicate
// report is silently dropped (recognised=true, err=nil). Parse failures
// land a parsed_ok=false row carrying the parse error so operators can
// debug; on those rows the rows-table is empty.
func (i *Ingestor) IngestMessage(ctx context.Context, raw []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if i == nil || i.meta == nil {
		return false, errors.New("maildmarc: Ingestor not initialised")
	}

	msg, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		// We cannot recognise a malformed message as a DMARC report; let
		// the caller continue.
		return false, nil
	}

	att, ok := findReportAttachment(&msg)
	if !ok {
		// No plausible attachment; if the message also lacks the textual
		// signals fall through.
		if !textuallyLooksLikeReport(&msg) {
			return false, nil
		}
		// Textual signals without an attachment is a malformed DMARC
		// report — recognise but record a parse error.
		i.persistFailure(ctx, &msg, raw, "no aggregate report attachment found")
		return true, nil
	}
	if !textuallyLooksLikeReport(&msg) && !filenameLooksLikeReport(att.filename) {
		// Just an attachment that happens to be application/zip without
		// any DMARC subject signal. Decline.
		return false, nil
	}

	xmlBytes, decodeErr := decodeReportPayload(att)
	if decodeErr != nil {
		i.persistFailure(ctx, &msg, raw, "decode payload: "+decodeErr.Error())
		return true, nil
	}

	report, rows, parseErr := parseAggregateReport(xmlBytes)
	if parseErr != nil {
		i.persistFailure(ctx, &msg, raw, "parse XML: "+parseErr.Error())
		return true, nil
	}

	header := store.DMARCReport{
		ReceivedAt:    i.clock.Now(),
		ReporterEmail: reporterEmail(&msg),
		ReporterOrg:   report.OrgName,
		ReportID:      report.ReportID,
		Domain:        strings.ToLower(report.PolicyDomain),
		DateBegin:     report.DateBegin,
		DateEnd:       report.DateEnd,
		// XMLBlobHash is left empty: the raw XML is small and lives
		// inside the message blob already; the schema permits an empty
		// hash for backends that do not surface a separate XML blob.
		ParsedOK: true,
	}

	_, err = i.meta.InsertDMARCReport(ctx, header, rows)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			i.logger.InfoContext(ctx, "maildmarc: duplicate DMARC report ignored",
				slog.String("activity", "system"),
				slog.String("subsystem", "maildmarc"),
				slog.String("reporter_org", header.ReporterOrg),
				slog.String("report_id", header.ReportID),
				slog.String("domain", header.Domain),
			)
			return true, nil
		}
		return true, fmt.Errorf("maildmarc: persist report: %w", err)
	}
	i.logger.InfoContext(ctx, "maildmarc: ingested DMARC aggregate report",
		slog.String("activity", "system"),
		slog.String("subsystem", "maildmarc"),
		slog.String("reporter_org", header.ReporterOrg),
		slog.String("report_id", header.ReportID),
		slog.String("domain", header.Domain),
		slog.Int("rows", len(rows)),
	)
	return true, nil
}

// persistFailure writes a parsed_ok=false header so operators can spot
// reports that arrived but failed to parse. Errors from the store call
// itself are swallowed at the warn level; the caller still observes
// recognised=true so the change-feed cursor advances.
func (i *Ingestor) persistFailure(ctx context.Context, msg *mailparse.Message, raw []byte, reason string) {
	header := store.DMARCReport{
		ReceivedAt:    i.clock.Now(),
		ReporterEmail: reporterEmail(msg),
		ReporterOrg:   reporterOrgFromMessage(msg),
		ReportID:      reportIDFromHeaders(msg),
		Domain:        strings.ToLower(domainFromSubject(msg.Envelope.Subject)),
		ParsedOK:      false,
		ParseError:    truncate(reason, 512),
	}
	if header.ReportID == "" {
		// Use the message-id when the report-id header is absent; the
		// store dedupes on (ReporterOrg, ReportID) so this gives us a
		// natural-enough key to avoid double-recording the same broken
		// report on retry.
		header.ReportID = strings.TrimSpace(msg.Envelope.MessageID)
	}
	if _, err := i.meta.InsertDMARCReport(ctx, header, nil); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return
		}
		i.logger.WarnContext(ctx, "maildmarc: persist parse failure",
			slog.String("activity", "internal"),
			slog.String("subsystem", "maildmarc"),
			slog.String("reason", reason),
			slog.Any("err", err),
		)
	}
	_ = raw // raw kept on the param list for symmetry / future blob persistence
}

// reportAttachment holds the located report payload plus enough metadata
// to pick the right decoder.
type reportAttachment struct {
	filename    string
	contentType string
	bytes       []byte
}

// findReportAttachment walks the parsed MIME tree depth-first looking
// for the first leaf whose filename or media type matches an aggregate
// report attachment (gzip or zip carrying XML, or raw application/xml
// for the rare inline report).
func findReportAttachment(msg *mailparse.Message) (reportAttachment, bool) {
	var found reportAttachment
	var ok bool
	walkParts(&msg.Body, func(p *mailparse.Part) bool {
		if len(p.Children) > 0 {
			return true
		}
		ct := strings.ToLower(strings.TrimSpace(p.ContentType))
		fn := p.Filename
		switch {
		case strings.HasPrefix(ct, "application/gzip"),
			strings.HasPrefix(ct, "application/x-gzip"):
			found = reportAttachment{filename: fn, contentType: ct, bytes: p.Bytes}
			ok = true
			return false
		case strings.HasPrefix(ct, "application/zip"),
			strings.HasPrefix(ct, "application/x-zip-compressed"):
			found = reportAttachment{filename: fn, contentType: ct, bytes: p.Bytes}
			ok = true
			return false
		case ct == "application/xml" || ct == "text/xml":
			// Some reporters ship the XML uncompressed; accept when the
			// filename also fits the DMARC pattern so we don't slurp a
			// random XML attachment.
			if filenameLooksLikeReport(fn) {
				body := p.Bytes
				if len(body) == 0 && p.Text != "" {
					body = []byte(p.Text)
				}
				found = reportAttachment{filename: fn, contentType: ct, bytes: body}
				ok = true
				return false
			}
		}
		// application/octet-stream with a matching filename — the
		// reporter forgot to set Content-Type but the filename is still
		// canonical. Honour it.
		if (ct == "application/octet-stream" || ct == "") && filenameLooksLikeReport(fn) {
			found = reportAttachment{filename: fn, contentType: ct, bytes: p.Bytes}
			ok = true
			return false
		}
		return true
	})
	return found, ok
}

// walkParts performs a depth-first traversal of the MIME tree, invoking
// fn at each part. The traversal stops as soon as fn returns false.
func walkParts(p *mailparse.Part, fn func(*mailparse.Part) bool) bool {
	if p == nil {
		return true
	}
	if !fn(p) {
		return false
	}
	for i := range p.Children {
		if !walkParts(&p.Children[i], fn) {
			return false
		}
	}
	return true
}

// decodeReportPayload decompresses the attachment into the underlying
// XML bytes.
func decodeReportPayload(att reportAttachment) ([]byte, error) {
	switch {
	case strings.HasSuffix(strings.ToLower(att.filename), ".gz"),
		strings.Contains(att.contentType, "gzip"):
		return decodeGzip(att.bytes)
	case strings.HasSuffix(strings.ToLower(att.filename), ".zip"),
		strings.Contains(att.contentType, "zip"):
		return decodeZip(att.bytes)
	default:
		// Plain XML.
		if int64(len(att.bytes)) > reportSizeCap {
			return nil, fmt.Errorf("payload exceeds %d bytes", reportSizeCap)
		}
		return att.bytes, nil
	}
}

func decodeGzip(in []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(in))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, reportSizeCap+1))
	if err != nil {
		return nil, fmt.Errorf("gzip read: %w", err)
	}
	if int64(len(out)) > reportSizeCap {
		return nil, fmt.Errorf("gzip payload exceeds %d bytes", reportSizeCap)
	}
	return out, nil
}

func decodeZip(in []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(in), int64(len(in)))
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	if len(zr.File) == 0 {
		return nil, errors.New("zip: empty archive")
	}
	// Pick the first .xml entry (Yahoo + others put exactly one).
	var entry *zip.File
	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			entry = f
			break
		}
	}
	if entry == nil {
		entry = zr.File[0]
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("zip open: %w", err)
	}
	defer rc.Close()
	out, err := io.ReadAll(io.LimitReader(rc, reportSizeCap+1))
	if err != nil {
		return nil, fmt.Errorf("zip read: %w", err)
	}
	if int64(len(out)) > reportSizeCap {
		return nil, fmt.Errorf("zip payload exceeds %d bytes", reportSizeCap)
	}
	return out, nil
}

// textuallyLooksLikeReport applies header-only heuristics: the Subject
// matches the canonical "Report Domain:" template, OR the message
// carries a Report-ID / X-Report-ID header. We deliberately keep the
// detection conservative — false positives would inflate the
// dmarc_reports_raw table, which is more harmful than missing one
// idiosyncratic reporter.
func textuallyLooksLikeReport(msg *mailparse.Message) bool {
	if msg == nil {
		return false
	}
	subj := strings.ToLower(msg.Envelope.Subject)
	if strings.Contains(subj, "report domain:") {
		return true
	}
	if strings.Contains(subj, "dmarc") && strings.Contains(subj, "report") {
		return true
	}
	if msg.Headers.Get("Report-ID") != "" || msg.Headers.Get("X-Report-ID") != "" {
		return true
	}
	return false
}

// filenameLooksLikeReport tests an attachment filename against the RFC
// 7489 §7.2.1.1 canonical pattern: <receiver>!<domain>!<begin>!<end>.<ext>
// where the extension is xml, xml.gz, or xml.zip.
func filenameLooksLikeReport(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	if !(strings.HasSuffix(lower, ".xml") ||
		strings.HasSuffix(lower, ".xml.gz") ||
		strings.HasSuffix(lower, ".xml.zip") ||
		strings.HasSuffix(lower, ".gz") ||
		strings.HasSuffix(lower, ".zip")) {
		return false
	}
	// The canonical pattern uses '!' as the field separator; some
	// reporters use '_' or '.' but '!' is the spec's literal example.
	return strings.Contains(lower, "!") || strings.Contains(lower, "dmarc") ||
		strings.Contains(lower, "report")
}

// reporterEmail extracts the From: addr-spec from msg, lowercased.
func reporterEmail(msg *mailparse.Message) string {
	if msg == nil || len(msg.Envelope.From) == 0 {
		return ""
	}
	return strings.ToLower(msg.Envelope.From[0].Address)
}

// reporterOrgFromMessage falls back to the From: domain when no XML is
// available (parse-failure path).
func reporterOrgFromMessage(msg *mailparse.Message) string {
	addr := reporterEmail(msg)
	if at := strings.IndexByte(addr, '@'); at >= 0 {
		return addr[at+1:]
	}
	return addr
}

// reportIDFromHeaders pulls the Report-ID / X-Report-ID header value if
// present; the headers carry it as an angle-bracketed message-id-style
// token.
func reportIDFromHeaders(msg *mailparse.Message) string {
	if msg == nil {
		return ""
	}
	if v := msg.Headers.Get("Report-ID"); v != "" {
		return strings.Trim(strings.TrimSpace(v), "<>")
	}
	if v := msg.Headers.Get("X-Report-ID"); v != "" {
		return strings.Trim(strings.TrimSpace(v), "<>")
	}
	return ""
}

// domainFromSubject extracts the policy domain from a "Report Domain: X"
// style Subject. Returns empty when the pattern is absent.
func domainFromSubject(subject string) string {
	lower := strings.ToLower(subject)
	idx := strings.Index(lower, "report domain:")
	if idx < 0 {
		return ""
	}
	rest := subject[idx+len("Report Domain:"):]
	rest = strings.TrimSpace(rest)
	// Subject continues with "Submitter: ..." typically; cut at the
	// first whitespace.
	if sp := strings.IndexAny(rest, " \t"); sp > 0 {
		rest = rest[:sp]
	}
	return strings.TrimSpace(rest)
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

// -------- XML schema (RFC 7489 Appendix B) ---------------------------

// aggregateReport is the deserialised top-level <feedback> document.
// The element + tag names match the canonical schema; we only model the
// subset the store schema needs.
type aggregateReport struct {
	XMLName xml.Name `xml:"feedback"`
	Meta    struct {
		OrgName  string `xml:"org_name"`
		Email    string `xml:"email"`
		ReportID string `xml:"report_id"`
		Range    struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy struct {
		Domain string `xml:"domain"`
		ADKIM  string `xml:"adkim"`
		ASPF   string `xml:"aspf"`
		P      string `xml:"p"`
		SP     string `xml:"sp"`
		Pct    string `xml:"pct"`
	} `xml:"policy_published"`
	Records []reportRecord `xml:"record"`
}

type reportRecord struct {
	Row struct {
		SourceIP    string `xml:"source_ip"`
		Count       int64  `xml:"count"`
		Disposition struct {
			Disposition string `xml:"disposition"`
			DKIM        string `xml:"dkim"`
			SPF         string `xml:"spf"`
		} `xml:"policy_evaluated"`
	} `xml:"row"`
	Identifiers struct {
		HeaderFrom   string `xml:"header_from"`
		EnvelopeFrom string `xml:"envelope_from"`
		EnvelopeTo   string `xml:"envelope_to"`
	} `xml:"identifiers"`
	AuthResults struct {
		DKIM []struct {
			Domain   string `xml:"domain"`
			Result   string `xml:"result"`
			Selector string `xml:"selector"`
		} `xml:"dkim"`
		SPF []struct {
			Domain string `xml:"domain"`
			Result string `xml:"result"`
		} `xml:"spf"`
	} `xml:"auth_results"`
}

// parsedReport is the post-XML, pre-store shape. Cheaper for callers
// (intake.go, tests) than re-walking the raw XML structures.
type parsedReport struct {
	OrgName      string
	ReporterMail string
	ReportID     string
	PolicyDomain string
	DateBegin    time.Time
	DateEnd      time.Time
}

// parseAggregateReport runs encoding/xml against the bytes and returns
// (header, rows, error). On error the caller persists a parsed_ok=false
// raw row.
func parseAggregateReport(b []byte) (parsedReport, []store.DMARCRow, error) {
	var rep aggregateReport
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.CharsetReader = identityCharsetReader
	if err := dec.Decode(&rep); err != nil {
		return parsedReport{}, nil, err
	}
	out := parsedReport{
		OrgName:      strings.TrimSpace(rep.Meta.OrgName),
		ReporterMail: strings.ToLower(strings.TrimSpace(rep.Meta.Email)),
		ReportID:     strings.TrimSpace(rep.Meta.ReportID),
		PolicyDomain: strings.ToLower(strings.TrimSpace(rep.Policy.Domain)),
	}
	if rep.Meta.Range.Begin > 0 {
		out.DateBegin = time.Unix(rep.Meta.Range.Begin, 0).UTC()
	}
	if rep.Meta.Range.End > 0 {
		out.DateEnd = time.Unix(rep.Meta.Range.End, 0).UTC()
	}
	if out.OrgName == "" || out.ReportID == "" || out.PolicyDomain == "" {
		return parsedReport{}, nil, errors.New("missing required report metadata")
	}

	rows := make([]store.DMARCRow, 0, len(rep.Records))
	for _, rec := range rep.Records {
		rows = append(rows, store.DMARCRow{
			SourceIP:     strings.TrimSpace(rec.Row.SourceIP),
			Count:        rec.Row.Count,
			Disposition:  int32(dispositionFromToken(rec.Row.Disposition.Disposition)),
			SPFAligned:   strings.EqualFold(strings.TrimSpace(rec.Row.Disposition.SPF), "pass"),
			DKIMAligned:  strings.EqualFold(strings.TrimSpace(rec.Row.Disposition.DKIM), "pass"),
			SPFResult:    strings.ToLower(strings.TrimSpace(firstSPFResult(rec))),
			DKIMResult:   strings.ToLower(strings.TrimSpace(firstDKIMResult(rec))),
			HeaderFrom:   strings.ToLower(strings.TrimSpace(rec.Identifiers.HeaderFrom)),
			EnvelopeFrom: strings.ToLower(strings.TrimSpace(rec.Identifiers.EnvelopeFrom)),
			EnvelopeTo:   strings.ToLower(strings.TrimSpace(rec.Identifiers.EnvelopeTo)),
		})
	}
	return out, rows, nil
}

// identityCharsetReader passes the input through unchanged. RFC 7489
// mandates UTF-8 reports; some reporters declare us-ascii or windows-
// 1252 even though the bytes are pure ASCII. The stdlib's xml.Decoder
// rejects unknown charsets unless we provide a passthrough.
func identityCharsetReader(_ string, in io.Reader) (io.Reader, error) {
	return in, nil
}

func firstDKIMResult(r reportRecord) string {
	if len(r.AuthResults.DKIM) == 0 {
		return ""
	}
	return r.AuthResults.DKIM[0].Result
}

func firstSPFResult(r reportRecord) string {
	if len(r.AuthResults.SPF) == 0 {
		return ""
	}
	return r.AuthResults.SPF[0].Result
}

func dispositionFromToken(s string) mailauth.DMARCDisposition {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "quarantine":
		return mailauth.DispositionQuarantine
	case "reject":
		return mailauth.DispositionReject
	default:
		return mailauth.DispositionNone
	}
}
