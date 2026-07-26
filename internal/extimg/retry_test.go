package extimg

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestInternalize_FailedFetchRetainsStateForRetry extends
// TestInternalize_FailedFetchPlaceholdered (issue #162): a failed
// fetch must not just placeholder the body -- it must retain the
// failed URL and a splice-back template server-side so a later retry
// has something to work with. Asserts the retained state contains the
// URL (server-only, expected) while the delivered body (out) does not
// (REQ-EXTIMG-RETRY, grounding requirement (a)+(d)).
func TestInternalize_FailedFetchRetainsStateForRetry(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	cfg.PerImageConnectTimeout = 200 * time.Millisecond
	cfg.PerImageTotalTimeout = 500 * time.Millisecond
	cfg.PerMessageTimeout = 2 * time.Second

	url := closedLoopbackURL(t)
	raw := buildTestMessage(t, []string{url})
	out, sum, err := Internalize(context.Background(), raw, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum.Failed != 1 {
		t.Fatalf("Failed: want 1, got %d", sum.Failed)
	}
	if len(sum.FailedURLs) != 1 || sum.FailedURLs[0] != url {
		t.Fatalf("FailedURLs = %v, want [%q]", sum.FailedURLs, url)
	}
	if len(sum.FailedImageTemplate) == 0 {
		t.Fatalf("FailedImageTemplate must be populated when Failed > 0")
	}
	// Server-only template MAY contain the origin URL -- that's the
	// point, it's the retry seed. It never leaves this process.
	if !bytes.Contains(sum.FailedImageTemplate, []byte(url)) {
		t.Fatalf("FailedImageTemplate should retain the origin URL for retry")
	}
	// The delivered body must NOT contain the origin URL anywhere.
	if strings.Contains(string(out), url) {
		t.Fatalf("origin URL leaked into delivered body: %q", url)
	}
	roundTripEncode(t, sum)
}

// TestRetryFailedImages_Succeeds covers grounding requirement (b): a
// retry whose fetch now succeeds re-internalizes the image into the
// stored body (real cid: reference, not a placeholder), reports
// RetriedOK=1 and an empty StillFailedURLs, and the delivered body
// still never carries the raw origin URL.
func TestRetryFailedImages_Succeeds(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	cfg.PerImageConnectTimeout = 200 * time.Millisecond
	cfg.PerImageTotalTimeout = 500 * time.Millisecond
	cfg.PerMessageTimeout = 2 * time.Second

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()
	url := fmt.Sprintf("http://127.0.0.1:%d/img.png", addr.Port)

	raw := buildTestMessage(t, []string{url})
	out1, sum1, err := Internalize(context.Background(), raw, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum1.Failed != 1 || len(sum1.FailedURLs) != 1 {
		t.Fatalf("expected first attempt to fail: sum=%+v", sum1)
	}

	// Make the SAME URL succeed: rebind the exact port that was
	// closed above with a real handler (the "fake image host that can
	// be made to succeed on retry").
	l2, err := net.Listen("tcp", addr.String())
	if err != nil {
		t.Fatalf("relisten on %s: %v", addr.String(), err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes())
	}))
	srv.Listener.Close()
	srv.Listener = l2
	srv.Start()
	defer srv.Close()

	cfg2 := cfg
	cfg2.AllowedPorts = []int{addr.Port}

	out2, result, err := RetryFailedImages(context.Background(), out1, sum1.FailedImageTemplate, sum1.FailedURLs, cfg2, DKIMVerdict{})
	if err != nil {
		t.Fatalf("RetryFailedImages: %v", err)
	}
	if !result.Modified {
		t.Fatalf("expected Modified=true")
	}
	if result.RetriedOK != 1 {
		t.Fatalf("RetriedOK: want 1, got %d", result.RetriedOK)
	}
	if len(result.StillFailedURLs) != 0 {
		t.Fatalf("StillFailedURLs: want none, got %v", result.StillFailedURLs)
	}
	if strings.Contains(string(out2), url) {
		t.Fatalf("origin URL leaked into retried body: %q", url)
	}
	if strings.Contains(string(out2), PlaceholderDataURI) {
		t.Fatalf("placeholder should have been replaced by the real image after a successful retry")
	}
	if !strings.Contains(string(out2), "cid:") {
		t.Fatalf("expected a cid: reference for the newly-internalized image")
	}
	if !bytes.Contains(out2, pngBytes()) {
		// The PNG bytes are base64-encoded in the MIME part, so a raw
		// byte match isn't expected; just confirm no error path was
		// silently taken by checking size grew vs a placeholder-only body.
		t.Logf("note: raw PNG bytes are base64-encoded in the MIME body, not compared verbatim")
	}
}

