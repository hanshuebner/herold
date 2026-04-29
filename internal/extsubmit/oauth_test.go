package extsubmit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// trackingMeta is a minimal extsubmit.SubmissionUpsertStore that records
// UpsertIdentitySubmission calls for assertion.
type trackingMeta struct {
	mu       sync.Mutex
	upserted []store.IdentitySubmission
}

func (m *trackingMeta) UpsertIdentitySubmission(_ context.Context, sub store.IdentitySubmission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserted = append(m.upserted, sub)
	return nil
}

func (m *trackingMeta) last() (store.IdentitySubmission, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.upserted) == 0 {
		return store.IdentitySubmission{}, false
	}
	return m.upserted[len(m.upserted)-1], true
}

// TestRefresher_HappyPath verifies that Refresh contacts the token endpoint,
// seals the new access token, and calls UpsertIdentitySubmission.
func TestRefresher_HappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" {
			http.Error(w, "bad grant_type", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "new-access-token-001",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "new-refresh-token-001",
		})
	}))
	defer ts.Close()

	meta := &trackingMeta{}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	refresher := &extsubmit.Refresher{
		Meta:    meta,
		DataKey: testDataKey,
		Now:     func() time.Time { return now },
	}

	sub := store.IdentitySubmission{
		IdentityID:         "identity-refresh",
		SubmitAuthMethod:   "oauth2",
		OAuthAccessCT:      sealSecret(t, "old-access-token"),
		OAuthRefreshCT:     sealSecret(t, "old-refresh-token"),
		OAuthTokenEndpoint: ts.URL,
		OAuthClientID:      "client-id-test",
	}
	creds := extsubmit.OAuthClientCredentials{
		ClientID:      "client-id-test",
		ClientSecret:  "client-secret-test",
		TokenEndpoint: ts.URL,
	}

	accessToken, err := refresher.Refresh(context.Background(), sub, creds)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if accessToken != "new-access-token-001" {
		t.Errorf("access token = %q; want new-access-token-001", accessToken)
	}

	// Verify the store was updated.
	got, ok := meta.last()
	if !ok {
		t.Fatal("UpsertIdentitySubmission was not called")
	}
	// The sealed new access token must decrypt to the new value.
	pt, err := secrets.Open(testDataKey, got.OAuthAccessCT)
	if err != nil {
		t.Fatalf("open new access CT: %v", err)
	}
	if string(pt) != "new-access-token-001" {
		t.Errorf("sealed access token decrypts to %q; want new-access-token-001", string(pt))
	}
	if got.State != store.IdentitySubmissionStateOK {
		t.Errorf("state after refresh = %q; want ok", got.State)
	}
}

// TestRefresher_AuthFailed verifies that a 4xx from the token endpoint returns
// ErrAuthFailed and does not call UpsertIdentitySubmission.
func TestRefresher_AuthFailed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "Token has been expired or revoked",
		})
	}))
	defer ts.Close()

	meta := &trackingMeta{}
	refresher := &extsubmit.Refresher{Meta: meta, DataKey: testDataKey}

	sub := store.IdentitySubmission{
		IdentityID:         "identity-auth-fail-refresh",
		SubmitAuthMethod:   "oauth2",
		OAuthRefreshCT:     sealSecret(t, "expired-refresh"),
		OAuthTokenEndpoint: ts.URL,
	}
	creds := extsubmit.OAuthClientCredentials{
		ClientID: "client-id", ClientSecret: "secret", TokenEndpoint: ts.URL,
	}

	_, err := refresher.Refresh(context.Background(), sub, creds)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, extsubmit.ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed, got: %v", err)
	}

	// UpsertIdentitySubmission must NOT have been called on auth failure.
	if _, ok := meta.last(); ok {
		t.Error("UpsertIdentitySubmission must not be called on auth failure")
	}
}

// TestRefresher_ServerError verifies that a 5xx from the token endpoint is
// returned as a non-ErrAuthFailed error.
func TestRefresher_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "server_error"})
	}))
	defer ts.Close()

	meta := &trackingMeta{}
	refresher := &extsubmit.Refresher{Meta: meta, DataKey: testDataKey}

	sub := store.IdentitySubmission{
		IdentityID:         "identity-server-err",
		SubmitAuthMethod:   "oauth2",
		OAuthRefreshCT:     sealSecret(t, "some-refresh"),
		OAuthTokenEndpoint: ts.URL,
	}
	creds := extsubmit.OAuthClientCredentials{
		ClientID: "client-id", ClientSecret: "secret", TokenEndpoint: ts.URL,
	}

	_, err := refresher.Refresh(context.Background(), sub, creds)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// A 5xx is not an auth failure.
	if errors.Is(err, extsubmit.ErrAuthFailed) {
		t.Errorf("5xx server error should not be ErrAuthFailed, got: %v", err)
	}
}
