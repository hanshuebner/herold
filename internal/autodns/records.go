package autodns

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/store"
)

// MTASTSMode is the policy mode published in an MTA-STS policy file.
// The wire-form tokens come from RFC 8461 §3.2.
type MTASTSMode uint8

// MTASTSMode values.
const (
	// MTASTSModeUnknown is the zero value and must not be persisted.
	MTASTSModeUnknown MTASTSMode = iota
	// MTASTSModeEnforce — receivers MUST refuse insecure delivery.
	MTASTSModeEnforce
	// MTASTSModeTesting — record failures via TLS-RPT but still deliver.
	MTASTSModeTesting
	// MTASTSModeNone — informational; receivers ignore the policy.
	MTASTSModeNone
)

// String returns the RFC 8461 token for m.
func (m MTASTSMode) String() string {
	switch m {
	case MTASTSModeEnforce:
		return "enforce"
	case MTASTSModeTesting:
		return "testing"
	case MTASTSModeNone:
		return "none"
	default:
		return "unknown"
	}
}

// MTASTSPolicy is the operator-supplied content for an MTA-STS policy.
// PolicyID is filled in by BuildMTASTSPolicy when zero so re-publication
// produces a fresh ID without callers tracking the clock themselves.
type MTASTSPolicy struct {
	// Mode selects the published policy mode.
	Mode MTASTSMode
	// MX is the list of MX hostnames clients must accept (RFC 8461 §3.2
	// "mx:" stanza). Wildcards permitted.
	MX []string
	// MaxAgeSeconds is the policy max_age (RFC 8461 §3.2). The spec ceiling
	// is 31557600 (one year); we do not enforce that here so operators can
	// publish shorter testing values.
	MaxAgeSeconds int
	// PolicyID is the id= tag value carried in the _mta-sts TXT record.
	// When zero BuildMTASTSPolicy uses the supplied unix timestamp.
	PolicyID string
}

// DMARCAlignment is the aspf= / adkim= mode.
type DMARCAlignment uint8

// DMARCAlignment values match RFC 7489 §6.3.
const (
	// DMARCAlignmentUnspecified is the zero value (defaults to relaxed).
	DMARCAlignmentUnspecified DMARCAlignment = iota
	// DMARCAlignmentRelaxed — same Organisational Domain.
	DMARCAlignmentRelaxed
	// DMARCAlignmentStrict — exact domain match.
	DMARCAlignmentStrict
)

// String returns the RFC 7489 token for a.
func (a DMARCAlignment) String() string {
	switch a {
	case DMARCAlignmentStrict:
		return "s"
	default:
		return "r"
	}
}

// DMARCFailureOptions encodes the fo= bitmask of DMARC failure-report
// triggers. RFC 7489 §6.3 defines the four tokens; an empty value means
// the publisher omits the fo= tag.
type DMARCFailureOptions struct {
	// FailAny — fo=0 (default), report when DKIM and SPF both fail.
	FailAny bool
	// FailAll — fo=1, report when either DKIM or SPF fail.
	FailAll bool
	// FailDKIM — fo=d, report on DKIM failure regardless of result.
	FailDKIM bool
	// FailSPF — fo=s, report on SPF failure regardless of result.
	FailSPF bool
}

// DMARCPolicy is the operator-supplied content for a DMARC TXT record.
type DMARCPolicy struct {
	// Policy is the p= tag.
	Policy mailauth.DMARCPolicy
	// SubdomainPolicy is the sp= tag; zero means omit sp=.
	SubdomainPolicy mailauth.DMARCPolicy
	// HasSubdomainPolicy gates whether SubdomainPolicy is published.
	HasSubdomainPolicy bool
	// RUA is the list of aggregate-report destinations (mailto:/https:).
	RUA []string
	// RUF is the list of failure-report destinations.
	RUF []string
	// Pct is the pct= sampling fraction, 0..100. -1 means "omit pct=".
	Pct int
	// ADKIM is the DKIM alignment mode (adkim=).
	ADKIM DMARCAlignment
	// ASPF is the SPF alignment mode (aspf=).
	ASPF DMARCAlignment
	// FO is the failure-report option set (fo=).
	FO DMARCFailureOptions
}

