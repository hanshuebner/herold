package extimg

// REQ-EXTIMG-RETRY-01..09 (issue #162): server-side retry for images
// that failed to internalize. REQ-EXTIMG-BG-14 placeholders every
// failed-fetch URL out of the stored body permanently -- this file
// lets a later user action re-attempt exactly those URLs without ever
// re-deriving them from the delivered (placeholder-only) body, and
// without ever handing the origin URL back to the browser.
//
// RetainedState is the server-only record a caller persists after a
// partial-success Internalize/InternalizeReader call (Failed > 0):
// the failed URLs, the pre-placeholder HTML template needed to splice
// a successful retry back into the correct document position, and the
// DKIM verdict so a rebuilt message re-stamps the same
// Authentication-Results line. Callers encode this via
// EncodeRetainedState into an opaque string for storage (herold's
// store layer treats it as an opaque blob, never interpreting it) and
// decode it back via DecodeRetainedState when a retry is requested.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/jhillyerd/enmime"
)

// RetainedState is the server-only retry record for one message.
type RetainedState struct {
	// URLs is the deduplicated, document-order list of URLs that have
	// not yet been successfully internalized.
	URLs []string
	// Template is the HTML with already-successful images as cid:
	// references and still-failing ones carrying their original
	// http(s) URL (extimg.AuditSummary.FailedImageTemplate).
	Template []byte
	// DKIM is the verdict captured at the original internalize time,
	// reused so a retry's rebuilt Authentication-Results header
	// matches the original rather than regressing to "dkim=none".
	DKIM DKIMVerdict
}

// retainedStateWire is the JSON-serialisable form. Template is
// base64-encoded because html.Render output, while normally valid
// UTF-8, is not guaranteed to survive JSON string escaping unscathed
// for arbitrary byte sequences (e.g. a malformed entity byte a lenient
// parser passed through); base64 sidesteps the question entirely.
type retainedStateWire struct {
	URLs         []string `json:"urls"`
	TemplateB64  string   `json:"templateB64"`
	DKIMResult   string   `json:"dkimResult,omitempty"`
	DKIMDomain   string   `json:"dkimDomain,omitempty"`
	DKIMSelector string   `json:"dkimSelector,omitempty"`
}

// EncodeRetainedState marshals s into the opaque string the store
// layer persists. Empty URLs encodes as "" (the store's "nothing
// retained" sentinel).
func EncodeRetainedState(s RetainedState) (string, error) {
	if len(s.URLs) == 0 {
		return "", nil
	}
	wire := retainedStateWire{
		URLs:         s.URLs,
		TemplateB64:  base64.StdEncoding.EncodeToString(s.Template),
		DKIMResult:   s.DKIM.Result,
		DKIMDomain:   s.DKIM.SigningDomain,
		DKIMSelector: s.DKIM.Selector,
	}
	b, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("extimg: encode retained state: %w", err)
	}
	return string(b), nil
}

// DecodeRetainedState parses the opaque string back into a
// RetainedState. Empty input decodes to the zero value with no error.
func DecodeRetainedState(raw string) (RetainedState, error) {
	if raw == "" {
		return RetainedState{}, nil
	}
	var wire retainedStateWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return RetainedState{}, fmt.Errorf("extimg: decode retained state: %w", err)
	}
	tmpl, err := base64.StdEncoding.DecodeString(wire.TemplateB64)
	if err != nil {
		return RetainedState{}, fmt.Errorf("extimg: decode retained state template: %w", err)
	}
	return RetainedState{
		URLs:     wire.URLs,
		Template: tmpl,
		DKIM: DKIMVerdict{
			Result:        wire.DKIMResult,
			SigningDomain: wire.DKIMDomain,
			Selector:      wire.DKIMSelector,
		},
	}, nil
}

