package extimg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/jhillyerd/enmime"
)

// REQ-EXTIMG-02 / REQ-EXTIMG-50..53: orchestrator. Takes wire bytes of
// an inbound message, parses it, fetches every external image, attaches
// the bytes as inline parts, rewrites the HTML to cid: refs, and
// returns a rebuilt message. Strips DKIM-Signature and inbound
// Authentication-Results when DKIM == strip (REQ-EXTIMG-41/-42), emits
// a server-stamped Authentication-Results recording the pre-rewrite
// verdict (REQ-EXTIMG-43), and adds X-Herold-Body-Modified
// (REQ-EXTIMG-44).
//
// On any unrecoverable error the orchestrator returns the original
// bytes unchanged with a Modified=false summary; the caller stores the
// message verbatim and delivery does not fail (REQ-EXTIMG-61).

// DKIMVerdict describes the result of DKIM verification on the wire-
// original body, captured by the caller before Internalize runs. It is
// rendered into the server-stamped Authentication-Results header
// (REQ-EXTIMG-40 / REQ-EXTIMG-43).
type DKIMVerdict struct {
	// Result is one of "pass", "fail", "none", "tempfail",
	// "permerror", "neutral", "policy". Empty means "no verification
	// happened" and produces a `dkim=none` line.
	Result string
	// SigningDomain is the d= value from the DKIM-Signature when the
	// verification succeeded; empty otherwise.
	SigningDomain string
	// Selector is the s= value from the DKIM-Signature.
	Selector string
}

// AuditSummary is the per-message rewrite outcome (REQ-EXTIMG-62).
// Returned alongside the rewritten bytes so the ingest path can stash
// it in the message's audit field.
type AuditSummary struct {
	Mode              Mode
	Modified          bool
	HTMLPartsScanned  int
	Candidates        int
	Internalized      int
	Failed            int
	FailureCounts     map[FetchOutcome]int
	OriginalSize      int
	RewrittenSize     int
	WallClock         time.Duration
	ParseError        string // non-empty when parse failed
	NotEligibleReason string // non-empty when message was eligible-but-skipped
	TruncatedAtImage  int    // REQ-EXTIMG-22; 0 = not truncated
	// Placeholdered is the count of candidate URLs that survived the
	// fetch pass (typically because the origin returned an error or the
	// fetcher rejected the response) and were rewritten to the inline
	// placeholder data URI before the message body was rebuilt. Distinct
	// from Failed: Failed counts attempted-but-failed fetches; this
	// counts how many of those URLs were replaced in the stored body.
	// Equals Failed in the success path; 0 when the placeholder pass is
	// skipped (parse failure, no candidates, passthrough mode).
	Placeholdered int
}