// BuildDKIMRecord renders a DKIM TXT record body for alg + the supplied
// base64-encoded public key. The result is the on-the-wire string that
// goes after `<selector>._domainkey.<domain>. IN TXT`.
//
// Long records are returned as a single semicolon-separated string; the
// caller (or DNS plugin) is responsible for any 255-byte segmentation
// the wire format demands. SegmentTXT is the helper.
func BuildDKIMRecord(alg store.DKIMAlgorithm, publicKeyB64 string) (string, error) {
	var k string
	switch alg {
	case store.DKIMAlgorithmRSASHA256:
		k = "rsa"
	case store.DKIMAlgorithmEd25519SHA256:
		k = "ed25519"
	default:
		return "", fmt.Errorf("autodns: unsupported DKIM algorithm %v", alg)
	}
	if publicKeyB64 == "" {
		return "", errors.New("autodns: empty DKIM public key")
	}
	return "v=DKIM1; k=" + k + "; p=" + publicKeyB64, nil
}

// SegmentTXT splits txt into <=255 byte chunks per RFC 1035 §3.3.14.
// Each chunk is one character-string in the TXT RDATA. Operators and
// DNS plugins that emit on-wire bytes use this helper; plugins that take
// the higher-level "value" string can pass the unsplit form.
func SegmentTXT(txt string) []string {
	const max = 255
	if len(txt) <= max {
		return []string{txt}
	}
	out := make([]string, 0, (len(txt)+max-1)/max)
	for i := 0; i < len(txt); i += max {
		end := i + max
		if end > len(txt) {
			end = len(txt)
		}
		out = append(out, txt[i:end])
	}
	return out
}

// ParsedDKIMRecord is the structured form of a DKIM v=DKIM1 record.
// BuildDKIMRecord round-trips through ParseDKIMRecord.
type ParsedDKIMRecord struct {
	Version   string
	KeyType   string
	PublicKey string
	HashAlgs  string
	Service   string
	Flags     string
	Notes     string
}

// ParseDKIMRecord parses a v=DKIM1 record body. Unknown tags are
// preserved so callers can echo them back unchanged on a republish; the
// publisher is concerned only with v=, k=, p=.
func ParseDKIMRecord(s string) (ParsedDKIMRecord, error) {
	out := ParsedDKIMRecord{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "v":
			out.Version = v
		case "k":
			out.KeyType = v
		case "p":
			out.PublicKey = v
		case "h":
			out.HashAlgs = v
		case "s":
			out.Service = v
		case "t":
			out.Flags = v
		case "n":
			out.Notes = v
		}
	}
	if out.Version == "" {
		return out, errors.New("autodns: DKIM record missing v= tag")
	}
	return out, nil
}

// BuildMTASTSPolicy renders the policy resources for one MTA-STS-publishing
// domain. The first return value is the TXT record body for
// `_mta-sts.<domain>` (`v=STSv1; id=<unique>`); the second is the policy
// body served at `https://mta-sts.<domain>/.well-known/mta-sts.txt`.
//
// nowUnix supplies the policy ID when p.PolicyID is empty. Callers that
// want a stable ID across re-publications populate p.PolicyID themselves.
func BuildMTASTSPolicy(p MTASTSPolicy, nowUnix int64) (txt string, policy string, err error) {
	if p.Mode == MTASTSModeUnknown {
		return "", "", errors.New("autodns: MTA-STS mode unspecified")
	}
	if len(p.MX) == 0 {
		return "", "", errors.New("autodns: MTA-STS policy needs at least one MX")
	}
	if p.MaxAgeSeconds <= 0 {
		return "", "", errors.New("autodns: MTA-STS max_age must be > 0")
	}
	id := p.PolicyID
	if id == "" {
		id = strconv.FormatInt(nowUnix, 10)
	}
	txt = "v=STSv1; id=" + id

	var b strings.Builder
	b.WriteString("version: STSv1\n")
	b.WriteString("mode: ")
	b.WriteString(p.Mode.String())
	b.WriteByte('\n')
	for _, mx := range p.MX {
		b.WriteString("mx: ")
		b.WriteString(mx)
		b.WriteByte('\n')
	}
	b.WriteString("max_age: ")
	b.WriteString(strconv.Itoa(p.MaxAgeSeconds))
	b.WriteByte('\n')
	return txt, b.String(), nil
}

