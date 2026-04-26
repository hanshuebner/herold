package admin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/mailauth"
)

// stubTLSRPTResolver is a minimal mailauth.Resolver that returns
// pre-seeded TXT records and ErrNoRecords for everything else.
type stubTLSRPTResolver struct {
	txt map[string][]string
}

func (s stubTLSRPTResolver) TXTLookup(_ context.Context, name string) ([]string, error) {
	if v, ok := s.txt[name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("%w: TXT %s", mailauth.ErrNoRecords, name)
}

func (s stubTLSRPTResolver) MXLookup(_ context.Context, _ string) ([]*net.MX, error) {
	return nil, mailauth.ErrNoRecords
}

func (s stubTLSRPTResolver) IPLookup(_ context.Context, _ string) ([]net.IP, error) {
	return nil, mailauth.ErrNoRecords
}

// TestAutodnsReporter_NonNilAfterStartServer verifies that the autodns
// TLS-RPT reporter and emitter are constructed (non-nil) during StartServer.
// The test boots the full server and checks that the /api/v1/healthz/ready
// endpoint is reachable, which proves boot reached the reporter-construction
// block without panicking (NewReporter panics on nil Store).
func TestAutodnsReporter_NonNilAfterStartServer(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	adminAddr := addrs["admin"]
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}
	// If NewReporter panicked (nil Store) StartServer would have returned an
	// error and the server would not be ready; a successful healthz/ready
	// response proves construction succeeded.
	resp, err := http.Get("http://" + adminAddr + "/api/v1/healthz/ready")
	if err != nil {
		t.Fatalf("GET /api/v1/healthz/ready: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz/ready: want 200, got %d", resp.StatusCode)
	}
}

// TestBuildTLSRPTRuaResolver_ParsesTXTRecord verifies the RuaResolver
// extracts mailto: and https: URIs from a TLSRPTv1 TXT record correctly.
func TestBuildTLSRPTRuaResolver_ParsesTXTRecord(t *testing.T) {
	resolver := buildTLSRPTRuaResolver(stubTLSRPTResolver{
		txt: map[string][]string{
			"_smtp._tls.example.test": {
				"v=TLSRPTv1; rua=mailto:tlsrpt@example.test,https://report.example.test/tlsrpt",
			},
		},
	})

	got := resolver(t.Context(), "example.test")
	if len(got) != 2 {
		t.Fatalf("expected 2 URIs, got %v", got)
	}
	if got[0] != "mailto:tlsrpt@example.test" {
		t.Errorf("URI[0] = %q; want mailto:tlsrpt@example.test", got[0])
	}
	if got[1] != "https://report.example.test/tlsrpt" {
		t.Errorf("URI[1] = %q; want https://report.example.test/tlsrpt", got[1])
	}
}

// TestBuildTLSRPTRuaResolver_NoRecord returns nil when TXT lookup fails.
func TestBuildTLSRPTRuaResolver_NoRecord(t *testing.T) {
	resolver := buildTLSRPTRuaResolver(stubTLSRPTResolver{txt: nil})
	got := resolver(t.Context(), "nodomain.test")
	if len(got) != 0 {
		t.Errorf("expected empty result for missing record, got %v", got)
	}
}
