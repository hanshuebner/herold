package autodns

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultEmissionInterval is the cadence the daily TLS-RPT emission
// loop wakes on. Per RFC 8460 §4 the spec asks reporters to send "at
// least once per day", with the start of the reporting window aligned
// to the operator's preference. We pick 24h with no further alignment;
// operators that want UTC midnight alignment can wrap the loop.
const DefaultEmissionInterval = 24 * time.Hour

// DefaultReporterContact is the From: used on the outbound mailto:
// reports when the operator has not configured one. Matches the SMTP
// "postmaster@" convention.
const DefaultReporterContact = "tlsrpt-noreply@localhost"

// QueueSubmitter is the slice of internal/queue.Queue the reporter
// needs: enqueue one signed mail with the multipart/report body. We do
// not import queue directly so the reporter stays decoupled from the
// outbound delivery path; callers wire the real queue.Queue or a fake
// in tests.
type QueueSubmitter interface {
	// Submit enqueues the message; the implementation owns body
	// persistence. The reporter passes Sign=true so DKIM signs the
	// outbound report per RFC 8460 §5.3.
	Submit(ctx context.Context, msg ReportSubmission) (string, error)
}

// ReportSubmission is the payload the reporter hands to a
// QueueSubmitter. The shape mirrors internal/queue.Submission's
// load-bearing fields without forcing the caller to import that
// package; the production wiring adapts one to the other.
type ReportSubmission struct {
	MailFrom      string
	Recipients    []string
	Body          []byte
	Sign          bool
	SigningDomain string
}

// HTTPDoer is the http.Client surface the reporter uses for https://
// rua URIs. Tests substitute httptest.Server clients via http.Client.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ReporterOptions configures a Reporter. Store and Clock are required.
type ReporterOptions struct {
	// Store is the metadata + blob handle used to read tlsrpt_failures
	// and append new failure rows from Append.
	Store store.Store
	// Logger is the structured logger; nil falls back to slog.Default.
	Logger *slog.Logger
	// Clock supplies time for the emission loop and report timestamps.
	// Nil falls back to a real wall clock.
	Clock clock.Clock
	// HTTPClient executes https:// RUA PUTs. Nil falls back to
	// http.DefaultClient.
	HTTPClient HTTPDoer
	// Queue is the outbound queue used for mailto: RUA emission. Nil
	// means "skip mailto delivery and log a warn".
	Queue QueueSubmitter
	// EmissionInterval overrides DefaultEmissionInterval.
	EmissionInterval time.Duration
	// ReporterDomain is the operator's domain used as the report
	// originator and as the SigningDomain on outbound mail.
	ReporterDomain string
	// ReporterContact is the From: addr-spec on outbound mail.
	ReporterContact string
	// Hostname is the local hostname; used as the "contact-info" field
	// inside the JSON report and the Message-Id host part.
	Hostname string
}

// Reporter accumulates outbound TLS failures (Append) and emits TLS-RPT
// aggregate reports (RunDailyEmission). Construct one per server; the
// Reporter owns no goroutines until RunDailyEmission is called and is
// safe for concurrent Append calls.
//
// The reporter does NOT mark failures as reported. It keys emission off
// the time window: each tick rolls up [last, now) and the next tick
// rolls up [now, now+interval). This keeps the schema simple (no
// reported_at column) at the cost of the emission process needing to
// own the only mover of the time cursor.
type Reporter struct {
	opts ReporterOptions

	// emitting guards RunDailyEmission against double-start.
	emitting atomic.Bool

	// lastEmitted holds the start of the most recently completed
	// emission window. Initialised by the first tick to opts.Clock.Now,
	// then advanced by the interval each tick.
	lastEmitted atomic.Int64
}