// ParsedMTASTSPolicy is the round-trip target of BuildMTASTSPolicy's
// policy body. It is intentionally a thin parser; callers that need
// strict validation use a dedicated MTA-STS verifier.
type ParsedMTASTSPolicy struct {
	Version string
	Mode    string
	MX      []string
	MaxAge  int
}

// ParseMTASTSPolicy parses the body served at /.well-known/mta-sts.txt.
func ParseMTASTSPolicy(body string) (ParsedMTASTSPolicy, error) {
	var out ParsedMTASTSPolicy
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "version":
			out.Version = v
		case "mode":
			out.Mode = v
		case "mx":
			out.MX = append(out.MX, v)
		case "max_age":
			n, err := strconv.Atoi(v)
			if err != nil {
				return out, fmt.Errorf("autodns: max_age: %w", err)
			}
			out.MaxAge = n
		}
	}
	if out.Version == "" {
		return out, errors.New("autodns: MTA-STS policy missing version")
	}
	return out, nil
}

// BuildTLSRPTRecord renders the TXT body for `_smtp._tls.<domain>` per
// RFC 8460 §3. RUA URIs are rendered comma-separated.
func BuildTLSRPTRecord(rua []string) (string, error) {
	if len(rua) == 0 {
		return "", errors.New("autodns: TLSRPT requires at least one rua")
	}
	for _, u := range rua {
		if !strings.HasPrefix(u, "mailto:") && !strings.HasPrefix(u, "https:") {
			return "", fmt.Errorf("autodns: TLSRPT rua %q must use scheme mailto or https", u)
		}
	}
	return "v=TLSRPTv1; rua=" + strings.Join(rua, ","), nil
}

// BuildDMARCRecord renders the TXT body for `_dmarc.<domain>` per RFC 7489.
func BuildDMARCRecord(p DMARCPolicy) (string, error) {
	tags := []string{"v=DMARC1", "p=" + p.Policy.String()}
	if p.HasSubdomainPolicy {
		tags = append(tags, "sp="+p.SubdomainPolicy.String())
	}
	if len(p.RUA) > 0 {
		tags = append(tags, "rua="+strings.Join(p.RUA, ","))
	}
	if len(p.RUF) > 0 {
		tags = append(tags, "ruf="+strings.Join(p.RUF, ","))
	}
	if p.Pct >= 0 && p.Pct <= 100 && p.Pct != 100 {
		tags = append(tags, "pct="+strconv.Itoa(p.Pct))
	} else if p.Pct == 100 {
		tags = append(tags, "pct=100")
	}
	tags = append(tags, "adkim="+p.ADKIM.String())
	tags = append(tags, "aspf="+p.ASPF.String())
	if fo := encodeFO(p.FO); fo != "" {
		tags = append(tags, "fo="+fo)
	}
	return strings.Join(tags, "; "), nil
}

func encodeFO(f DMARCFailureOptions) string {
	var parts []string
	if f.FailAny {
		parts = append(parts, "0")
	}
	if f.FailAll {
		parts = append(parts, "1")
	}
	if f.FailDKIM {
		parts = append(parts, "d")
	}
	if f.FailSPF {
		parts = append(parts, "s")
	}
	sort.Strings(parts)
	return strings.Join(parts, ":")
}
