package maildmarc

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/emersion/go-msgauth/dmarc"

	"github.com/hanshuebner/herold/internal/mailauth"
)

// Evaluator applies RFC 7489 DMARC evaluation using the inbound SPF and
// DKIM results plus a DMARC record fetched from DNS.
type Evaluator struct {
	resolver mailauth.Resolver
}

// New returns an Evaluator using resolver for DMARC record lookups.
func New(resolver mailauth.Resolver) *Evaluator {
	if resolver == nil {
		panic("maildmarc: nil resolver")
	}
	return &Evaluator{resolver: resolver}
}

// Evaluate applies DMARC to the given message. headerFrom is the raw
// RFC 5322 From: header value (not yet parsed); spf and dkim carry the
// already-computed verification verdicts from mailspf and maildkim.
//
// The returned DMARCResult is never filled when headerFrom is
// un-parseable or the domain carries no DMARC record — those surface as
// Status == AuthNone with a reason string explaining why. A non-nil
// error indicates an internal failure (context cancel, DNS transport).
func (e *Evaluator) Evaluate(
	ctx context.Context,
	headerFrom string,
	spf mailauth.SPFResult,
	dkim []mailauth.DKIMResult,
) (mailauth.DMARCResult, error) {
	if err := ctx.Err(); err != nil {
		return mailauth.DMARCResult{}, err
	}
	fromDomain, err := parseHeaderFromDomain(headerFrom)
	if err != nil {
		return mailauth.DMARCResult{
			Status: mailauth.AuthNone,
			Reason: "unparseable From: " + err.Error(),
		}, nil
	}

	orgDomain := OrganizationalDomain(fromDomain)
	rec, recDomain, lookupErr := e.lookup(ctx, fromDomain, orgDomain)
	if lookupErr != nil {
		if mailauth.IsTemporary(lookupErr) {
			return mailauth.DMARCResult{
				Status:     mailauth.AuthTempError,
				HeaderFrom: fromDomain,
				OrgDomain:  orgDomain,
				Reason:     "DNS TXT lookup: " + lookupErr.Error(),
			}, nil
		}
		return mailauth.DMARCResult{
			Status:     mailauth.AuthNone,
			HeaderFrom: fromDomain,
			OrgDomain:  orgDomain,
			Reason:     "no DMARC record: " + lookupErr.Error(),
		}, nil
	}

	// Which policy applies: p= for the Organizational Domain itself, sp=
	// for subdomains (if published). RFC 7489 §6.6.3.
	policy := policyFromRecord(rec)
	if !strings.EqualFold(fromDomain, recDomain) && rec.SubdomainPolicy != "" {
		policy = convertPolicy(rec.SubdomainPolicy)
	}

	adkim := rec.DKIMAlignment
	if adkim == "" {
		adkim = dmarc.AlignmentRelaxed
	}
	aspf := rec.SPFAlignment
	if aspf == "" {
		aspf = dmarc.AlignmentRelaxed
	}

	dkimAligned := false
	for _, d := range dkim {
		if d.Status != mailauth.AuthPass {
			continue
		}
		if isAligned(d.Domain, fromDomain, adkim) {
			dkimAligned = true
			break
		}
	}
	spfAligned := spf.Status == mailauth.AuthPass &&
		isAligned(senderDomainOf(spf.From), fromDomain, aspf)

	pass := dkimAligned || spfAligned

	result := mailauth.DMARCResult{
		Policy:      policy,
		SPFAligned:  spfAligned,
		DKIMAligned: dkimAligned,
		HeaderFrom:  fromDomain,
		OrgDomain:   orgDomain,
	}
	if pass {
		result.Status = mailauth.AuthPass
		result.Disposition = mailauth.DispositionNone
		return result, nil
	}
	result.Status = mailauth.AuthFail
	result.Disposition = dispositionFromPolicy(policy, rec.Percent)
	return result, nil
}

