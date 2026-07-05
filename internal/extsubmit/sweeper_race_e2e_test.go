package extsubmit

// sweeper_race_e2e_test.go verifies the sweeper race-condition fix described
// in issue #131 against a real SQLite store and a deterministic fake OAuth
// IdP. It proves that when:
//
//  1. An identity row has state=auth-failed with a stale RefreshDue and an
//     old (now-revoked) refresh token.
//  2. handleOAuthCallback has already written state=ok with fresh tokens and
//     a newer StateAt to the store.
//  3. The sweeper's worker calls refreshRow using the stale row.
//
// The guard introduced in refreshRow re-reads the current store row and skips
// the auth-failed write because the store already has a fresher ok state.
// The test drives a real extsubmit.Refresher against the fake IdP so the
// "refresh fails" outcome is a genuine invalid_grant 400 response from the
// revoked refresh token, not a stubbed error.
//
// Unlike the unit tests in sweeper_test.go (which use an in-memory fakeStore),
// this test crosses the real SQLite store boundary, making it an integration
// test that matches CI's test surface.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testfakes/fakeidp"
)

// sweeperRaceDataKey is a 32-byte AEAD key used to seal tokens in this test.
var sweeperRaceDataKey = func() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*11 + 5)
	}
	return key
}()

// sealRaceToken seals plaintext using sweeperRaceDataKey.
func sealRaceToken(t *testing.T, pt string) []byte {
	t.Helper()
	ct, err := secrets.Seal(sweeperRaceDataKey, []byte(pt))
	if err != nil {
		t.Fatalf("sealRaceToken: %v", err)
	}
	return ct
}

// discardSlogLogger returns a slog.Logger that discards all output.
func discardSlogLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

// TestSweeper_RefreshRowGuard_RealStore exercises the sweeper race guard
// against a real SQLite store and a real fake OAuth IdP (so the refresh
// failure is a genuine invalid_grant HTTP 400 response from a revoked
// refresh token, not a stubbed error).
//
// Scenario:
//  1. Obtain tokens T1 from fakeidp via the authorization_code grant.
//  2. Write a stale submission row to SQLite: state=auth-failed, StateAt=T0,
//     RefreshDue=past, OAuthRefreshCT=sealed(T1.RefreshToken).
//  3. Simulate what handleOAuthCallback writes: obtain tokens T2, write
//     state=ok, StateAt=T1 (T1 > T0), OAuthRefreshCT=sealed(T2.RefreshToken).
//  4. Revoke T1.RefreshToken on fakeidp (delete from its valid-refresh set).
//  5. Call refreshRow with the stale row (StateAt=T0, OAuthRefreshCT=T1).
//     The Refresher tries T1.RefreshToken against fakeidp -> 400 invalid_grant.
//     The guard re-reads the store, sees state=ok with StateAt=T1 > T0 -> skip.
//  6. Assert SQLite still has state=ok.
func TestSweeper_RefreshRowGuard_RealStore(t *testing.T) {
	const identityID = "race-e2e-identity"

	// Start a real fake OAuth IdP.
	idp := fakeidp.New(t, fakeidp.Options{AccessTTL: time.Hour})

	// Open a real SQLite store in a temp directory.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "race_e2e.sqlite")
	clk := clock.NewReal()
	st, err := storesqlite.Open(context.Background(), dbPath, discardSlogLogger(), clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()

	// Seed the store: insert a principal and JMAP identity so the FK chain for
	// identity_submission is satisfied. identity_submission -> jmap_identities ->
	// principals; no domain is required by principals.
	princ, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@race.test",
		CreatedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: princ.ID,
		Name:        "Race Test Identity",
		Email:       "alice@race.test",
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	t0 := time.Now().Add(-10 * time.Minute) // "old" StateAt (when auth-failed was set)
	t1 := time.Now()                        // "now" StateAt (when OAuth callback wrote ok)

	// Seal the fakeidp's client secret so the Refresher can authenticate at the
	// token endpoint. Without this, the request fails as invalid_client (wrong
	// secret) instead of invalid_grant (revoked refresh token), which would still
	// trigger the guard but is less faithful to the production scenario.
	clientSecretCT := sealRaceToken(t, idp.ClientSecret())

	// Step 1: issue tokens T1 from fakeidp via the authorization_code grant.
	tok1 := idp.Authenticate(t, "http://localhost/callback", "state-race-1", "")

	// Step 2: write the stale auth-failed row the sweeper would have captured
	// before the OAuth re-auth completed.
	staleRow := store.IdentitySubmission{
		IdentityID:          identityID,
		SubmitHost:          "smtp.ext.test",
		SubmitPort:          587,
		SubmitSecurity:      "starttls",
		SubmitAuthMethod:    "oauth2",
		OAuthRefreshCT:      sealRaceToken(t, tok1.RefreshToken),
		OAuthAccessCT:       sealRaceToken(t, tok1.AccessToken),
		OAuthTokenEndpoint:  idp.TokenURL(),
		OAuthClientID:       idp.ClientID(),
		OAuthClientSecretCT: clientSecretCT,
		RefreshDue:          t0.Add(-1 * time.Minute), // stale / overdue
		State:               store.IdentitySubmissionStateAuthFailed,
		StateAt:             t0,
	}
	if err := st.Meta().UpsertIdentitySubmission(ctx, staleRow); err != nil {
		t.Fatalf("UpsertIdentitySubmission (stale row): %v", err)
	}

	// Step 3: simulate what handleOAuthCallback writes after the user completes
	// OAuth re-auth: fresh tokens T2, state=ok, newer StateAt=T1.
	tok2 := idp.Authenticate(t, "http://localhost/callback", "state-race-2", "")
	freshRow := staleRow
	freshRow.OAuthRefreshCT = sealRaceToken(t, tok2.RefreshToken)
	freshRow.OAuthAccessCT = sealRaceToken(t, tok2.AccessToken)
	freshRow.State = store.IdentitySubmissionStateOK
	freshRow.StateAt = t1
	freshRow.RefreshDue = t1.Add(55 * time.Minute) // far future — not due again
	if err := st.Meta().UpsertIdentitySubmission(ctx, freshRow); err != nil {
		t.Fatalf("UpsertIdentitySubmission (fresh row): %v", err)
	}

	// Step 4: revoke T1's refresh token on the fake IdP. Any subsequent
	// grant_type=refresh_token request for it will return 400 invalid_grant.
	idp.RevokeRefreshToken(tok1.RefreshToken)

	// Step 5: build a real Sweeper with a real Refresher pointed at fakeidp
	// and the real SQLite store, then call refreshRow with the stale row.
	sw := &Sweeper{
		Store: st.Meta(),
		TokenRefresh: &Refresher{
			Meta:    st.Meta(),
			DataKey: sweeperRaceDataKey,
		},
		DataKey: sweeperRaceDataKey,
		Logger:  discardSlogLogger(),
		Now:     func() time.Time { return t1 },
	}

	sw.refreshRow(ctx, staleRow)

	// Step 6: the store must still hold state=ok. The guard in refreshRow saw
	// that the current row had StateAt=T1 > staleRow.StateAt=T0 and state=ok,
	// so it skipped writing auth-failed.
	got, err := st.Meta().GetIdentitySubmission(ctx, identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission after refreshRow: %v", err)
	}
	if got.State != store.IdentitySubmissionStateOK {
		t.Errorf("State = %q after sweeper race; want ok (guard must not overwrite fresher callback state)", got.State)
	}
}