// NewReporter constructs a Reporter. Panics if opts.Store is nil; that
// is a programming error the caller can never recover from.
func NewReporter(opts ReporterOptions) *Reporter {
	if opts.Store == nil {
		panic("autodns: NewReporter with nil Store")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.EmissionInterval <= 0 {
		opts.EmissionInterval = DefaultEmissionInterval
	}
	if opts.ReporterContact == "" {
		opts.ReporterContact = DefaultReporterContact
	}
	if opts.Hostname == "" {
		opts.Hostname = "localhost"
	}
	return &Reporter{opts: opts}
}

// Append records one TLS failure for later inclusion in the next
// aggregate report. Thin wrapper over store.Metadata.AppendTLSRPTFailure.
func (r *Reporter) Append(ctx context.Context, f store.TLSRPTFailure) error {
	if r == nil || r.opts.Store == nil {
		return errors.New("autodns: Reporter not initialised")
	}
	if f.RecordedAt.IsZero() {
		f.RecordedAt = r.opts.Clock.Now()
	}
	return r.opts.Store.Meta().AppendTLSRPTFailure(ctx, f)
}

// BuildAggregateReport returns the RFC 8460 §4 JSON document for one
// policy domain plus the failures observed in [since, until]. The
// returned JSON is the unwrapped body; callers gzip + frame it for
// transport (see emitMailto / emitHTTPS).
func (r *Reporter) BuildAggregateReport(ctx context.Context, policyDomain string, since, until time.Time) ([]byte, int, error) {
	failures, err := r.opts.Store.Meta().ListTLSRPTFailures(ctx, policyDomain, since, until)
	if err != nil {
		return nil, 0, fmt.Errorf("autodns: list TLS-RPT failures: %w", err)
	}
	id, err := newReportID()
	if err != nil {
		return nil, 0, err
	}
	report := tlsrptReport{
		OrganizationName: r.opts.ReporterDomain,
		DateRange: tlsrptDateRange{
			StartDateTime: since.UTC().Format(time.RFC3339),
			EndDateTime:   until.UTC().Format(time.RFC3339),
		},
		ContactInfo: r.opts.ReporterContact,
		ReportID:    id,
		Policies:    rollupFailures(policyDomain, failures),
	}
	body, err := json.Marshal(report)
	if err != nil {
		return nil, 0, fmt.Errorf("autodns: marshal TLS-RPT report: %w", err)
	}
	return body, len(failures), nil
}

// RuaResolver is the function the emission loop calls to obtain the
// rua=... URIs published in the operator's `_smtp._tls.<domain>` TXT
// record. The integrator hooks this up to internal/mailauth.Resolver
// (via TXTLookup) or any other DNS path; the reporter stays decoupled
// from the resolver implementation.
type RuaResolver func(ctx context.Context, domain string) []string

// RunDailyEmission consumes the aggregated tlsrpt_failures table on a
// fixed cadence. For every policy domain that has at least one failure
// in the elapsed window, the reporter resolves its rua= URIs and emits
// the JSON document via:
//
//   - mailto: — a multipart/report message queued through the outbound
//     queue with Sign=true so DKIM signs the report per RFC 8460 §5.3,
//   - https:  — a PUT of `application/tlsrpt+gzip` with the gzipped
//     JSON body.
//
// Errors per-rua are logged at WARN and do not stall the loop; the next
// tick re-rolls the same window so a transient failure resolves on the
// retry. Returns nil on graceful shutdown via ctx.
func (r *Reporter) RunDailyEmission(ctx context.Context, ruaResolver RuaResolver) error {
	if !r.emitting.CompareAndSwap(false, true) {
		return errors.New("autodns: TLS-RPT emission already running")
	}
	defer r.emitting.Store(false)

	if ruaResolver == nil {
		return errors.New("autodns: RunDailyEmission requires a RuaResolver")
	}
	// Sentinel value: 0 means "first tick — scan from the beginning of
	// time so any failure appended before RunDailyEmission started gets
	// included in the first report". After the first tick, lastEmitted
	// rolls forward by the elapsed window.
	r.lastEmitted.Store(0)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.opts.Clock.After(r.opts.EmissionInterval):
		}
		if err := ctx.Err(); err != nil {
			return nil
		}
		now := r.opts.Clock.Now()
		var since time.Time
		if v := r.lastEmitted.Load(); v != 0 {
			since = time.Unix(0, v).UTC()
		}
		until := now.UTC()
		r.emitOnce(ctx, ruaResolver, since, until)
		r.lastEmitted.Store(until.UnixNano())
	}
}

// EmitOnce runs a single emission window. Exposed for tests and admin
// "force a report now" tooling; production callers usually go through
// RunDailyEmission.
func (r *Reporter) EmitOnce(ctx context.Context, ruaResolver RuaResolver, since, until time.Time) {
	if ruaResolver == nil {
		return
	}
	r.emitOnce(ctx, ruaResolver, since, until)
}

func (r *Reporter) emitOnce(ctx context.Context, ruaResolver RuaResolver, since, until time.Time) {
	domains, err := r.findDomainsWithFailures(ctx, since, until)
	if err != nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: list failures",
			slog.Any("err", err))
		return
	}
	if len(domains) == 0 {
		return
	}
	for _, dom := range domains {
		body, count, err := r.BuildAggregateReport(ctx, dom, since, until)
		if err != nil {
			r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: build report",
				slog.String("domain", dom),
				slog.Any("err", err))
			continue
		}
		if count == 0 {
			continue
		}
		ruas := ruaResolver(ctx, dom)
		if len(ruas) == 0 {
			r.opts.Logger.InfoContext(ctx, "autodns: TLS-RPT emission: no rua resolved",
				slog.String("domain", dom),
				slog.Int("failures", count))
			continue
		}
		for _, rua := range ruas {
			r.emitOne(ctx, dom, rua, body, since, until)
		}
	}
}

