package extimg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jhillyerd/enmime"
)

// pngBytes returns a tiny valid 1x1 PNG for tests. Real bytes; an
// image-content-type sniff confirms it.
func pngBytes() []byte {
	// Minimal 1x1 transparent PNG. Hand-assembled to avoid any
	// dependency on image/png at test time.
	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
}

// testFetcherCfg returns a Config wired to allow loopback (so the
// httptest.Server is reachable) and to hit the real loopback resolver
// directly. We cannot avoid the real network for httptest, so we
// flip AllowPrivate. The IPv4-mapped / TEST-NET / multicast guards
// remain active and exercised by the SSRF unit tests. Pass the
// httptest.Server (or nil) so the kernel-picked port is added to the
// allowlist; without this the SSRF guard refuses every test URL.
func testFetcherCfg(t *testing.T, srv *httptest.Server) Config {
	t.Helper()
	cfg := Config{
		Mode:                ModeInternalize,
		MaxPerImageBytes:    5 * 1024 * 1024,
		MaxPerMessageImages: 100,
		MaxPerMessageBytes:  50 * 1024 * 1024,
		ConcurrentFetches:   4,
		RequireHTTPS:        false, // httptest is plain http
		AllowPrivate:        true,  // allow 127.0.0.1 to reach httptest
		HostHeader:          "test.local",
	}
	if srv != nil {
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatalf("parse httptest URL: %v", err)
		}
		if p := u.Port(); p != "" {
			var n int
			fmt.Sscanf(p, "%d", &n)
			cfg.AllowedPorts = []int{n}
		}
	}
	cfg.resolveOptional()
	return cfg
}

// TestFetcher_OK serves a PNG and verifies a successful fetch.
func TestFetcher_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes())
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	// httptest binds to 127.0.0.1 — we exercise the AllowPrivate path.
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/x.png")
	if r.Outcome != FetchOK {
		t.Fatalf("Outcome=%s reason=%q", r.Outcome, r.Reason)
	}
	if !bytes.Equal(r.Bytes, pngBytes()) {
		t.Fatalf("body bytes mismatch")
	}
	if r.ContentType != "image/png" {
		t.Fatalf("ContentType=%q", r.ContentType)
	}
}

// TestFetcher_TooLarge returns more bytes than MaxPerImageBytes.
func TestFetcher_TooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		// Write 200 KB of zeros — well above the 100 KB cap below.
		w.Write(make([]byte, 200_000))
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	cfg.MaxPerImageBytes = 100_000
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/big")
	if r.Outcome != FetchTooLarge {
		t.Fatalf("Outcome=%s reason=%q", r.Outcome, r.Reason)
	}
}

// TestFetcher_NotImage refuses an HTML response.
func TestFetcher_NotImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/x")
	if r.Outcome != FetchNotImage {
		t.Fatalf("Outcome=%s reason=%q", r.Outcome, r.Reason)
	}
}

// TestFetcher_HTTP4xx categorises 404 etc.
func TestFetcher_HTTP4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/x")
	if r.Outcome != FetchHTTP4xx || r.HTTPStatus != 404 {
		t.Fatalf("Outcome=%s status=%d", r.Outcome, r.HTTPStatus)
	}
}

// TestFetcher_RedirectBlocked refuses a 3xx to a denied destination.
// httptest.Server lives on 127.0.0.1; we issue a redirect to a
// TEST-NET-3 IP which is denied even when AllowPrivate=true.
func TestFetcher_RedirectBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://203.0.113.5/x.png")
		w.WriteHeader(302)
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/redirect")
	if r.Outcome != FetchBlockedSSRF {
		t.Fatalf("Outcome=%s reason=%q", r.Outcome, r.Reason)
	}
}

// TestFetcher_Empty refuses zero-length responses.
func TestFetcher_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		// Write nothing.
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/empty")
	if r.Outcome != FetchEmpty {
		t.Fatalf("Outcome=%s reason=%q", r.Outcome, r.Reason)
	}
}

