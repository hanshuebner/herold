package protocall

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// TestHTTPHandler_Auth_Required asserts that a request without a
// resolved principal is rejected with 401.
func TestHTTPHandler_Auth_Required(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	s := New(Options{
		Clock: clk,
		TURN: TURNConfig{
			URIs:         []string{"turn:t.example.com:3478"},
			SharedSecret: []byte("k"),
		},
		Authn: func(r *http.Request) (store.PrincipalID, bool) {
			return 0, false
		},
	})
	defer s.Close()
	srv := httptest.NewServer(s.HTTPHandler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/call/credentials", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Type"), "application/problem+json"; got != want {
		t.Fatalf("content-type = %q, want %q", got, want)
	}
}

// TestHTTPHandler_RateLimit_429 asserts that the 11th request inside
// a one-minute window returns 429 with a Retry-After header.
func TestHTTPHandler_RateLimit_429(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	s := New(Options{
		Clock: clk,
		TURN: TURNConfig{
			URIs:         []string{"turn:t.example.com:3478"},
			SharedSecret: []byte("topsecret"),
		},
		Authn: func(r *http.Request) (store.PrincipalID, bool) {
			return 7, true
		},
	})
	defer s.Close()
	srv := httptest.NewServer(s.HTTPHandler())
	defer srv.Close()
	// Burst of 10 succeeds.
	for i := 0; i < callRateLimitBurst; i++ {
		resp, err := http.Post(srv.URL+"/api/v1/call/credentials",
			"application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("POST %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("burst[%d] status = %d body=%s", i, resp.StatusCode, body)
		}
		var cred Credential
		if err := json.Unmarshal(body, &cred); err != nil {
			t.Fatalf("burst[%d] decode: %v", i, err)
		}
		if cred.Username == "" || cred.Password == "" {
			t.Fatalf("burst[%d] empty cred: %+v", i, cred)
		}
	}
	// 11th request denied.
	resp, err := http.Post(srv.URL+"/api/v1/call/credentials",
		"application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST 11: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatalf("Retry-After missing")
	}
	// After window slides past, requests succeed again.
	clk.Advance(callRateLimitWindow + time.Second)
	resp2, err := http.Post(srv.URL+"/api/v1/call/credentials",
		"application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST after-window: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("after-window status = %d, want 200", resp2.StatusCode)
	}
}

// TestHTTPHandler_MethodNotAllowed asserts a GET is rejected with
// 405 and Allow: POST.
func TestHTTPHandler_MethodNotAllowed(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	s := New(Options{
		Clock: clk,
		TURN: TURNConfig{
			URIs:         []string{"turn:t.example.com:3478"},
			SharedSecret: []byte("k"),
		},
		Authn: func(r *http.Request) (store.PrincipalID, bool) { return 1, true },
	})
	defer s.Close()
	srv := httptest.NewServer(s.HTTPHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/call/credentials")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", got)
	}
}