// lookup fetches _dmarc.<domain>; if that fails with no-records and the
// from domain is not already the organisational domain, it retries with
// _dmarc.<orgDomain> per RFC 7489 §6.6.3.
func (e *Evaluator) lookup(ctx context.Context, fromDomain, orgDomain string) (*dmarc.Record, string, error) {
	rec, err := e.lookupOnce(ctx, fromDomain)
	if err == nil {
		return rec, fromDomain, nil
	}
	if !errors.Is(err, mailauth.ErrNoRecords) {
		return nil, "", err
	}
	if strings.EqualFold(fromDomain, orgDomain) {
		return nil, "", err
	}
	rec, err = e.lookupOnce(ctx, orgDomain)
	if err != nil {
		return nil, "", err
	}
	return rec, orgDomain, nil
}

func (e *Evaluator) lookupOnce(ctx context.Context, domain string) (*dmarc.Record, error) {
	txts, err := e.resolver.TXTLookup(ctx, "_dmarc."+domain)
	if err != nil {
		return nil, err
	}
	for _, t := range txts {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=dmarc1") {
			continue
		}
		rec, perr := dmarc.Parse(t)
		if perr == nil {
			return rec, nil
		}
		return nil, fmt.Errorf("maildmarc: parse: %w", perr)
	}
	return nil, fmt.Errorf("%w: no v=DMARC1", mailauth.ErrNoRecords)
}

func policyFromRecord(rec *dmarc.Record) mailauth.DMARCPolicy {
	return convertPolicy(rec.Policy)
}

func convertPolicy(p dmarc.Policy) mailauth.DMARCPolicy {
	switch p {
	case dmarc.PolicyNone, "":
		return mailauth.DMARCPolicyNone
	case dmarc.PolicyQuarantine:
		return mailauth.DMARCPolicyQuarantine
	case dmarc.PolicyReject:
		return mailauth.DMARCPolicyReject
	}
	return mailauth.DMARCPolicyNone
}

// dispositionFromPolicy maps policy + pct= to the disposition applied on
// a DMARC fail. Per RFC 7489 §6.6.4, pct= selects the fraction of mail
// that receives the full policy action; the rest is downgraded one rung
// (reject -> quarantine; quarantine -> none).
//
// We apply the policy deterministically at the "full action" level and
// rely on the caller to implement pct= sampling when they want the
// fractional downgrade: an evaluator that downgrades is racy to test and
// usually not what operators want for a single-message check. Callers
// that need the downgrade path can consult the policy + record directly.
func dispositionFromPolicy(p mailauth.DMARCPolicy, _ *int) mailauth.DMARCDisposition {
	switch p {
	case mailauth.DMARCPolicyReject:
		return mailauth.DispositionReject
	case mailauth.DMARCPolicyQuarantine:
		return mailauth.DispositionQuarantine
	default:
		return mailauth.DispositionNone
	}
}

// isAligned reports whether child is aligned with parent under the given
// alignment mode. Strict = exact domain match; relaxed = same
// organizational domain.
func isAligned(child, parent string, mode dmarc.AlignmentMode) bool {
	child = strings.ToLower(strings.TrimSuffix(child, "."))
	parent = strings.ToLower(strings.TrimSuffix(parent, "."))
	if child == "" || parent == "" {
		return false
	}
	if child == parent {
		return true
	}
	if mode == dmarc.AlignmentStrict {
		return false
	}
	return OrganizationalDomain(child) == OrganizationalDomain(parent)
}

// parseHeaderFromDomain extracts the domain portion of the RFC 5322
// From: header. The spec requires exactly one mailbox; multi-mailbox
// From headers fail DMARC evaluation per RFC 7489 §6.6.1.
func parseHeaderFromDomain(header string) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", errors.New("empty")
	}
	addrs, err := mail.ParseAddressList(header)
	if err != nil {
		return "", err
	}
	if len(addrs) != 1 {
		return "", fmt.Errorf("DMARC requires single From address, got %d", len(addrs))
	}
	i := strings.LastIndex(addrs[0].Address, "@")
	if i < 0 {
		return "", fmt.Errorf("no @ in From address %q", addrs[0].Address)
	}
	return strings.ToLower(addrs[0].Address[i+1:]), nil
}

// senderDomainOf extracts the domain portion of an address.
func senderDomainOf(addr string) string {
	if addr == "" {
		return ""
	}
	addr = strings.Trim(addr, "<>")
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return strings.ToLower(addr)
	}
	return strings.ToLower(addr[i+1:])
}
