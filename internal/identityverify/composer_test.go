package identityverify

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestGenerateToken_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 16; i++ {
		tok, raw, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		if len(tok) != 43 {
			t.Fatalf("token length: got %d, want 43 (base64url(32 bytes) without padding)", len(tok))
		}
		// Decode and verify the raw bytes round-trip.
		dec, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("base64url decode: %v", err)
		}
		if len(dec) != rawTokenBytes {
			t.Fatalf("decoded length: got %d, want %d", len(dec), rawTokenBytes)
		}
		if string(dec) != string(raw) {
			t.Fatalf("raw vs decoded mismatch: %x vs %x", raw, dec)
		}
		// Alphabet: base64url uses [A-Za-z0-9_-], no padding.
		for j, c := range tok {
			switch {
			case 'A' <= c && c <= 'Z':
			case 'a' <= c && c <= 'z':
			case '0' <= c && c <= '9':
			case c == '-' || c == '_':
			default:
				t.Fatalf("token[%d]=%q not in base64url alphabet (token=%q)", j, c, tok)
			}
		}
	}
}

func TestGenerateToken_NotPredictable(t *testing.T) {
	// Two consecutive tokens should never collide. Birthday probability
	// against 256 bits of entropy is astronomically low; this catches
	// reader regressions (e.g. returning a constant or zero buffer).
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		tok, _, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token %q within 64 draws — entropy source broken?", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestGenerateToken_EntropyError(t *testing.T) {
	want := errors.New("entropy starved")
	_, _, err := generateTokenFrom(func(b []byte) (int, error) { return 0, want })
	if err == nil {
		t.Fatalf("expected error from broken entropy reader")
	}
	if !errors.Is(err, want) {
		t.Fatalf("error chain: got %v, want wrap of %v", err, want)
	}
}

