package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protoadmin"
)

// fakeDNSVerifier returns a canned report.
type fakeDNSVerifier struct {
	report protoadmin.DNSVerifyReport
	err    error
}

func (f *fakeDNSVerifier) VerifyDomain(ctx context.Context, domain string) (protoadmin.DNSVerifyReport, error) {
	if f.err != nil {
		return protoadmin.DNSVerifyReport{}, f.err
	}
	return f.report, nil
}

func TestCLIDiagDNSCheck_RendersDriftReport(t *testing.T) {
	verifier := &fakeDNSVerifier{
		report: protoadmin.DNSVerifyReport{
			Domain: "example.test",
			OK:     false,
			Records: []protoadmin.DNSVerifyRecord{
				{Name: "selector._domainkey.example.test", Expected: "v=DKIM1;p=ABC", Actual: "v=DKIM1;p=ABC", State: "match"},
				{Name: "_dmarc.example.test", Expected: "v=DMARC1;p=quarantine", Actual: "v=DMARC1;p=none", State: "drift"},
				{Name: "_mta-sts.example.test", Expected: "v=STSv1;id=1234", State: "missing"},
			},
		},
	}
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.DNSVerifier = verifier
	})
	out, _, err := env.run("diag", "dns-check", "example.test", "--json")
	if err != nil {
		t.Fatalf("diag dns-check: %v", err)
	}
	for _, marker := range []string{"drift", "missing", "match", "_dmarc.example.test"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("output missing %q: %s", marker, out)
		}
	}
}

func TestCLIDiagDNSCheck_VerifierError(t *testing.T) {
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.DNSVerifier = &fakeDNSVerifier{err: errors.New("dns: plugin unreachable")}
	})
	_, _, err := env.run("diag", "dns-check", "example.test")
	if err == nil {
		t.Fatalf("expected error from verifier")
	}
	if !strings.Contains(err.Error(), "plugin unreachable") {
		t.Fatalf("expected 'plugin unreachable' in error: %v", err)
	}
}

func TestCLIDiagDNSCheck_NoVerifierConfigured(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("diag", "dns-check", "example.test")
	if err == nil {
		t.Fatalf("expected 501 not_implemented")
	}
	if !strings.Contains(err.Error(), "501") && !strings.Contains(err.Error(), "not_implemented") {
		t.Fatalf("expected 501 / not_implemented; got %v", err)
	}
}
