package extsubmit_test

// submitter_oauth_refresh_test.go verifies that a Submitter with a Refresher
// proactively refreshes an expired OAuth access token when RefreshDue has passed,
// rather than presenting the stale token to the SMTP server and receiving a 535
// (re #131, new facet: no Refresher was wired to prebuiltExtSubmitter).

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/store"
)

// TestSubmit_ExpiredOAuthToken_RefreshedProactively verifies that Submit
// proactively refreshes an expired OAuth access token via the attached Refresher
// when RefreshDue has passed, and uses the new token for XOAUTH2 AUTH — not the
// stale sealed token that would produce a 535 (re #131).
func TestSubmit_ExpiredOAuthToken_RefreshedProactively(t *testing.T) {
	// Track whether the token endpoint was called.
	var tokenEndpointCalls int32

	// Fake token endpoint: accepts grant_type=refresh_token, returns a fresh token.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenEndpointCalls, 1)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" {
			http.Error(w, "unexpected grant_type", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "same-refresh-token",
		})
	}))
	defer tokenSrv.Close()

	// Fake SMTP server: accepts XOAUTH2, captures the token from the initial
	// response (IR) so the test can assert which token was presented.
	var receivedToken string
	smtpSrv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := newBufioReader(conn)
		w := newBufioWriter(conn)
		srvWrite(w, "220 smtp.gmail.com ESMTP test")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.gmail.com")
		srvWrite(w, "250 AUTH XOAUTH2")
		line := srvRead(r) // AUTH XOAUTH2 <base64-IR>
		// Parse XOAUTH2 IR: user=<email>\x01auth=Bearer <token>\x01\x01
		if parts := strings.SplitN(line, " ", 3); len(parts) == 3 {
			if decoded, err := base64.StdEncoding.DecodeString(parts[2]); err == nil {
				ir := string(decoded)
				if idx := strings.Index(ir, "Bearer "); idx >= 0 {
					rest := ir[idx+7:]
					if end := strings.Index(rest, "\x01"); end >= 0 {
						receivedToken = rest[:end]
					}
				}
			}
		}
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "250 ok")
		srvRead(r) // DATA
		srvWrite(w, "354 send data")
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 queued")
		srvRead(r) // QUIT
	})

	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	meta := &trackingMeta{}

	refresher := &extsubmit.Refresher{
		Meta:    meta,
		DataKey: testDataKey,
		Now:     func() time.Time { return fixedNow },
	}

	s := &extsubmit.Submitter{
		DataKey:   testDataKey,
		HostName:  "client.test",
		Refresher: refresher,
		Now:       func() time.Time { return fixedNow },
	}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", smtpSrv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:         "identity-expired-oauth",
		SubmitHost:         "smtp.gmail.com",
		SubmitPort:         465,
		SubmitSecurity:     "none",
		SubmitAuthMethod:   "oauth2",
		OAuthAccessCT:      sealSecret(t, "stale-access-token"),
		OAuthRefreshCT:     sealSecret(t, "valid-refresh-token"),
		OAuthTokenEndpoint: tokenSrv.URL,
		OAuthClientID:      "user@gmail.com",
		// RefreshDue is 1 minute in the past: accessToken must trigger a refresh.
		RefreshDue: fixedNow.Add(-time.Minute),
	}
	env := extsubmit.Envelope{
		MailFrom:      "user@gmail.com",
		RcptTo:        []string{"user@gmail.com"},
		Body:          strings.NewReader("Subject: refresh test\r\n\r\nBody\r\n"),
		CorrelationID: "corr-refresh-test",
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}

	// The token endpoint must have been called exactly once (proactive refresh).
	if n := atomic.LoadInt32(&tokenEndpointCalls); n != 1 {
		t.Errorf("token endpoint called %d times; want 1 (proactive OAuth refresh must happen)", n)
	}

	// The SMTP server must have received the fresh token, not the stale one.
	if receivedToken != "fresh-access-token" {
		t.Errorf("SMTP AUTH received token %q; want fresh-access-token (stale expired token must not reach SMTP)", receivedToken)
	}

	// The Refresher must have persisted the new token material to the store.
	if _, ok := meta.last(); !ok {
		t.Error("UpsertIdentitySubmission was not called; the refreshed token must be persisted")
	}
}

// TestSubmit_FreshOAuthToken_NoRefreshTriggered verifies that when RefreshDue is
// in the future (token is still fresh), Submit uses the stored access token
// directly without contacting the token endpoint.
func TestSubmit_FreshOAuthToken_NoRefreshTriggered(t *testing.T) {
	var tokenEndpointCalls int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenEndpointCalls, 1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer tokenSrv.Close()

	smtpSrv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := newBufioReader(conn)
		w := newBufioWriter(conn)
		srvWrite(w, "220 smtp.gmail.com ESMTP test")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.gmail.com")
		srvWrite(w, "250 AUTH XOAUTH2")
		srvRead(r) // AUTH XOAUTH2
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "250 ok")
		srvRead(r) // DATA
		srvWrite(w, "354 send")
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 ok")
		srvRead(r) // QUIT
	})

	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	meta := &trackingMeta{}
	refresher := &extsubmit.Refresher{
		Meta:    meta,
		DataKey: testDataKey,
		Now:     func() time.Time { return fixedNow },
	}

	s := &extsubmit.Submitter{
		DataKey:   testDataKey,
		HostName:  "client.test",
		Refresher: refresher,
		Now:       func() time.Time { return fixedNow },
	}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", smtpSrv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:         "identity-fresh-oauth",
		SubmitHost:         "smtp.gmail.com",
		SubmitPort:         465,
		SubmitSecurity:     "none",
		SubmitAuthMethod:   "oauth2",
		OAuthAccessCT:      sealSecret(t, "still-valid-token"),
		OAuthRefreshCT:     sealSecret(t, "some-refresh-token"),
		OAuthTokenEndpoint: tokenSrv.URL,
		OAuthClientID:      "user@gmail.com",
		// RefreshDue is 55 minutes in the future: no refresh needed.
		RefreshDue: fixedNow.Add(55 * time.Minute),
	}
	env := extsubmit.Envelope{
		MailFrom:      "user@gmail.com",
		RcptTo:        []string{"user@gmail.com"},
		Body:          strings.NewReader("Subject: no-refresh test\r\n\r\nBody\r\n"),
		CorrelationID: "corr-no-refresh",
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	if n := atomic.LoadInt32(&tokenEndpointCalls); n != 0 {
		t.Errorf("token endpoint called %d times; want 0 (fresh token must not trigger refresh)", n)
	}
	// No store update expected (token was not refreshed).
	if _, ok := meta.last(); ok {
		t.Error("UpsertIdentitySubmission was called; expected no store update when token is fresh")
	}
}

// newBufioReader / newBufioWriter are thin wrappers to create bufio reader/writer
// from a net.Conn, for use in the inline SMTP server handlers in this file.
// These avoid name collision with srvWrite / srvRead from submitter_test.go, which
// already take *bufio.Writer / *bufio.Reader.
func newBufioReader(conn net.Conn) *bufio.Reader {
	return bufio.NewReader(conn)
}

func newBufioWriter(conn net.Conn) *bufio.Writer {
	return bufio.NewWriter(conn)
}
