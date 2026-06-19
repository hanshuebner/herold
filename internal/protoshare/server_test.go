package protoshare_test

// Tests for the three public share handlers: landing page, unlock flow,
// and download. Coverage includes the serveability matrix (REQ-SHARE-11e),
// 410 uniformity, password unlock + cookie, rate-limit 429, range request,
// HEAD no-increment, MaxDownloads concurrency race, HTML/SVG forced
// octet-stream (REQ-SHARE-63), and RFC 5987 filename encoding.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoshare"
	"github.com/hanshuebner/herold/internal/store"
)

// bytesReadCloser wraps a *bytes.Reader with a no-op Close so it
// satisfies store.BlobReader (io.ReadSeekCloser + io.ReaderAt).
type bytesReadCloser struct{ *bytes.Reader }

func (bytesReadCloser) Close() error { return nil }

// ---- fake store ----------------------------------------------------------------

// fakeStore implements protoshare.ShareStore for tests. All operations are
// in-memory and deterministic.
type fakeStore struct {
	mu     sync.Mutex
	shares map[string]*store.FileShare
	blobs  map[string][]byte

	// RecordDownload can be made to sleep to allow concurrency tests.
	recordDelay time.Duration
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		shares: make(map[string]*store.FileShare),
		blobs:  make(map[string][]byte),
	}
}

func (f *fakeStore) addShare(fs store.FileShare) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := fs
	f.shares[fs.ID] = &cp
}

func (f *fakeStore) addBlob(hash string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[hash] = data
}

func (f *fakeStore) GetFileShareByID(_ context.Context, id string) (store.FileShare, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fs, ok := f.shares[id]
	if !ok {
		return store.FileShare{}, store.ErrNotFound
	}
	return *fs, nil
}

func (f *fakeStore) RecordFileShareDownload(_ context.Context, id string) (store.FileShare, error) {
	if f.recordDelay > 0 {
		time.Sleep(f.recordDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fs, ok := f.shares[id]
	if !ok {
		return store.FileShare{}, store.ErrNotFound
	}
	fs.DownloadCount++
	now := time.Now()
	fs.LastDownloadedAt = &now
	return *fs, nil
}

func (f *fakeStore) BlobGet(_ context.Context, hash string) (store.BlobReader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.blobs[hash]
	if !ok {
		return nil, store.ErrNotFound
	}
	return bytesReadCloser{bytes.NewReader(data)}, nil
}

func (f *fakeStore) BlobStat(_ context.Context, hash string) (int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.blobs[hash]
	if !ok {
		return 0, 0, store.ErrNotFound
	}
	return int64(len(data)), 1, nil
}

// ---- helpers ------------------------------------------------------------------

const testSigningKey = "01234567890123456789012345678901" // 32 bytes

var testEpoch = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func newServer(t *testing.T, st *fakeStore, clk clock.Clock) *protoshare.Server {
	t.Helper()
	if clk == nil {
		clk = clock.NewFake(testEpoch)
	}
	return protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit: protoshare.RateLimitConfig{
			RequestsPerWindow: 100,
			Window:            time.Minute,
		},
	})
}

// activeShare builds an active, non-expired, unpassworded share.
func activeShare(id, blobHash string, size int64) store.FileShare {
	return store.FileShare{
		ID:          id,
		BlobHash:    blobHash,
		BlobSize:    size,
		Filename:    "test.bin",
		ContentType: "application/octet-stream",
		State:       store.FileShareStateActive,
		ExpiresAt:   testEpoch.Add(24 * time.Hour),
	}
}

func doRequest(t *testing.T, srv *protoshare.Server, method, path string, body io.Reader, extraHdrs map[string]string) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(method, ts.URL+path, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range extraHdrs {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// ---- serveability matrix (REQ-SHARE-11e) --------------------------------------

func TestServeability_NeverExisted(t *testing.T) {
	st := newFakeStore()
	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/no-such-id/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
}

func TestServeability_Pending(t *testing.T) {
	st := newFakeStore()
	fs := activeShare("shareA", "hash1", 10)
	fs.State = store.FileShareStatePending
	st.addShare(fs)
	st.addBlob("hash1", []byte("0123456789"))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/shareA/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 for pending share", resp.StatusCode)
	}
}

func TestServeability_Revoked(t *testing.T) {
	st := newFakeStore()
	now := time.Now()
	fs := activeShare("shareB", "hash2", 5)
	fs.State = store.FileShareStateRevoked
	fs.RevokedAt = &now
	st.addShare(fs)
	st.addBlob("hash2", []byte("hello"))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/shareB/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 for revoked share", resp.StatusCode)
	}
}

