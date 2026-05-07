package storefts

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// TestExtractor_PerAttachmentCap_Fires_AndCountsTruncated drives a
// multipart message with one large HTML attachment past the
// PerAttachmentMaxBytes ceiling and verifies (a) the indexed text is
// bounded, (b) FTSAttachmentExtractedTotal{format=html,
// outcome=truncated_attachment} increments by exactly one. The
// per-message cap is set high so the per-attachment branch is the
// one exercised.
func TestExtractor_PerAttachmentCap_Fires_AndCountsTruncated(t *testing.T) {
	observe.RegisterFTSMetrics(nil)
	const perAttachCap = 500
	before := testutil.ToFloat64(observe.FTSAttachmentExtractedTotal.WithLabelValues("html", "truncated_attachment"))

	bigHTML := "<html><body>" + strings.Repeat("CONTENT-TOKEN ", 200) + "</body></html>"
	if len(bigHTML) <= perAttachCap {
		t.Fatalf("test fixture too small to exercise cap: html=%d cap=%d", len(bigHTML), perAttachCap)
	}
	raw := buildMultipart(t, multipartParts{
		Plain:          "short body",
		HTMLAttachment: bigHTML,
	})

	e := NewMailparseExtractor()
	e.PerAttachmentMaxBytes = perAttachCap
	e.PerMessageMaxBytes = 1 << 20 // generous; only the per-attachment cap should fire

	got, err := e.Extract(context.Background(), store.Message{}, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	tail := tailAfter(got, "short body")
	// appendAttachmentText writes a "\n\n" separator before each
	// appended attachment chunk; the cap bounds the chunk content,
	// not the leading separator. Allow up to len("\n\n") slack.
	if len(tail) > perAttachCap+len("\n\n") {
		t.Errorf("attachment chunk not capped at %d (+sep); got %d bytes", perAttachCap, len(tail))
	}
	after := testutil.ToFloat64(observe.FTSAttachmentExtractedTotal.WithLabelValues("html", "truncated_attachment"))
	if delta := after - before; delta != 1 {
		t.Errorf("truncated_attachment counter delta = %v; want 1", delta)
	}
}

// TestExtractor_PerMessageCap_Fires_AndCountsTruncated installs a
// per-message budget below the cumulative size of two HTML
// attachments. The first attachment fits in full; the second is
// truncated mid-extraction. The metric records
// outcome=truncated_message for the second attachment, and the
// running total is at most PerMessageMaxBytes after the body anchor.
func TestExtractor_PerMessageCap_Fires_AndCountsTruncated(t *testing.T) {
	observe.RegisterFTSMetrics(nil)
	const perMsgCap = 800
	beforeMsg := testutil.ToFloat64(observe.FTSAttachmentExtractedTotal.WithLabelValues("html", "truncated_message"))

	htmlA := "<html><body>" + strings.Repeat("AAA-TOKEN ", 60) + "</body></html>"
	htmlB := "<html><body>" + strings.Repeat("BBB-TOKEN ", 60) + "</body></html>"
	raw := buildTwoHTMLMultipart(t, "anchor body", htmlA, htmlB)

	e := NewMailparseExtractor()
	e.PerAttachmentMaxBytes = 1 << 20 // generous; only the per-message cap should fire
	e.PerMessageMaxBytes = perMsgCap

	got, err := e.Extract(context.Background(), store.Message{}, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	tail := tailAfter(got, "anchor body")
	// Two attachment chunks land after the anchor; each is preceded
	// by a "\n\n" separator (4 bytes total across both chunks).
	if len(tail) > perMsgCap+2*len("\n\n") {
		t.Errorf("attachment cumulative output not capped at %d (+seps); got %d bytes", perMsgCap, len(tail))
	}
	afterMsg := testutil.ToFloat64(observe.FTSAttachmentExtractedTotal.WithLabelValues("html", "truncated_message"))
	if delta := afterMsg - beforeMsg; delta < 1 {
		t.Errorf("truncated_message counter must increment when per-message cap fires; delta=%v", delta)
	}
}

// TestExtractor_LoadOneHundredMessages exercises the extractor under
// a small load (100 messages, each with two attachments) and asserts
// every Extract call returns within the bounded text envelope. This
// is a smoke test rather than a benchmark; it pins the absence of
// runaway allocations or per-message regressions when caps are
// engaged.
func TestExtractor_LoadOneHundredMessages(t *testing.T) {
	observe.RegisterFTSMetrics(nil)
	const perAttachCap = 800
	const perMsgCap = 1500
	htmlBase := "<html><body>" + strings.Repeat("LOAD-TOKEN ", 200) + "</body></html>"
	raw := buildTwoHTMLMultipart(t, "anchor body", htmlBase, htmlBase)

	e := NewMailparseExtractor()
	e.PerAttachmentMaxBytes = perAttachCap
	e.PerMessageMaxBytes = perMsgCap

	for i := 0; i < 100; i++ {
		got, err := e.Extract(context.Background(), store.Message{}, bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("Extract iter=%d: %v", i, err)
		}
		tail := tailAfter(got, "anchor body")
		// Two attachments × "\n\n" separator each = 4 bytes slack.
		if len(tail) > perMsgCap+2*len("\n\n") {
			t.Fatalf("iter=%d: cumulative attachment output exceeded perMsgCap+seps (%d > %d)", i, len(tail), perMsgCap+2*len("\n\n"))
		}
	}
}

// tailAfter returns the substring of s following the first occurrence
// of anchor, or "" if anchor is absent. Used by the cap tests so the
// assertion focuses on the attachment portion of the indexed string,
// not the body anchor + separators.
func tailAfter(s, anchor string) string {
	idx := strings.Index(s, anchor)
	if idx < 0 {
		return ""
	}
	return s[idx+len(anchor):]
}

// buildTwoHTMLMultipart synthesises a multipart/mixed message with
// the supplied text body and two HTML attachments. Used by the
// per-message cap tests so the cumulative HTML output crosses the
// configured budget.
func buildTwoHTMLMultipart(t *testing.T, plain, htmlA, htmlB string) []byte {
	t.Helper()
	const boundary = "BOUNDARY-storefts-cap-test"
	var buf bytes.Buffer
	buf.WriteString("From: sender@example.com\r\n")
	buf.WriteString("To: recipient@example.com\r\n")
	buf.WriteString("Subject: caps test\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString(plain)
	buf.WriteString("\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	buf.WriteString("Content-Disposition: attachment; filename=\"a.html\"\r\n\r\n")
	buf.WriteString(htmlA)
	buf.WriteString("\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	buf.WriteString("Content-Disposition: attachment; filename=\"b.html\"\r\n\r\n")
	buf.WriteString(htmlB)
	buf.WriteString("\r\n")
	buf.WriteString("--" + boundary + "--\r\n")
	return buf.Bytes()
}
