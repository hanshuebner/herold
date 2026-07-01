package protoadmin_test

// detect_provider_test.go covers GET /api/v1/identities/detect-provider.
//
// Test matrix
//   - Heuristic table tests driven through the HTTP endpoint with a
//     fake MX resolver: google MX variants, Microsoft MX variants,
//     unknown, empty set, case-insensitivity, anti-spoofing near-miss
//     hostnames (dot-boundary suffix matching).
//   - HTTP integration tests: authenticated happy paths (google,
//     microsoft, null), unauthenticated request (401), missing /
//     malformed email parameter (400), MX lookup failure -> null (200).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fakeMXResolver is an in-test MXResolver that returns pre-configured
// MX records from a map keyed by domain. Missing domains (not in the
// map) return an error to exercise the "no MX / lookup failure -> null"
// path. A nil slice in the map also simulates a DNS error.
type fakeMXResolver struct {
	records map[string][]*net.MX
}

func (f *fakeMXResolver) MXLookup(_ context.Context, domain string) ([]*net.MX, error) {
	recs, ok := f.records[domain]
	if !ok {
		return nil, fmt.Errorf("fakedns: no MX records for %s", domain)
	}
	if recs == nil {
		return nil, fmt.Errorf("fakedns: simulated DNS error for %s", domain)
	}
	return recs, nil
}

// makeMX constructs a []*net.MX slice from a list of hostnames.
func makeMX(hosts ...string) []*net.MX {
	out := make([]*net.MX, len(hosts))
	for i, h := range hosts {
		out[i] = &net.MX{Host: h, Pref: uint16(10 * (i + 1))}
	}
	return out
}

// newDetectProviderHarness builds a test server with the given MX resolver.
func newDetectProviderHarness(t *testing.T, resolver protoadmin.MXResolver) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		MXResolver:              resolver,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &harness{
		t: t, h: h, srv: srv, client: client, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
}

// ptrStr returns a pointer to s; convenience helper for expected provider literals.
func ptrStr(s string) *string { return &s }

