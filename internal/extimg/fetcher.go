package extimg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FetchOutcome enumerates the per-fetch audit codes (REQ-EXTIMG-80).
// One of these gets recorded for every candidate URL the rewriter
// considers — both successes and failures — so the operator can debug
// "why didn't this image render?" without a server-side log dive.
type FetchOutcome string

const (
	FetchOK            FetchOutcome = "ok"
	FetchTooLarge      FetchOutcome = "too_large"
	FetchTimeout       FetchOutcome = "timeout"
	FetchNotImage      FetchOutcome = "not_image"
	FetchEmpty         FetchOutcome = "empty"
	FetchBlockedSSRF   FetchOutcome = "blocked_ssrf"
	FetchHTTP4xx       FetchOutcome = "http_4xx"
	FetchHTTP5xx       FetchOutcome = "http_5xx"
	FetchRedirectLoop  FetchOutcome = "redirect_loop"
	FetchTransport     FetchOutcome = "transport_error"
	FetchMessageBudget FetchOutcome = "message_budget"
	// FetchBlockedScheme is a scheme-policy rejection (a non-http(s)
	// scheme, or RequireHTTPS refusing a plain http:// URL) -- distinct
	// from FetchBlockedSSRF, which is a genuine internal-destination
	// block (issue #271). Separating the two lets the audit, the log,
	// and the JMAP failedImageReason category tell "we don't fetch this
	// protocol" apart from "this host resolves inside the network".
	FetchBlockedScheme FetchOutcome = "blocked_scheme"
)

// Retryable reports whether a fetch that failed with this outcome
// might succeed if retried (issue #271): the JMAP Email object's
// retryableFailedImageCount tallies the subset of a message's failed
// images whose outcome satisfies this. FetchOK is not a failure
// category and never appears in a failure tally.
func (o FetchOutcome) Retryable() bool {
	switch o {
	case FetchTimeout, FetchHTTP5xx, FetchMessageBudget, FetchTransport:
		return true
	default:
		return false
	}
}

// FetchResult is the per-URL output of a single Fetcher.Fetch call.
// Bytes is non-empty only when Outcome == FetchOK.
type FetchResult struct {
	URL         string
	Outcome     FetchOutcome
	Reason      string // free-form detail; included in audit + logs
	Bytes       []byte
	ContentType string
	HTTPStatus  int
	Duration    time.Duration
}

// Fetcher wraps a Config-bound *http.Client and the SSRF guard. The
// budgets in cfg are consulted on every Fetch — both per-image (in
// the http.Client timeout + the manual byte cap) and per-message
// (the caller threads ctx with a deadline derived from cfg.PerMessageTimeout).
type Fetcher struct {
	cfg    Config
	guard  *SSRFGuard
	client *http.Client
}

// NewFetcher constructs a Fetcher. The cfg is copied; mutations after
// construction are not observed.
func NewFetcher(cfg Config) *Fetcher {
	cfg.resolveOptional()
	guard := NewSSRFGuard(cfg)
	transport := &http.Transport{
		DialContext:           guard.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   cfg.PerImageConnectTimeout,
		ResponseHeaderTimeout: cfg.PerImageConnectTimeout,
		DisableKeepAlives:     false,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.PerImageTotalTimeout,
		// Re-validate every redirect target (REQ-EXTIMG-26 / REQ-EXTIMG-34).
		// http.ErrUseLastResponse short-circuits the redirect; the
		// Fetcher reads the 3xx response and treats it as an error
		// (we don't actually want the redirect body, just to refuse).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= cfg.FollowRedirectsMax {
				return errTooManyRedirects
			}
			if err := guard.ValidateURL(req.URL); err != nil {
				return err
			}
			return nil
		},
	}
	return &Fetcher{cfg: cfg, guard: guard, client: client}
}

var errTooManyRedirects = errors.New("extimg: too many redirects")

