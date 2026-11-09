package protosend_test

import (
	"encoding/base64"
	"net/http"
	"testing"
)

// TestSend_SubmissionIsNotInteractive (re #565): mail queued through the
// HTTP send API is automation, not the user's own interactive send, so the
// queue row must not carry InteractiveSubmission; the loopback deliverer
// then delivers a self-addressed send-API message unread. The submission
// stays AuthenticatedLocal (re #559).
func TestSend_SubmissionIsNotInteractive(t *testing.T) {
	h := newSendHarness(t)
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, map[string]any{
		"source":      "alice@example.test",
		"destination": map[string]any{"toAddresses": []string{"alice@example.test"}},
		"message":     map[string]any{"subject": "Release", "body": map[string]any{"text": "notes"}},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	c := h.q.Calls()[0]
	if c.InteractiveSubmission {
		t.Errorf("send: InteractiveSubmission = true")
	}
	if !c.AuthenticatedLocal {
		t.Errorf("send: AuthenticatedLocal = false")
	}
}

func TestSendRaw_SubmissionIsNotInteractive(t *testing.T) {
	raw := "From: alice@example.test\r\nTo: alice@example.test\r\nSubject: raw\r\n\r\nx\r\n"
	h := newSendHarness(t)
	res, buf := h.doRequest("POST", "/api/v1/mail/send-raw", h.apiKey, map[string]any{
		"destinations": []string{"alice@example.test"},
		"rawMessage":   base64.StdEncoding.EncodeToString([]byte(raw)),
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	c := h.q.Calls()[0]
	if c.InteractiveSubmission {
		t.Errorf("send-raw: InteractiveSubmission = true")
	}
	if !c.AuthenticatedLocal {
		t.Errorf("send-raw: AuthenticatedLocal = false")
	}
}
