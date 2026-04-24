package maildmarc_test

import (
	"context"
	"net"
	"testing"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

type fakeResolver struct{ inner *fakedns.Resolver }

func (f fakeResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	v, err := f.inner.LookupTXT(ctx, name)
	if err != nil {
		return nil, mailauth.ErrNoRecords
	}
	return v, nil
}

func (f fakeResolver) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	return nil, mailauth.ErrNoRecords
}

func (f fakeResolver) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	return nil, mailauth.ErrNoRecords
}

func newEvaluator(dns *fakedns.Resolver) *maildmarc.Evaluator {
	return maildmarc.New(fakeResolver{inner: dns})
}

func TestEvaluate_PassViaDKIM(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("_dmarc.example.com", "v=DMARC1; p=reject; adkim=r; aspf=r")
	e := newEvaluator(dns)

	res, err := e.Evaluate(
		context.Background(),
		"Alice <alice@example.com>",
		mailauth.SPFResult{Status: mailauth.AuthFail, From: "alice@other.invalid"},
		[]mailauth.DKIMResult{{Status: mailauth.AuthPass, Domain: "example.com"}},
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Status != mailauth.AuthPass {
		t.Fatalf("status = %v want pass (reason=%q)", res.Status, res.Reason)
	}
	if !res.DKIMAligned {
		t.Error("want DKIMAligned true")
	}
	if res.Disposition != mailauth.DispositionNone {
		t.Errorf("disposition = %v want none", res.Disposition)
	}
}

func TestEvaluate_PassViaSPFRelaxed(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("_dmarc.example.com", "v=DMARC1; p=quarantine")
	e := newEvaluator(dns)

	res, err := e.Evaluate(
		context.Background(),
		"alice@example.com",
		mailauth.SPFResult{Status: mailauth.AuthPass, From: "bounces@mail.example.com"},
		nil,
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Status != mailauth.AuthPass {
		t.Fatalf("status = %v want pass (reason=%q)", res.Status, res.Reason)
	}
	if !res.SPFAligned {
		t.Error("want SPFAligned true under relaxed alignment (mail.example.com ~ example.com)")
	}
}

func TestEvaluate_StrictSPFRejectsSubdomain(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("_dmarc.example.com", "v=DMARC1; p=reject; aspf=s")
	e := newEvaluator(dns)

	res, err := e.Evaluate(
		context.Background(),
		"alice@example.com",
		mailauth.SPFResult{Status: mailauth.AuthPass, From: "bounces@mail.example.com"},
		nil,
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail under strict alignment", res.Status)
	}
	if res.Disposition != mailauth.DispositionReject {
		t.Errorf("disposition = %v want reject", res.Disposition)
	}
}

func TestEvaluate_FailAppliesQuarantine(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("_dmarc.example.com", "v=DMARC1; p=quarantine")
	e := newEvaluator(dns)

	res, err := e.Evaluate(
		context.Background(),
		"alice@example.com",
		mailauth.SPFResult{Status: mailauth.AuthFail},
		nil,
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Status != mailauth.AuthFail {
		t.Fatalf("status = %v want fail", res.Status)
	}
	if res.Disposition != mailauth.DispositionQuarantine {
		t.Errorf("disposition = %v want quarantine", res.Disposition)
	}
}

func TestEvaluate_NoPolicy(t *testing.T) {
	dns := fakedns.New()
	e := newEvaluator(dns)
	res, err := e.Evaluate(
		context.Background(),
		"alice@example.com",
		mailauth.SPFResult{Status: mailauth.AuthPass, From: "alice@example.com"},
		nil,
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Status != mailauth.AuthNone {
		t.Errorf("status = %v want none", res.Status)
	}
}

func TestEvaluate_SubdomainPolicyWins(t *testing.T) {
	dns := fakedns.New()
	// Org-level record at _dmarc.example.com with sp=reject; the
	// header-from is a subdomain that does not publish its own record,
	// so the subdomain policy applies.
	dns.AddTXT("_dmarc.example.com", "v=DMARC1; p=none; sp=reject")
	e := newEvaluator(dns)
	res, err := e.Evaluate(
		context.Background(),
		"alice@sub.example.com",
		mailauth.SPFResult{Status: mailauth.AuthFail},
		nil,
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Policy != mailauth.DMARCPolicyReject {
		t.Errorf("policy = %v want reject (sp= wins for subdomain)", res.Policy)
	}
}

func TestEvaluate_UnparseableFrom(t *testing.T) {
	dns := fakedns.New()
	e := newEvaluator(dns)
	res, err := e.Evaluate(context.Background(), "not an address",
		mailauth.SPFResult{}, nil)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if res.Status != mailauth.AuthNone {
		t.Errorf("status = %v want none", res.Status)
	}
}
