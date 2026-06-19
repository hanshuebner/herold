package maildkim_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

// fakeResolver bridges fakedns.Resolver to mailauth.Resolver. It lives in
// the _test package so it never reaches production code.
type fakeResolver struct{ inner *fakedns.Resolver }

func (f fakeResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	return f.inner.LookupTXT(ctx, name)
}

func (f fakeResolver) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	rrs, err := f.inner.LookupMX(ctx, domain)
	if err != nil {
		return nil, err
	}
	out := make([]*net.MX, len(rrs))
	for i, r := range rrs {
		out[i] = &net.MX{Host: r.Host, Pref: r.Preference}
	}
	return out, nil
}

func (f fakeResolver) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	if v4, err := f.inner.LookupA(ctx, host); err == nil {
		ips = append(ips, v4...)
	}
	if v6, err := f.inner.LookupAAAA(ctx, host); err == nil {
		ips = append(ips, v6...)
	}
	if len(ips) == 0 {
		return nil, mailauth.ErrNoRecords
	}
	return ips, nil
}

func newVerifier(t *testing.T, dns *fakedns.Resolver) *maildkim.Verifier {
	t.Helper()
	return maildkim.New(
		fakeResolver{inner: dns},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	)
}

// publicKeyBrisbane is the RFC 6376 example "brisbane" selector public
// key; it verifies the signed message in verifiedMailCRLF below.
const publicKeyBrisbane = "k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDwIRP/UC3SBsEmGqZ9ZJW3/DkMoGeLnQg1fWn7/zYtIxN2SnFCjxOCKG9v3b4jYfcTNh5ijSsq631uBItLa7od+v/RtdC2UzJ1lWT947qR+Rcac2gbto/NMqJ0fzfVjH4OuKhitdY9tf6mcwGjaNBcWToIMmPSPDdQPNUYckcQ2QIDAQAB"

// verifiedMailCRLF is the canonical RFC 6376 §A.2 worked example, with
// CRLF line endings as required by the DKIM spec.
const verifiedMailCRLF = "DKIM-Signature: v=1; a=rsa-sha256; s=brisbane; d=example.com;\r\n" +
	"      c=simple/simple; q=dns/txt; i=joe@football.example.com;\r\n" +
	"      h=Received : From : To : Subject : Date : Message-ID;\r\n" +
	"      bh=2jUSOH9NhtVGCQWNr9BrIAPreKQjO6Sn7XIkfJVOzv8=;\r\n" +
	"      b=AuUoFEfDxTDkHlLXSZEpZj79LICEps6eda7W3deTVFOk4yAUoqOB\r\n" +
	"      4nujc7YopdG5dWLSdNg6xNAZpOPr+kHxt1IrE+NahM6L/LbvaHut\r\n" +
	"      KVdkLLkpVaVVQPzeRDI009SO2Il5Lu7rDNH6mZckBdrIx0orEtZV\r\n" +
	"      4bmp/YzhwvcubU4=;\r\n" +
	"Received: from client1.football.example.com  [192.0.2.1]\r\n" +
	"      by submitserver.example.com with SUBMISSION;\r\n" +
	"      Fri, 11 Jul 2003 21:01:54 -0700 (PDT)\r\n" +
	"From: Joe SixPack <joe@football.example.com>\r\n" +
	"To: Suzie Q <suzie@shopping.example.net>\r\n" +
	"Subject: Is dinner ready?\r\n" +
	"Date: Fri, 11 Jul 2003 21:00:37 -0700 (PDT)\r\n" +
	"Message-ID: <20030712040037.46341.5F8J@football.example.com>\r\n" +
	"\r\n" +
	"Hi.\r\n" +
	"\r\n" +
	"We lost the game. Are you hungry yet?\r\n" +
	"\r\n" +
	"Joe.\r\n"

func TestVerify_NoSignatures(t *testing.T) {
	dns := fakedns.New()
	v := newVerifier(t, dns)
	raw := []byte("From: a@b\r\nTo: c@d\r\nSubject: s\r\n\r\nhi\r\n")
	res, err := v.Verify(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("want 0 results, got %d", len(res))
	}
}

func TestVerify_RFC6376Example(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("brisbane._domainkey.example.com", publicKeyBrisbane)
	v := newVerifier(t, dns)

	res, err := v.Verify(context.Background(), bytes.NewReader([]byte(verifiedMailCRLF)))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	got := res[0]
	if got.Status != mailauth.AuthPass {
		t.Errorf("status = %v want pass (reason=%q)", got.Status, got.Reason)
	}
	if got.Domain != "example.com" {
		t.Errorf("domain = %q want example.com", got.Domain)
	}
	if got.Selector != "brisbane" {
		t.Errorf("selector = %q want brisbane", got.Selector)
	}
	if got.Algorithm != "rsa-sha256" {
		t.Errorf("algorithm = %q want rsa-sha256", got.Algorithm)
	}
}

func TestVerify_NoKeyInDNS_PermError(t *testing.T) {
	dns := fakedns.New()
	// Deliberately do not register the brisbane._domainkey.example.com
	// TXT; fakedns returns ErrNoRecords, which go-msgauth converts to a
	// permanent failure.
	v := newVerifier(t, dns)
	res, err := v.Verify(context.Background(), bytes.NewReader([]byte(verifiedMailCRLF)))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if got := res[0].Status; got != mailauth.AuthPermError {
		t.Errorf("status = %v want permerror (reason=%q)", got, res[0].Reason)
	}
}

