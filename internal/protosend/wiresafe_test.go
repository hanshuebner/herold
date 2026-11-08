package protosend_test

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"testing"
)

// TestSend_CustomHeaderWithLineBreakRejected: a CR or LF in a caller
// supplied header value or tag value is a header-injection input and is
// rejected with a validation problem; nothing reaches the queue.
func TestSend_CustomHeaderWithLineBreakRejected(t *testing.T) {
	for name, val := range map[string]string{
		"lf":   "a\nb",
		"crlf": "a\r\nBcc: victim@dest.test",
		"cr":   "a\rb",
	} {
		for _, where := range []string{"header", "tag"} {
			t.Run(where+"-"+name, func(t *testing.T) {
				h := newSendHarness(t)
				bodyObj := map[string]any{"text": "Hi"}
				body := map[string]any{
					"source":      "alice@example.test",
					"destination": map[string]any{"toAddresses": []string{"bob@dest.test"}},
				}
				if where == "header" {
					bodyObj["headers"] = map[string]string{"X-Foo": val}
				} else {
					body["tags"] = []map[string]string{{"name": "campaign", "value": val}}
				}
				body["message"] = map[string]any{"subject": "Hello", "body": bodyObj}
				res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
				if res.StatusCode != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", res.StatusCode, buf)
				}
				if !bytes.Contains(buf, []byte("validation-failed")) {
					t.Errorf("not a validation problem: %s", buf)
				}
				if n := len(h.q.Calls()); n != 0 {
					t.Errorf("queue received %d submissions", n)
				}
			})
		}
	}
}

// TestSend_CustomHeaderNameWithLineBreakRejected: a header name carrying
// CR/LF or a colon is rejected as well.
func TestSend_CustomHeaderNameWithLineBreakRejected(t *testing.T) {
	for _, name := range []string{"X-Foo\nBcc", "X-Foo: x\r\nBcc", "X Foo"} {
		h := newSendHarness(t)
		body := map[string]any{
			"source":      "alice@example.test",
			"destination": map[string]any{"toAddresses": []string{"bob@dest.test"}},
			"message": map[string]any{"subject": "Hello", "body": map[string]any{
				"text": "Hi", "headers": map[string]string{name: "v"}}},
		}
		res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q: status=%d body=%s", name, res.StatusCode, buf)
		}
	}
}

// TestSendRaw_BareLFNormalisedToCRLF: a raw message uploaded with LF line
// endings reaches the queue with CRLF only.
func TestSendRaw_BareLFNormalisedToCRLF(t *testing.T) {
	h := newSendHarness(t)
	raw := "From: alice@example.test\nTo: bob@dest.test\nSubject: raw\n\nline one\nline two\n"
	body := map[string]any{
		"destinations": []string{"bob@dest.test"},
		"rawMessage":   base64.StdEncoding.EncodeToString([]byte(raw)),
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send-raw", h.apiKey, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	bodies := h.q.Bodies()
	if len(bodies) != 1 {
		t.Fatalf("bodies=%d", len(bodies))
	}
	b := bodies[0]
	if bytes.Count(b, []byte("\n")) != bytes.Count(b, []byte("\r\n")) {
		t.Errorf("bare LF reached the queue: %q", b)
	}
	if !bytes.Contains(b, []byte("line one\r\nline two\r\n")) {
		t.Errorf("body not preserved: %q", b)
	}
}
