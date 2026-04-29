package linkpreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/hanshuebner/herold/internal/netguard"
)

// Preview is the parsed metadata for a single URL. Empty fields mean the
// page did not advertise a value (or the parse failed); rendering code
// is expected to gracefully omit empty rows from the card.
type Preview struct {
	// URL is the URL that produced this preview, after redirect
	// resolution. May differ from the requested URL when the upstream
	// returns 3xx with a Location header.
	URL string `json:"url"`
	// CanonicalURL is the value of <link rel="canonical">, if present.
	// The renderer prefers this over URL when displaying the link
	// because publishers set it to deduplicate trackers / params.
	CanonicalURL string `json:"canonicalUrl,omitempty"`
	// Title is the og:title, twitter:title, or <title> contents in
	// that priority order.
	Title string `json:"title,omitempty"`
	// Description is the og:description, twitter:description, or
	// <meta name="description"> contents in that priority order.
	Description string `json:"description,omitempty"`
	// ImageURL is the og:image or twitter:image URL. Always returned
	// in absolute form; relative values are resolved against URL.
	ImageURL string `json:"imageUrl,omitempty"`
	// SiteName is the og:site_name; falls back to the URL's hostname
	// when absent.
	SiteName string `json:"siteName,omitempty"`
}

// Options configures a Fetcher. Zero values fall back to documented
// defaults.
type Options struct {
	// FetchTimeout caps the total time spent on one Fetch (connect,
	// headers, body). Default 4s; values <= 0 fall back.
	FetchTimeout time.Duration
	// ConnectTimeout caps the connect / TLS handshake / response-
	// header phase of a single dial. Default 2s.
	ConnectTimeout time.Duration
	// MaxBodyBytes caps the number of response-body bytes parsed for
	// metadata. Default 1 MiB. Bodies larger than the cap are
	// silently truncated and the visible head is parsed.
	MaxBodyBytes int64
	// MaxRedirects caps the redirect chain length. Default 5.
	MaxRedirects int
	// UserAgent is the User-Agent header value sent with the GET
	// request. Default "herold-link-preview/1.0".
	UserAgent string
	// Resolver is the DNS resolver used for the pre-flight
	// netguard.CheckHost. nil falls back to net.DefaultResolver.
	Resolver *net.Resolver
	// Logger receives Warn-level breadcrumbs on fetch failures. nil
	// is replaced by slog.Default().
	Logger *slog.Logger
}

// Fetcher fetches link previews. Safe for concurrent use.
type Fetcher struct {
	client       *http.Client
	maxBody      int64
	userAgent    string
	resolver     *net.Resolver
	log          *slog.Logger
	fetchTimeout time.Duration
	// skipHostCheck is set by NewWithClient to bypass the pre-flight
	// netguard.CheckHost — a test-only injection that lets the suite
	// hit httptest.Server's loopback address. Production New() leaves
	// this false so the SSRF guard runs.
	skipHostCheck bool
}

// NewWithClient returns a Fetcher backed by the supplied http.Client.
// The pre-flight netguard.CheckHost is still applied to the URL host
// before each request, so an injected client does not bypass SSRF
// protection at the resolver layer; the dialer-side ControlContext
// is the caller's responsibility to install on client.Transport.
//
// Intended for tests against httptest.Server (which binds 127.0.0.1
// and would be refused by the production guard) and for any future
// integration that already runs through a hardened transport.
func NewWithClient(client *http.Client, opts Options) *Fetcher {
	if opts.FetchTimeout <= 0 {
		opts.FetchTimeout = 4 * time.Second
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 1 << 20
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "herold-link-preview/1.0"
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Fetcher{
		client:       client,
		maxBody:      opts.MaxBodyBytes,
		userAgent:    opts.UserAgent,
		resolver:     opts.Resolver,
		log:          opts.Logger,
		fetchTimeout: opts.FetchTimeout,
		// Fetch reads this nil to mean "skip the pre-flight host
		// check" — an injected client must own SSRF policy itself.
		skipHostCheck: true,
	}
}

// New returns a Fetcher with the supplied options merged on top of the
// documented defaults.
func New(opts Options) *Fetcher {
	if opts.FetchTimeout <= 0 {
		opts.FetchTimeout = 4 * time.Second
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 2 * time.Second
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 1 << 20 // 1 MiB
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = 5
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "herold-link-preview/1.0"
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	dialer := &net.Dialer{
		Timeout:        opts.ConnectTimeout,
		ControlContext: netguard.ControlContext(),
	}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   opts.ConnectTimeout,
		ResponseHeaderTimeout: opts.ConnectTimeout,
		ExpectContinueTimeout: opts.ConnectTimeout,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		// No automatic compression: we cap body size BEFORE
		// decompression so an attacker can't ship a 100MB compressed
		// payload that decompresses to gigabytes.
		DisableCompression: true,
	}
	client := &http.Client{
		Transport: tr,
	}
	maxRedirects := opts.MaxRedirects
	resolver := opts.Resolver
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("linkpreview: too many redirects (>%d)", maxRedirects)
		}
		// Re-run the SSRF check on the redirect target's host.
		if !isAllowedScheme(req.URL.Scheme) {
			return fmt.Errorf("linkpreview: redirect to disallowed scheme %q", req.URL.Scheme)
		}
		// CheckHost honours the request context's deadline.
		if err := netguard.CheckHost(req.Context(), resolver, req.URL.Hostname()); err != nil {
			return fmt.Errorf("linkpreview: redirect rejected: %w", err)
		}
		return nil
	}
	return &Fetcher{
		client:       client,
		maxBody:      opts.MaxBodyBytes,
		userAgent:    opts.UserAgent,
		resolver:     opts.Resolver,
		log:          opts.Logger,
		fetchTimeout: opts.FetchTimeout,
	}
}

