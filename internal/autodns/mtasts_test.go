package autodns_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMTASTSPolicyHandler_ServesCachedPolicy(t *testing.T) {
	pub, _, _, _ := newPublisher(t, "fake-dns")
	ctx := t.Context()
	if err := pub.PublishDomain(ctx, "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain: %v", err)
	}
	srv := httptest.NewServer(pub.PolicyHandler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/.well-known/mta-sts.txt", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.Host = "mta-sts.example.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	wantBody := "version: STSv1\n" +
		"mode: enforce\n" +
		"mx: mx.example.test\n" +
		"max_age: 604800\n"
	if body != wantBody {
		t.Fatalf("body mismatch:\ngot:\n%q\nwant:\n%q", body, wantBody)
	}

	// Unknown host -> 404.
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/.well-known/mta-sts.txt", nil)
	req2.Host = "mta-sts.unknown.test"
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("Do unknown: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown host status: got %d want 404", resp2.StatusCode)
	}
}