// RetryResult is the outcome of one RetryFailedImages call.
type RetryResult struct {
	// RetriedOK is the number of previously-failed URLs that were
	// successfully fetched this attempt.
	RetriedOK int
	// StillFailedURLs is the subset of the input urls that failed
	// again (or were newly truncated/budget-capped) this attempt.
	// Empty means every retained URL is now resolved.
	StillFailedURLs []string
	// NewTemplate is the updated pre-placeholder HTML template: newly
	// -successful URLs are now cid: references, still-failing ones
	// retain their original URL. Only meaningful when Modified is
	// true; callers persist it (paired with StillFailedURLs) so a
	// further retry can operate on the smaller remaining set.
	NewTemplate []byte
	// Modified is true when at least one URL was newly fetched and
	// out (RetryFailedImages's first return value) differs from the
	// input raw bytes.
	Modified bool
}

// RetryFailedImages re-fetches urls (the retained failed-fetch list
// for one message) and, for each success, rewrites its occurrence in
// template into a cid: reference before rebuilding the full message
// from currentRaw (the CURRENT stored blob bytes -- still carrying
// the all-placeholder body plus every previously-successful inline
// image as a real MIME part). URLs that fail again are placeholdered
// again in the rebuilt body, exactly as the original Internalize
// pass would have done, so the delivered body never regresses to
// carrying a raw external URL.
//
// Returns (currentRaw, RetryResult{}, nil) unchanged when urls is
// empty or nothing newly succeeds -- callers should treat a
// Modified=false result as "nothing to persist".
func RetryFailedImages(
	ctx context.Context,
	currentRaw []byte,
	template []byte,
	urls []string,
	cfg Config,
	verdict DKIMVerdict,
) ([]byte, RetryResult, error) {
	if len(urls) == 0 {
		return currentRaw, RetryResult{}, nil
	}
	cfg.resolveOptional()
	if err := cfg.validate(); err != nil {
		return currentRaw, RetryResult{StillFailedURLs: urls}, nil
	}

	env, err := enmime.ReadEnvelope(bytes.NewReader(currentRaw))
	if err != nil {
		return currentRaw, RetryResult{StillFailedURLs: urls}, nil
	}

	cands := make([]candidate, len(urls))
	for i, u := range urls {
		cands[i] = candidate{URL: u}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, cfg.PerMessageTimeout)
	defer cancel()
	fetcher := NewFetcher(cfg)
	sum := AuditSummary{FailureCounts: map[FetchOutcome]int{}}
	results := fetchAllBudgeted(fetchCtx, fetcher, cfg, cands, &sum)

	cidMap := make(map[string]string, len(results))
	var inlines []inlinePart
	var stillFailed []string
	for _, r := range results {
		if r.Outcome != FetchOK {
			stillFailed = append(stillFailed, r.URL)
			continue
		}
		cid := newContentID()
		cidMap[r.URL] = cid
		inlines = append(inlines, inlinePart{cid: cid, ct: r.ContentType, body: r.Bytes})
	}
	if len(inlines) == 0 {
		// Nothing improved; no rebuild needed.
		return currentRaw, RetryResult{StillFailedURLs: urls}, nil
	}

	rewrittenHTML, err := rewriteHTML(template, cidMap)
	if err != nil {
		return currentRaw, RetryResult{StillFailedURLs: urls}, nil
	}
	// newTemplate captures the state BEFORE the remaining failures are
	// placeholdered, so a further retry still has raw URLs to work
	// with for whatever is still failing.
	newTemplate := append([]byte(nil), rewrittenHTML...)
	if len(stillFailed) > 0 {
		if placeheld, perr := RewriteForPlaceholder(rewrittenHTML); perr == nil {
			rewrittenHTML = placeheld
		}
	}

	out, err := rebuildMessage(env, rewrittenHTML, inlines, cfg, verdict)
	if err != nil {
		return currentRaw, RetryResult{StillFailedURLs: urls}, nil
	}
	return out, RetryResult{
		RetriedOK:       len(inlines),
		StillFailedURLs: stillFailed,
		NewTemplate:     newTemplate,
		Modified:        true,
	}, nil
}