// assertDetectProvider checks the HTTP status and decodes the
// {"provider":...} field of the detect-provider response body.
func assertDetectProvider(t *testing.T, resp *http.Response, body []byte, wantStatus int, wantProv *string) {
	t.Helper()
	if resp.StatusCode != wantStatus {
		t.Fatalf("want status %d got %d: %s", wantStatus, resp.StatusCode, body)
	}
	if wantStatus != http.StatusOK {
		return
	}
	var result struct {
		Provider *string `json:"provider"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	switch {
	case wantProv == nil && result.Provider == nil:
		// correct null
	case wantProv == nil && result.Provider != nil:
		t.Errorf("want null provider, got %q", *result.Provider)
	case wantProv != nil && result.Provider == nil:
		t.Errorf("want provider %q, got null", *wantProv)
	case *wantProv != *result.Provider:
		t.Errorf("want provider %q, got %q", *wantProv, *result.Provider)
	}
}

// TestClassifyMXProvider drives the MX-heuristic classifier through the
// HTTP endpoint using a single-MX fake resolver per case. This covers the
// full heuristic matrix without exposing the package-internal
// classifyMXProvider function directly.
func TestClassifyMXProvider(t *testing.T) {
	cases := []struct {
		name     string
		mxHosts  []string // MX target hostnames fed into the fake resolver
		wantProv *string  // nil means provider:null
	}{
		// --- Google ---
		{
			name:     "gmail_canonical",
			mxHosts:  []string{"aspmx.l.google.com"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "gmail_alt1",
			mxHosts:  []string{"alt1.aspmx.l.google.com"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "gmail_alt4",
			mxHosts:  []string{"alt4.aspmx.l.google.com"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "googlemail_legacy",
			mxHosts:  []string{"mx.googlemail.com"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "google_bare_domain",
			mxHosts:  []string{"google.com"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "google_uppercase",
			mxHosts:  []string{"ASPMX.L.GOOGLE.COM"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "googlemail_uppercase",
			mxHosts:  []string{"MX.GOOGLEMAIL.COM"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "google_trailing_dot",
			mxHosts:  []string{"aspmx.l.google.com."},
			wantProv: ptrStr("google"),
		},

		// --- Microsoft ---
		{
			name:     "m365_protection",
			mxHosts:  []string{"contoso-com.mail.protection.outlook.com"},
			wantProv: ptrStr("microsoft"),
		},
		{
			name:     "m365_subdomain",
			mxHosts:  []string{"acmeinc.mail.protection.outlook.com"},
			wantProv: ptrStr("microsoft"),
		},
		{
			name:     "outlook_com_subdomain",
			mxHosts:  []string{"mx1.hotmail.com.outlook.com"},
			wantProv: ptrStr("microsoft"),
		},
		{
			name:     "outlook_bare_domain",
			mxHosts:  []string{"outlook.com"},
			wantProv: ptrStr("microsoft"),
		},
		{
			name:     "mail_protection_bare",
			mxHosts:  []string{"mail.protection.outlook.com"},
			wantProv: ptrStr("microsoft"),
		},
		{
			name:     "microsoft_uppercase",
			mxHosts:  []string{"CONTOSO-COM.MAIL.PROTECTION.OUTLOOK.COM"},
			wantProv: ptrStr("microsoft"),
		},
		{
			name:     "microsoft_trailing_dot",
			mxHosts:  []string{"contoso-com.mail.protection.outlook.com."},
			wantProv: ptrStr("microsoft"),
		},

		// --- Unknown / null ---
		{
			name:     "unknown_domain",
			mxHosts:  []string{"mx.example.com"},
			wantProv: nil,
		},
		{
			name:     "empty_mx_set",
			mxHosts:  []string{},
			wantProv: nil,
		},

		// --- Multi-MX: classification stops at the first recognised host ---
		{
			name:     "multi_google_first_wins",
			mxHosts:  []string{"aspmx.l.google.com", "mx.example.com"},
			wantProv: ptrStr("google"),
		},
		{
			name:     "multi_unknown_then_microsoft",
			mxHosts:  []string{"mx.example.com", "contoso-com.mail.protection.outlook.com"},
			wantProv: ptrStr("microsoft"),
		},

		// --- Anti-spoofing: dot-boundary suffix match must reject these ---
		//
		// A naive strings.Contains check would be fooled by hostnames that
		// embed a recognised domain as a non-suffix substring. The dot-anchored
		// HasSuffix(host, ".google.com") correctly rejects all of these because
		// none of them actually ends with ".google.com" or ".googlemail.com".
		{
			name:    "spoof_google_com_as_evil_subdomain",
			mxHosts: []string{"notgoogle.com.evil.com"},
			// Ends with .evil.com, not .google.com.
			wantProv: nil,
		},
		{
			name:    "spoof_google_com_as_prefix_of_evil",
			mxHosts: []string{"google.com.attacker.net"},
			// Ends with .attacker.net.
			wantProv: nil,
		},
		{
			name:    "spoof_googlemail_as_evil_subdomain",
			mxHosts: []string{"googlemail.com.evil.com"},
			// Ends with .evil.com, not .googlemail.com.
			wantProv: nil,
		},
		{
			name:    "spoof_notgoogle_suffix",
			mxHosts: []string{"notgoogle.com"},
			// Does not end with .google.com; "notgoogle.com"[-10:] = "tgoogle.com".
			wantProv: nil,
		},
		{
			name:     "spoof_outlook_com_as_evil_subdomain",
			mxHosts:  []string{"outlook.com.evil.com"},
			wantProv: nil,
		},
		{
			name:     "spoof_mail_protection_as_evil_subdomain",
			mxHosts:  []string{"mail.protection.outlook.com.evil.net"},
			wantProv: nil,
		},
		{
			name:    "spoof_contains_google_not_suffix",
			mxHosts: []string{"evildomain-google.com"},
			// Ends with "-google.com", not ".google.com".
			wantProv: nil,
		},
	}

	const testDomain = "testdomain.example"
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeMXResolver{
				records: map[string][]*net.MX{
					testDomain: makeMX(tc.mxHosts...),
				},
			}
			h := newDetectProviderHarness(t, resolver)
			_, apiKey := h.bootstrap("admin@example.com")

			resp, body := h.doRequest(http.MethodGet,
				"/api/v1/identities/detect-provider?email=user@"+testDomain,
				apiKey, nil)
			assertDetectProvider(t, resp, body, http.StatusOK, tc.wantProv)
		})
	}
}

// TestDetectProviderHTTP covers the HTTP-level behaviour of the endpoint:
// auth gating, query-parameter validation, and lookup-failure handling.
func TestDetectProviderHTTP(t *testing.T) {
	resolver := &fakeMXResolver{
		records: map[string][]*net.MX{
			"gmail.com":       makeMX("aspmx.l.google.com", "alt1.aspmx.l.google.com"),
			"example.org":     makeMX("contoso-com.mail.protection.outlook.com"),
			"unknown.example": makeMX("mx.unknown.example"),
			// nil triggers a simulated DNS error.
			"error.example": nil,
		},
	}
	h := newDetectProviderHarness(t, resolver)
	_, apiKey := h.bootstrap("admin@example.com")

	t.Run("google_domain", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=alice@gmail.com",
			apiKey, nil)
		assertDetectProvider(t, resp, body, http.StatusOK, ptrStr("google"))
	})

	t.Run("microsoft_domain", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=bob@example.org",
			apiKey, nil)
		assertDetectProvider(t, resp, body, http.StatusOK, ptrStr("microsoft"))
	})

	t.Run("unknown_domain_returns_null", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=carol@unknown.example",
			apiKey, nil)
		assertDetectProvider(t, resp, body, http.StatusOK, nil)
	})

	t.Run("mx_lookup_error_returns_null", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=dave@error.example",
			apiKey, nil)
		assertDetectProvider(t, resp, body, http.StatusOK, nil)
	})

	t.Run("nxdomain_returns_null", func(t *testing.T) {
		// nxdomain.example is not in the resolver map (NXDOMAIN equivalent).
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=eve@nxdomain.example",
			apiKey, nil)
		assertDetectProvider(t, resp, body, http.StatusOK, nil)
	})

	t.Run("missing_email_param_returns_400", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider",
			apiKey, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("want 400 got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("email_without_at_returns_400", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=notanemail",
			apiKey, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("want 400 got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("email_with_empty_domain_returns_400", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=user@",
			apiKey, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("want 400 got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("unauthenticated_returns_401", func(t *testing.T) {
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=alice@gmail.com",
			"", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("want 401 got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("bare_domain_without_at_returns_400", func(t *testing.T) {
		// Passing a bare domain name (no @) returns 400 because it is
		// not a valid email address. The endpoint accepts ?email= only.
		resp, body := h.doRequest(http.MethodGet,
			"/api/v1/identities/detect-provider?email=gmail.com",
			apiKey, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("want 400 got %d: %s", resp.StatusCode, body)
		}
	})
}