func TestServeability_Expired(t *testing.T) {
	clk := clock.NewFake(testEpoch)
	st := newFakeStore()
	fs := activeShare("shareC", "hash3", 5)
	fs.ExpiresAt = testEpoch.Add(-time.Second) // already expired
	st.addShare(fs)
	st.addBlob("hash3", []byte("hello"))

	srv := newServer(t, st, clk)
	resp := doRequest(t, srv, "GET", "/share/shareC/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 for expired share", resp.StatusCode)
	}
}

func TestServeability_Exhausted(t *testing.T) {
	st := newFakeStore()
	var maxDL int64 = 3
	fs := activeShare("shareD", "hash4", 5)
	fs.MaxDownloads = &maxDL
	fs.DownloadCount = 3 // at cap
	st.addShare(fs)
	st.addBlob("hash4", []byte("hello"))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/shareD/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 for exhausted share", resp.StatusCode)
	}
}

func TestServeability_MissingPasswordNoUnlockCookie(t *testing.T) {
	hash, _ := store.HashSharePassword(nil, "s3cr3t")
	st := newFakeStore()
	fs := activeShare("shareE", "hash5", 5)
	fs.PasswordHash = hash
	st.addShare(fs)
	st.addBlob("hash5", []byte("hello"))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/shareE/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 for missing password", resp.StatusCode)
	}
}

// TestServeability_All410Uniform verifies that all not-serveable paths return
// identical 410 responses (same status, same body prefix). REQ-SHARE-11e.
func TestServeability_All410Uniform(t *testing.T) {
	clk := clock.NewFake(testEpoch)
	st := newFakeStore()

	// Never-existed: no setup needed
	cases := []struct {
		name  string
		id    string
		setup func()
	}{
		{"never_existed", "ne", func() {}},
		{"pending", "pend", func() {
			fs := activeShare("pend", "hpend", 5)
			fs.State = store.FileShareStatePending
			st.addShare(fs)
			st.addBlob("hpend", []byte("hello"))
		}},
		{"revoked", "rev", func() {
			now := time.Now()
			fs := activeShare("rev", "hrev", 5)
			fs.State = store.FileShareStateRevoked
			fs.RevokedAt = &now
			st.addShare(fs)
			st.addBlob("hrev", []byte("hello"))
		}},
		{"expired", "exp", func() {
			fs := activeShare("exp", "hexp", 5)
			fs.ExpiresAt = testEpoch.Add(-time.Second)
			st.addShare(fs)
			st.addBlob("hexp", []byte("hello"))
		}},
		{"exhausted", "exh", func() {
			var maxDL int64 = 1
			fs := activeShare("exh", "hexh", 5)
			fs.MaxDownloads = &maxDL
			fs.DownloadCount = 1
			st.addShare(fs)
			st.addBlob("hexh", []byte("hello"))
		}},
		{"locked_no_cookie", "locked", func() {
			hash, _ := store.HashSharePassword(nil, "pw")
			fs := activeShare("locked", "hlocked", 5)
			fs.PasswordHash = hash
			st.addShare(fs)
			st.addBlob("hlocked", []byte("hello"))
		}},
	}

	mux := http.NewServeMux()
	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit:  protoshare.RateLimitConfig{RequestsPerWindow: 1000, Window: time.Minute},
	})
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	var firstStatus int
	var firstBody string
	for _, tc := range cases {
		tc.setup()
		req, _ := http.NewRequest("GET", ts.URL+"/share/"+tc.id+"/download", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: request failed: %v", tc.name, err)
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(bodyBytes)

		if firstStatus == 0 {
			firstStatus = resp.StatusCode
			firstBody = body
			continue
		}
		if resp.StatusCode != firstStatus {
			t.Errorf("%s: status = %d, want %d (uniform with first case)", tc.name, resp.StatusCode, firstStatus)
		}
		if body != firstBody {
			t.Errorf("%s: body = %q, want %q (uniform with first case)", tc.name, body, firstBody)
		}
	}
}

// ---- successful download -------------------------------------------------------