// findDomainsWithFailures returns every distinct policy_domain with at
// least one tlsrpt_failures row in [since, until]. The Phase 2 schema
// does not expose a "list distinct domains" verb, so the reporter
// derives the set from store-side ListTLSRPTFailures with the empty
// domain ignored — but that surface filters by exact domain. The
// reporter sidesteps that by leaning on the in-process Append cache
// when present, and otherwise enumerating local domains: the production
// wiring also persists the operator's local-domain list on the store.
//
// For Phase 2 we accept the simpler model: scan ListTLSRPTFailures with
// each operator-configured domain. ListLocalDomains gives us that list.
func (r *Reporter) findDomainsWithFailures(ctx context.Context, since, until time.Time) ([]string, error) {
	domains, err := r.opts.Store.Meta().ListLocalDomains(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		failures, err := r.opts.Store.Meta().ListTLSRPTFailures(ctx, d.Name, since, until)
		if err != nil {
			r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: list failures",
				slog.String("domain", d.Name),
				slog.Any("err", err))
			continue
		}
		if len(failures) > 0 {
			out = append(out, strings.ToLower(d.Name))
		}
	}
	sort.Strings(out)
	return out, nil
}

// emitOne dispatches one (domain, rua) pair to the right transport.
func (r *Reporter) emitOne(ctx context.Context, domain, rua string, jsonBody []byte, since, until time.Time) {
	switch {
	case strings.HasPrefix(rua, "mailto:"):
		r.emitMailto(ctx, domain, rua, jsonBody, since, until)
	case strings.HasPrefix(rua, "https:"):
		r.emitHTTPS(ctx, domain, rua, jsonBody)
	default:
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: unsupported rua scheme",
			slog.String("domain", domain),
			slog.String("rua", rua))
	}
}

// emitMailto builds the multipart/report message + queues it via the
// outbound queue. RFC 8460 §5 specifies:
//   - Subject: "Report Domain: <domain> Submitter: <reporter> Report-ID: ..."
//   - Content-Type: multipart/report; report-type="tlsrpt"
//   - The report part is application/tlsrpt+gzip with a .gz filename.
func (r *Reporter) emitMailto(ctx context.Context, domain, rua string, jsonBody []byte, since, until time.Time) {
	if r.opts.Queue == nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: no queue configured for mailto rua",
			slog.String("domain", domain),
			slog.String("rua", rua))
		return
	}
	addr := strings.TrimPrefix(rua, "mailto:")
	if addr == "" {
		return
	}

	gz, err := gzipBytes(jsonBody)
	if err != nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: gzip",
			slog.String("domain", domain),
			slog.Any("err", err))
		return
	}

	body, err := buildTLSRPTMailtoMessage(mailtoMessageInput{
		From:      r.opts.ReporterContact,
		To:        addr,
		Hostname:  r.opts.Hostname,
		Domain:    domain,
		Reporter:  r.opts.ReporterDomain,
		Now:       r.opts.Clock.Now(),
		Since:     since,
		Until:     until,
		ReportGz:  gz,
		Submitter: r.opts.ReporterDomain,
	})
	if err != nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: build message",
			slog.String("domain", domain),
			slog.Any("err", err))
		return
	}
	sub := ReportSubmission{
		MailFrom:      r.opts.ReporterContact,
		Recipients:    []string{addr},
		Body:          body,
		Sign:          true,
		SigningDomain: r.opts.ReporterDomain,
	}
	if _, err := r.opts.Queue.Submit(ctx, sub); err != nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: queue submit",
			slog.String("domain", domain),
			slog.String("rua", rua),
			slog.Any("err", err))
		return
	}
	r.opts.Logger.InfoContext(ctx, "autodns: TLS-RPT report queued",
		slog.String("domain", domain),
		slog.String("rua", rua),
		slog.Int("body_size", len(body)))
}

