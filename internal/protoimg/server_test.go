package protoimg_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoimg"
	"github.com/hanshuebner/herold/internal/store"
)

// pidHeader is the inbound test header that selects a "principal" via
// the test session resolver. Tests set "X-Test-PID: 1" to authenticate;
// absence yields anonymous.
const pidHeader = "X-Test-PID"

func testResolver(r *http.Request) (store.PrincipalID, bool) {
	v := r.Header.Get(pidHeader)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return store.PrincipalID(n), true
}

// upstreamSpy lets a test capture the inbound headers / observe how
// many times the upstream was called.
type upstreamSpy struct {
	mu      sync.Mutex
	calls   int
	lastHdr http.Header
	handler http.HandlerFunc
}

func newSpy(h http.HandlerFunc) *upstreamSpy {
	return &upstreamSpy{handler: h}
}

func (s *upstreamSpy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls++
	// Clone the header map; the underlying values may be mutated by
	// the test server between calls.
	s.lastHdr = r.Header.Clone()
	s.mu.Unlock()
	s.handler(w, r)
}

func (s *upstreamSpy) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *upstreamSpy) LastHeaders() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastHdr
}

// proxyHarness wires a protoimg.Server against a TLS httptest upstream.
type proxyHarness struct {
	t        *testing.T
	srv      *protoimg.Server
	clk      *clock.FakeClock
	upstream *httptest.Server
	spy      *upstreamSpy
	handler  http.Handler
}

func newProxyHarness(t *testing.T, h http.HandlerFunc, opts ...func(*protoimg.Options)) *proxyHarness {
	t.Helper()
	spy := newSpy(h)
	upstream := httptest.NewTLSServer(spy)
	t.Cleanup(upstream.Close)

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Trust the httptest TLS cert by passing the test client's cert
	// pool through into a custom http.Client. We must not use
	// InsecureSkipVerify: the redirect-policy hook still expects the
	// chain to validate.
	tr := &http.Transport{
		TLSClientConfig: upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}
	httpClient := &http.Client{Transport: tr}

	o := protoimg.Options{
		Clock:               clk,
		HTTPClient:          httpClient,
		MaxBytes:            1024 * 1024,
		ConnectTimeout:      2 * time.Second,
		TotalTimeout:        5 * time.Second,
		MaxRedirects:        3,
		CacheMaxBytes:       4 * 1024 * 1024,
		CacheMaxEntries:     16,
		CacheMaxAge:         24 * time.Hour,
		PerUserPerMin:       200,
		PerUserOriginPerMin: 10,
		PerUserConcurrent:   8,
		SessionResolver:     testResolver,
	}
	for _, fn := range opts {
		fn(&o)
	}
	srv := protoimg.New(o)
	return &proxyHarness{
		t:        t,
		srv:      srv,
		clk:      clk,
		upstream: upstream,
		spy:      spy,
		handler:  srv.Handler(),
	}
}

// do issues a GET /proxy/image?url=upstreamURL request through the
// handler with the supplied principal id. Empty pid yields an
// anonymous request.
func (h *proxyHarness) do(pid store.PrincipalID, upstreamURL string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/proxy/image?url="+url.QueryEscape(upstreamURL), nil)
	if pid != 0 {
		req.Header.Set(pidHeader, strconv.FormatUint(uint64(pid), 10))
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *proxyHarness) doRaw(pid store.PrincipalID, rawURLValue string) *httptest.ResponseRecorder {
	// Build the URL with rawURLValue placed verbatim — used by the
	// "url too long" / "missing url" tests.
	target := "/proxy/image"
	if rawURLValue != "" {
		target += "?url=" + rawURLValue
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if pid != 0 {
		req.Header.Set(pidHeader, strconv.FormatUint(uint64(pid), 10))
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// pngBody is a tiny valid-looking image body. The proxy does not
// validate image bytes; it only checks the Content-Type.
var pngBody = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func writePNG(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(pngBody)))
	_, _ = w.Write(pngBody)
}

func TestProxy_Anonymous_401(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) { writePNG(w) })
	rec := h.do(0, h.upstream.URL+"/img.png")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
}

func TestProxy_BadURL_400(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) { writePNG(w) })

	cases := []struct {
		name string
		url  string
		raw  bool
	}{
		{"missing", "", true},
		{"http_scheme", "http://example.com/x.png", false},
		{"too_long", "https://example.com/" + strings.Repeat("a", protoimg.MaxURLLength), false},
		{"missing_host", "https:///x.png", false},
		{"userinfo", "https://user:pass@example.com/x.png", false},
		{"upper_scheme", "HTTP://example.com/x.png", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if tc.raw {
				rec = h.doRaw(1, "")
			} else {
				rec = h.do(1, tc.url)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want 400 (body=%q)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestProxy_NonImageContentType_415(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>nope</html>")
	})
	rec := h.do(1, h.upstream.URL+"/x")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status: got %d, want 415", rec.Code)
	}
}