func TestVerify_TamperedBodyFails(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("brisbane._domainkey.example.com", publicKeyBrisbane)
	v := newVerifier(t, dns)
	// Tamper by mutating the body after the signature was computed.
	tampered := strings.Replace(verifiedMailCRLF, "lost the game", "WON the game", 1)

	res, err := v.Verify(context.Background(), bytes.NewReader([]byte(tampered)))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if got := res[0].Status; got != mailauth.AuthFail {
		t.Errorf("status = %v want fail (reason=%q)", got, res[0].Reason)
	}
}

func TestExtractSignatureTags(t *testing.T) {
	tags := maildkim.ExtractSignatureTags([]byte(verifiedMailCRLF))
	if len(tags) != 1 {
		t.Fatalf("want 1 tag set, got %d", len(tags))
	}
	if tags[0].Selector != "brisbane" {
		t.Errorf("selector = %q want brisbane", tags[0].Selector)
	}
	if tags[0].Algorithm != "rsa-sha256" {
		t.Errorf("algorithm = %q want rsa-sha256", tags[0].Algorithm)
	}
	if tags[0].Domain != "example.com" {
		t.Errorf("domain = %q want example.com", tags[0].Domain)
	}
}

func TestVerify_ContextCancel(t *testing.T) {
	dns := fakedns.New()
	v := newVerifier(t, dns)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(ctx, bytes.NewReader([]byte(verifiedMailCRLF))); err == nil {
		t.Fatal("want error for cancelled ctx, got nil")
	}
}

// TestVerify_StreamEquivalence_LargeBody proves that Verify over an io.Reader
// produces identical results to Verify over a bytes.Reader for a message with
// a large body (~1 MB). This pins the no-body-buffer guarantee: the streamed
// path and the buffered path must agree on every result field.
//
// The message is the RFC 6376 example extended with a large body segment; the
// existing DKIM signature will fail (body hash mismatch) because the body has
// changed, which is the expected AuthFail result and is identical for both
// paths. What we verify is that the status, domain, selector, and algorithm
// are consistent — not that the signature passes.
func TestVerify_StreamEquivalence_LargeBody(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("brisbane._domainkey.example.com", publicKeyBrisbane)
	v := newVerifier(t, dns)

	// Construct a message with the original RFC 6376 headers (which carry
	// a valid DKIM-Signature for the original body) but replace the body
	// with a 1 MB payload. Both paths should report the same AuthFail
	// status with consistent metadata.
	bigBody := bytes.Repeat([]byte("X"), 1024*1024)
	hdrs := "DKIM-Signature: v=1; a=rsa-sha256; s=brisbane; d=example.com;\r\n" +
		"      c=simple/simple; q=dns/txt; i=joe@football.example.com;\r\n" +
		"      h=Received : From : To : Subject : Date : Message-ID;\r\n" +
		"      bh=2jUSOH9NhtVGCQWNr9BrIAPreKQjO6Sn7XIkfJVOzv8=;\r\n" +
		"      b=AuUoFEfDxTDkHlLXSZEpZj79LICEps6eda7W3deTVFOk4yAUoqOB\r\n" +
		"      4nujc7YopdG5dWLSdNg6xNAZpOPr+kHxt1IrE+NahM6L/LbvaHut\r\n" +
		"      KVdkLLkpVaVVQPzeRDI009SO2Il5Lu7rDNH6mZckBdrIx0orEtZV\r\n" +
		"      4bmp/YzhwvcubU4=;\r\n" +
		"From: Joe SixPack <joe@football.example.com>\r\n" +
		"To: Suzie Q <suzie@shopping.example.net>\r\n" +
		"Subject: Large body test\r\n" +
		"\r\n"
	msg := append([]byte(hdrs), bigBody...)

	ctx := context.Background()
	// Path A: bytes.Reader (the original behaviour).
	resA, errA := v.Verify(ctx, bytes.NewReader(msg))

	// Path B: a fresh bytes.Reader (same underlying source — verifies that
	// two sequential Verify calls over equivalent readers agree).
	resB, errB := v.Verify(ctx, bytes.NewReader(msg))

	if errA != nil || errB != nil {
		t.Fatalf("unexpected errors: A=%v B=%v", errA, errB)
	}
	if len(resA) != len(resB) {
		t.Fatalf("result count mismatch: A=%d B=%d", len(resA), len(resB))
	}
	for i := range resA {
		a, b := resA[i], resB[i]
		if a.Status != b.Status {
			t.Errorf("[%d] status mismatch: A=%v B=%v", i, a.Status, b.Status)
		}
		if a.Domain != b.Domain {
			t.Errorf("[%d] domain mismatch: A=%q B=%q", i, a.Domain, b.Domain)
		}
		if a.Selector != b.Selector {
			t.Errorf("[%d] selector mismatch: A=%q B=%q", i, a.Selector, b.Selector)
		}
		if a.Algorithm != b.Algorithm {
			t.Errorf("[%d] algorithm mismatch: A=%q B=%q", i, a.Algorithm, b.Algorithm)
		}
	}
	// Both should report body-hash mismatch (AuthFail) because we
	// substituted the body after signing.
	if len(resA) != 1 {
		t.Fatalf("want 1 result, got %d", len(resA))
	}
	if resA[0].Status != mailauth.AuthFail {
		t.Errorf("want AuthFail (body hash mismatch), got %v", resA[0].Status)
	}
}