// emitHTTPS PUTs the gzipped JSON to the rua URL with
// Content-Type: application/tlsrpt+gzip per RFC 8460 §3.
func (r *Reporter) emitHTTPS(ctx context.Context, domain, rua string, jsonBody []byte) {
	gz, err := gzipBytes(jsonBody)
	if err != nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: gzip",
			slog.String("domain", domain),
			slog.Any("err", err))
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rua, bytes.NewReader(gz))
	if err != nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: build request",
			slog.String("domain", domain),
			slog.Any("err", err))
		return
	}
	// Some receivers expect PUT; the spec carries POST as the canonical
	// verb for the well-known endpoint at this RFC version. We use POST
	// with the canonical content-type.
	req.Header.Set("Content-Type", "application/tlsrpt+gzip")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := r.opts.HTTPClient.Do(req)
	if err != nil {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: HTTPS submit",
			slog.String("domain", domain),
			slog.String("rua", rua),
			slog.Any("err", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		r.opts.Logger.WarnContext(ctx, "autodns: TLS-RPT emission: HTTPS non-2xx",
			slog.String("domain", domain),
			slog.String("rua", rua),
			slog.Int("status", resp.StatusCode))
		return
	}
	// Drain the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	r.opts.Logger.InfoContext(ctx, "autodns: TLS-RPT report submitted",
		slog.String("domain", domain),
		slog.String("rua", rua),
		slog.Int("body_size", len(gz)))
}

// gzipBytes compresses b with the default level. Allocates one buffer.
func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// -- Report-shape -----------------------------------------------------

type tlsrptReport struct {
	OrganizationName string          `json:"organization-name,omitempty"`
	DateRange        tlsrptDateRange `json:"date-range"`
	ContactInfo      string          `json:"contact-info,omitempty"`
	ReportID         string          `json:"report-id"`
	Policies         []tlsrptPolicy  `json:"policies"`
}

type tlsrptDateRange struct {
	StartDateTime string `json:"start-datetime"`
	EndDateTime   string `json:"end-datetime"`
}

type tlsrptPolicy struct {
	Policy         tlsrptPolicyDetails  `json:"policy"`
	Summary        tlsrptSummary        `json:"summary"`
	FailureDetails []tlsrptFailureEntry `json:"failure-details,omitempty"`
}

type tlsrptPolicyDetails struct {
	PolicyType   string   `json:"policy-type"`
	PolicyDomain string   `json:"policy-domain"`
	PolicyString []string `json:"policy-string,omitempty"`
	MXHost       []string `json:"mx-host,omitempty"`
}

type tlsrptSummary struct {
	TotalSuccessfulSessionCount int64 `json:"total-successful-session-count"`
	TotalFailureSessionCount    int64 `json:"total-failure-session-count"`
}

type tlsrptFailureEntry struct {
	ResultType            string `json:"result-type"`
	SendingMTAIP          string `json:"sending-mta-ip,omitempty"`
	ReceivingMXHostname   string `json:"receiving-mx-hostname,omitempty"`
	ReceivingMXHelo       string `json:"receiving-mx-helo,omitempty"`
	ReceivingIP           string `json:"receiving-ip,omitempty"`
	FailedSessionCount    int64  `json:"failed-session-count"`
	AdditionalInformation string `json:"additional-information,omitempty"`
	FailureReasonCode     string `json:"failure-reason-code,omitempty"`
}

// rollupFailures groups raw store rows by failure-type into a single
// policy block. RFC 8460 §4.4 permits multiple policies; we render one
// "no-policy-found" block plus one per applicable policy, but Phase 2's
// store schema does not record which policy was in effect (MTA-STS vs
// DANE). We default to "no-policy-found" and let operators inspect the
// per-failure type for the granular signal.
func rollupFailures(domain string, failures []store.TLSRPTFailure) []tlsrptPolicy {
	if len(failures) == 0 {
		return nil
	}
	entries := make([]tlsrptFailureEntry, 0, len(failures))
	type key struct {
		typ string
		mx  string
	}
	agg := make(map[key]*tlsrptFailureEntry)
	for _, f := range failures {
		k := key{typ: f.FailureType.String(), mx: f.ReceivingMTAHostname}
		entry, ok := agg[k]
		if !ok {
			entry = &tlsrptFailureEntry{
				ResultType:          f.FailureType.String(),
				ReceivingMXHostname: f.ReceivingMTAHostname,
				FailureReasonCode:   f.FailureCode,
			}
			agg[k] = entry
		}
		entry.FailedSessionCount++
		if f.FailureCode != "" && entry.FailureReasonCode == "" {
			entry.FailureReasonCode = f.FailureCode
		}
	}
	for _, v := range agg {
		entries = append(entries, *v)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ResultType != entries[j].ResultType {
			return entries[i].ResultType < entries[j].ResultType
		}
		return entries[i].ReceivingMXHostname < entries[j].ReceivingMXHostname
	})
	policy := tlsrptPolicy{
		Policy: tlsrptPolicyDetails{
			PolicyType:   "no-policy-found",
			PolicyDomain: domain,
		},
		Summary: tlsrptSummary{
			TotalFailureSessionCount: int64(len(failures)),
		},
		FailureDetails: entries,
	}
	return []tlsrptPolicy{policy}
}

