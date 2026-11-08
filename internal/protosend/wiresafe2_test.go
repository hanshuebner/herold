package protosend_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestSend_RejectsUnsafeHeaderBoundInput: injection through address
// comments, control characters, and unbreakable over-long tokens are all
// rejected with 400 and nothing reaches the queue.
func TestSend_RejectsUnsafeHeaderBoundInput(t *testing.T) {
	long := strings.Repeat("a", 3000)
	cases := map[string]func(body map[string]any){
		"to-comment-lf": func(b map[string]any) {
			b["destination"] = map[string]any{"toAddresses": []string{"bob@dest.test (c\nBcc: v@x.test)"}}
		},
		"to-comment-cr": func(b map[string]any) {
			b["destination"] = map[string]any{"toAddresses": []string{"bob@dest.test (c\rBcc: v@x.test)"}}
		},
		"cc-comment-lf": func(b map[string]any) {
			b["destination"] = map[string]any{
				"toAddresses": []string{"bob@dest.test"},
				"ccAddresses": []string{"c@dest.test (c\nBcc: v@x.test)"}}
		},
		"bcc-comment-lf": func(b map[string]any) {
			b["destination"] = map[string]any{
				"toAddresses":  []string{"bob@dest.test"},
				"bccAddresses": []string{"c@dest.test (c\nBcc: v@x.test)"}}
		},
		"source-lf": func(b map[string]any) { b["source"] = "alice@example.test (c\nBcc: v@x.test)" },
		"header-long-token": func(b map[string]any) {
			b["message"].(map[string]any)["body"].(map[string]any)["headers"] = map[string]string{"X-Foo": long}
		},
		"header-nul": func(b map[string]any) {
			b["message"].(map[string]any)["body"].(map[string]any)["headers"] = map[string]string{"X-Foo": "a\x00b"}
		},
		"header-c0": func(b map[string]any) {
			b["message"].(map[string]any)["body"].(map[string]any)["headers"] = map[string]string{"X-Foo": "a\x07b"}
		},
		"tag-long-token": func(b map[string]any) {
			b["tags"] = []map[string]string{{"name": "campaign", "value": long}}
		},
		"subject-long-token": func(b map[string]any) { b["message"].(map[string]any)["subject"] = long },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newSendHarness(t)
			body := map[string]any{
				"source":      "alice@example.test",
				"destination": map[string]any{"toAddresses": []string{"bob@dest.test"}},
				"message": map[string]any{"subject": "Hello",
					"body": map[string]any{"text": "Hi"}},
			}
			mutate(body)
			res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d body=%.200s", res.StatusCode, buf)
			}
			if n := len(h.q.Calls()); n != 0 {
				t.Errorf("queue received %d submissions", n)
			}
		})
	}
}

// TestSend_HeaderTabAndNormalAddressAccepted: HTAB in a header value and a
// display-name address are still accepted and rendered from the parsed
// address.
func TestSend_HeaderTabAndNormalAddressAccepted(t *testing.T) {
	h := newSendHarness(t)
	body := map[string]any{
		"source":      "alice@example.test",
		"destination": map[string]any{"toAddresses": []string{"Bob <bob@dest.test>"}},
		"message": map[string]any{"subject": "Hello",
			"body": map[string]any{"text": "Hi", "headers": map[string]string{"X-Foo": "a\tb"}}},
	}
	res, buf := h.doRequest("POST", "/api/v1/mail/send", h.apiKey, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, buf)
	}
}