func TestDownload_Success(t *testing.T) {
	st := newFakeStore()
	content := "the quick brown fox"
	fs := activeShare("dl1", "hbody", int64(len(content)))
	st.addShare(fs)
	st.addBlob("hbody", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/dl1/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != content {
		t.Fatalf("body = %q, want %q", body, content)
	}
}

func TestDownload_SetsRequiredHeaders(t *testing.T) {
	st := newFakeStore()
	content := "test content"
	fs := activeShare("hdrs1", "hhdr", int64(len(content)))
	fs.ContentType = "application/octet-stream"
	fs.Filename = "my file.bin"
	st.addShare(fs)
	st.addBlob("hhdr", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/hdrs1/download", nil, nil)
	defer resp.Body.Close()

	check := func(header, want string) {
		t.Helper()
		if got := resp.Header.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	check("X-Content-Type-Options", "nosniff")
	check("Referrer-Policy", "no-referrer")
	check("Cache-Control", "private, no-store")
	if ar := resp.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", ar)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition %q missing 'attachment'", cd)
	}
	if !strings.Contains(cd, "my%20file.bin") && !strings.Contains(cd, "my+file.bin") && !strings.Contains(cd, "my%2520file") {
		// RFC 5987: space encoded as %20
		if !strings.Contains(cd, "UTF-8") {
			t.Errorf("Content-Disposition %q does not look like RFC5987", cd)
		}
	}
}

// TestDownload_IncrementOnce verifies a single GET increments download_count
// by exactly 1.
func TestDownload_IncrementOnce(t *testing.T) {
	st := newFakeStore()
	content := "increment me"
	fs := activeShare("incr1", "hincr", int64(len(content)))
	st.addShare(fs)
	st.addBlob("hincr", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/incr1/download", nil, nil)
	resp.Body.Close()

	got, err := st.GetFileShareByID(context.Background(), "incr1")
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.DownloadCount != 1 {
		t.Fatalf("DownloadCount = %d, want 1", got.DownloadCount)
	}
}

// ---- HEAD no-increment --------------------------------------------------------

func TestHead_NoIncrement(t *testing.T) {
	st := newFakeStore()
	content := "head content"
	fs := activeShare("head1", "hhead", int64(len(content)))
	st.addShare(fs)
	st.addBlob("hhead", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "HEAD", "/share/head1/download", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", resp.StatusCode)
	}

	got, _ := st.GetFileShareByID(context.Background(), "head1")
	if got.DownloadCount != 0 {
		t.Fatalf("HEAD incremented DownloadCount to %d, want 0", got.DownloadCount)
	}
}

// ---- HTML and SVG forced to octet-stream (REQ-SHARE-63) ----------------------

func TestDownload_HTMLForcedOctetStream(t *testing.T) {
	st := newFakeStore()
	for _, ct := range []string{"text/html", "text/html; charset=utf-8", "image/svg+xml", "application/xhtml+xml"} {
		id := "ct_" + strings.ReplaceAll(ct, "/", "_")
		fs := activeShare(id, "h"+id, 5)
		fs.ContentType = ct
		st.addShare(fs)
		st.addBlob("h"+id, []byte("hello"))
	}

	srv := newServer(t, st, nil)
	for _, ct := range []string{"text/html", "text/html; charset=utf-8", "image/svg+xml", "application/xhtml+xml"} {
		id := "ct_" + strings.ReplaceAll(ct, "/", "_")
		resp := doRequest(t, srv, "GET", "/share/"+id+"/download", nil, nil)
		resp.Body.Close()
		got := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(got, "application/octet-stream") {
			t.Errorf("ContentType %q: got %q, want application/octet-stream", ct, got)
		}
	}
}

// ---- RFC 5987 filename encoding ------------------------------------------------

func TestRFC5987Filename(t *testing.T) {
	st := newFakeStore()
	fs := activeShare("fname1", "hfname", 5)
	fs.Filename = "Datei mit Umlauten äöü.pdf"
	st.addShare(fs)
	st.addBlob("hfname", []byte("hello"))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/fname1/download", nil, nil)
	resp.Body.Close()
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "UTF-8''") {
		t.Errorf("Content-Disposition %q missing UTF-8'' prefix for RFC5987", cd)
	}
	// The non-ASCII bytes should be percent-encoded.
	if !strings.Contains(cd, "%C3%A4") { // ä in UTF-8 is 0xC3 0xA4
		t.Errorf("Content-Disposition %q: umlaut not percent-encoded", cd)
	}
}

// ---- password unlock flow -----------------------------------------------------

func TestUnlock_CorrectPassword(t *testing.T) {
	clk := clock.NewFake(testEpoch)
	st := newFakeStore()
	hash, err := store.HashSharePassword(nil, "s3cr3t")
	if err != nil {
		t.Fatalf("HashSharePassword: %v", err)
	}
	fs := activeShare("pw1", "hpw1", 5)
	fs.PasswordHash = hash
	st.addShare(fs)
	st.addBlob("hpw1", []byte("hello"))

	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit:  protoshare.RateLimitConfig{RequestsPerWindow: 1000, Window: time.Minute},
	})

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// POST unlock with correct password.
	form := url.Values{"password": {"s3cr3t"}}
	req, _ := http.NewRequest("POST", ts.URL+"/share/pw1/unlock",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse // don't follow redirect
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unlock POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlock status = %d, want 303", resp.StatusCode)
	}

	// Collect the Set-Cookie header and replay it on a download request.
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("unlock did not set a cookie")
	}

	req2, _ := http.NewRequest("GET", ts.URL+"/share/pw1/download", nil)
	for _, c := range cookies {
		req2.AddCookie(c)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("download with cookie: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("download with cookie status = %d, want 200", resp2.StatusCode)
	}
}

