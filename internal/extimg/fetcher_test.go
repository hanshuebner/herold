package extimg

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestFetcher_MalformedImageContentType covers issue #324: a server
// that answers a malformed "image/" media type (empty subtype, with
// or without stray parameters, or mixed case) must not have that
// header written verbatim onto the fetch result -- the fetcher falls
// back to sniffing the body's magic bytes, exactly as it already does
// for a missing header.
func TestFetcher_MalformedImageContentType(t *testing.T) {
	cases := []struct {
		name string
		ct   string
	}{
		{"empty subtype", "image/"},
		{"empty subtype with params", "image/;charset=x"},
		{"uppercase empty subtype", "IMAGE/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", c.ct)
				w.Write(pngBytes())
			}))
			defer srv.Close()

			cfg := testFetcherCfg(t, srv)
			f := NewFetcher(cfg)
			r := f.Fetch(context.Background(), srv.URL+"/x")
			if r.Outcome != FetchOK {
				t.Fatalf("Outcome=%s reason=%q, want %s", r.Outcome, r.Reason, FetchOK)
			}
			if r.ContentType != "image/png" {
				t.Fatalf("ContentType=%q, want image/png (sniffed from PNG bytes)", r.ContentType)
			}
		})
	}
}

// TestFetcher_ValidImageContentType_Passthrough is the counterpart to
// TestFetcher_MalformedImageContentType: a well-formed image/* header
// is trusted as-is (case-normalised) rather than overridden by the
// sniff, even for a format sniffImage does not recognise.
func TestFetcher_ValidImageContentType_Passthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		// RIFF/WEBP magic so looksLikeImage's header-trust path is the
		// only thing under test, not an accidental sniff match on
		// unrelated bytes.
		w.Write([]byte{
			'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P',
			'V', 'P', '8', ' ',
		})
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/x.webp")
	if r.Outcome != FetchOK {
		t.Fatalf("Outcome=%s reason=%q, want %s", r.Outcome, r.Reason, FetchOK)
	}
	if r.ContentType != "image/webp" {
		t.Fatalf("ContentType=%q, want image/webp", r.ContentType)
	}
}

// TestFetcher_TrustsExtraCACertPEM covers issue #443: NewFetcher's
// transport must trust cfg.ExtraCACertPEM the same way
// NewGuardedClient already does for Email/unsubscribe's outbound POST
// (issue #412), so a dev/test origin's self-signed certificate is
// trusted for the actual image fetch. Before this fix NewFetcher's
// transport never consulted ExtraCACertPEM at all; every fetch against
// a self-signed HTTPS origin failed with a TLS verification error
// regardless of the configured extra_ca_file.
func TestFetcher_TrustsExtraCACertPEM(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes())
	}))
	defer srv.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	cfg := Config{
		Mode:                ModeInternalize,
		MaxPerImageBytes:    5 * 1024 * 1024,
		MaxPerMessageImages: 100,
		MaxPerMessageBytes:  50 * 1024 * 1024,
		ConcurrentFetches:   4,
		RequireHTTPS:        true,
		AllowPrivate:        true, // allow 127.0.0.1 to reach httptest
		HostHeader:          "test.local",
		ExtraCACertPEM:      certPEM,
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	var port int
	fmt.Sscanf(u.Port(), "%d", &port)
	cfg.AllowedPorts = []int{port}
	cfg.resolveOptional()

	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/image.png")
	if r.Outcome != FetchOK {
		t.Fatalf("Outcome=%s reason=%q, want %s (self-signed cert should be trusted via ExtraCACertPEM)", r.Outcome, r.Reason, FetchOK)
	}
}

// TestLooksLikeImage_MalformedHeaderRejectsNonImageBody covers the
// tightened half of issue #324's fix: a malformed "image/*" header no
// longer waves non-image bytes through unchecked. The prefix alone is
// not proof; the body must still sniff as an image.
func TestLooksLikeImage_MalformedHeaderRejectsNonImageBody(t *testing.T) {
	if looksLikeImage("image/", []byte("<html>not an image</html>")) {
		t.Fatalf("looksLikeImage(%q, html-bytes) = true, want false", "image/")
	}
}

// TestCanonicalImageContentType_Table pins the fallback chain
// directly (issue #324's Acceptance criteria): a malformed header
// falls back to the sniffed type for image bytes, and a well-formed
// header always wins outright.
func TestCanonicalImageContentType_Table(t *testing.T) {
	png := pngBytes()
	cases := []struct {
		name string
		ct   string
		body []byte
		want string
	}{
		{"empty subtype", "image/", png, "image/png"},
		{"empty subtype with charset param", "image/;charset=x", png, "image/png"},
		{"uppercase empty subtype", "IMAGE/", png, "image/png"},
		{"valid webp passthrough", "image/webp", png, "image/webp"},
		{"missing header sniffs", "", png, "image/png"},
		{"malformed header, unsniffable body falls back to octet-stream", "image/", []byte("not an image"), "application/octet-stream"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := canonicalImageContentType(c.ct, c.body)
			if got != c.want {
				t.Errorf("canonicalImageContentType(%q, ...) = %q, want %q", c.ct, got, c.want)
			}
		})
	}
}