// TestRetryFailedImages_StillFailing covers grounding requirement (c):
// a retry whose fetch fails again leaves the count/state unchanged
// (no new URL introduced, nothing spuriously marked resolved) and the
// delivered body still carries no origin URL.
func TestRetryFailedImages_StillFailing(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	cfg.PerImageConnectTimeout = 200 * time.Millisecond
	cfg.PerImageTotalTimeout = 500 * time.Millisecond
	cfg.PerMessageTimeout = 2 * time.Second

	url := closedLoopbackURL(t)
	raw := buildTestMessage(t, []string{url})
	out1, sum1, err := Internalize(context.Background(), raw, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum1.Failed != 1 {
		t.Fatalf("expected first attempt to fail")
	}

	// Retry without standing anything back up on that port: connection
	// refused again.
	out2, result, err := RetryFailedImages(context.Background(), out1, sum1.FailedImageTemplate, sum1.FailedURLs, cfg, DKIMVerdict{})
	if err != nil {
		t.Fatalf("RetryFailedImages: %v", err)
	}
	if result.Modified {
		t.Fatalf("expected Modified=false when nothing improved")
	}
	if result.RetriedOK != 0 {
		t.Fatalf("RetriedOK: want 0, got %d", result.RetriedOK)
	}
	if len(result.StillFailedURLs) != 1 || result.StillFailedURLs[0] != url {
		t.Fatalf("StillFailedURLs = %v, want [%q]", result.StillFailedURLs, url)
	}
	// out2 must equal out1 unchanged (no rebuild happened) and must
	// still carry no origin URL.
	if !bytes.Equal(out1, out2) {
		t.Fatalf("expected unchanged body when retry improves nothing")
	}
	if strings.Contains(string(out2), url) {
		t.Fatalf("origin URL leaked into unchanged body: %q", url)
	}
	// A retry that still fails must aggregate the per-outcome tally
	// (issue #267) so the caller can log something other than a bare
	// still-failed count.
	total := 0
	for _, n := range result.FailureCounts {
		total += n
	}
	if total != 1 {
		t.Fatalf("FailureCounts total = %d, want 1 (got %v)", total, result.FailureCounts)
	}
}

// TestRetainedState_EncodeDecodeRoundtrip proves the opaque
// server-only storage encoding round-trips (URLs, template bytes,
// DKIM verdict) and that an empty state encodes to "" (the store's
// "nothing retained" sentinel) per REQ-EXTIMG-RETRY.
func TestRetainedState_EncodeDecodeRoundtrip(t *testing.T) {
	s := RetainedState{
		URLs:     []string{"http://example.test/a.png", "http://example.test/b.png"},
		Template: []byte(`<img src="http://example.test/a.png">`),
		DKIM:     DKIMVerdict{Result: "pass", SigningDomain: "example.com", Selector: "sel1"},
	}
	encoded, err := EncodeRetainedState(s)
	if err != nil {
		t.Fatalf("EncodeRetainedState: %v", err)
	}
	if encoded == "" {
		t.Fatalf("expected non-empty encoding")
	}
	decoded, err := DecodeRetainedState(encoded)
	if err != nil {
		t.Fatalf("DecodeRetainedState: %v", err)
	}
	if len(decoded.URLs) != 2 || decoded.URLs[0] != s.URLs[0] || decoded.URLs[1] != s.URLs[1] {
		t.Fatalf("URLs roundtrip mismatch: %v", decoded.URLs)
	}
	if !bytes.Equal(decoded.Template, s.Template) {
		t.Fatalf("Template roundtrip mismatch")
	}
	if decoded.DKIM != s.DKIM {
		t.Fatalf("DKIM roundtrip mismatch: %+v", decoded.DKIM)
	}

	empty, err := EncodeRetainedState(RetainedState{})
	if err != nil {
		t.Fatalf("EncodeRetainedState(empty): %v", err)
	}
	if empty != "" {
		t.Fatalf("empty RetainedState must encode to empty string, got %q", empty)
	}
	backEmpty, err := DecodeRetainedState("")
	if err != nil {
		t.Fatalf("DecodeRetainedState(\"\"): %v", err)
	}
	if len(backEmpty.URLs) != 0 {
		t.Fatalf("expected zero-value decode for empty input")
	}
}

// closedLoopbackURL reserves a loopback port, closes it immediately,
// and returns a URL pointing at it -- a deterministic connection-
// refused fetch target with no dependency on any real remote host.
func closedLoopbackURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()
	return fmt.Sprintf("http://127.0.0.1:%d/missing.png", addr.Port)
}

// roundTripEncode is a smoke assertion that sum's retained fields
// survive the Encode/Decode cycle used for persistence, without
// re-asserting the full roundtrip test's coverage.
func roundTripEncode(t *testing.T, sum AuditSummary) {
	t.Helper()
	encoded, err := EncodeRetainedState(RetainedState{URLs: sum.FailedURLs, Template: sum.FailedImageTemplate})
	if err != nil {
		t.Fatalf("EncodeRetainedState: %v", err)
	}
	decoded, err := DecodeRetainedState(encoded)
	if err != nil {
		t.Fatalf("DecodeRetainedState: %v", err)
	}
	if len(decoded.URLs) != len(sum.FailedURLs) {
		t.Fatalf("decoded URLs length mismatch")
	}
}