func TestProxy_LargeUpstream_413(t *testing.T) {
	// Configure a tiny cap. The upstream advertises and writes more
	// than the cap; the proxy must abort and 413.
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 8*1024)
		for i := range body {
			body[i] = 0xff
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}, func(o *protoimg.Options) {
		o.MaxBytes = 1024 // smaller than the 8 KiB upstream
	})
	rec := h.do(1, h.upstream.URL+"/big.png")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want 413 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestProxy_HappyPath_ServesBytes(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) { writePNG(w) })
	rec := h.do(1, h.upstream.URL+"/img.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "image/png"; got != want {
		t.Errorf("content-type: got %q, want %q", got, want)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff missing: got %q", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Cache-Control"), "private,") {
		t.Errorf("cache-control: got %q, want private prefix", rec.Header().Get("Cache-Control"))
	}
	if rec.Body.Len() != len(pngBody) {
		t.Errorf("body bytes: got %d, want %d", rec.Body.Len(), len(pngBody))
	}
}

func TestProxy_RedirectFollowedUp3Hops(t *testing.T) {
	// Three intermediate redirects, then the image. The redirect-cap
	// is exclusive of the final fetch (Go counts the via stack), so
	// 3 hops is the budget.
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/b", http.StatusFound)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/c", http.StatusFound)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/d", http.StatusFound)
	})
	mux.HandleFunc("/d", func(w http.ResponseWriter, r *http.Request) {
		writePNG(w)
	})
	upstream := httptest.NewTLSServer(mux)
	defer upstream.Close()
	base = upstream.URL

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}}
	srv := protoimg.New(protoimg.Options{
		Clock:           clk,
		HTTPClient:      httpClient,
		MaxBytes:        1024,
		MaxRedirects:    3,
		TotalTimeout:    5 * time.Second,
		SessionResolver: testResolver,
	})
	req := httptest.NewRequest(http.MethodGet, "/proxy/image?url="+url.QueryEscape(base+"/a"), nil)
	req.Header.Set(pidHeader, "1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestProxy_RedirectBeyondLimit_502(t *testing.T) {
	// Four redirects with MaxRedirects=3 — the fourth must be denied
	// and the proxy must surface 502.
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/b", http.StatusFound)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/c", http.StatusFound)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/d", http.StatusFound)
	})
	mux.HandleFunc("/d", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+"/e", http.StatusFound)
	})
	mux.HandleFunc("/e", func(w http.ResponseWriter, r *http.Request) {
		writePNG(w)
	})
	upstream := httptest.NewTLSServer(mux)
	defer upstream.Close()
	base = upstream.URL

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}}
	srv := protoimg.New(protoimg.Options{
		Clock:           clk,
		HTTPClient:      httpClient,
		MaxBytes:        1024,
		MaxRedirects:    3,
		TotalTimeout:    5 * time.Second,
		SessionResolver: testResolver,
	})
	req := httptest.NewRequest(http.MethodGet, "/proxy/image?url="+url.QueryEscape(base+"/a"), nil)
	req.Header.Set(pidHeader, "1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", rec.Code)
	}
}

func TestProxy_HTTPRedirect_RejectsNonHTTPS(t *testing.T) {
	// httptest TLS upstream redirects to a plain http target. The
	// redirect predicate must abort and the proxy must 502.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePNG(w)
	}))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/x", http.StatusFound)
	}))
	defer tls.Close()

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: tls.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}}
	srv := protoimg.New(protoimg.Options{
		Clock:           clk,
		HTTPClient:      httpClient,
		MaxBytes:        1024,
		TotalTimeout:    5 * time.Second,
		SessionResolver: testResolver,
	})
	req := httptest.NewRequest(http.MethodGet, "/proxy/image?url="+url.QueryEscape(tls.URL+"/r"), nil)
	req.Header.Set(pidHeader, "1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestProxy_NoCookieNoReferer_SentUpstream(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) { writePNG(w) })
	// Build a request that itself carries a Cookie / Referer; the
	// proxy must not propagate either to the upstream.
	req := httptest.NewRequest(http.MethodGet, "/proxy/image?url="+url.QueryEscape(h.upstream.URL+"/x.png"), nil)
	req.Header.Set(pidHeader, "1")
	req.Header.Set("Cookie", "secret=do-not-leak")
	req.Header.Set("Referer", "https://internal/private")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	hdr := h.spy.LastHeaders()
	if got := hdr.Get("Cookie"); got != "" {
		t.Errorf("upstream Cookie: got %q, want empty", got)
	}
	if got := hdr.Get("Referer"); got != "" {
		t.Errorf("upstream Referer: got %q, want empty", got)
	}
	if got, want := hdr.Get("User-Agent"), protoimg.DefaultUserAgent; got != want {
		t.Errorf("upstream User-Agent: got %q, want %q", got, want)
	}
}