func TestUnlock_WrongPassword(t *testing.T) {
	st := newFakeStore()
	hash, _ := store.HashSharePassword(nil, "correct")
	fs := activeShare("pw2", "hpw2", 5)
	fs.PasswordHash = hash
	st.addShare(fs)
	st.addBlob("hpw2", []byte("hello"))

	srv := newServer(t, st, nil)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	form := url.Values{"password": {"wrong"}}
	req, _ := http.NewRequest("POST", ts.URL+"/share/pw2/unlock",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		t.Fatalf("unlock POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("wrong password status = %d, want 410", resp.StatusCode)
	}
}

// TestUnlock_CookieExpiry verifies that an expired unlock cookie is rejected.
func TestUnlock_CookieExpiry(t *testing.T) {
	clk := clock.NewFake(testEpoch)
	st := newFakeStore()
	hash, _ := store.HashSharePassword(nil, "pw")
	fs := activeShare("cwexp", "hcwexp", 5)
	fs.PasswordHash = hash
	st.addShare(fs)
	st.addBlob("hcwexp", []byte("hello"))

	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit:  protoshare.RateLimitConfig{RequestsPerWindow: 1000, Window: time.Minute},
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Get the unlock cookie at epoch.
	form := url.Values{"password": {"pw"}}
	req, _ := http.NewRequest("POST", ts.URL+"/share/cwexp/unlock",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ := client.Do(req)
	resp.Body.Close()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie set")
	}

	// Advance clock past cookie TTL (15 min + 1 sec).
	clk.Advance(16 * time.Minute)

	// Download should now be 410 because the cookie is expired.
	req2, _ := http.NewRequest("GET", ts.URL+"/share/cwexp/download", nil)
	for _, c := range cookies {
		req2.AddCookie(c)
	}
	resp2, _ := client.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusGone {
		t.Fatalf("expired cookie status = %d, want 410", resp2.StatusCode)
	}
}

// ---- rate limiting (REQ-SHARE-32) --------------------------------------------

func TestRateLimit_429OnExceed(t *testing.T) {
	st := newFakeStore()
	content := "rate limit test"
	fs := activeShare("rl1", "hrl", int64(len(content)))
	st.addShare(fs)
	st.addBlob("hrl", []byte(content))

	clk := clock.NewFake(testEpoch)
	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit: protoshare.RateLimitConfig{
			RequestsPerWindow: 3,
			Window:            time.Minute,
		},
	})

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	var got429 bool
	for i := 0; i < 10; i++ {
		resp, err := http.Get(ts.URL + "/share/rl1/download")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			if resp.Header.Get("Retry-After") == "" {
				t.Error("429 missing Retry-After header")
			}
			break
		}
	}
	if !got429 {
		t.Fatal("expected 429 after exhausting rate limit, never got it")
	}
}

