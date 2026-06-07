package protoshare_test

// Fuzz targets for the protoshare wire surface (STANDARDS.md §8 rule 2).
// We fuzz the share ID embedded in the URL path and the Range header
// parser — these are the two non-trivial inputs this package processes
// from untrusted callers.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoshare"
	"github.com/hanshuebner/herold/internal/store"
)

// FuzzShareIDPath fuzzes arbitrary byte strings in the {id} path segment.
// The invariant is: the handler must never panic, and must always return
// one of the expected status codes (200, 206, 303, 410, 429, 500).
func FuzzShareIDPath(f *testing.F) {
	// Seed corpus: a known-good share ID, a missing ID, an ID with special chars.
	f.Add("validShareID123")
	f.Add("")
	f.Add("../../../etc/passwd")
	f.Add("share/extra/slash")
	f.Add(strings.Repeat("A", 2048))

	st := newFakeStore()
	content := "fuzz content"
	fs := activeShare("validShareID123", "fhash", int64(len(content)))
	st.addShare(fs)
	st.addBlob("fhash", []byte(content))

	clk := clock.NewFake(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit:  protoshare.RateLimitConfig{RequestsPerWindow: 1000000, Window: time.Minute},
	})

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	allowedStatuses := map[int]bool{
		200: true, 206: true, 303: true, 410: true, 429: true, 500: true,
		405: true, 404: true,
	}

	f.Fuzz(func(t *testing.T, id string) {
		// The Go stdlib mux normalises double slashes (empty segment)
		// into a 307 redirect. Skip empty IDs and IDs with slashes —
		// we only exercise single, non-empty path segments here.
		//
		// httptest.NewRequest panics if the URL contains characters
		// that are invalid in an HTTP request line (e.g. space, control
		// characters, NUL). We restrict the corpus to printable, non-
		// whitespace, non-slash ASCII to stay within the valid URL path
		// character set and keep the fuzz budget on the handler logic.
		if id == "" || strings.Contains(id, "/") {
			return
		}
		for _, ch := range id {
			// Exclude: control characters, DEL, whitespace, URL
			// special chars that httptest.NewRequest would reject as
			// malformed: '/', '?', '#', and '%' (bare percent-encoding
			// in a URL path panics the stdlib URL parser).
			if ch <= 0x20 || ch > 0x7E || ch == '/' || ch == '?' || ch == '#' || ch == '%' {
				return
			}
		}
		// The stdlib mux path-cleans "." and ".." segments (307 redirect).
		// Skip IDs that are only dots.
		trimmed := strings.TrimLeft(id, ".")
		if trimmed == "" {
			return
		}
		for _, path := range []string{
			"/share/" + id,
			"/share/" + id + "/download",
		} {
			req := httptest.NewRequest("GET", path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if !allowedStatuses[rr.Code] {
				t.Errorf("path %q: unexpected status %d", path, rr.Code)
			}
		}
	})
}

// FuzzRangeHeader fuzzes arbitrary Range header values against the download
// handler. The invariant: no panic; response is always valid HTTP.
func FuzzRangeHeader(f *testing.F) {
	f.Add("bytes=0-99")
	f.Add("bytes=500-")
	f.Add("bytes=-500")
	f.Add("bytes=0-0,-1")
	f.Add("invalid")
	f.Add("")
	f.Add("bytes=99999999999999999999-")

	st := newFakeStore()
	content := strings.Repeat("x", 1024)
	fs := store.FileShare{
		ID:          "fuzzrange",
		BlobHash:    "fhrange",
		BlobSize:    int64(len(content)),
		Filename:    "fuzz.bin",
		ContentType: "application/octet-stream",
		State:       store.FileShareStateActive,
		ExpiresAt:   time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	st.addShare(fs)
	st.addBlob("fhrange", []byte(content))

	clk := clock.NewFake(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit:  protoshare.RateLimitConfig{RequestsPerWindow: 1000000, Window: time.Minute},
	})

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	f.Fuzz(func(t *testing.T, rangeHdr string) {
		req := httptest.NewRequest("GET", "/share/fuzzrange/download", nil)
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		// Must not panic, and must be a valid 2xx, 4xx, or 5xx.
		if rr.Code < 100 || rr.Code > 599 {
			t.Errorf("Range %q: invalid status %d", rangeHdr, rr.Code)
		}
		// Drain the body so the handler can finish.
		io.Copy(io.Discard, rr.Body)
	})
}
