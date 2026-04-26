package vapid

import (
	"strings"
	"testing"
	"time"
)

func TestSignVAPIDJWT_RoundTrip(t *testing.T) {
	t.Parallel()
	kp, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mgr := NewWithKey(kp)

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	tok, err := mgr.SignVAPIDJWT("https://fcm.googleapis.com", now, exp, "mailto:hans@example.org")
	if err != nil {
		t.Fatalf("SignVAPIDJWT: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	header, claims, err := VerifyVAPIDJWT(tok, &kp.Private.PublicKey)
	if err != nil {
		t.Fatalf("VerifyVAPIDJWT: %v", err)
	}
	if got, _ := header["alg"].(string); got != "ES256" {
		t.Fatalf("alg=%q want ES256", got)
	}
	if got, _ := header["typ"].(string); got != "JWT" {
		t.Fatalf("typ=%q want JWT", got)
	}
	if got, _ := claims["aud"].(string); got != "https://fcm.googleapis.com" {
		t.Fatalf("aud=%q", got)
	}
	if got, _ := claims["sub"].(string); got != "mailto:hans@example.org" {
		t.Fatalf("sub=%q", got)
	}
	gotExp, _ := claims["exp"].(float64)
	if int64(gotExp) != exp.Unix() {
		t.Fatalf("exp=%v want %v", int64(gotExp), exp.Unix())
	}
}

func TestSignVAPIDJWT_CapsExpiry(t *testing.T) {
	t.Parallel()
	kp, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mgr := NewWithKey(kp)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Caller asks for 48h — must be capped at 24h.
	asked := now.Add(48 * time.Hour)
	tok, err := mgr.SignVAPIDJWT("https://push.example.com", now, asked, "mailto:op@example.com")
	if err != nil {
		t.Fatalf("SignVAPIDJWT: %v", err)
	}
	_, claims, err := VerifyVAPIDJWT(tok, &kp.Private.PublicKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	gotExp := int64(claims["exp"].(float64))
	maxExp := now.Add(MaxJWTExpiry).Unix()
	if gotExp != maxExp {
		t.Fatalf("exp=%d want capped at %d", gotExp, maxExp)
	}
}

func TestSignVAPIDJWT_RejectsBadInputs(t *testing.T) {
	t.Parallel()
	kp, err := Generate(nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mgr := NewWithKey(kp)
	now := time.Now()
	cases := []struct {
		name, aud, sub string
		exp            time.Time
	}{
		{"empty audience", "", "mailto:o@e.com", now.Add(time.Hour)},
		{"path in audience", "https://fcm.googleapis.com/fcm/send", "mailto:o@e.com", now.Add(time.Hour)},
		{"bad scheme", "wss://push.example.com", "mailto:o@e.com", now.Add(time.Hour)},
		{"empty sub", "https://push.example.com", "", now.Add(time.Hour)},
		{"bad sub scheme", "https://push.example.com", "ftp://op@example.com", now.Add(time.Hour)},
		{"exp in past", "https://push.example.com", "mailto:o@e.com", now.Add(-time.Minute)},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := mgr.SignVAPIDJWT(c.aud, now, c.exp, c.sub); err == nil {
				t.Fatalf("want error for %q", c.name)
			}
		})
	}
}

func TestAudienceFromEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://fcm.googleapis.com/fcm/send/AAA-BBB", "https://fcm.googleapis.com"},
		{"https://updates.push.services.mozilla.com/wpush/v2/abc", "https://updates.push.services.mozilla.com"},
		{"https://web.push.apple.com/A/B/C", "https://web.push.apple.com"},
	}
	for _, c := range cases {
		got, err := AudienceFromEndpoint(c.in)
		if err != nil {
			t.Fatalf("AudienceFromEndpoint(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("AudienceFromEndpoint(%q)=%q want %q", c.in, got, c.want)
		}
	}
	if _, err := AudienceFromEndpoint("not-a-url"); err == nil {
		t.Fatalf("expected error on malformed URL")
	}
}

func TestSignVAPIDJWT_RequiresConfiguredKey(t *testing.T) {
	t.Parallel()
	mgr := New()
	_, err := mgr.SignVAPIDJWT("https://push.example.com",
		time.Now(), time.Now().Add(time.Hour), "mailto:o@e.com")
	if err == nil {
		t.Fatalf("expected ErrNotConfigured")
	}
}