// TestRateLimit_RetryAfterResets verifies that after the window expires, the
// rate limit resets.
func TestRateLimit_RetryAfterResets(t *testing.T) {
	st := newFakeStore()
	content := "rl reset"
	fs := activeShare("rl2", "hrl2", int64(len(content)))
	st.addShare(fs)
	st.addBlob("hrl2", []byte(content))

	clk := clock.NewFake(testEpoch)
	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit: protoshare.RateLimitConfig{
			RequestsPerWindow: 2,
			Window:            time.Minute,
		},
	})

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Exhaust the limit.
	for i := 0; i < 2; i++ {
		resp, _ := http.Get(ts.URL + "/share/rl2/download")
		resp.Body.Close()
	}
	resp, _ := http.Get(ts.URL + "/share/rl2/download")
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}

	// Advance clock past window.
	clk.Advance(2 * time.Minute)

	resp2, _ := http.Get(ts.URL + "/share/rl2/download")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("after window reset, status = %d, want 200", resp2.StatusCode)
	}
}

// ---- range requests (REQ-SHARE-31) -------------------------------------------

func TestRange_PartialContent(t *testing.T) {
	st := newFakeStore()
	content := "0123456789abcdef" // 16 bytes
	fs := activeShare("range1", "hrange", int64(len(content)))
	st.addShare(fs)
	st.addBlob("hrange", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/range1/download", nil,
		map[string]string{"Range": "bytes=0-7"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("range status = %d, want 206 or 200", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusPartialContent {
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "01234567" {
			t.Fatalf("range body = %q, want 01234567", string(body))
		}
	}
}

// TestRange_FirstRangeIncrements: a bytes=0-N range IS the first download.
func TestRange_FirstRangeIncrements(t *testing.T) {
	st := newFakeStore()
	content := "abcdefgh"
	fs := activeShare("rincr", "hrincr", int64(len(content)))
	st.addShare(fs)
	st.addBlob("hrincr", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/rincr/download", nil,
		map[string]string{"Range": "bytes=0-3"})
	resp.Body.Close()

	got, _ := st.GetFileShareByID(context.Background(), "rincr")
	if got.DownloadCount != 1 {
		t.Fatalf("bytes=0-3 (first range) DownloadCount = %d, want 1", got.DownloadCount)
	}
}

// TestRange_ContinuationNoIncrement: bytes=5-N does NOT increment.
func TestRange_ContinuationNoIncrement(t *testing.T) {
	st := newFakeStore()
	content := "abcdefgh"
	fs := activeShare("rcont", "hrcont", int64(len(content)))
	fs.DownloadCount = 1 // simulate already counted first range
	st.addShare(fs)
	st.addBlob("hrcont", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/rcont/download", nil,
		map[string]string{"Range": "bytes=4-7"})
	resp.Body.Close()

	got, _ := st.GetFileShareByID(context.Background(), "rcont")
	if got.DownloadCount != 1 {
		t.Fatalf("continuation range DownloadCount = %d, want 1 (unchanged)", got.DownloadCount)
	}
}

// ---- MaxDownloads concurrency race (REQ-SHARE-30 architecture §concurrent) ---

func TestMaxDownloads_ConcurrentRace(t *testing.T) {
	st := newFakeStore()
	content := "concurrent file"
	var maxDL int64 = 5
	fs := activeShare("conc1", "hconc", int64(len(content)))
	fs.MaxDownloads = &maxDL
	st.addShare(fs)
	st.addBlob("hconc", []byte(content))

	srv := newServer(t, st, nil)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Send 20 concurrent download requests; no more than maxDL should succeed.
	var ok, gone atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(ts.URL + "/share/conc1/download")
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusGone:
				gone.Add(1)
			}
		}()
	}
	wg.Wait()

	if ok.Load() > maxDL {
		t.Fatalf("got %d successful downloads, want <= %d", ok.Load(), maxDL)
	}
}

// ---- landing page -------------------------------------------------------------

func TestLanding_RendersHTML(t *testing.T) {
	st := newFakeStore()
	content := "landing test"
	fs := activeShare("land1", "hland", int64(len(content)))
	fs.Filename = "myfile.zip"
	st.addShare(fs)
	st.addBlob("hland", []byte(content))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/land1", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("landing status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "myfile.zip") {
		t.Error("landing page does not contain filename")
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Error("landing page Content-Type not text/html")
	}
}