func TestGenerateToken_ShaInvariant(t *testing.T) {
	// The store stores sha256(raw ASCII token). The callback handler
	// in protoadmin/identity_verify.go hashes the URL query verbatim
	// (the same ASCII), so the contract is symmetric.
	tok, _, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	a := sha256.Sum256([]byte(tok))
	b := sha256.Sum256([]byte(tok))
	if a != b {
		t.Fatalf("sha256 non-deterministic: %x vs %x", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("sha256 output length: got %d, want 32", len(a))
	}
}

func TestGenerateCode_FormatAndAlphabet(t *testing.T) {
	for i := 0; i < 32; i++ {
		c, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		if len(c) != codeDigits {
			t.Fatalf("code length: got %d, want %d (code=%q)", len(c), codeDigits, c)
		}
		for j, r := range c {
			if r < '0' || r > '9' {
				t.Fatalf("code[%d]=%q not a decimal digit (code=%q)", j, r, c)
			}
		}
	}
}

func TestGenerateCode_PreservesLeadingZeros(t *testing.T) {
	// Force the rand source to always return 0; the resulting code
	// must be "000000" (not "0" with leading zeros stripped, not a
	// short string). REQ-IDENT-32: leading zeros preserved.
	zero := func(_ io.Reader, _ *big.Int) (*big.Int, error) {
		return big.NewInt(0), nil
	}
	c, err := generateCodeFrom(zero)
	if err != nil {
		t.Fatalf("generateCodeFrom: %v", err)
	}
	if c != "000000" {
		t.Fatalf("zero-source code: got %q, want %q", c, "000000")
	}
}

func TestGenerateCode_EntropyError(t *testing.T) {
	want := errors.New("entropy starved")
	bad := func(_ io.Reader, _ *big.Int) (*big.Int, error) { return nil, want }
	_, err := generateCodeFrom(bad)
	if err == nil {
		t.Fatalf("expected error from broken entropy source")
	}
	if !errors.Is(err, want) {
		t.Fatalf("error chain: got %v, want wrap of %v", err, want)
	}
}

func TestCompose_RequiredFieldValidation(t *testing.T) {
	base := ComposeInput{
		From:           "postmaster@example.com",
		To:             "alice@external.test",
		Token:          "abc123",
		Code:           "654321",
		Hostname:       "mail.example.com",
		InitiatorEmail: "alice@example.com",
		Now:            time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
	}
	cases := []struct {
		name string
		mut  func(*ComposeInput)
	}{
		{"empty From", func(in *ComposeInput) { in.From = "" }},
		{"empty To", func(in *ComposeInput) { in.To = "" }},
		{"empty Token", func(in *ComposeInput) { in.Token = "" }},
		{"empty Code", func(in *ComposeInput) { in.Code = "" }},
		{"empty Hostname", func(in *ComposeInput) { in.Hostname = "" }},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mut(&in)
			_, err := Compose(in)
			if err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestCompose_RendersHeadersAndBody(t *testing.T) {
	in := ComposeInput{
		From:           "postmaster@example.com",
		To:             "alice@external.test",
		Token:          "tok-abc",
		Code:           "012345",
		Hostname:       "mail.example.com",
		InitiatorEmail: "alice@example.com",
		Now:            time.Date(2026, 5, 11, 12, 34, 56, 0, time.UTC),
	}
	out, err := Compose(in)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if out.MessageID == "" {
		t.Fatalf("MessageID is empty")
	}
	if out.Subject != "Verify your email address" {
		t.Fatalf("Subject: got %q", out.Subject)
	}

	// Parse the rendered bytes as an RFC 5322 message to confirm we
	// produced a structurally valid envelope.
	msg, err := mail.ReadMessage(strings.NewReader(string(out.Bytes)))
	if err != nil {
		t.Fatalf("mail.ReadMessage: %v\nbody=%s", err, out.Bytes)
	}
	if got := msg.Header.Get("From"); got != in.From {
		t.Errorf("From: got %q, want %q", got, in.From)
	}
	if got := msg.Header.Get("To"); got != in.To {
		t.Errorf("To: got %q, want %q", got, in.To)
	}
	if got := msg.Header.Get("Subject"); got != "Verify your email address" {
		t.Errorf("Subject header: got %q", got)
	}
	if got := msg.Header.Get("Auto-Submitted"); got != "auto-generated" {
		t.Errorf("Auto-Submitted: got %q", got)
	}
	if got := msg.Header.Get("X-Herold-Identity-Verification"); got != "1" {
		t.Errorf("X-Herold-Identity-Verification: got %q", got)
	}
	if got := msg.Header.Get("MIME-Version"); got != "1.0" {
		t.Errorf("MIME-Version: got %q", got)
	}
	if ct := msg.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/alternative") {
		t.Errorf("Content-Type: got %q, want multipart/alternative", ct)
	}

	// Body checks: the click link AND the code MUST appear verbatim
	// in the rendered bytes. Mail clients that strip HTML still see
	// the plain-text alternative; clients that strip plain-text are
	// rare in 2026 but the HTML alternative also has both.
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	wantLink := "https://mail.example.com/verify-identity?token=tok-abc"
	if !strings.Contains(string(body), wantLink) {
		t.Errorf("body missing callback link %q", wantLink)
	}
	// Code appears in both parts; count both. Each text part includes
	// it at least once.
	if strings.Count(string(body), "012345") < 2 {
		t.Errorf("body missing code in both parts: %q", body)
	}
	// Initiator email should appear in the body for the "initiated
	// by" line (REQ-IDENT-33).
	if !strings.Contains(string(body), in.InitiatorEmail) {
		t.Errorf("body missing initiator email %q", in.InitiatorEmail)
	}
}

func TestCompose_FallsBackOnEmptyInitiator(t *testing.T) {
	// REQ-IDENT-33 requires the initiator marker; when the resolver
	// could not fetch the principal row the composer falls back to a
	// sentinel so the body is still well-formed and audit-trailed.
	in := ComposeInput{
		From:           "postmaster@example.com",
		To:             "alice@external.test",
		Token:          "tok-abc",
		Code:           "012345",
		Hostname:       "mail.example.com",
		InitiatorEmail: "",
		Now:            time.Date(2026, 5, 11, 12, 34, 56, 0, time.UTC),
	}
	out, err := Compose(in)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "(unknown)") {
		t.Errorf("body missing initiator fallback marker; bytes=%s", out.Bytes)
	}
}

func TestCompose_HTMLEscapesUserInput(t *testing.T) {
	// Defence in depth: the To/initiator are typically just email
	// addresses, but if a hostile principal stuffed angle brackets
	// into one (the JMAP validator already rejects malformed ones),
	// the HTML part MUST escape them so the rendered message does
	// not break out of the markup. Plain-text alternative is
	// inherently safe (no markup interpretation), so we only assert
	// against the HTML alternative segment.
	in := ComposeInput{
		From:           "postmaster@example.com",
		To:             "<bad>@external.test",
		Token:          "tok-abc",
		Code:           "012345",
		Hostname:       "mail.example.com",
		InitiatorEmail: "<script>alert(1)</script>@evil",
		Now:            time.Date(2026, 5, 11, 12, 34, 56, 0, time.UTC),
	}
	out, err := Compose(in)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	// Split body at the HTML part boundary marker and inspect only
	// the segment after the text/html content-type header.
	htmlIdx := strings.Index(string(out.Bytes), "Content-Type: text/html")
	if htmlIdx < 0 {
		t.Fatalf("could not locate HTML alternative in body: %s", out.Bytes)
	}
	htmlPart := string(out.Bytes)[htmlIdx:]
	if strings.Contains(htmlPart, "<script>alert(1)</script>") {
		t.Errorf("HTML part did not escape <script>: %s", htmlPart)
	}
	if !strings.Contains(htmlPart, "&lt;script&gt;") {
		t.Errorf("HTML part missing escaped form: %s", htmlPart)
	}
}
