package authsession_test

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/store"
)

var testKey = []byte("authsession-test-key-32bytes-xxxx")

func makeSession(pid store.PrincipalID, csrf string, exp time.Time) authsession.Session {
	return authsession.Session{
		PrincipalID: pid,
		ExpiresAt:   exp,
		CSRFToken:   csrf,
		Scopes:      auth.NewScopeSet(auth.ScopeAdmin),
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exp := now.Add(24 * time.Hour)
	sess := makeSession(42, "tok-abc", exp)

	wire := authsession.EncodeSession(sess, testKey)
	if wire == "" {
		t.Fatal("EncodeSession returned empty string")
	}

	got, err := authsession.DecodeSession(wire, testKey, now)
	if err != nil {
		t.Fatalf("DecodeSession: %v", err)
	}
	if got.PrincipalID != sess.PrincipalID {
		t.Errorf("PrincipalID: got %d, want %d", got.PrincipalID, sess.PrincipalID)
	}
	if !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, sess.ExpiresAt)
	}
	if got.CSRFToken != sess.CSRFToken {
		t.Errorf("CSRFToken: got %q, want %q", got.CSRFToken, sess.CSRFToken)
	}
}

func TestDecodeSession_WrongKey(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	sess := makeSession(7, "csrf-xyz", exp)
	wire := authsession.EncodeSession(sess, testKey)

	wrongKey := []byte("wrong-key-for-verifying-32bytes-!")
	_, err := authsession.DecodeSession(wire, wrongKey, now)
	if err != authsession.ErrSessionInvalid {
		t.Errorf("expected ErrSessionInvalid, got %v", err)
	}
}

func TestDecodeSession_Expired(t *testing.T) {
	issueAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exp := issueAt.Add(time.Hour)
	sess := makeSession(9, "csrf-exp", exp)
	wire := authsession.EncodeSession(sess, testKey)

	afterExp := exp.Add(time.Second)
	_, err := authsession.DecodeSession(wire, testKey, afterExp)
	if err != authsession.ErrSessionExpired {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}
}

func TestDecodeSession_Malformed(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []string{
		"",
		"not.enough.parts",
		"a.b.c.d.e.f", // too many parts
	}
	for _, c := range cases {
		_, err := authsession.DecodeSession(c, testKey, now)
		if err != authsession.ErrSessionInvalid {
			t.Errorf("input %q: expected ErrSessionInvalid, got %v", c, err)
		}
	}
}

func TestDecodeSession_Tampered(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	sess := makeSession(5, "csrf-ok", exp)
	wire := authsession.EncodeSession(sess, testKey)

	// Flip a character in the payload (before the sig).
	tampered := []byte(wire)
	tampered[0] ^= 1
	_, err := authsession.DecodeSession(string(tampered), testKey, now)
	if err != authsession.ErrSessionInvalid {
		t.Errorf("expected ErrSessionInvalid for tampered cookie, got %v", err)
	}
}