// Internalize is the top-level entry. raw is the wire bytes accepted
// off the SMTP transport. cfg is the operator config; verdict is the
// DKIM result captured before rewriting (may be zero-value when DKIM
// is not in scope for this message).
//
// Returns (rewrittenBytes, summary, nil) on success; the rewrittenBytes
// MAY equal raw when there were no candidates or when mode is
// passthrough. Errors are reserved for catastrophes the caller cannot
// reasonably fall back from (e.g. ctx cancellation propagated mid-
// fetch); routine fetch failures are recorded in summary, not returned.
func Internalize(
	ctx context.Context,
	raw []byte,
	cfg Config,
	verdict DKIMVerdict,
) ([]byte, AuditSummary, error) {
	cfg.resolveOptional()
	if err := cfg.validate(); err != nil {
		return raw, AuditSummary{Mode: cfg.Mode, OriginalSize: len(raw),
			RewrittenSize: len(raw), NotEligibleReason: err.Error()}, nil
	}
	start := time.Now()
	sum := AuditSummary{
		Mode:          cfg.Mode,
		OriginalSize:  len(raw),
		RewrittenSize: len(raw),
		FailureCounts: map[FetchOutcome]int{},
	}

	if cfg.Mode == ModePassthrough {
		sum.NotEligibleReason = "mode=passthrough"
		sum.WallClock = time.Since(start)
		return raw, sum, nil
	}

	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		// Parse failure is a soft error: the caller stores the
		// original message verbatim. We log via the audit summary.
		// (REQ-EXTIMG-13 / REQ-EXTIMG-61)
		sum.ParseError = err.Error()
		sum.WallClock = time.Since(start)
		return raw, sum, nil
	}
	if strings.TrimSpace(env.HTML) == "" {
		sum.NotEligibleReason = "no_html_body"
		sum.WallClock = time.Since(start)
		return raw, sum, nil
	}
	sum.HTMLPartsScanned = 1

	cands, cerr := extractCandidates([]byte(env.HTML))
	if cerr != nil {
		sum.ParseError = cerr.Error()
		sum.WallClock = time.Since(start)
		return raw, sum, nil
	}
	sum.Candidates = len(cands)
	if len(cands) == 0 {
		sum.NotEligibleReason = "no_external_refs"
		sum.WallClock = time.Since(start)
		return raw, sum, nil
	}

	// Per-message wall-clock budget (REQ-EXTIMG-24).
	fetchCtx, cancel := context.WithTimeout(ctx, cfg.PerMessageTimeout)
	defer cancel()

	fetcher := NewFetcher(cfg)
	results := fetchAllBudgeted(fetchCtx, fetcher, cfg, cands, &sum)

	// Map URL -> cid. Only successful fetches end up here.
	cidMap := make(map[string]string, len(results))
	var inlines []inlinePart
	for _, r := range results {
		if r.Outcome != FetchOK {
			sum.Failed++
			sum.FailureCounts[r.Outcome]++
			continue
		}
		cid := newContentID()
		cidMap[r.URL] = cid
		inlines = append(inlines, inlinePart{cid: cid, ct: r.ContentType, body: r.Bytes})
		sum.Internalized++
	}
	// Pre-REQ-EXTIMG-BG-14 the all-fail case (len(inlines) == 0) bailed
	// out with the original bytes. We now fall through to rebuildMessage
	// so the placeholder pass below can de-fang the surviving external
	// URLs in the stored body. NotEligibleReason is set as an audit
	// breadcrumb but does not change the rebuild path.
	if len(inlines) == 0 {
		sum.NotEligibleReason = "no_successful_fetches"
	}

	rewrittenHTML, err := rewriteHTML([]byte(env.HTML), cidMap)
	if err != nil {
		sum.ParseError = err.Error()
		sum.WallClock = time.Since(start)
		return raw, sum, nil
	}

	// Failed-fetch URLs survive rewriteHTML unchanged because cidMap
	// only carries successful fetches. Replace them now with the
	// placeholder data URI so the stored body contains no external
	// fetchable references after a partial-success internalize. Without
	// this, AdminPanel privacy gating would be the sole protection
	// against the still-external URLs leaking on render — and any path
	// that bypasses the SPA gate (Email/get with bodyValues, blob
	// download of the raw source, IMAP FETCH) would expose them.
	// (REQ-EXTIMG-BG-14)
	if sum.Failed > 0 {
		if placeheld, perr := RewriteForPlaceholder(rewrittenHTML); perr == nil {
			rewrittenHTML = placeheld
			sum.Placeholdered = sum.Failed
		}
	}

	// Rebuild the message via enmime.Builder. The Builder produces a
	// canonical tree (mixed → related → alternative → text/plain +
	// text/html, plus inlines/attachments). We re-populate top-level
	// fields from the original envelope; arbitrary headers (List-Id,
	// X-*, etc.) are forwarded through Header() except for the ones
	// that would mislead recipients (DKIM, AuthRes — REQ-EXTIMG-41/-42).
	out, err := rebuildMessage(env, rewrittenHTML, inlines, cfg, verdict)
	if err != nil {
		sum.ParseError = err.Error()
		sum.WallClock = time.Since(start)
		return raw, sum, nil
	}
	sum.Modified = true
	sum.RewrittenSize = len(out)
	sum.WallClock = time.Since(start)
	return out, sum, nil
}

