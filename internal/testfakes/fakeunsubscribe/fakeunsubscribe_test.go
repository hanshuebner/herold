package fakeunsubscribe_test

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/testfakes/fakeunsubscribe"
)

func trustingClient(t *testing.T, certPEM []byte) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatalf("failed to parse server certificate")
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
}

func TestSuccessURL_Returns200AndRecordsRequest(t *testing.T) {
	srv := fakeunsubscribe.New(t, fakeunsubscribe.Options{})
	client := trustingClient(t, srv.CertPEM())

	resp, err := client.Post(srv.SuccessURL(), "application/x-www-form-urlencoded", strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Path != "/ok" || r.Body != "List-Unsubscribe=One-Click" || r.ContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("recorded request = %+v", r)
	}
	if r.HasCookie {
		t.Fatalf("HasCookie = true, want false")
	}
}

func TestFailureURL_ReturnsConfiguredStatus(t *testing.T) {
	srv := fakeunsubscribe.New(t, fakeunsubscribe.Options{FailStatus: http.StatusServiceUnavailable})
	client := trustingClient(t, srv.CertPEM())

	resp, err := client.Post(srv.FailureURL(), "application/x-www-form-urlencoded", strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestFailureURL_DefaultsTo500(t *testing.T) {
	srv := fakeunsubscribe.New(t, fakeunsubscribe.Options{})
	client := trustingClient(t, srv.CertPEM())

	resp, err := client.Post(srv.FailureURL(), "application/x-www-form-urlencoded", strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

// TestImageURL_ServesPNG pins issue #443's remote-image fixture: the
// same origin used for the unsubscribe POST endpoints also serves a
// real, fetchable PNG at ImageURL.
func TestImageURL_ServesPNG(t *testing.T) {
	srv := fakeunsubscribe.New(t, fakeunsubscribe.Options{})
	client := trustingClient(t, srv.CertPEM())

	resp, err := client.Get(srv.ImageURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) < 8 || string(body[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("body does not start with the PNG signature: %x", body[:min(8, len(body))])
	}
}

// TestUntrustedClientRejectsCertificate pins the reason
// cmd/heroldfakeunsubscribe writes CertPEM to a file dev-instance.sh
// points SSL_CERT_FILE at: a default client (system roots only) must
// NOT trust this self-signed certificate.
func TestUntrustedClientRejectsCertificate(t *testing.T) {
	srv := fakeunsubscribe.New(t, fakeunsubscribe.Options{})
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{}}}
	_, err := client.Get(srv.SuccessURL())
	if err == nil {
		t.Fatalf("expected a certificate-verification error from an untrusting client")
	}
}
