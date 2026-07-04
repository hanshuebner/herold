package fakeidp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/testfakes/fakeidp"
)

// TestAuthenticate_FullDance verifies the authorization-code flow produces
// deterministic tokens and that the refresh grant issues a fresh access token.
func TestAuthenticate_FullDance(t *testing.T) {
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	idp := fakeidp.New(t, fakeidp.Options{
		Now:       func() time.Time { return fixed },
		AccessTTL: 90 * time.Minute,
	})

	tok := idp.Authenticate(t, "http://localhost/callback", "state-xyz", "scope-a")
	if !strings.HasPrefix(tok.AccessToken, "access-") {
		t.Errorf("access token = %q; want access-* prefix", tok.AccessToken)
	}
	if !strings.HasPrefix(tok.RefreshToken, "refresh-") {
		t.Errorf("refresh token = %q; want refresh-* prefix", tok.RefreshToken)
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("token_type = %q; want Bearer", tok.TokenType)
	}
	if tok.ExpiresIn != int((90 * time.Minute).Seconds()) {
		t.Errorf("expires_in = %d; want 5400", tok.ExpiresIn)
	}

	// A refresh yields a new access token but keeps the refresh token valid.
	refreshed := idp.Refresh(t, tok.RefreshToken)
	if refreshed.AccessToken == tok.AccessToken {
		t.Errorf("refresh returned the same access token %q", refreshed.AccessToken)
	}
	if refreshed.RefreshToken != tok.RefreshToken {
		t.Errorf("refresh rotated the refresh token: %q -> %q", tok.RefreshToken, refreshed.RefreshToken)
	}
}

// TestAuthenticate_DistinctCodes verifies two flows issue distinct tokens
// (the monotonic counter advances), proving there is no accidental reuse.
func TestAuthenticate_DistinctCodes(t *testing.T) {
	idp := fakeidp.New(t, fakeidp.Options{})
	a := idp.Authenticate(t, "http://localhost/cb", "s1", "")
	b := idp.Authenticate(t, "http://localhost/cb", "s2", "")
	if a.AccessToken == b.AccessToken {
		t.Errorf("two flows returned the same access token %q", a.AccessToken)
	}
}
