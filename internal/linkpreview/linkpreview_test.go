package linkpreview_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/linkpreview"
	"github.com/hanshuebner/herold/internal/netguard"
)

// permissiveFetcher returns a Fetcher with a custom transport whose
// dialer does NOT install netguard.ControlContext. httptest binds to
// 127.0.0.1, which the production guard would refuse; the tests need
// to actually hit the loopback test server, so we override the
// transport for them. The pre-flight CheckHost is bypassed by
// supplying a Resolver that maps any host to a public-looking address
// — but easier still: we just route via the test server's URL
// directly, which already contains 127.0.0.1, and rely on a custom
// http.Client that swaps out the dialer.
//
// In practice the simplest approach: we DON'T use linkpreview.New
// here. We test parseHTML / ExtractURLs in isolation, and test the
// SSRF guard by pointing Fetch at private hostnames and asserting it
// refuses BEFORE issuing a connect. That covers the security claims
// without needing to spin up a fake DNS resolver.

func TestExtractURLs_BasicAndDedupe(t *testing.T) {
	in := "see https://example.com/foo and also https://example.com/foo plus http://b.test/x"
	got := linkpreview.ExtractURLs(in, 5)
	want := []string{"https://example.com/foo", "http://b.test/x"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestExtractURLs_StripsTrailingPunctuation(t *testing.T) {
	in := "check this out: https://example.com/path. it's nice."
	got := linkpreview.ExtractURLs(in, 5)
	if len(got) != 1 {
		t.Fatalf("got %v, want 1", got)
	}
	if got[0] != "https://example.com/path" {
		t.Errorf("got %q, want trailing dot stripped", got[0])
	}
}

func TestExtractURLs_RespectsMaxCap(t *testing.T) {
	in := "https://a.test https://b.test https://c.test https://d.test"
	got := linkpreview.ExtractURLs(in, 2)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (capped)", len(got))
	}
}

func TestExtractURLs_EmptyAndZeroMax(t *testing.T) {
	if got := linkpreview.ExtractURLs("", 5); got != nil {
		t.Errorf("empty input: got %v, want nil", got)
	}
	if got := linkpreview.ExtractURLs("https://x.test", 0); got != nil {
		t.Errorf("max=0: got %v, want nil", got)
	}
}

func TestFetch_RejectsNonHTTPScheme(t *testing.T) {
	f := linkpreview.New(linkpreview.Options{})
	_, err := f.Fetch(context.Background(), "ftp://example.com/")
	if !errors.Is(err, linkpreview.ErrUnsupportedScheme) {
		t.Fatalf("got %v, want ErrUnsupportedScheme", err)
	}
	_, err = f.Fetch(context.Background(), "javascript:alert(1)")
	if !errors.Is(err, linkpreview.ErrUnsupportedScheme) {
		t.Fatalf("javascript: got %v, want ErrUnsupportedScheme", err)
	}
	_, err = f.Fetch(context.Background(), "file:///etc/passwd")
	if !errors.Is(err, linkpreview.ErrUnsupportedScheme) {
		t.Fatalf("file: got %v, want ErrUnsupportedScheme", err)
	}
}

func TestFetch_RejectsPrivateIP(t *testing.T) {
	f := linkpreview.New(linkpreview.Options{})
	for _, target := range []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://[::1]/",
		"http://169.254.169.254/", // EC2 metadata service
	} {
		_, err := f.Fetch(context.Background(), target)
		if !errors.Is(err, netguard.ErrBlockedIP) {
			t.Errorf("%s: got %v, want ErrBlockedIP", target, err)
		}
	}
}

func TestFetch_PublicURL_SmokeParsesOG(t *testing.T) {
	// Spin up an httptest server but route through a custom Fetcher
	// whose transport bypasses the SSRF guard (we trust the loopback
	// address in the test). This is the only test that actually
	// exercises the HTTP path end to end.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
<head>
  <title>Plain Title</title>
  <meta property="og:title" content="OG Title">
  <meta property="og:description" content="OG description">
  <meta property="og:image" content="/static/cover.png">
  <meta property="og:site_name" content="Example Site">
  <link rel="canonical" href="/canonical-path"/>
</head>
<body>...</body>
</html>`))
	}))
	defer srv.Close()

	// Build a Fetcher pointed at a permissive transport. We use the
	// test server's client which has no SSRF guard.
	f := linkpreview.NewWithClient(srv.Client(), linkpreview.Options{
		FetchTimeout: 2 * time.Second,
	})
	preview, err := f.Fetch(context.Background(), srv.URL+"/post/1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if preview.Title != "OG Title" {
		t.Errorf("Title = %q, want OG Title", preview.Title)
	}
	if preview.Description != "OG description" {
		t.Errorf("Description = %q, want OG description", preview.Description)
	}
	if !strings.HasSuffix(preview.ImageURL, "/static/cover.png") {
		t.Errorf("ImageURL = %q, want absolute /static/cover.png", preview.ImageURL)
	}
	if preview.SiteName != "Example Site" {
		t.Errorf("SiteName = %q, want Example Site", preview.SiteName)
	}
	if !strings.HasSuffix(preview.CanonicalURL, "/canonical-path") {
		t.Errorf("CanonicalURL = %q, want absolute /canonical-path", preview.CanonicalURL)
	}
}

func TestFetch_FallsBackToTitleAndHostname(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Just a Title</title></head><body></body></html>`))
	}))
	defer srv.Close()

	f := linkpreview.NewWithClient(srv.Client(), linkpreview.Options{})
	preview, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if preview.Title != "Just a Title" {
		t.Errorf("Title = %q", preview.Title)
	}
	if preview.SiteName == "" {
		t.Errorf("SiteName falls back to hostname; got empty")
	}
}

func TestFetch_RejectsNonHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer srv.Close()

	f := linkpreview.NewWithClient(srv.Client(), linkpreview.Options{})
	_, err := f.Fetch(context.Background(), srv.URL)
	if !errors.Is(err, linkpreview.ErrNotHTML) {
		t.Fatalf("got %v, want ErrNotHTML", err)
	}
}

func TestFetch_RejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer srv.Close()

	f := linkpreview.NewWithClient(srv.Client(), linkpreview.Options{})
	_, err := f.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatalf("got nil, want error for 404")
	}
}

func TestFetchAll_ParallelAndOrdered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>` + r.URL.Path + `</title></head></html>`))
	}))
	defer srv.Close()

	urls := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}
	f := linkpreview.NewWithClient(srv.Client(), linkpreview.Options{})
	got := f.FetchAll(context.Background(), urls, 2)
	if len(got) != 3 {
		t.Fatalf("got %d previews, want 3", len(got))
	}
	for i, want := range []string{"/a", "/b", "/c"} {
		if got[i].Title != want {
			t.Errorf("got[%d].Title = %q, want %q", i, got[i].Title, want)
		}
	}
}