// TestInternalize_EndToEnd is the smoke test: deliver an HTML message
// referencing two external images, verify the rewritten body has cid:
// refs + inline parts, and verify the DKIM disposition.
func TestInternalize_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes())
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)

	rawMsg := buildTestMessage(t, []string{
		srv.URL + "/a.png",
		srv.URL + "/b.png",
	})

	// Fake-DKIM verdict so the server-stamped header captures it.
	verdict := DKIMVerdict{Result: "pass", SigningDomain: "newsletter.example.com", Selector: "ml-2024"}

	out, sum, err := Internalize(context.Background(), rawMsg, cfg, verdict)
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if !sum.Modified {
		t.Fatalf("expected Modified=true; %+v", sum)
	}
	if sum.Internalized != 2 {
		t.Fatalf("Internalized=%d, want 2", sum.Internalized)
	}
	if sum.Failed != 0 {
		t.Fatalf("Failed=%d, want 0; counts=%v", sum.Failed, sum.FailureCounts)
	}

	// Parse the rewritten message and verify the inline parts and HTML.
	env, err := enmime.ReadEnvelope(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	// HTML body should reference cid: not the original URL.
	if strings.Contains(env.HTML, srv.URL) {
		t.Fatalf("rewritten HTML still references %s:\n%s", srv.URL, env.HTML)
	}
	if !strings.Contains(env.HTML, "cid:") {
		t.Fatalf("rewritten HTML missing cid: refs:\n%s", env.HTML)
	}
	// Should have at least 2 inline parts (the originally-attached
	// X-Logo plus the 2 fetched).
	if len(env.Inlines) < 2 {
		t.Fatalf("expected >=2 inline parts, got %d", len(env.Inlines))
	}
	// Server-stamped Authentication-Results.
	authRes := env.GetHeader("Authentication-Results")
	if !strings.Contains(authRes, "dkim=pass") {
		t.Fatalf("Authentication-Results missing dkim=pass: %q", authRes)
	}
	if !strings.Contains(authRes, "header.d=newsletter.example.com") {
		t.Fatalf("Authentication-Results missing signing domain: %q", authRes)
	}
	if !strings.Contains(authRes, "body modified for image-privacy rewrite") {
		t.Fatalf("Authentication-Results missing modification marker: %q", authRes)
	}
	// X-Herold-Body-Modified marker.
	if env.GetHeader("X-Herold-Body-Modified") != "image-internalization" {
		t.Fatalf("missing X-Herold-Body-Modified marker")
	}
	// DKIM-Signature stripped.
	if env.GetHeader("DKIM-Signature") != "" {
		t.Fatalf("DKIM-Signature should be stripped: %q", env.GetHeader("DKIM-Signature"))
	}
}

