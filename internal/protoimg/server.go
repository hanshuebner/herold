package protoimg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/netguard"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultUserAgent is the fixed UA REQ-SEND-72 mandates for every
// upstream request. Same value for every fetch — no per-user
// fingerprinting and no operator override on a per-request basis.
const DefaultUserAgent = "herold-image-proxy/1"

// MaxURLLength caps the URL query parameter at 2048 chars per
// REQ-SEND-71. Longer URLs are 400'd before we touch the upstream.
const MaxURLLength = 2048

// MaxRedirectsDefault is the redirect-hop ceiling REQ-SEND-71 calls for.
const MaxRedirectsDefault = 3

// Defaults match REQ-SEND-74 / REQ-SEND-75 / REQ-SEND-77.
const (
	defaultMaxBytes            int64 = 25 * 1024 * 1024
	defaultConnectTimeout            = 10 * time.Second
	defaultTotalTimeout              = 30 * time.Second
	defaultCacheMaxBytes       int64 = 256 * 1024 * 1024
	defaultCacheMaxEntries           = 8192
	defaultCacheMaxAge               = 24 * time.Hour
	defaultPerUserPerMin             = 200
	defaultPerUserOriginPerMin       = 10
	defaultPerUserConcurrent         = 8
)

// retryDelay is the inter-attempt delay REQ-SEND-76 specifies. Tests
// override the clock to skip the wait deterministically.
const retryDelay = time.Second

// Options configures a Server. Zero values fall through to the
// REQ-SEND defaults documented per-field.
type Options struct {
	Logger *slog.Logger
	Clock  clock.Clock
	// HTTPClient is the upstream-egress client. Callers SHOULD pass a
	// dedicated *http.Client so the proxy's DialContext / redirect
	// constraints do not leak into the rest of the server's HTTP. Nil
	// makes New construct an internally bounded client.
	HTTPClient *http.Client

	MaxBytes       int64         // default 25 * 1024 * 1024 (25 MB)
	ConnectTimeout time.Duration // default 10s
	TotalTimeout   time.Duration // default 30s
	MaxRedirects   int           // default 3
	UserAgent      string        // default "herold-image-proxy/1"

	CacheMaxBytes   int64         // default 256 MiB
	CacheMaxEntries int           // default 8192
	CacheMaxAge     time.Duration // default 24h ceiling

	PerUserPerMin       int // default 200
	PerUserOriginPerMin int // default 10
	PerUserConcurrent   int // default 8

	// SessionResolver returns the principal id for an authenticated
	// HTTP request, or zero + false if not authenticated. Production
	// wiring uses internal/protoui.Server.ResolveSession.
	SessionResolver func(r *http.Request) (store.PrincipalID, bool)
}

// Server is the protoimg handle. One *Server backs the whole proxy
// surface; tests construct one against an httptest upstream, production
// constructs one in internal/admin and mounts Handler() under
// /proxy/image.
type Server struct {
	logger     *slog.Logger
	clk        clock.Clock
	httpClient *http.Client

	maxBytes       int64
	totalTimeout   time.Duration
	maxRedirects   int
	userAgent      string
	cacheMaxAge    time.Duration
	resolveSession func(r *http.Request) (store.PrincipalID, bool)

	cache *imageCache
	lim   *limiter
}

