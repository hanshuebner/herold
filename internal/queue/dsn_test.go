package queue

// White-box unit tests for the DSN builder (buildDSN). These live in
// the queue package (not queue_test) so they can call the unexported
// buildDSN and dsnInput directly without exporting them.

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// TestBuildDSN_NoEmptyParts asserts that every MIME part produced by
// buildDSN has non-empty body content (fixes issue #41: an empty
// message/rfc822-headers part appeared as an empty attachment chip).
func TestBuildDSN_NoEmptyParts(t *testing.T) {
	cases := []struct {
		name  string
		input dsnInput
	}{
		{
			name: "failure without original headers",
			input: dsnInput{
				Kind:           DSNKindFailure,
				ReportingMTA:   "dns; mail.example.test",
				From:           "postmaster@mail.example.test",
				To:             "alice@sender.test",
				FinalRcpt:      "bob@dest.test",
				DiagnosticCode: "smtp; 550 5.1.1 no such user",
				StatusCode:     "5.1.1",
				Now:            testNow,
				// OriginalHeaders intentionally left nil.
			},
		},
		{
			name: "delay without original headers",
			input: dsnInput{
				Kind:           DSNKindDelay,
				ReportingMTA:   "dns; mail.example.test",
				From:           "postmaster@mail.example.test",
				To:             "alice@sender.test",
				FinalRcpt:      "bob@dest.test",
				WillRetryUntil: testNow.Add(48 * time.Hour),
				StatusCode:     "4.0.0",
				Now:            testNow,
			},
		},
		{
			name: "success without original headers",
			input: dsnInput{
				Kind:         DSNKindSuccess,
				ReportingMTA: "dns; mail.example.test",
				From:         "postmaster@mail.example.test",
				To:           "alice@sender.test",
				FinalRcpt:    "bob@dest.test",
				StatusCode:   "2.0.0",
				Now:          testNow,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := buildDSN(tc.input)
			if err != nil {
				t.Fatalf("buildDSN: %v", err)
			}
			msg := string(raw)

			// Extract the boundary from the Content-Type header.
			boundary := extractBoundary(t, msg)
			if boundary == "" {
				t.Fatalf("could not extract multipart boundary from DSN:\n%s", msg)
			}

			// Split into parts; skip the first element (outer headers).
			delimiter := "--" + boundary
			parts := strings.Split(msg, delimiter)
			// parts[0] = outer headers
			// parts[1..n-1] = MIME parts
			// parts[n] = "--\r\n" (closing delimiter suffix)
			if len(parts) < 3 {
				t.Fatalf("expected at least 2 MIME parts, got %d raw segments:\n%s", len(parts)-1, msg)
			}

			for i, part := range parts[1:] {
				if strings.HasPrefix(part, "--") {
					// Closing delimiter fragment — not a content part.
					continue
				}
				// Each part must not have an empty body.
				// A MIME part body is separated from its headers by \r\n\r\n.
				sep := "\r\n\r\n"
				idx := strings.Index(part, sep)
				if idx < 0 {
					t.Errorf("part %d: no header/body separator found", i+1)
					continue
				}
				body := part[idx+len(sep):]
				// Strip any trailing boundary material.
				body = strings.TrimRight(body, "\r\n")
				if len(body) == 0 {
					headers := part[:idx]
					t.Errorf("part %d has empty body (headers: %q)", i+1, headers)
				}
			}
		})
	}
}

// TestBuildDSN_WithHeaders asserts that when OriginalHeaders is
// non-empty the message/rfc822-headers part IS present and contains the
// supplied bytes.
func TestBuildDSN_WithHeaders(t *testing.T) {
	origHdrs := []byte("From: alice@sender.test\r\nSubject: hello\r\n")
	raw, err := buildDSN(dsnInput{
		Kind:            DSNKindFailure,
		ReportingMTA:    "dns; mail.example.test",
		From:            "postmaster@mail.example.test",
		To:              "alice@sender.test",
		FinalRcpt:       "bob@dest.test",
		DiagnosticCode:  "smtp; 550 5.1.1 no such user",
		StatusCode:      "5.1.1",
		OriginalHeaders: origHdrs,
		Now:             testNow,
	})
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	msg := string(raw)
	if !strings.Contains(msg, "message/rfc822-headers") {
		t.Error("expected message/rfc822-headers part when OriginalHeaders is set")
	}
	if !strings.Contains(msg, "From: alice@sender.test") {
		t.Errorf("expected original header content in DSN\n--BODY--\n%s\n--END--", msg)
	}
}

// TestBuildDSN_WithoutHeaders asserts that when OriginalHeaders is nil
// the message/rfc822-headers part is absent entirely (fixes issue #41).
func TestBuildDSN_WithoutHeaders(t *testing.T) {
	raw, err := buildDSN(dsnInput{
		Kind:           DSNKindFailure,
		ReportingMTA:   "dns; mail.example.test",
		From:           "postmaster@mail.example.test",
		To:             "alice@sender.test",
		FinalRcpt:      "bob@dest.test",
		DiagnosticCode: "smtp; 550 5.1.1 no such user",
		StatusCode:     "5.1.1",
		// OriginalHeaders nil.
		Now: testNow,
	})
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	msg := string(raw)
	if strings.Contains(msg, "message/rfc822-headers") {
		t.Errorf("message/rfc822-headers part must be absent when OriginalHeaders is nil\n--BODY--\n%s\n--END--", msg)
	}
}

// TestBuildDSN_ValidMultipartReport checks the outer structure of a
// generated DSN: correct Content-Type, report-type parameter, and
// closing delimiter.
func TestBuildDSN_ValidMultipartReport(t *testing.T) {
	raw, err := buildDSN(dsnInput{
		Kind:           DSNKindFailure,
		ReportingMTA:   "dns; mail.example.test",
		From:           "postmaster@mail.example.test",
		To:             "alice@sender.test",
		FinalRcpt:      "bob@dest.test",
		DiagnosticCode: "smtp; 550 5.1.1 no such user",
		StatusCode:     "5.1.1",
		Now:            testNow,
	})
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	msg := string(raw)

	for _, want := range []string{
		"Content-Type: multipart/report",
		"report-type=delivery-status",
		"MIME-Version: 1.0",
		"Auto-Submitted: auto-replied",
		"text/plain",
		"message/delivery-status",
		"Action: failed",
		"Final-Recipient: rfc822;bob@dest.test",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("DSN missing %q\n--BODY--\n%s\n--END--", want, msg)
		}
	}

	// Closing MIME delimiter must be present.
	boundary := extractBoundary(t, msg)
	if boundary != "" && !strings.Contains(msg, "--"+boundary+"--") {
		t.Errorf("DSN missing closing delimiter --%s--", boundary)
	}
}

// extractBoundary parses the multipart boundary from the Content-Type
// header in the DSN message. Returns "" when not found; the caller
// skips boundary-dependent checks.
func extractBoundary(t *testing.T, msg string) string {
	t.Helper()
	const prefix = `boundary="`
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return ""
	}
	rest := msg[idx+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}