// ErrNotHTML is returned when the response is fetched successfully but
// its Content-Type is not text/html (or unspecified). Callers may treat
// this as a non-error "no preview available" signal.
var ErrNotHTML = errors.New("linkpreview: response is not text/html")

// ErrUnsupportedScheme is returned for URLs that aren't http or https.
var ErrUnsupportedScheme = errors.New("linkpreview: unsupported URL scheme")

// Fetch retrieves rawURL, parses its metadata, and returns a Preview.
// The fetch is bounded by the Fetcher's FetchTimeout; the supplied ctx
// further bounds it. Non-2xx responses, oversized non-HTML bodies, and
// SSRF rejections are returned as errors; the caller is responsible for
// logging or swallowing them as appropriate.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Preview, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Preview{}, fmt.Errorf("linkpreview: parse url: %w", err)
	}
	if !isAllowedScheme(parsed.Scheme) {
		return Preview{}, fmt.Errorf("%w (%q)", ErrUnsupportedScheme, parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return Preview{}, fmt.Errorf("linkpreview: missing host in %q", rawURL)
	}
	// Pre-flight DNS check so we don't even open a connection to a
	// hostname whose A/AAAA records resolve to an internal range.
	// CheckHost runs the resolver and rejects every blocked address;
	// the dialer ControlContext is the second line of defence.
	if !f.skipHostCheck {
		if err := netguard.CheckHost(ctx, f.resolver, parsed.Hostname()); err != nil {
			return Preview{}, err
		}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, f.fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Preview{}, fmt.Errorf("linkpreview: new request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := f.client.Do(req)
	if err != nil {
		return Preview{}, fmt.Errorf("linkpreview: get %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Preview{}, fmt.Errorf("linkpreview: status %d from %s", resp.StatusCode, rawURL)
	}
	ct := resp.Header.Get("Content-Type")
	if !looksLikeHTML(ct) {
		return Preview{}, fmt.Errorf("%w (got %q)", ErrNotHTML, ct)
	}

	// Cap the body parse. We deliberately use io.LimitReader rather
	// than io.ReadAll(&io.LimitedReader{...}) because the LimitedReader
	// would silently swallow the rest of the body without surfacing
	// the truncation; for parsing the head of an HTML document the
	// truncation is benign.
	body := io.LimitReader(resp.Body, f.maxBody)

	preview, err := parseHTML(body, resp.Request.URL)
	if err != nil {
		return Preview{}, fmt.Errorf("linkpreview: parse html: %w", err)
	}

	// resp.Request.URL is the FINAL URL after redirects. Promote it
	// onto the preview so the renderer shows the resolved address.
	preview.URL = resp.Request.URL.String()
	if preview.SiteName == "" {
		preview.SiteName = resp.Request.URL.Hostname()
	}
	return preview, nil
}

// FetchAll fetches each URL in parallel (capped at maxParallel) and
// returns the Previews in the same order, with empty slots for URLs
// that failed. Errors are logged at warn level on the Fetcher's logger
// and never propagated; this is the call shape chat uses, where a
// failed preview should not block the message from being delivered.
func (f *Fetcher) FetchAll(ctx context.Context, urls []string, maxParallel int) []Preview {
	if maxParallel <= 0 {
		maxParallel = 3
	}
	out := make([]Preview, len(urls))
	if len(urls) == 0 {
		return out
	}
	sem := make(chan struct{}, maxParallel)
	done := make(chan struct{})
	pending := len(urls)
	for i, raw := range urls {
		sem <- struct{}{}
		go func(idx int, u string) {
			defer func() {
				<-sem
				if pending--; pending == 0 {
					close(done)
				}
			}()
			p, err := f.Fetch(ctx, u)
			if err != nil {
				f.log.Warn("linkpreview.fetch_failed",
					"url", u,
					"err", err.Error())
				return
			}
			out[idx] = p
		}(i, raw)
	}
	<-done
	return out
}

// isAllowedScheme reports whether scheme is http or https.
func isAllowedScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}

// looksLikeHTML reports whether the Content-Type header value indicates
// an HTML or XHTML document. Empty Content-Type is treated as HTML
// because some upstreams omit the header on small responses.
func looksLikeHTML(contentType string) bool {
	if contentType == "" {
		return true
	}
	mime := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	return mime == "text/html" || mime == "application/xhtml+xml"
}

// urlPattern matches absolute http/https URLs in a body of text. The
// regexp deliberately stops before trailing punctuation (closing
// parens, periods, commas, exclamation marks) so URLs in prose like
// "see https://example.com." don't pull the trailing dot in.
var urlPattern = regexp.MustCompile(`https?://[^\s<>()"']+[^\s<>()"',.!?;:]`)

// ExtractURLs returns up to max URLs found in s. Duplicate URLs are
// returned only once (in first-seen order). Used by the chat dispatcher
// to discover preview candidates in body.text without parsing HTML.
func ExtractURLs(s string, max int) []string {
	if max <= 0 {
		return nil
	}
	matches := urlPattern.FindAllString(s, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, dup := seen[m]; dup {
			continue
		}
		// Round-trip through url.Parse to drop obviously malformed
		// captures (the regexp is permissive on purpose).
		if _, err := url.Parse(m); err != nil {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
		if len(out) >= max {
			break
		}
	}
	return out
}

// parseHTML walks the supplied HTML stream and returns a Preview
// populated from the head metadata it finds. baseURL is the URL the
// document was fetched from; relative og:image / canonical values are
// resolved against it.
func parseHTML(r io.Reader, baseURL *url.URL) (Preview, error) {
	z := html.NewTokenizer(r)
	var out Preview
	var inHead bool
	var inTitle bool
	var titleBuf strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			err := z.Err()
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return out, err
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			name := strings.ToLower(string(tn))
			switch name {
			case "head":
				inHead = true
			case "body":
				// Once we hit <body> the head is over; metadata is
				// already extracted, so stop walking to keep the
				// parse bounded even on huge documents.
				if out.Title == "" {
					out.Title = strings.TrimSpace(titleBuf.String())
				} else {
					out.Title = strings.TrimSpace(out.Title)
				}
				return out, nil
			case "title":
				if inHead {
					inTitle = true
					titleBuf.Reset()
				}
			case "meta":
				if !hasAttr {
					continue
				}
				m := readAttrs(z)
				property := strings.ToLower(m["property"])
				name := strings.ToLower(m["name"])
				content := m["content"]
				if content == "" {
					continue
				}
				switch property {
				case "og:title":
					if out.Title == "" {
						out.Title = content
					}
				case "og:description":
					if out.Description == "" {
						out.Description = content
					}
				case "og:image", "og:image:url", "og:image:secure_url":
					if out.ImageURL == "" {
						out.ImageURL = absoluteURL(baseURL, content)
					}
				case "og:site_name":
					out.SiteName = content
				}
				switch name {
				case "twitter:title":
					if out.Title == "" {
						out.Title = content
					}
				case "twitter:description":
					if out.Description == "" {
						out.Description = content
					}
				case "twitter:image":
					if out.ImageURL == "" {
						out.ImageURL = absoluteURL(baseURL, content)
					}
				case "description":
					if out.Description == "" {
						out.Description = content
					}
				}
			case "link":
				if !hasAttr {
					continue
				}
				m := readAttrs(z)
				rel := strings.ToLower(m["rel"])
				if rel == "canonical" && m["href"] != "" {
					out.CanonicalURL = absoluteURL(baseURL, m["href"])
				}
			}
		case html.EndTagToken:
			tn, _ := z.TagName()
			name := strings.ToLower(string(tn))
			switch name {
			case "head":
				inHead = false
			case "title":
				inTitle = false
			}
		case html.TextToken:
			if inTitle {
				titleBuf.Write(z.Text())
			}
		}
	}
	if out.Title == "" {
		out.Title = strings.TrimSpace(titleBuf.String())
	}
	return out, nil
}

// readAttrs drains the current tag's attribute list into a map. The
// tokenizer's TagAttr only returns one attribute per call.
func readAttrs(z *html.Tokenizer) map[string]string {
	m := map[string]string{}
	for {
		k, v, more := z.TagAttr()
		m[strings.ToLower(string(k))] = string(v)
		if !more {
			break
		}
	}
	return m
}

// absoluteURL returns href resolved against base. Returns href verbatim
// when the resolution fails (so a malformed value still surfaces in
// logs rather than being silently dropped).
func absoluteURL(base *url.URL, href string) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if base == nil {
		return u.String()
	}
	return base.ResolveReference(u).String()
}