// New constructs a Server. When opts.SessionResolver is nil every
// request is treated as anonymous and rejected with 401; that keeps a
// misconfigured wiring safe-by-default rather than opening the
// internet to anonymous fetches.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = defaultConnectTimeout
	}
	if opts.TotalTimeout <= 0 {
		opts.TotalTimeout = defaultTotalTimeout
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = MaxRedirectsDefault
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.CacheMaxBytes <= 0 {
		opts.CacheMaxBytes = defaultCacheMaxBytes
	}
	if opts.CacheMaxEntries <= 0 {
		opts.CacheMaxEntries = defaultCacheMaxEntries
	}
	if opts.CacheMaxAge <= 0 {
		opts.CacheMaxAge = defaultCacheMaxAge
	}
	if opts.PerUserPerMin <= 0 {
		opts.PerUserPerMin = defaultPerUserPerMin
	}
	if opts.PerUserOriginPerMin <= 0 {
		opts.PerUserOriginPerMin = defaultPerUserOriginPerMin
	}
	if opts.PerUserConcurrent <= 0 {
		opts.PerUserConcurrent = defaultPerUserConcurrent
	}

	s := &Server{
		logger:         logger,
		clk:            clk,
		maxBytes:       opts.MaxBytes,
		totalTimeout:   opts.TotalTimeout,
		maxRedirects:   opts.MaxRedirects,
		userAgent:      opts.UserAgent,
		cacheMaxAge:    opts.CacheMaxAge,
		resolveSession: opts.SessionResolver,
		cache:          newImageCache(opts.CacheMaxEntries, opts.CacheMaxBytes),
		lim:            newLimiter(clk, opts.PerUserPerMin, opts.PerUserOriginPerMin, opts.PerUserConcurrent),
	}

	if opts.HTTPClient != nil {
		// Caller-supplied client: take it verbatim. The redirect
		// predicate must still be installed so http -> https
		// downgrades cannot slip past us; we wrap CheckRedirect
		// without touching the transport so a test fixture's RoundTripper
		// still sees the constructed requests.
		c := *opts.HTTPClient
		c.CheckRedirect = s.checkRedirect
		s.httpClient = &c
	} else {
		// Production transport: ControlContext fires after Go has
		// resolved the target's IP but before the connect(2) syscall,
		// giving us an SSRF-time veto over private / loopback / link-
		// local destinations on every dial — including dials issued
		// by Transport while following redirects.
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
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   4,
		}
		s.httpClient = &http.Client{
			Transport:     tr,
			CheckRedirect: s.checkRedirect,
		}
	}
	return s
}

// Handler returns the GET /proxy/image handler. The caller mounts it
// onto the parent ServeMux; the handler does its own method check so
// the path multiplexer can be exact ("/proxy/image") without needing
// a method router.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveProxy)
}

// checkRedirect implements REQ-SEND-71 and REQ-SEND-72: bound the hop
// count, reject any non-https target, and strip identifying request
// headers from the next request.
//
// Go invokes CheckRedirect *before* following each redirect, with via
// containing every prior request in the chain. A budget of N hops
// means we permit up to N entries in via; the (N+1)th hop is denied.
func (s *Server) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > s.maxRedirects {
		return errTooManyRedirects
	}
	if req.URL.Scheme != "https" {
		return errNonHTTPSRedirect
	}
	// Strip headers Go would otherwise carry forward. We set the
	// generic User-Agent here too in case a transport injected one
	// (e.g. a test RoundTripper).
	req.Header.Del("Cookie")
	req.Header.Del("Referer")
	req.Header.Set("User-Agent", s.userAgent)
	return nil
}

var (
	errTooManyRedirects = errors.New("protoimg: too many redirects")
	errNonHTTPSRedirect = errors.New("protoimg: redirect target is not https")
	errBodyTooLarge     = errors.New("protoimg: upstream body exceeded byte cap")
)