func TestProxy_CacheHit_NoUpstreamCall_OnSecond(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		writePNG(w)
	})
	upstreamURL := h.upstream.URL + "/cache.png"

	rec := h.do(1, upstreamURL)
	if rec.Code != http.StatusOK {
		t.Fatalf("first: got %d, want 200", rec.Code)
	}
	if h.spy.Calls() != 1 {
		t.Fatalf("first call count: got %d, want 1", h.spy.Calls())
	}

	// Second request: cache must serve without a second upstream hit.
	rec2 := h.do(1, upstreamURL)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second: got %d, want 200", rec2.Code)
	}
	if h.spy.Calls() != 1 {
		t.Fatalf("second call count: got %d, want 1 (cache miss)", h.spy.Calls())
	}
}

func TestProxy_CacheRespectsCacheControl_NoStore(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writePNG(w)
	})
	upstreamURL := h.upstream.URL + "/no-store.png"

	rec := h.do(1, upstreamURL)
	if rec.Code != http.StatusOK {
		t.Fatalf("first: got %d, want 200", rec.Code)
	}
	rec2 := h.do(1, upstreamURL)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second: got %d, want 200", rec2.Code)
	}
	if h.spy.Calls() != 2 {
		t.Fatalf("expected two upstream calls (no caching), got %d", h.spy.Calls())
	}
}

func TestProxy_RateLimit_PerUser_429(t *testing.T) {
	// Per-user budget = 2; third request inside the window is denied.
	// Use distinct URLs to avoid cache hits short-circuiting the
	// limiter; bypass per-origin throttle by configuring a high
	// per-origin cap.
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writePNG(w)
	}, func(o *protoimg.Options) {
		o.PerUserPerMin = 2
		o.PerUserOriginPerMin = 100
	})

	rec1 := h.do(1, h.upstream.URL+"/a.png")
	if rec1.Code != http.StatusOK {
		t.Fatalf("rec1: got %d", rec1.Code)
	}
	rec2 := h.do(1, h.upstream.URL+"/b.png")
	if rec2.Code != http.StatusOK {
		t.Fatalf("rec2: got %d", rec2.Code)
	}
	rec3 := h.do(1, h.upstream.URL+"/c.png")
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("rec3: got %d, want 429", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") == "" {
		t.Errorf("Retry-After header missing")
	}
}

func TestProxy_RateLimit_PerUserOrigin_429(t *testing.T) {
	// Per-origin budget = 1. A second hit to the SAME origin is
	// denied; a hit to a DIFFERENT origin still goes through.
	upA := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writePNG(w)
	}))
	defer upA.Close()
	upB := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writePNG(w)
	}))
	defer upB.Close()

	// Build a transport that trusts both upstreams. Skip verification
	// so a single transport can talk to both httptest TLS servers
	// (each runs with its own self-signed cert); both upstreams are
	// in-process and the test does not exercise certificate trust.
	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	srv := protoimg.New(protoimg.Options{
		Clock:               clk,
		HTTPClient:          httpClient,
		MaxBytes:            1024,
		TotalTimeout:        5 * time.Second,
		PerUserPerMin:       100,
		PerUserOriginPerMin: 1,
		PerUserConcurrent:   8,
		SessionResolver:     testResolver,
	})

	doReq := func(rawURL string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/proxy/image?url="+url.QueryEscape(rawURL), nil)
		req.Header.Set(pidHeader, "1")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := doReq(upA.URL + "/1"); rec.Code != http.StatusOK {
		t.Fatalf("upA #1: got %d", rec.Code)
	}
	if rec := doReq(upA.URL + "/2"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("upA #2: got %d, want 429", rec.Code)
	}
	if rec := doReq(upB.URL + "/1"); rec.Code != http.StatusOK {
		t.Fatalf("upB #1: got %d (different origin must still be allowed)", rec.Code)
	}
}

