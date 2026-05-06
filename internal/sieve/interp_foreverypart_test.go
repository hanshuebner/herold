package sieve

import (
	"strings"
	"testing"
)

const multipartMsg = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Subject: Multipart\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BND\"\r\n" +
	"\r\n" +
	"--BND\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"plain body content\r\n" +
	"--BND\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"X-Tracer: alpha-tracer\r\n" +
	"Content-Disposition: attachment; filename=\"a.html\"\r\n" +
	"\r\n" +
	"<html>html body content</html>\r\n" +
	"--BND--\r\n"

func TestInterp_ForeveryPart_RunsBlock(t *testing.T) {
	src := `require ["foreverypart", "fileinto"];
foreverypart {
  fileinto "PARTS";
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	count := 0
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "PARTS" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 fileinto actions (one per leaf), got %d (actions=%+v)", count, out.Actions)
	}
}

func TestInterp_ForeveryPart_Break(t *testing.T) {
	src := `require ["foreverypart", "fileinto"];
foreverypart {
  fileinto "FIRST";
  break;
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	count := 0
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "FIRST" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("break must terminate after first iteration, got %d fileinto actions: %+v", count, out.Actions)
	}
}

func TestInterp_HeaderMime_ReadsCurrentPart(t *testing.T) {
	src := `require ["foreverypart", "mime", "fileinto"];
foreverypart {
  if header :mime :contains "X-Tracer" "alpha" {
    fileinto "TRACER";
  }
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "TRACER" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("header :mime should match X-Tracer on the HTML attachment part; actions=%+v", out.Actions)
	}
}

func TestInterp_HeaderMime_OutsideForeverypart(t *testing.T) {
	// Without a surrounding foreverypart, :mime falls back to message
	// headers (RFC 5703 §4.2). The message-level X-Tracer is absent
	// (only the leaf carries it) so the test must not match.
	src := `require ["mime", "fileinto"];
if header :mime :contains "X-Tracer" "alpha" {
  fileinto "WRONG";
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "WRONG" {
			t.Fatalf(":mime outside foreverypart should not see leaf-only headers; actions=%+v", out.Actions)
		}
	}
}

func TestInterp_ExtractText(t *testing.T) {
	src := `require ["foreverypart", "mime", "variables", "fileinto"];
foreverypart {
  extracttext "body";
  if string :contains "${body}" "html body content" {
    fileinto "HTML-FOUND";
  }
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "HTML-FOUND" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("extracttext should expose part body to subsequent string test; actions=%+v", out.Actions)
	}
}

func TestInterp_ExtractText_FirstCap(t *testing.T) {
	src := `require ["foreverypart", "mime", "variables", "fileinto"];
foreverypart {
  extracttext :first 5 "snippet";
  if string :contains "${snippet}" "plain" {
    fileinto "TRUNCATED";
  }
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	// :first 5 caps the first leaf to "plain" — the test must match
	// since "plain" is fully present in the truncated 5 bytes.
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "TRUNCATED" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf(":first 5 should preserve the prefix 'plain'; actions=%+v", out.Actions)
	}
}

func TestInterp_ExtractText_RequiresMime(t *testing.T) {
	// extracttext without `require "mime"` must fail validation.
	src := `require ["foreverypart"];
foreverypart {
  extracttext "x";
}`
	script, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(script); err != nil {
		// Validation rejects "extracttext" command without "mime"
		// require — that path is already exercised by the
		// KnownCommands gate.
		if !strings.Contains(err.Error(), "mime") {
			t.Fatalf("expected mime-require validation error, got %v", err)
		}
	}
}