// serveProxy is the GET /proxy/image handler.
func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Authenticate.
	pid, ok := store.PrincipalID(0), false
	if s.resolveSession != nil {
		pid, ok = s.resolveSession(r)
	}
	if !ok || pid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "missing url query parameter", http.StatusBadRequest)
		return
	}
	if len(rawURL) > MaxURLLength {
		http.Error(w, "url query parameter too long", http.StatusBadRequest)
		return
	}
	origin, err := validateImageProxyURL(rawURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Re-parse so we have the lowercased-scheme URL to forward; the
	// validator already normalised in place but returns only the
	// origin, so re-parse for canonical .String() output.
	u, perr := url.Parse(rawURL)
	if perr != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	u.Scheme = strings.ToLower(u.Scheme)

	// Rate-limit.
	release, reason, retry := s.lim.admit(pid, origin)
	if reason != admitOK {
		w.Header().Set("Retry-After", retryAfterSeconds(retry))
		s.logger.LogAttrs(r.Context(), slog.LevelInfo, "protoimg.ratelimit",
			slog.Uint64("principal_id", uint64(pid)),
			slog.String("origin", origin),
			slog.Int("reason", int(reason)),
		)
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	defer release()

	// Cache hit?
	key := hashURL(u.String())
	if entry, hit := s.cache.get(key, s.clk.Now()); hit {
		s.writeImage(w, entry, r.Method)
		return
	}

	// Bound the upstream call by the configured total deadline,
	// derived from the request context so a client disconnect cancels
	// the in-flight fetch.
	ctx, cancel := context.WithTimeout(r.Context(), s.totalTimeout)
	defer cancel()

	resp, body, fetchErr := s.fetchWithRetry(ctx, u.String())
	if fetchErr != nil {
		// Network failure (or redirect-policy violation) — REQ-SEND-76
		// maps both to 502 after retry exhaustion.
		s.logger.LogAttrs(r.Context(), slog.LevelInfo, "protoimg.fetch_failed",
			slog.Uint64("principal_id", uint64(pid)),
			slog.String("origin", origin),
			slog.String("err", fetchErr.Error()),
		)
		statusForErr(w, fetchErr)
		return
	}
	// resp body has been drained into body or aborted; we still own
	// the headers.
	if resp.StatusCode >= 400 {
		// Verbatim upstream status, no caching, no retry on 4xx
		// (REQ-SEND-76). 5xx after retry exhaustion lands here too.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.WriteString(w, http.StatusText(resp.StatusCode))
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if !isImageContentType(contentType) {
		http.Error(w, "upstream content-type is not image/*", http.StatusUnsupportedMediaType)
		return
	}

	now := s.clk.Now()
	ttl := s.computeTTL(resp.Header.Get("Cache-Control"))
	entry := cacheEntry{
		key:          key,
		bytes:        body,
		contentType:  contentType,
		etag:         resp.Header.Get("ETag"),
		lastModified: parseHTTPDate(resp.Header.Get("Last-Modified")),
		expiresAt:    now.Add(ttl),
	}
	if ttl > 0 {
		s.cache.put(entry)
	}
	s.writeImage(w, entry, r.Method)
}

// fetchWithRetry implements REQ-SEND-76: one retry on 5xx or network
// error, no retry on 4xx, with a 1 s gap between attempts. Errors that
// are deterministic (body-too-large, redirect-policy violations) are
// not retried — repeating the request will produce the same outcome.
func (s *Server) fetchWithRetry(ctx context.Context, target string) (*http.Response, []byte, error) {
	resp, body, err := s.fetchOnce(ctx, target)
	if err == nil && resp.StatusCode < 500 {
		return resp, body, nil
	}
	if err != nil && !isRetryable(err) {
		return nil, nil, err
	}
	// Either a transient network error or a 5xx. Wait the configured
	// gap then retry once.
	select {
	case <-ctx.Done():
		if err != nil {
			return nil, nil, err
		}
		return resp, body, nil
	case <-s.clk.After(retryDelay):
	}
	resp2, body2, err2 := s.fetchOnce(ctx, target)
	if err2 != nil {
		// Both attempts failed: surface the second error.
		return nil, nil, err2
	}
	return resp2, body2, nil
}

// isRetryable returns true for errors that are worth a second attempt:
// transport-level network failures and timeouts. Deterministic errors
// (body cap exceeded, redirect-policy violations) return false.
func isRetryable(err error) bool {
	if errors.Is(err, errBodyTooLarge) {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, errTooManyRedirects) || errors.Is(ue.Err, errNonHTTPSRedirect) {
			return false
		}
	}
	return true
}

// fetchOnce performs one upstream GET and reads up to s.maxBytes+1 of
// the body. Returning maxBytes+1 bytes signals "exceeded cap": the
// caller maps that to 413. Headers we strip per REQ-SEND-72: Cookie
// is never set (the handler ignores the inbound cookie), and we leave
// Referer unset.
func (s *Server) fetchOnce(ctx context.Context, target string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "image/*")
	// Be explicit: do not propagate any header that would identify the
	// suite user. We did not copy from the inbound request, so this is
	// belt-and-braces against a future refactor.
	req.Header.Del("Cookie")
	req.Header.Del("Referer")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	// Read up to maxBytes+1 to detect overflow without buffering the
	// whole oversize response.
	limited := io.LimitReader(resp.Body, s.maxBytes+1)
	body, readErr := io.ReadAll(limited)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if closeErr != nil {
		// A failed Close after a successful read is unusual but
		// surface it so the retry path can do its thing.
		return nil, nil, closeErr
	}
	if int64(len(body)) > s.maxBytes {
		return resp, nil, errBodyTooLarge
	}
	return resp, body, nil
}