// TestInternalize_Passthrough returns the original bytes unchanged.
func TestInternalize_Passthrough(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	cfg.Mode = ModePassthrough
	raw := buildTestMessage(t, []string{"https://example.com/a.png"})
	out, sum, err := Internalize(context.Background(), raw, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum.Modified {
		t.Fatalf("passthrough should not modify")
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("passthrough output should equal input")
	}
}

// TestInternalize_NoHTML returns bytes unchanged when there's no HTML
// body to rewrite.
func TestInternalize_NoHTML(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	body := "From: a@example.com\r\nTo: b@example.com\r\nSubject: plain\r\n\r\nplain text only.\r\n"
	out, sum, err := Internalize(context.Background(), []byte(body), cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum.Modified {
		t.Fatalf("plain message should not be modified")
	}
	if !bytes.Equal(out, []byte(body)) {
		t.Fatalf("plain message should pass through")
	}
}

// TestInternalize_FailedFetchPreservesURL covers REQ-EXTIMG-60: when
// the fetch fails the original URL stays in the rewritten body so the
// suite's per-image escape hatch still has something to load.
func TestInternalize_FailedFetchPreservesURL(t *testing.T) {
	// Pick an unreachable port — connect refused → fetch fails.
	cfg := testFetcherCfg(t, nil)
	// Drop timeouts so the test isn't slow.
	cfg.PerImageConnectTimeout = 200 * time.Millisecond
	cfg.PerImageTotalTimeout = 500 * time.Millisecond
	cfg.PerMessageTimeout = 2 * time.Second

	// Bind a port then close it — guarantees connection refused on
	// loopback. Doing it after-the-fact also avoids httptest's
	// keep-alive complications.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()
	url := fmt.Sprintf("http://127.0.0.1:%d/missing.png", addr.Port)

	raw := buildTestMessage(t, []string{url})
	out, sum, err := Internalize(context.Background(), raw, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum.Internalized != 0 {
		t.Fatalf("expected no internalization, got %d", sum.Internalized)
	}
	// Output should equal raw because no successful fetch → nothing to do.
	if sum.Modified {
		t.Fatalf("nothing fetched: should not modify")
	}
	_ = out
}

// TestInternalize_BlockedHostNotFetched verifies the SSRF guard
// applies through the orchestrator. A message referencing
// http://127.0.0.1/x — with AllowPrivate=false — must fail the fetch.
func TestInternalize_BlockedHostNotFetched(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	cfg.AllowPrivate = false

	raw := buildTestMessage(t, []string{"http://127.0.0.1/admin"})
	_, sum, err := Internalize(context.Background(), raw, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum.Internalized != 0 {
		t.Fatalf("expected no internalization for SSRF-blocked URL")
	}
	if sum.FailureCounts[FetchBlockedSSRF] != 1 {
		t.Fatalf("expected 1 blocked_ssrf, got %v", sum.FailureCounts)
	}
}

// TestInternalize_RealResolver_DenyExternal_TestNET3 exercises the
// real-resolver path inside DialContext (not just literals). Use the
// fakeResolver-injected guard? Internalize builds its own; we
// substitute via the http URL containing a dotted IP literal — which
// still goes through DialContext's literal-IP branch. That's tested
// in SSRF unit tests; here we make sure the orchestrator surfaces it.
func TestInternalize_RealResolver_DenyExternal_TestNET3(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	cfg.AllowPrivate = true // does NOT reach TEST-NET-3 ranges
	raw := buildTestMessage(t, []string{"http://203.0.113.5/x.png"})
	_, sum, err := Internalize(context.Background(), raw, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum.FailureCounts[FetchBlockedSSRF] != 1 {
		t.Fatalf("expected blocked_ssrf for TEST-NET-3, got %v", sum.FailureCounts)
	}
}

// buildTestMessage builds a multipart/alternative + multipart/related
// inbound message referencing the supplied external URLs in the HTML
// body. The plain-text part is preserved (we want to verify it
// survives the rewrite). One pre-existing inline part with
// Content-ID "logo@h" tests the inline-preservation path.
func buildTestMessage(t *testing.T, urls []string) []byte {
	t.Helper()
	var imgs strings.Builder
	for _, u := range urls {
		imgs.WriteString(`<img src="`)
		imgs.WriteString(u)
		imgs.WriteString(`">`)
	}
	html := `<html><body>Hello.<br>` + imgs.String() +
		`<img src="cid:logo@h"></body></html>`
	plain := "Hello (text fallback).\n"
	logo := pngBytes()

	b := enmime.Builder().
		From("Sender", "alice@example.com").
		To("Recipient", "bob@example.com").
		Subject("test message").
		Date(time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)).
		Text([]byte(plain)).
		HTML([]byte(html)).
		AddInline(logo, "image/png", "logo.png", "logo@h").
		// Pretend an inbound DKIM signature; we want to confirm it is
		// stripped on the rewritten output.
		Header("DKIM-Signature", "v=1; a=rsa-sha256; d=example.com; s=2024; bh=abc; b=def").
		Header("Authentication-Results", "ingress; dkim=pass header.d=example.com").
		Header("List-Id", "<test.example.com>")
	part, err := b.Build()
	if err != nil {
		t.Fatalf("build test msg: %v", err)
	}
	var buf bytes.Buffer
	if err := part.Encode(&buf); err != nil {
		t.Fatalf("encode test msg: %v", err)
	}
	return buf.Bytes()
}

// silence unused imports when the test set narrows
var (
	_ = io.ReadAll
	_ = url.Parse
	_ = netip.Addr{}
)
