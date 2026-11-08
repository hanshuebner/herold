package protosend_test

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"testing"
)

// TestSend_EnvelopeIsBareAddrSpec: display-name and comment recipients
// are queued as the bare addr-spec, bcc stays out of the headers, and the
// source becomes the bare addr-spec MAIL FROM (re #562).
func TestSend_EnvelopeIsBareAddrSpec(t *testing.T) {
	h := newSendHarness(t)
	body := map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses":  []string{"Bob <bob@dest.test>", "carol@dest.test (hi)"},
			"bccAddresses": []string{"\"Dan\" <dan@dest.test>"},
		},
		"message": map[string]any{"subject": "Hello", "body": map[string]any{"text": "Hi"}},
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
	c := h.q.Calls()[0]
	want := []string{"bob@dest.test", "carol@dest.test", "dan@dest.test"}
	if len(c.Recipients) != len(want) {
		t.Fatalf("Recipients=%q", c.Recipients)
	}
	for i := range want {
		if c.Recipients[i] != want[i] {
			t.Errorf("Recipients[%d]=%q want %q", i, c.Recipients[i], want[i])
		}
	}
	if c.MailFrom != "alice@example.test" {
		t.Errorf("MailFrom=%q", c.MailFrom)
	}
	if bytes.Contains(h.q.Bodies()[0], []byte("dan@dest.test")) {
		t.Errorf("bcc address leaked into message headers")
	}
}

// TestSend_ParameterInjectionRecipientNeverEnvelope: the comment-borne
// NOTIFY injection is reduced to the addr-spec; no queued recipient
// carries anything but the bare address.
func TestSend_ParameterInjectionRecipientNeverEnvelope(t *testing.T) {
	h := newSendHarness(t)
	body := map[string]any{
		"source":      "alice@example.test",
		"destination": map[string]any{"toAddresses": []string{"bob@dest.test (a> NOTIFY=NEVER <b)"}},
		"message":     map[string]any{"subject": "Hello", "body": map[string]any{"text": "Hi"}},
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res.StatusCode == http.StatusOK {
		c := h.q.Calls()[0]
		if len(c.Recipients) != 1 || c.Recipients[0] != "bob@dest.test" {
			t.Fatalf("Recipients=%q; want bare addr-spec", c.Recipients)
		}
		return
	}
	if res.StatusCode != http.StatusBadRequest || len(h.q.Calls()) != 0 {
		t.Fatalf("status=%d body=%s calls=%d", res.StatusCode, buf, len(h.q.Calls()))
	}
}

// TestSendRaw_DestinationsAreBareAddrSpec: send-raw destinations get the
// same treatment; an unparsable one is refused.
func TestSendRaw_DestinationsAreBareAddrSpec(t *testing.T) {
	raw := "From: alice@example.test\r\nTo: bob@dest.test\r\nSubject: raw\r\n\r\nx\r\n"
	enc := base64.StdEncoding.EncodeToString([]byte(raw))
	h := newSendHarness(t)
	res, buf := h.doRequest("POST", "/api/v1/mail/send-raw", h.apiKey, map[string]any{
		"destinations": []string{"Bob <bob@dest.test>", "x@dest.test (a> NOTIFY=NEVER <b)"},
		"rawMessage":   enc,
	})
	if res.StatusCode == http.StatusOK {
		for _, r := range h.q.Calls()[0].Recipients {
			if r != "bob@dest.test" && r != "x@dest.test" {
				t.Errorf("recipient %q is not a bare addr-spec", r)
			}
		}
	} else if len(h.q.Calls()) != 0 {
		t.Fatalf("status=%d body=%s but queue called", res.StatusCode, buf)
	}
	h2 := newSendHarness(t)
	res, _ = h2.doRequest("POST", "/api/v1/mail/send-raw", h2.apiKey, map[string]any{
		"destinations": []string{"not an address"}, "rawMessage": enc,
	})
	if res.StatusCode != http.StatusBadRequest || len(h2.q.Calls()) != 0 {
		t.Fatalf("garbage destination: status=%d calls=%d", res.StatusCode, len(h2.q.Calls()))
	}
}
