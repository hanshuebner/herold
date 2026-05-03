package protoadmin_test

// routes_test.go verifies that RegisterSelfServiceRoutes mounts exactly the
// expected self-service paths and that admin-only paths are NOT registered.
//
// The test strategy is to call RegisterSelfServiceRoutes on a fresh
// http.ServeMux and send probe requests through httptest.NewRecorder. Routes
// that exist return any status except 404 (the handler runs, even if it
// returns 401 for lack of auth); routes that are absent return 404 or 405.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// probeStatus sends a request with the given method and path to mux via
// httptest and returns the response status code.
func probeStatus(mux http.Handler, method, path string) int {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// TestRegisterSelfServiceRoutes_ExpectedPathsArePresent verifies that every
// self-service endpoint returns a non-404 status when queried (they may
// return 401 for missing auth or 400/422 for missing body — both indicate
// the route is registered).
func TestRegisterSelfServiceRoutes_ExpectedPathsArePresent(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{})

	mux := http.NewServeMux()
	srv.RegisterSelfServiceRoutes(mux)

	// Routes expected to be present. The exact HTTP status is not the
	// assertion — any status other than 404 means the route is registered.
	cases := []struct {
		method string
		path   string
	}{
		// Health (unauthenticated).
		{"GET", "/api/v1/healthz/live"},
		{"GET", "/api/v1/healthz/ready"},
		// Principal self-service.
		{"GET", "/api/v1/principals/1"},
		{"PATCH", "/api/v1/principals/1"},
		{"PUT", "/api/v1/principals/1/password"},
		// TOTP.
		{"POST", "/api/v1/principals/1/totp/enroll"},
		{"POST", "/api/v1/principals/1/totp/confirm"},
		{"DELETE", "/api/v1/principals/1/totp"},
		// API key management.
		{"GET", "/api/v1/api-keys"},
		{"DELETE", "/api/v1/api-keys/42"},
		{"POST", "/api/v1/principals/1/api-keys"},
		// OIDC identity links.
		{"GET", "/api/v1/principals/1/oidc-links"},
		{"POST", "/api/v1/principals/1/oidc-links/begin"},
		{"DELETE", "/api/v1/principals/1/oidc-links/someprovider"},
		// Spam-classifier feedback signal (Wave 3.15).
		{"POST", "/api/v1/spam-feedback"},
	}

	for _, tc := range cases {
		status := probeStatus(mux, tc.method, tc.path)
		if status == http.StatusNotFound {
			t.Errorf("self-service route missing: %s %s returned 404", tc.method, tc.path)
		}
	}
}

// TestRegisterSelfServiceRoutes_AdminOnlyPathsAbsent verifies that paths
// reserved for the admin surface are NOT registered on the self-service mux
// (they must return 404 so the public listener cannot accidentally expose
// admin functionality).
func TestRegisterSelfServiceRoutes_AdminOnlyPathsAbsent(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{})

	mux := http.NewServeMux()
	srv.RegisterSelfServiceRoutes(mux)

	// Routes that must NOT be registered on the self-service mux.
	adminOnlyPaths := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/queue"},
		{"GET", "/api/v1/queue/stats"},
		{"GET", "/api/v1/queue/123"},
		{"GET", "/api/v1/certs"},
		{"GET", "/api/v1/audit"},
		{"GET", "/api/v1/domains"},
		{"POST", "/api/v1/domains"},
		{"GET", "/api/v1/aliases"},
		{"GET", "/api/v1/principals"},
		{"POST", "/api/v1/principals"},
		{"GET", "/api/v1/spam/policy"},
		{"GET", "/api/v1/server/status"},
		{"GET", "/api/v1/oidc/providers"},
		{"GET", "/api/v1/webhooks"},
		{"POST", "/api/v1/bootstrap"},
		{"POST", "/api/v1/auth/login"},
		{"POST", "/api/v1/auth/logout"},
		{"GET", "/api/v1/auth/whoami"},
		{"POST", "/api/v1/oidc/callback"},
	}

	for _, tc := range adminOnlyPaths {
		status := probeStatus(mux, tc.method, tc.path)
		if status != http.StatusNotFound {
			t.Errorf("admin-only route exposed on self-service mux: %s %s returned %d (want 404)",
				tc.method, tc.path, status)
		}
	}
}