// Fetch retrieves rawURL through the guarded transport, capped at
// cfg.MaxPerImageBytes. Returns a FetchResult whose Outcome encodes
// success or the failure category; never returns a Go error — the
// caller's audit only cares about the categorical outcome.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) FetchResult {
	start := time.Now()
	r := FetchResult{URL: rawURL}

	u, err := url.Parse(rawURL)
	if err != nil {
		r.Outcome = FetchTransport
		r.Reason = "parse: " + err.Error()
		r.Duration = time.Since(start)
		return r
	}
	if err := f.guard.ValidateURL(u); err != nil {
		r.Outcome = classifySSRFBlock(err)
		r.Reason = err.Error()
		r.Duration = time.Since(start)
		return r
	}

	// Per-image total timeout is enforced by f.client.Timeout. The
	// caller's per-message ctx imposes a parallel ceiling: whichever
	// fires first cancels the request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		r.Outcome = FetchTransport
		r.Reason = err.Error()
		r.Duration = time.Since(start)
		return r
	}
	// Conservative UA that doesn't claim to be a browser. Some CDNs
	// rate-limit "unknown" UAs less harshly than empty-UA requests;
	// the string identifies us as a server-side prefetcher.
	req.Header.Set("User-Agent", "herold-imgproxy/1.0 (+https://github.com/hanshuebner/herold)")
	req.Header.Set("Accept", "image/*,*/*;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		r.Duration = time.Since(start)
		// Categorise.
		var blocked *ErrSSRFBlocked
		switch {
		case errors.As(err, &blocked):
			r.Outcome = classifySSRFBlock(err)
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			r.Outcome = FetchTimeout
		case errors.Is(err, errTooManyRedirects):
			r.Outcome = FetchRedirectLoop
		default:
			// SSRF errors can also be wrapped inside url.Error from
			// the redirect path — check the textual indicator as a
			// belt-and-suspenders for go versions where errors.As
			// does not unwrap through *url.Error consistently.
			if strings.Contains(err.Error(), "ssrf:") {
				r.Outcome = classifySSRFBlock(err)
			} else {
				r.Outcome = FetchTransport
			}
		}
		r.Reason = err.Error()
		return r
	}
	defer resp.Body.Close()

	r.HTTPStatus = resp.StatusCode
	switch {
	case resp.StatusCode >= 500:
		r.Outcome = FetchHTTP5xx
		r.Reason = resp.Status
		r.Duration = time.Since(start)
		return r
	case resp.StatusCode >= 400:
		r.Outcome = FetchHTTP4xx
		r.Reason = resp.Status
		r.Duration = time.Since(start)
		return r
	}

	// Hard byte cap with one trailing read to detect overruns
	// (REQ-EXTIMG-21). LimitReader stops at exactly cap bytes; we
	// then peek one more to distinguish "fits" from "truncated".
	cap := f.cfg.MaxPerImageBytes
	if cap <= 0 {
		cap = 5 * 1024 * 1024
	}
	body, err := readWithCap(resp.Body, cap)
	r.Duration = time.Since(start)
	if errors.Is(err, errOverCap) {
		r.Outcome = FetchTooLarge
		r.Reason = fmt.Sprintf(">%d bytes", cap)
		return r
	}
	if err != nil {
		r.Outcome = FetchTransport
		r.Reason = "read: " + err.Error()
		return r
	}
	if len(body) == 0 {
		r.Outcome = FetchEmpty
		r.Reason = "zero-length body"
		return r
	}

	ct := resp.Header.Get("Content-Type")
	if !looksLikeImage(ct, body) {
		r.Outcome = FetchNotImage
		r.Reason = "content-type=" + ct
		return r
	}

	r.ContentType = canonicalImageContentType(ct, body)
	r.Bytes = body
	r.Outcome = FetchOK
	return r
}

// classifySSRFBlock maps an SSRF-guard refusal to the specific
// FetchOutcome the caller records (issue #271): a scheme-policy
// rejection (ssrfBadScheme -- a disallowed scheme, or RequireHTTPS
// refusing plain http://) is a distinct, operator-policy outcome from
// a destination block (the host resolves inside the network). Falls
// back to a textual check because the redirect path (http.Client.
// CheckRedirect) can hand back the error wrapped inside a *url.Error
// that does not always survive errors.As on every Go version.
func classifySSRFBlock(err error) FetchOutcome {
	var blocked *ErrSSRFBlocked
	if errors.As(err, &blocked) && blocked.Reason == ssrfBadScheme {
		return FetchBlockedScheme
	}
	if strings.Contains(err.Error(), "scheme not allowed") {
		return FetchBlockedScheme
	}
	return FetchBlockedSSRF
}

var errOverCap = errors.New("extimg: response exceeded byte cap")

// readWithCap reads up to cap bytes; if the response has more it
// returns errOverCap so the caller can treat the fetch as too_large.
func readWithCap(r io.Reader, cap int64) ([]byte, error) {
	limited := &io.LimitedReader{R: r, N: cap + 1}
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > cap {
		return nil, errOverCap
	}
	return buf, nil
}

// looksLikeImage reports whether the response is plausibly an image.
// Header is checked first; if missing/wrong we sniff the magic bytes.
// Refuses HTML, JSON, plain text — common 4xx-served-as-200 patterns.
func looksLikeImage(ct string, body []byte) bool {
	low := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(low, ';'); i >= 0 {
		low = strings.TrimSpace(low[:i])
	}
	if strings.HasPrefix(low, "image/") {
		return true
	}
	if low == "" || low == "application/octet-stream" {
		return sniffImage(body) != ""
	}
	// Anything else (text/html, application/json, etc.) is suspect
	// even if it happens to start with image magic bytes; refuse.
	return false
}

// canonicalImageContentType returns the content-type we record on the
// inline part. We prefer the server's header when it claims an image
// type, else fall back to the sniffed type.
func canonicalImageContentType(ct string, body []byte) string {
	low := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(low, ';'); i >= 0 {
		low = strings.TrimSpace(low[:i])
	}
	if strings.HasPrefix(low, "image/") {
		return low
	}
	if sniffed := sniffImage(body); sniffed != "" {
		return sniffed
	}
	return "application/octet-stream"
}

// sniffImage inspects the leading bytes for a known image magic.
// Returns the canonical Content-Type or "" when nothing matches.
// We deliberately keep this list short — exotic formats (HEIF, AVIF,
// JPEG-XL) work fine when the server's Content-Type header is honest;
// sniffing is the fallback for naked octet-stream replies.
func sniffImage(b []byte) string {
	if len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return "image/jpeg"
	}
	if len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(b) >= 4 && (string(b[:4]) == "<svg" || string(b[:5]) == "<?xml") {
		// Heuristic: SVG often arrives as text/xml; accept when the
		// first few bytes look like XML/SVG even without an image/
		// content-type. Cheap and good enough.
		if strings.Contains(strings.ToLower(string(b)), "<svg") {
			return "image/svg+xml"
		}
	}
	return ""
}