func TestProxy_RateLimit_Concurrent_BlocksThenServes(t *testing.T) {
	// 9 concurrent requests with a per-user concurrency cap of 8 +
	// fast retry. Configure the upstream to block until tests release
	// it; assert exactly one of the requests is rejected with 429
	// while the others proceed.
	release := make(chan struct{})
	var inflight int32
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&inflight, 1)
		<-release
		w.Header().Set("Cache-Control", "no-store")
		writePNG(w)
	}, func(o *protoimg.Options) {
		o.PerUserConcurrent = 2
		o.PerUserPerMin = 100
		o.PerUserOriginPerMin = 100
	})

	codes := make(chan int, 3)
	for i := 0; i < 3; i++ {
		i := i
		go func() {
			rec := h.do(1, fmt.Sprintf("%s/c%d.png", h.upstream.URL, i))
			codes <- rec.Code
		}()
	}
	// Wait until two requests are inside the upstream handler.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&inflight) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for 2 in-flight requests")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The third request, if it issued before the others release,
	// will hit the concurrency limit. Give it a moment to do so.
	time.Sleep(50 * time.Millisecond)

	// Release in-flight requests.
	close(release)

	got := make(map[int]int)
	for i := 0; i < 3; i++ {
		got[<-codes]++
	}
	if got[http.StatusTooManyRequests] < 1 {
		t.Fatalf("expected at least one 429 from concurrency cap; got distribution %v", got)
	}
	if got[http.StatusOK] < 2 {
		t.Fatalf("expected at least two 200s; got distribution %v", got)
	}
}

func TestProxy_5xxRetried_ThenSucceeds(t *testing.T) {
	// First call: 503; subsequent calls: 200. The proxy must retry
	// once and surface the second response.
	var calls int32
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writePNG(w)
	})
	// Drive the FakeClock past the 1 s retry gap. clk.Advance only
	// fires waiters that have already been registered, so a single
	// sleep+advance races the request goroutine: under CI contention
	// Advance can fire before the handler reaches s.clk.After, leaving
	// the retry waiter unfired. Tickle Advance in a loop until the
	// second upstream call lands.
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if atomic.LoadInt32(&calls) >= 2 {
					return
				}
				h.clk.Advance(time.Second)
			}
		}
	}()
	rec := h.do(1, h.upstream.URL+"/retry.png")
	close(stop)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (calls=%d)", rec.Code, atomic.LoadInt32(&calls))
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls: got %d, want 2", got)
	}
}

func TestProxy_5xxExhausted_502_AfterRetry(t *testing.T) {
	// Both attempts return 5xx; the proxy must surface a 5xx (the
	// upstream status, not 502, per "return upstream status verbatim").
	var calls int32
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	})
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if atomic.LoadInt32(&calls) >= 2 {
					return
				}
				h.clk.Advance(time.Second)
			}
		}
	}()
	rec := h.do(1, h.upstream.URL+"/x")
	close(stop)
	// Upstream verbatim: 502. The handler maps "5xx after retry" to
	// the upstream status code.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls: got %d, want 2 (one retry)", got)
	}
}

// TestProxy_SSRF_DefaultClient_BlocksPrivateIP verifies that when
// callers do NOT supply an HTTPClient (the production path), the
// internally-constructed transport refuses to dial loopback /
// link-local / private targets. We deliberately do not provide an
// HTTPClient so the New() constructor installs the netguard.ControlContext
// hook on the default transport.
func TestProxy_SSRF_DefaultClient_BlocksPrivateIP(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	srv := protoimg.New(protoimg.Options{
		Clock:           clk,
		MaxBytes:        1024,
		ConnectTimeout:  500 * time.Millisecond,
		TotalTimeout:    1 * time.Second,
		MaxRedirects:    3,
		SessionResolver: testResolver,
	})
	cases := []string{
		"https://127.0.0.1/img.png",
		"https://10.0.0.1/img.png",
		"https://169.254.169.254/latest/meta-data/",
		"https://192.168.1.1/x",
		"https://[::1]/x",
		"https://[fe80::1]/x",
		"https://[fc00::1]/x",
		"https://0.0.0.0/x",
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				"/proxy/image?url="+url.QueryEscape(target), nil)
			req.Header.Set(pidHeader, "1")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status: got %d, want 502 (body=%q)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestProxy_URL_Validate_RejectionMatrix exercises validateImageProxyURL
// via the public handler: each row trips a 400 with the matching
// reason class. Mirrors the production "rejected before dial" path.
func TestProxy_URL_Validate_RejectionMatrix(t *testing.T) {
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) { writePNG(w) })
	cases := []struct {
		name string
		url  string
	}{
		{"http", "http://example.com/x.png"},
		{"ftp", "ftp://example.com/x.png"},
		{"userinfo", "https://user:pass@example.com/x.png"},
		{"userinfo_no_pass", "https://user@example.com/x.png"},
		{"empty_host", "https:///x.png"},
		{"upper_scheme_http", "HTTP://example.com/x.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(1, tc.url)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want 400 (body=%q)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestProxy_4xx_NoRetry_VerbatimUpstreamStatus(t *testing.T) {
	var calls int32
	h := newProxyHarness(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "nope", http.StatusNotFound)
	})
	rec := h.do(1, h.upstream.URL+"/missing.png")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls: got %d, want 1 (no retry on 4xx)", got)
	}
}