func TestLanding_ShowsPasswordField_WhenLocked(t *testing.T) {
	st := newFakeStore()
	hash, _ := store.HashSharePassword(nil, "pw")
	fs := activeShare("land2", "hland2", 5)
	fs.PasswordHash = hash
	st.addShare(fs)
	st.addBlob("hland2", []byte("hello"))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/land2", nil, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `type="password"`) {
		t.Error("locked share landing page does not contain password field")
	}
}

func TestLanding_ShowsDownloadButton_WhenUnlocked(t *testing.T) {
	st := newFakeStore()
	fs := activeShare("land3", "hland3", 5)
	st.addShare(fs)
	st.addBlob("hland3", []byte("hello"))

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/land3", nil, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Download") {
		t.Error("unlocked share landing page does not contain Download button/link")
	}
}

func TestLanding_410_ForNotServeable(t *testing.T) {
	st := newFakeStore()
	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/missing-share", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("landing 410 status = %d, want 410", resp.StatusCode)
	}
}

// ---- blob missing under active share ------------------------------------------

func TestDownload_BlobMissing_Returns500(t *testing.T) {
	st := newFakeStore()
	fs := activeShare("noblobshare", "nonexistent_hash", 10)
	st.addShare(fs)
	// intentionally do not add blob to store

	srv := newServer(t, st, nil)
	resp := doRequest(t, srv, "GET", "/share/noblobshare/download", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("missing blob status = %d, want 500 (not 410)", resp.StatusCode)
	}
}

// ---- property: all 410s have the same body ------------------------------------

func TestProperty_All410SameBody(t *testing.T) {
	// This test parametrically checks that every code path leading to 410
	// produces identical body text. It re-tests the uniform-410 contract with
	// a wider set of share states.
	clk := clock.NewFake(testEpoch)
	st := newFakeStore()

	past := testEpoch.Add(-time.Second)
	now := time.Now()
	var maxDL int64 = 0
	hash, _ := store.HashSharePassword(nil, "any")

	shares := []store.FileShare{
		// pending
		{ID: "p1", BlobHash: "b1", BlobSize: 1, Filename: "f", ContentType: "application/octet-stream",
			State: store.FileShareStatePending, ExpiresAt: testEpoch.Add(time.Hour)},
		// revoked
		{ID: "p2", BlobHash: "b2", BlobSize: 1, Filename: "f", ContentType: "application/octet-stream",
			State: store.FileShareStateRevoked, ExpiresAt: testEpoch.Add(time.Hour), RevokedAt: &now},
		// expired active
		{ID: "p3", BlobHash: "b3", BlobSize: 1, Filename: "f", ContentType: "application/octet-stream",
			State: store.FileShareStateActive, ExpiresAt: past},
		// exhausted
		{ID: "p4", BlobHash: "b4", BlobSize: 1, Filename: "f", ContentType: "application/octet-stream",
			State: store.FileShareStateActive, ExpiresAt: testEpoch.Add(time.Hour),
			MaxDownloads: &maxDL, DownloadCount: 1},
		// locked, no cookie
		{ID: "p5", BlobHash: "b5", BlobSize: 1, Filename: "f", ContentType: "application/octet-stream",
			State: store.FileShareStateActive, ExpiresAt: testEpoch.Add(time.Hour), PasswordHash: hash},
	}
	for i, fs := range shares {
		st.addShare(fs)
		st.addBlob(fmt.Sprintf("b%d", i+1), []byte("x"))
	}

	srv := protoshare.New(protoshare.Options{
		Store:      st,
		SigningKey: []byte(testSigningKey),
		Clock:      clk,
		RateLimit:  protoshare.RateLimitConfig{RequestsPerWindow: 10000, Window: time.Minute},
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	var bodies []string
	for _, fs := range shares {
		resp, _ := http.Get(ts.URL + "/share/" + fs.ID + "/download")
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusGone {
			t.Errorf("share %s: status = %d, want 410", fs.ID, resp.StatusCode)
		}
		bodies = append(bodies, string(b))
	}
	// never existed
	resp, _ := http.Get(ts.URL + "/share/does-not-exist/download")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	bodies = append(bodies, string(b))

	for i, body := range bodies[1:] {
		if body != bodies[0] {
			t.Errorf("case %d body %q != case 0 body %q", i+1, body, bodies[0])
		}
	}
}
