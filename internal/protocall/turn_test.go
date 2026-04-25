package protocall

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// TestMintCredential_HMACMatches_CoturnFormat verifies the HMAC bit-
// by-bit against an externally-computed expected value. This guards
// against silent mistakes in algorithm choice (e.g. swapping SHA-256
// for SHA-1) that coturn would reject at validate-time.
func TestMintCredential_HMACMatches_CoturnFormat(t *testing.T) {
	// Canonical example. With:
	//   secret = "topsecret"
	//   expiry = unix epoch second 1700000000
	//   principal = 4242
	// the username is "1700000000:4242" and the password is the
	// base64 of HMAC-SHA1("topsecret", "1700000000:4242").
	const (
		secret    = "topsecret"
		expiryUnx = int64(1700000000)
		principal = store.PrincipalID(4242)
	)
	wantUsername := fmt.Sprintf("%d:%d", expiryUnx, uint64(principal))
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(wantUsername))
	wantPassword := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Anchor the fake clock so expiry = clock.Now() + ttl == 1700000000.
	now := time.Unix(expiryUnx, 0).Add(-time.Hour)
	clk := clock.NewFake(now)
	s := New(Options{
		Clock: clk,
		TURN: TURNConfig{
			URIs:          []string{"turn:turn.example.com:3478?transport=udp"},
			SharedSecret:  []byte(secret),
			CredentialTTL: time.Hour,
		},
	})
	defer s.Close()

	cred, err := s.MintCredential(context.Background(), principal)
	if err != nil {
		t.Fatalf("MintCredential: %v", err)
	}
	if cred.Username != wantUsername {
		t.Fatalf("username = %q, want %q", cred.Username, wantUsername)
	}
	if cred.Password != wantPassword {
		t.Fatalf("password = %q, want %q", cred.Password, wantPassword)
	}
	if cred.TTLSeconds != int(time.Hour/time.Second) {
		t.Fatalf("ttl = %d, want %d", cred.TTLSeconds, int(time.Hour/time.Second))
	}
	if !cred.ExpiresAt.Equal(time.Unix(expiryUnx, 0)) {
		t.Fatalf("expiresAt = %s, want %s", cred.ExpiresAt, time.Unix(expiryUnx, 0))
	}
	if len(cred.URIs) != 1 || cred.URIs[0] != "turn:turn.example.com:3478?transport=udp" {
		t.Fatalf("uris = %v", cred.URIs)
	}
}

// TestMintCredential_TTLEnforced_MaxCap covers two cases: the
// DefaultCredentialTTL fallback (zero in TURNConfig.CredentialTTL)
// and the MaxCredentialTTL clamp (anything > 12h gets pinned).
func TestMintCredential_TTLEnforced_MaxCap(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	t.Run("default-when-zero", func(t *testing.T) {
		s := New(Options{
			Clock: clk,
			TURN: TURNConfig{
				URIs:          []string{"turn:t.example.com:3478"},
				SharedSecret:  []byte("k"),
				CredentialTTL: 0,
			},
		})
		defer s.Close()
		cred, err := s.MintCredential(context.Background(), 1)
		if err != nil {
			t.Fatalf("MintCredential: %v", err)
		}
		if cred.TTLSeconds != int(DefaultCredentialTTL/time.Second) {
			t.Fatalf("ttl = %d, want %d (default)", cred.TTLSeconds,
				int(DefaultCredentialTTL/time.Second))
		}
	})
	t.Run("clamps-above-max", func(t *testing.T) {
		s := New(Options{
			Clock: clk,
			TURN: TURNConfig{
				URIs:          []string{"turn:t.example.com:3478"},
				SharedSecret:  []byte("k"),
				CredentialTTL: 24 * time.Hour, // above MaxCredentialTTL
			},
		})
		defer s.Close()
		cred, err := s.MintCredential(context.Background(), 1)
		if err != nil {
			t.Fatalf("MintCredential: %v", err)
		}
		if cred.TTLSeconds != int(MaxCredentialTTL/time.Second) {
			t.Fatalf("ttl = %d, want %d (clamped)", cred.TTLSeconds,
				int(MaxCredentialTTL/time.Second))
		}
	})
	t.Run("rejects-when-no-uris", func(t *testing.T) {
		s := New(Options{
			Clock: clk,
			TURN:  TURNConfig{SharedSecret: []byte("k")},
		})
		defer s.Close()
		_, err := s.MintCredential(context.Background(), 1)
		if err == nil || !strings.Contains(err.Error(), "TURN") {
			t.Fatalf("err = %v, want TURN-disabled", err)
		}
	})
	t.Run("rejects-when-no-secret", func(t *testing.T) {
		s := New(Options{
			Clock: clk,
			TURN: TURNConfig{
				URIs: []string{"turn:t.example.com:3478"},
			},
		})
		defer s.Close()
		_, err := s.MintCredential(context.Background(), 1)
		if err == nil || !strings.Contains(err.Error(), "secret") {
			t.Fatalf("err = %v, want missing-secret", err)
		}
	})
}