// inlinePart is the per-image bundle the rebuilder hands to
// enmime.Builder.AddInline.
type inlinePart struct {
	cid  string
	ct   string
	body []byte
}

// fetchAllBudgeted runs the candidate fetches in parallel up to
// cfg.ConcurrentFetches, enforcing the per-message image count
// (REQ-EXTIMG-22) and cumulative byte (REQ-EXTIMG-23) caps.
func fetchAllBudgeted(
	ctx context.Context,
	fetcher *Fetcher,
	cfg Config,
	cands []candidate,
	sum *AuditSummary,
) []FetchResult {
	if len(cands) > cfg.MaxPerMessageImages && cfg.MaxPerMessageImages > 0 {
		sum.TruncatedAtImage = cfg.MaxPerMessageImages
		cands = cands[:cfg.MaxPerMessageImages]
	}
	out := make([]FetchResult, len(cands))
	sem := make(chan struct{}, cfg.ConcurrentFetches)
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		usedBytes  int64
		messageCap = cfg.MaxPerMessageBytes
	)
	for i, c := range cands {
		i, c := i, c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := fetcher.Fetch(ctx, c.URL)
			mu.Lock()
			defer mu.Unlock()
			// Per-message cumulative cap: if accepting this would
			// exceed messageCap, mark it as message_budget instead.
			if r.Outcome == FetchOK && messageCap > 0 &&
				usedBytes+int64(len(r.Bytes)) > messageCap {
				r.Outcome = FetchMessageBudget
				r.Reason = fmt.Sprintf("would exceed per-message cap %d", messageCap)
				r.Bytes = nil
			} else if r.Outcome == FetchOK {
				usedBytes += int64(len(r.Bytes))
			}
			out[i] = r
		}()
	}
	wg.Wait()
	return out
}

// newContentID returns a Message-ID-shaped opaque id without angle
// brackets. The "@herold" suffix matches the host stamp and helps
// debuggers spot rewriter-generated parts.
func newContentID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) + "@herold"
}