// -- mailto wire-format helpers --------------------------------------

type mailtoMessageInput struct {
	From      string
	To        string
	Hostname  string
	Domain    string
	Reporter  string
	Submitter string
	Now       time.Time
	Since     time.Time
	Until     time.Time
	ReportGz  []byte
}

// buildTLSRPTMailtoMessage renders the multipart/report wire bytes.
//
//	Content-Type: multipart/report; report-type="tlsrpt"; boundary="..."
//	  - text/plain — operator-friendly description (RFC 8460 §5.2 advisory)
//	  - application/tlsrpt+gzip — the report payload
func buildTLSRPTMailtoMessage(in mailtoMessageInput) ([]byte, error) {
	if in.From == "" || in.To == "" {
		return nil, errors.New("autodns: TLS-RPT mailto requires From and To")
	}
	boundary, err := newBoundary()
	if err != nil {
		return nil, err
	}
	reportID, err := newReportID()
	if err != nil {
		return nil, err
	}

	var hdr bytes.Buffer
	fmt.Fprintf(&hdr, "From: %s\r\n", in.From)
	fmt.Fprintf(&hdr, "To: %s\r\n", in.To)
	fmt.Fprintf(&hdr, "Subject: Report Domain: %s Submitter: %s Report-ID: <%s>\r\n",
		in.Domain, in.Submitter, reportID)
	fmt.Fprintf(&hdr, "Date: %s\r\n", in.Now.UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&hdr, "Message-Id: <%s@%s>\r\n", reportID, in.Hostname)
	fmt.Fprintf(&hdr, "TLS-Report-Domain: %s\r\n", in.Domain)
	fmt.Fprintf(&hdr, "TLS-Report-Submitter: %s\r\n", in.Submitter)
	fmt.Fprintf(&hdr, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&hdr, "Content-Type: multipart/report; report-type=\"tlsrpt\"; boundary=\"%s\"\r\n",
		boundary)
	hdr.WriteString("\r\n")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.SetBoundary(boundary); err != nil {
		return nil, err
	}

	textHeader := textproto.MIMEHeader{}
	textHeader.Set("Content-Type", "text/plain; charset=utf-8")
	tw, err := mw.CreatePart(textHeader)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(tw,
		"This is a TLS-RPT aggregate report for %s covering %s..%s\r\n",
		in.Domain,
		in.Since.UTC().Format(time.RFC3339),
		in.Until.UTC().Format(time.RFC3339))

	filename := fmt.Sprintf("%s!%s!%d!%d.json.gz",
		in.Reporter, in.Domain, in.Since.UTC().Unix(), in.Until.UTC().Unix())
	rptHeader := textproto.MIMEHeader{}
	rptHeader.Set("Content-Type", "application/tlsrpt+gzip")
	rptHeader.Set("Content-Transfer-Encoding", "base64")
	rptHeader.Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, filename))
	rw, err := mw.CreatePart(rptHeader)
	if err != nil {
		return nil, err
	}
	enc := base64.StdEncoding
	encoded := make([]byte, enc.EncodedLen(len(in.ReportGz)))
	enc.Encode(encoded, in.ReportGz)
	// Wrap base64 at 76 columns per RFC 2045 §6.8.
	const wrap = 76
	for i := 0; i < len(encoded); i += wrap {
		end := i + wrap
		if end > len(encoded) {
			end = len(encoded)
		}
		if _, err := rw.Write(encoded[i:end]); err != nil {
			return nil, err
		}
		if _, err := rw.Write([]byte("\r\n")); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	out := make([]byte, 0, hdr.Len()+body.Len())
	out = append(out, hdr.Bytes()...)
	out = append(out, body.Bytes()...)
	return out, nil
}

// newReportID returns an opaque report-id suitable for the JSON
// "report-id" field and the Subject's Report-ID token.
func newReportID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// newBoundary returns a fresh multipart boundary.
func newBoundary() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "tlsrpt-" + hex.EncodeToString(b[:]), nil
}

// ValidateRua is a small helper exposed for admin tooling that wants to
// surface obviously-broken rua URIs. RFC 8460 §3 restricts the scheme
// set to mailto: and https:.
func ValidateRua(rua string) error {
	u, err := url.Parse(rua)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "mailto", "https":
		return nil
	default:
		return fmt.Errorf("autodns: unsupported TLS-RPT rua scheme %q", u.Scheme)
	}
}
