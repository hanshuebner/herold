package authsession_test

import (
	"encoding/base64"
	"testing"

	"github.com/hanshuebner/herold/internal/authsession"
)

func TestNewCSRFToken_Length(t *testing.T) {
	tok := authsession.NewCSRFToken()
	if tok == "" {
		t.Fatal("NewCSRFToken returned empty string")
	}
	// 24 raw bytes -> 32 base64url characters (no padding).
	decoded, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		t.Fatalf("NewCSRFToken result is not valid base64url: %v (tok=%q)", err, tok)
	}
	if len(decoded) != 24 {
		t.Errorf("decoded token length=%d, want 24", len(decoded))
	}
}

func TestNewCSRFToken_Uniqueness(t *testing.T) {
	// Two successive calls must not return the same token (statistical
	// guarantee with 192-bit entropy; collision probability ~ 2^-192).
	a := authsession.NewCSRFToken()
	b := authsession.NewCSRFToken()
	if a == b {
		t.Errorf("NewCSRFToken returned identical tokens on two calls: %q", a)
	}
}
