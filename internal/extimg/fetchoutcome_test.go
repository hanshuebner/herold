package extimg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetcher_BlockedScheme_DistinctFromBlockedSSRF covers issue #271:
// a scheme-policy rejection (a disallowed scheme) must record
// FetchBlockedScheme, separate from FetchBlockedSSRF -- the outcome an
// internal-destination block (e.g. a TEST-NET redirect target,
// TestFetcher_RedirectBlocked) produces.
func TestFetcher_BlockedScheme_DistinctFromBlockedSSRF(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	f := NewFetcher(cfg)

	r := f.Fetch(context.Background(), "ftp://example.test/x.png")
	if r.Outcome != FetchBlockedScheme {
		t.Fatalf("scheme rejection: Outcome=%s reason=%q, want %s", r.Outcome, r.Reason, FetchBlockedScheme)
	}
	if r.Outcome == FetchBlockedSSRF {
		t.Fatalf("scheme rejection must not collapse into FetchBlockedSSRF")
	}
}

// TestFetcher_BlockedScheme_RequireHTTPS covers the RequireHTTPS=true
// case: a plain http:// URL is a scheme-policy rejection too, not a
// destination block.
func TestFetcher_BlockedScheme_RequireHTTPS(t *testing.T) {
	cfg := testFetcherCfg(t, nil)
	cfg.RequireHTTPS = true
	f := NewFetcher(cfg)

	r := f.Fetch(context.Background(), "http://example.test/x.png")
	if r.Outcome != FetchBlockedScheme {
		t.Fatalf("RequireHTTPS rejection: Outcome=%s reason=%q, want %s", r.Outcome, r.Reason, FetchBlockedScheme)
	}
}

// TestFetcher_BlockedSSRF_InternalDestination confirms a genuine
// internal-destination block still records FetchBlockedSSRF, not
// FetchBlockedScheme -- the two outcomes partition disjoint causes.
func TestFetcher_BlockedSSRF_InternalDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://203.0.113.5/x.png")
		w.WriteHeader(302)
	}))
	defer srv.Close()

	cfg := testFetcherCfg(t, srv)
	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/redirect")
	if r.Outcome != FetchBlockedSSRF {
		t.Fatalf("Outcome=%s reason=%q, want %s", r.Outcome, r.Reason, FetchBlockedSSRF)
	}
}

// TestFetchOutcome_Retryable is a table test over every FetchOutcome
// value (issue #271): TRANSIENT outcomes are retryable, PERMANENT ones
// are not. FetchOK is included as the non-failure baseline.
func TestFetchOutcome_Retryable(t *testing.T) {
	cases := []struct {
		outcome FetchOutcome
		want    bool
	}{
		{FetchOK, false},
		{FetchTimeout, true},
		{FetchHTTP5xx, true},
		{FetchMessageBudget, true},
		{FetchTransport, true},
		{FetchBlockedSSRF, false},
		{FetchBlockedScheme, false},
		{FetchHTTP4xx, false},
		{FetchNotImage, false},
		{FetchTooLarge, false},
		{FetchEmpty, false},
		{FetchRedirectLoop, false},
	}
	seen := make(map[FetchOutcome]bool, len(cases))
	for _, c := range cases {
		seen[c.outcome] = true
		if got := c.outcome.Retryable(); got != c.want {
			t.Errorf("%s.Retryable() = %v, want %v", c.outcome, got, c.want)
		}
	}
	// Guard against a future FetchOutcome constant landing without a
	// matching case above.
	allOutcomes := []FetchOutcome{
		FetchOK, FetchTooLarge, FetchTimeout, FetchNotImage, FetchEmpty,
		FetchBlockedSSRF, FetchHTTP4xx, FetchHTTP5xx, FetchRedirectLoop,
		FetchTransport, FetchMessageBudget, FetchBlockedScheme,
	}
	if len(allOutcomes) != len(cases) {
		t.Fatalf("allOutcomes has %d entries but the table has %d -- keep them in sync", len(allOutcomes), len(cases))
	}
	for _, o := range allOutcomes {
		if !seen[o] {
			t.Errorf("FetchOutcome %s missing from the Retryable() table test", o)
		}
	}
}

// TestDeriveFailureSignal covers the issue #271 reduction from a
// per-outcome failure tally to (retryable count, permanent reason
// category).
func TestDeriveFailureSignal(t *testing.T) {
	cases := []struct {
		name          string
		counts        map[FetchOutcome]int
		wantRetryable int
		wantReason    FailedImageReason
	}{
		{
			name:          "nil tally",
			counts:        nil,
			wantRetryable: 0,
			wantReason:    FailedImageReasonNone,
		},
		{
			name:          "all retryable",
			counts:        map[FetchOutcome]int{FetchTimeout: 2, FetchHTTP5xx: 1},
			wantRetryable: 3,
			wantReason:    FailedImageReasonNone,
		},
		{
			name:          "single permanent category",
			counts:        map[FetchOutcome]int{FetchHTTP4xx: 3},
			wantRetryable: 0,
			wantReason:    FailedImageReasonNotFound,
		},
		{
			name:          "mixed transient + permanent",
			counts:        map[FetchOutcome]int{FetchTimeout: 1, FetchHTTP4xx: 1},
			wantRetryable: 1,
			wantReason:    FailedImageReasonNotFound,
		},
		{
			name: "highest count wins",
			counts: map[FetchOutcome]int{
				FetchHTTP4xx:  1, // not_found: 1
				FetchTooLarge: 3, // too_large: 3
				FetchEmpty:    1, // too_large: +1 = 4
			},
			wantRetryable: 0,
			wantReason:    FailedImageReasonTooLarge,
		},
		{
			name: "tie broken by fixed priority",
			counts: map[FetchOutcome]int{
				FetchBlockedSSRF: 2, // blocked_by_policy: 2
				FetchHTTP4xx:     2, // not_found: 2
			},
			wantRetryable: 0,
			wantReason:    FailedImageReasonBlocked,
		},
		{
			name:          "scheme block maps to same category as ssrf block",
			counts:        map[FetchOutcome]int{FetchBlockedScheme: 1},
			wantRetryable: 0,
			wantReason:    FailedImageReasonBlocked,
		},
		{
			name:          "redirect loop maps to other",
			counts:        map[FetchOutcome]int{FetchRedirectLoop: 1},
			wantRetryable: 0,
			wantReason:    FailedImageReasonOther,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			retryable, reason := DeriveFailureSignal(c.counts)
			if retryable != c.wantRetryable || reason != c.wantReason {
				t.Errorf("DeriveFailureSignal(%v) = (%d, %q), want (%d, %q)",
					c.counts, retryable, reason, c.wantRetryable, c.wantReason)
			}
		})
	}
}