// rebuildMessage uses enmime.Builder to produce the rewritten bytes.
// Top-level addresses come from the parsed envelope; the Date header
// is preserved verbatim (Builder's own default would re-stamp it,
// which would break threading and conversation linkage). Custom
// headers (List-Id, X-*, etc.) are forwarded — except DKIM-Signature
// and Authentication-Results which are dropped per REQ-EXTIMG-41/-42.
func rebuildMessage(
	env *enmime.Envelope,
	htmlBody []byte,
	inlines []inlinePart,
	cfg Config,
	verdict DKIMVerdict,
) ([]byte, error) {
	b := enmime.Builder()

	from := mustParseAddr(env.GetHeader("From"))
	if from == nil {
		return nil, errors.New("extimg: rebuild: no usable From")
	}
	b = b.From(from.Name, from.Address)

	if to := splitAddrs(env.GetHeader("To")); len(to) > 0 {
		b = b.ToAddrs(to)
	}
	if cc := splitAddrs(env.GetHeader("Cc")); len(cc) > 0 {
		b = b.CCAddrs(cc)
	}
	if bcc := splitAddrs(env.GetHeader("Bcc")); len(bcc) > 0 {
		b = b.BCCAddrs(bcc)
	}
	if rt := splitAddrs(env.GetHeader("Reply-To")); len(rt) > 0 {
		b = b.ReplyToAddrs(rt)
	}
	b = b.Subject(env.GetHeader("Subject"))
	if d, err := env.Date(); err == nil && !d.IsZero() {
		b = b.Date(d)
	}

	// Forward arbitrary headers verbatim; drop the ones we explicitly
	// own or would invalidate.
	for _, k := range env.GetHeaderKeys() {
		if isDroppedHeader(k, cfg) {
			continue
		}
		for _, v := range env.GetHeaderValues(k) {
			b = b.Header(k, v)
		}
	}

	// Body parts.
	if env.Text != "" {
		b = b.Text([]byte(env.Text))
	}
	b = b.HTML(htmlBody)

	// Existing inline parts (cid: references that arrived inline) are
	// re-attached so the rewritten body still resolves them.
	for _, p := range env.Inlines {
		cid := stripAngles(p.ContentID)
		ct := p.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		b = b.AddInline(p.Content, ct, p.FileName, cid)
	}
	// Newly-fetched external images are attached as additional inlines.
	for _, in := range inlines {
		b = b.AddInline(in.body, in.ct, "", in.cid)
	}
	// Existing attachments forward unchanged.
	for _, p := range env.Attachments {
		ct := p.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		b = b.AddAttachment(p.Content, ct, p.FileName)
	}

	// Server-stamped Authentication-Results (REQ-EXTIMG-43).
	host := cfg.HostHeader
	if host == "" {
		host = "herold"
	}
	b = b.Header("Authentication-Results", buildAuthResults(host, verdict))
	// Marker (REQ-EXTIMG-44).
	b = b.Header("X-Herold-Body-Modified", "image-internalization")

	part, err := b.Build()
	if err != nil {
		return nil, fmt.Errorf("extimg: rebuild: %w", err)
	}
	var buf bytes.Buffer
	if err := part.Encode(&buf); err != nil {
		return nil, fmt.Errorf("extimg: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// isDroppedHeader returns true for headers the rebuild path replaces
// or strips. The Builder also writes its own From/To/Subject/Date and
// MIME-Version so we drop those to avoid duplicates.
func isDroppedHeader(name string, cfg Config) bool {
	switch strings.ToLower(name) {
	case "from", "to", "cc", "bcc", "reply-to", "subject", "date",
		"mime-version", "content-type", "content-transfer-encoding",
		"content-disposition", "content-id":
		return true
	case "authentication-results":
		// Always drop inbound AuthRes — they reference the original
		// body and would mislead local re-verifiers (REQ-EXTIMG-42).
		return true
	case "dkim-signature":
		// Drop only when DKIM == strip (REQ-EXTIMG-41).
		return cfg.DKIM != DKIMKeep
	}
	return false
}

// buildAuthResults renders the server-stamped Authentication-Results
// value (REQ-EXTIMG-43). When verdict.Result is empty we emit
// "dkim=none". Format follows RFC 8601's authres-result production.
func buildAuthResults(host string, v DKIMVerdict) string {
	res := strings.ToLower(strings.TrimSpace(v.Result))
	if res == "" {
		res = "none"
	}
	var b strings.Builder
	b.WriteString(host)
	b.WriteString("; dkim=")
	b.WriteString(res)
	if v.SigningDomain != "" {
		b.WriteString(" header.d=")
		b.WriteString(v.SigningDomain)
	}
	if v.Selector != "" {
		b.WriteString(" header.s=")
		b.WriteString(v.Selector)
	}
	b.WriteString(" (verified at delivery; body modified for image-privacy rewrite)")
	return b.String()
}

// stripAngles removes a single leading "<" and trailing ">" from a
// Content-ID value, matching the form enmime's AddInline expects.
func stripAngles(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		return s[1 : len(s)-1]
	}
	return s
}

// mustParseAddr returns the first parsed address from raw, or nil.
func mustParseAddr(raw string) *mail.Address {
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil || len(addrs) == 0 {
		// Try the single-address parser as a fallback.
		if a, e2 := mail.ParseAddress(raw); e2 == nil {
			return a
		}
		return nil
	}
	return addrs[0]
}

// splitAddrs parses a comma-separated address-list header value into
// []mail.Address. Returns nil when raw is empty or unparseable.
func splitAddrs(raw string) []mail.Address {
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		return nil
	}
	out := make([]mail.Address, len(addrs))
	for i, a := range addrs {
		out[i] = *a
	}
	return out
}
