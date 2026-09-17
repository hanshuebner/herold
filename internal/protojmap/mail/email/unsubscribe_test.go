package email_test

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
)

// unsubscribeMessage builds a raw RFC 5322 message carrying the given
// List-Unsubscribe / List-Unsubscribe-Post header values (either may be
// empty to omit the header) and inserts it into f's inbox, returning
// the Email id in JMAP wire form.
func unsubscribeMessage(t *testing.T, f *fixture, listUnsubscribe, listUnsubscribePost string) string {
	t.Helper()
	var raw string
	raw += "From: newsletter@sender.example\r\n"
	raw += "To: rcpt@example.test\r\n"
	raw += "Subject: Newsletter\r\n"
	if listUnsubscribe != "" {
		raw += "List-Unsubscribe: " + listUnsubscribe + "\r\n"
	}
	if listUnsubscribePost != "" {
		raw += "List-Unsubscribe-Post: " + listUnsubscribePost + "\r\n"
	}
	raw += "Content-Type: text/plain\r\n\r\nHello.\r\n"
	msg := f.insertMessage(t, raw, "Newsletter", "newsletter@sender.example", "rcpt@example.test", nil, "")
	return fmt.Sprintf("%d", msg.ID)
}

// invokeUnsubscribe calls Email/unsubscribe and decodes the response.
func invokeUnsubscribe(t *testing.T, f *fixture, emailID string) struct {
	EmailID    string `json:"emailId"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"httpStatus"`
	Error      string `json:"error"`
} {
	t.Helper()
	name, raw := f.invoke(t, "Email/unsubscribe", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emailId":   emailID,
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailUnsubscribe)
	if name != "Email/unsubscribe" {
		t.Fatalf("got response %q: %s", name, raw)
	}
	var resp struct {
		EmailID    string `json:"emailId"`
		Status     string `json:"status"`
		HTTPStatus int    `json:"httpStatus"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	return resp
}

// insecureTLSClient returns an *http.Client that trusts srv's
// self-signed certificate (httptest.NewTLSServer) without weakening
// the production guarded client's TLS posture -- production code never
// takes this path (see SetUnsubscribeClient's doc comment).
func insecureTLSClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
}

// testUnsubscribeSuite runs the full Email/unsubscribe acceptance
// matrix (issue #412) against a fixture built by newFixture, so the
// same scenarios run against both storesqlite (TestEmailUnsubscribe_
// SQLite) and storepg (TestEmailUnsubscribe_Postgres, HEROLD_PG_DSN
// gated) backends.
func testUnsubscribeSuite(t *testing.T, newFixture func(t *testing.T) *fixture) {
	t.Run("Success", func(t *testing.T) {
		f := newFixture(t)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
			}
			if _, ok := r.Header["Cookie"]; ok {
				t.Errorf("request carried a Cookie header")
			}
			if ref := r.Header.Get("Referer"); ref != "" {
				t.Errorf("Referer = %q, want empty", ref)
			}
			body := make([]byte, 64)
			n, _ := r.Body.Read(body)
			if got := string(body[:n]); got != "List-Unsubscribe=One-Click" {
				t.Errorf("body = %q, want List-Unsubscribe=One-Click", got)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		email.SetUnsubscribeClient(f.registry, insecureTLSClient(5*time.Second))

		id := unsubscribeMessage(t, f, "<"+srv.URL+">", "List-Unsubscribe=One-Click")
		resp := invokeUnsubscribe(t, f, id)
		if resp.Status != "ok" {
			t.Fatalf("status = %q, want ok (error=%q)", resp.Status, resp.Error)
		}
		if resp.HTTPStatus != 200 {
			t.Fatalf("httpStatus = %d, want 200", resp.HTTPStatus)
		}
		if resp.Error != "" {
			t.Fatalf("error = %q, want empty on success", resp.Error)
		}
		if resp.EmailID != id {
			t.Fatalf("emailId = %q, want %q", resp.EmailID, id)
		}
	})

	t.Run("NonTwoXX", func(t *testing.T) {
		f := newFixture(t)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		email.SetUnsubscribeClient(f.registry, insecureTLSClient(5*time.Second))

		id := unsubscribeMessage(t, f, "<"+srv.URL+">", "List-Unsubscribe=One-Click")
		resp := invokeUnsubscribe(t, f, id)
		if resp.Status != "failed" {
			t.Fatalf("status = %q, want failed", resp.Status)
		}
		if resp.HTTPStatus != 500 {
			t.Fatalf("httpStatus = %d, want 500", resp.HTTPStatus)
		}
		if resp.Error == "" {
			t.Fatalf("error should be populated on a non-2xx response")
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		f := newFixture(t)
		release := make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
			w.WriteHeader(http.StatusOK)
		}))
		defer func() {
			close(release)
			srv.Close()
		}()
		email.SetUnsubscribeClient(f.registry, insecureTLSClient(150*time.Millisecond))

		id := unsubscribeMessage(t, f, "<"+srv.URL+">", "List-Unsubscribe=One-Click")
		resp := invokeUnsubscribe(t, f, id)
		if resp.Status != "failed" {
			t.Fatalf("status = %q, want failed", resp.Status)
		}
		if resp.HTTPStatus != 0 {
			t.Fatalf("httpStatus = %d, want 0 (no response received)", resp.HTTPStatus)
		}
		if resp.Error != "timeout" {
			t.Fatalf("error = %q, want %q", resp.Error, "timeout")
		}
	})

	t.Run("CleartextRefused", func(t *testing.T) {
		f := newFixture(t)
		id := unsubscribeMessage(t, f,
			"<mailto:off@sender.example>, <http://sender.example/unsubscribe>",
			"List-Unsubscribe=One-Click")
		resp := invokeUnsubscribe(t, f, id)
		if resp.Status != "unsupported" {
			t.Fatalf("status = %q, want unsupported (REQ-UNS-04)", resp.Status)
		}
		if resp.HTTPStatus != 0 {
			t.Fatalf("httpStatus = %d, want 0 (no request attempted)", resp.HTTPStatus)
		}
		if resp.Error == "" {
			t.Fatalf("error should explain why the message is unsupported")
		}
	})

	t.Run("SSRFBlocked", func(t *testing.T) {
		f := newFixture(t)
		// No SetUnsubscribeClient override here: exercises the real
		// production extimg.NewGuardedClient path and its default
		// (AllowPrivate=false) deny list, which refuses 127.0.0.1
		// before any TLS handshake is attempted.
		id := unsubscribeMessage(t, f, "<https://127.0.0.1/unsubscribe>", "List-Unsubscribe=One-Click")
		resp := invokeUnsubscribe(t, f, id)
		if resp.Status != "failed" {
			t.Fatalf("status = %q, want failed", resp.Status)
		}
		if resp.HTTPStatus != 0 {
			t.Fatalf("httpStatus = %d, want 0 (blocked before any response)", resp.HTTPStatus)
		}
		if resp.Error == "" {
			t.Fatalf("error should be populated on an SSRF-guard block")
		}
	})

	t.Run("MissingOneClickHeader", func(t *testing.T) {
		f := newFixture(t)
		// A valid HTTPS List-Unsubscribe URL, but no
		// List-Unsubscribe-Post header at all -- not RFC 8058
		// one-click.
		id := unsubscribeMessage(t, f, "<https://sender.example/unsubscribe>", "")
		resp := invokeUnsubscribe(t, f, id)
		if resp.Status != "unsupported" {
			t.Fatalf("status = %q, want unsupported", resp.Status)
		}
		if resp.HTTPStatus != 0 {
			t.Fatalf("httpStatus = %d, want 0 (no request attempted)", resp.HTTPStatus)
		}
	})
}

// TestEmailUnsubscribe_SQLite runs the full acceptance matrix against
// storesqlite.
func TestEmailUnsubscribe_SQLite(t *testing.T) {
	testUnsubscribeSuite(t, setupFixture)
}

// TestEmailUnsubscribe_Postgres runs the full acceptance matrix against
// storepg (skips when HEROLD_PG_DSN is unset).
func TestEmailUnsubscribe_Postgres(t *testing.T) {
	testUnsubscribeSuite(t, setupFixturePostgres)
}

// TestEmailUnsubscribe_CapabilityAdvertised proves the vendor
// capability descriptor is present on the session so clients can
// detect the affordance before offering it (mirrors
// TestEmailRetryImages_CapabilityAdvertised).
func TestEmailUnsubscribe_CapabilityAdvertised(t *testing.T) {
	f := setupFixture(t)
	req, err := http.NewRequest(http.MethodGet, f.baseURL+"/.well-known/jmap", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var session struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if _, ok := session.Capabilities["https://netzhansa.com/jmap/unsubscribe"]; !ok {
		t.Fatalf("capability not advertised: %v", session.Capabilities)
	}
}