// statusForErr maps a fetch-time error to an HTTP response. Body-too-
// large yields 413; everything else (network, redirect-cap, dial
// timeout, total-deadline expiry) yields 502.
func statusForErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		http.Error(w, "upstream response exceeded size cap", http.StatusRequestEntityTooLarge)
		return
	}
	// url.Error wraps redirect-policy errors; unwrap to detect them.
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, errTooManyRedirects) || errors.Is(ue.Err, errNonHTTPSRedirect) {
			http.Error(w, "upstream redirect rejected", http.StatusBadGateway)
			return
		}
	}
	http.Error(w, "upstream fetch failed", http.StatusBadGateway)
}

// writeImage emits the cached entry to the client. We force a
// minimum-information response: the cache headers tell the user-agent
// it MAY cache for ttl seconds (REQ-SEND-75), nosniff prevents the
// browser from re-classifying the content if upstream lied, and the
// content-type is echoed verbatim from upstream.
func (s *Server) writeImage(w http.ResponseWriter, e cacheEntry, method string) {
	ttl := e.expiresAt.Sub(s.clk.Now())
	if ttl < 0 {
		ttl = 0
	}
	w.Header().Set("Content-Type", e.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", int(ttl.Seconds())))
	if e.etag != "" {
		w.Header().Set("ETag", e.etag)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(e.bytes)))
	w.WriteHeader(http.StatusOK)
	if method == http.MethodHead {
		return
	}
	_, _ = w.Write(e.bytes)
}

// isImageContentType returns true iff the content-type's primary type
// is "image". The upstream may quote parameters (e.g.
// "image/jpeg; charset=binary"); we match the prefix only.
func isImageContentType(ct string) bool {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if ct == "" {
		return false
	}
	// Cut at ';' to drop parameters before the primary/sub split.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return strings.HasPrefix(ct, "image/") && len(ct) > len("image/")
}

// computeTTL derives the cache TTL from upstream Cache-Control. The
// rule per REQ-SEND-75: honour upstream max-age, capped at
// CacheMaxAge. Upstream no-store / private / no-cache disable caching.
func (s *Server) computeTTL(cacheControl string) time.Duration {
	cc := strings.ToLower(cacheControl)
	if cc == "" {
		// No directive: take the full cap.
		return s.cacheMaxAge
	}
	if containsDirective(cc, "no-store") || containsDirective(cc, "no-cache") || containsDirective(cc, "private") {
		return 0
	}
	for _, p := range strings.Split(cc, ",") {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "max-age=") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(p, "max-age="))
		if err != nil || n < 0 {
			continue
		}
		d := time.Duration(n) * time.Second
		if d > s.cacheMaxAge {
			d = s.cacheMaxAge
		}
		return d
	}
	return s.cacheMaxAge
}

// containsDirective looks for the given Cache-Control directive as a
// standalone token (so "no-cache-foo" does not match "no-cache").
func containsDirective(cc, want string) bool {
	for _, p := range strings.Split(cc, ",") {
		if strings.TrimSpace(p) == want {
			return true
		}
	}
	return false
}

// parseHTTPDate is a tolerant http.TimeFormat parser. Returns the
// zero time if the header is missing or unparseable; the cache stores
// the value verbatim and never relies on it for freshness.
func parseHTTPDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{http.TimeFormat, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
