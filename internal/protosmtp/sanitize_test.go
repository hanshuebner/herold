package protosmtp

// sanitize_test.go covers the header-value sanitizer used to defend the
// stored-message Received: and Authentication-Results: headers from
// CRLF-injection via attacker-controlled wire input (HELO, remote IP,
// AR-rendered domain fields). The wire parser strips CRLF from a single
// ReadLine, but a future code path that hands raw bytes into render-side
// helpers (or a backend like ARC verify storing attacker-supplied
// domains in mailauth.AuthResults) would bypass that protection. The
// sanitizer is the structural backstop.

import (
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

func TestSanitizeHeaderValue_StripsCRLFAndControls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "client.example.test", "client.example.test"},
		{"crlf injection", "evil\r\nX-Injected: yes", "evil__X-Injected: yes"},
		{"bare cr", "a\rb", "a_b"},
		{"bare lf", "a\nb", "a_b"},
		{"nul byte", "a\x00b", "a_b"},
		{"tab", "a\tb", "a_b"},
		{"space preserved", "domain with space", "domain with space"},
		{"high-ascii", "a\xffb", "a_b"},
		{"utf8 dropped (non-printable-ascii policy)", "café", "caf__"},
		{"empty", "", ""},
		{"all printable", "abcXYZ.0-9_", "abcXYZ.0-9_"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeHeaderValue(c.in)
			if got != c.want {
				t.Fatalf("sanitizeHeaderValue(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.ContainsAny(got, "\r\n\x00") {
				t.Fatalf("sanitized output still contains CR/LF/NUL: %q", got)
			}
		})
	}
}

func TestSanitizeHeaderValue_LengthCap(t *testing.T) {
	in := strings.Repeat("a", maxHeaderFieldLen+500)
	got := sanitizeHeaderValue(in)
	if len(got) != maxHeaderFieldLen {
		t.Fatalf("length cap not enforced: got %d, want %d", len(got), maxHeaderFieldLen)
	}
}

// TestRenderReceived_HELOInjectionDefused proves a doctored HELO
// containing CRLF + a forged header line cannot inject extra headers
// into the Received: rendering. This is the concrete "Received: header
// injection via unchecked HELO" case from the Wave-4 security review:
// `bufio.Reader.ReadLine` strips CRLF at the wire, but if a future
// caller feeds a raw bytes-in-string into sess.helo (e.g. from an
// ESMTP parameter, AUTH parsing, or a future channel-binding hook),
// the rendering layer must structurally refuse to emit those bytes.
func TestRenderReceived_HELOInjectionDefused(t *testing.T) {
	// Hand-build a minimal session sufficient for renderReceived. We
	// avoid the full Server constructor — renderReceived only reads
	// sess.helo, sess.remoteIP, sess.isEHLO, sess.tlsEstablished,
	// sess.tlsVersion, sess.tlsCipherSuite, and sess.srv.opts.Hostname /
	// sess.srv.clk.
	clk := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	srv := &Server{
		opts: Options{Hostname: "mx.example.test"},
		clk:  clk,
	}
	sess := &session{
		srv:            srv,
		helo:           "client.example.test\r\nX-Injected: yes",
		remoteIP:       "203.0.113.7",
		isEHLO:         true,
		tlsEstablished: true,
		tlsVersion:     tls.VersionTLS13,
		tlsCipherSuite: tls.TLS_AES_128_GCM_SHA256,
	}

	got := sess.renderReceived()
	if strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("renderReceived must not contain CR/LF; got: %q", got)
	}
	if strings.Contains(got, "X-Injected:") {
		// The literal substring would still survive because '-' and ':'
		// are printable; what we MUST prevent is a CRLF before it. The
		// CR/LF check above is the structural guarantee. Nonetheless,
		// double-check the bytes preceding "X-Injected" are sanitized.
		idx := strings.Index(got, "X-Injected:")
		if idx >= 2 && got[idx-2] == '_' && got[idx-1] == '_' {
			// CRLF replaced by '__' — defused.
		} else {
			t.Fatalf("X-Injected: not preceded by sanitized CRLF: %q", got)
		}
	}

	// Also assert the sanitized HELO is present in the from-clause.
	// The CRLF was replaced with two underscores; the space inside the
	// forged "X-Injected: yes" remains but no longer terminates a line.
	wantHELO := "client.example.test__X-Injected: yes"
	if !strings.Contains(got, "from "+wantHELO) {
		t.Fatalf("renderReceived missing sanitized HELO in from-clause: %q", got)
	}
}

// TestAssembleStoredBytes_HELOInjectionDefused checks the full path:
// a doctored HELO routed through assembleStoredBytes (the function
// that prepends Received + Authentication-Results to the body bytes
// the store persists) must not break the body separator with an
// injected CRLF. The assembly layer is the last gate before
// store.Blobs.Put.
func TestAssembleStoredBytes_HELOInjectionDefused(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	srv := &Server{
		opts: Options{Hostname: "mx.example.test", AuthservID: "mx.example.test"},
		clk:  clk,
	}
	sess := &session{
		srv:      srv,
		helo:     "evil\r\nX-Injected: yes",
		remoteIP: "203.0.113.7",
		isEHLO:   true,
	}
	body := []byte("From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: hi\r\n\r\nbody.\r\n")
	out := sess.assembleStoredBytes(body, "")

	// Find the body separator (first CRLF CRLF) and assert no
	// "X-Injected:" occurs before it. That string would only appear
	// before the body if the sanitizer failed to neutralise the CRLF
	// within sess.helo.
	sep := strings.Index(string(out), "\r\n\r\n")
	if sep < 0 {
		t.Fatalf("no body separator in assembled output: %q", out)
	}
	headers := string(out[:sep])
	// Count newlines in the rendered prepended header. Exactly one CRLF
	// is expected (the one assembleStoredBytes adds after Received) plus
	// any internal CRLFs in the body's existing headers up to the sep.
	// The injected CRLF would create a freshly visible "X-Injected:"
	// header at column 0 of a header line; that would mean the bytes
	// "X-Injected:" appear in `headers` AND are preceded by CRLF.
	if idx := strings.Index(headers, "\r\nX-Injected:"); idx >= 0 {
		t.Fatalf("CRLF-injected header survived sanitization: %q", headers)
	}
}
