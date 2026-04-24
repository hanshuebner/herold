package mailauth

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AuthStatus is the RFC 7601-style verdict used across DKIM, SPF, DMARC,
// and ARC results. Not every status is valid for every method: see the
// comment on each *Result field.
type AuthStatus int

// AuthStatus values mirror the RFC 7601 / RFC 8601 "result" vocabulary.
// AuthUnknown is the zero value and indicates the check was not performed.
const (
	AuthUnknown AuthStatus = iota
	AuthPass
	AuthFail
	AuthSoftFail
	AuthNeutral
	AuthNone
	AuthPolicy
	AuthTempError
	AuthPermError
)

// String returns the canonical wire-form name for the status, matching the
// tokens used in Authentication-Results headers (RFC 8601 §2.7).
func (s AuthStatus) String() string {
	switch s {
	case AuthPass:
		return "pass"
	case AuthFail:
		return "fail"
	case AuthSoftFail:
		return "softfail"
	case AuthNeutral:
		return "neutral"
	case AuthNone:
		return "none"
	case AuthPolicy:
		return "policy"
	case AuthTempError:
		return "temperror"
	case AuthPermError:
		return "permerror"
	default:
		return "unknown"
	}
}

// MarshalJSON renders the status as its lowercase wire-form string so the
// admin REST and events payloads are stable across Go restarts and release
// builds.
func (s AuthStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON parses the wire-form status. Unknown tokens round-trip to
// AuthUnknown rather than erroring so that forward-compatible payloads
// from a newer server version do not break parsing on an older one.
func (s *AuthStatus) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	switch strings.ToLower(name) {
	case "pass":
		*s = AuthPass
	case "fail":
		*s = AuthFail
	case "softfail":
		*s = AuthSoftFail
	case "neutral":
		*s = AuthNeutral
	case "none":
		*s = AuthNone
	case "policy":
		*s = AuthPolicy
	case "temperror":
		*s = AuthTempError
	case "permerror":
		*s = AuthPermError
	default:
		*s = AuthUnknown
	}
	return nil
}

// DMARCPolicy is the "p=" (or "sp=") value from a DMARC record.
type DMARCPolicy int

// DMARCPolicy values match the tokens defined in RFC 7489 §6.3.
const (
	DMARCPolicyNone DMARCPolicy = iota
	DMARCPolicyQuarantine
	DMARCPolicyReject
)

// String returns the canonical token for the policy.
func (p DMARCPolicy) String() string {
	switch p {
	case DMARCPolicyNone:
		return "none"
	case DMARCPolicyQuarantine:
		return "quarantine"
	case DMARCPolicyReject:
		return "reject"
	default:
		return fmt.Sprintf("dmarc-policy(%d)", int(p))
	}
}

// MarshalJSON serialises the policy as its RFC 7489 token.
func (p DMARCPolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

// UnmarshalJSON accepts any of the RFC 7489 tokens.
func (p *DMARCPolicy) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch strings.ToLower(s) {
	case "none", "":
		*p = DMARCPolicyNone
	case "quarantine":
		*p = DMARCPolicyQuarantine
	case "reject":
		*p = DMARCPolicyReject
	default:
		return fmt.Errorf("mailauth: unknown DMARC policy %q", s)
	}
	return nil
}

// DMARCDisposition is the action the evaluator actually applied. It may
// differ from the published policy when pct= sampling or a local override
// kicks in.
type DMARCDisposition int

// DMARCDisposition values mirror RFC 7489 §6.6.
const (
	DispositionNone DMARCDisposition = iota
	DispositionQuarantine
	DispositionReject
)

// String returns the canonical token for the disposition.
func (d DMARCDisposition) String() string {
	switch d {
	case DispositionNone:
		return "none"
	case DispositionQuarantine:
		return "quarantine"
	case DispositionReject:
		return "reject"
	default:
		return fmt.Sprintf("dmarc-disposition(%d)", int(d))
	}
}

// MarshalJSON serialises the disposition as its RFC 7489 token.
func (d DMARCDisposition) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts any of the RFC 7489 tokens.
func (d *DMARCDisposition) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch strings.ToLower(s) {
	case "none", "":
		*d = DispositionNone
	case "quarantine":
		*d = DispositionQuarantine
	case "reject":
		*d = DispositionReject
	default:
		return fmt.Errorf("mailauth: unknown DMARC disposition %q", s)
	}
	return nil
}

// DKIMResult is the outcome of verifying one DKIM-Signature header.
type DKIMResult struct {
	// Status is Pass | Fail | Neutral | Policy | TempError | PermError | None.
	Status AuthStatus `json:"status"`
	// Domain is the d= tag (SDID).
	Domain string `json:"domain,omitempty"`
	// Selector is the s= tag.
	Selector string `json:"selector,omitempty"`
	// Algorithm is the a= tag, e.g. "rsa-sha256" or "ed25519-sha256".
	Algorithm string `json:"algorithm,omitempty"`
	// Identifier is the i= tag (AUID), e.g. "@example.com".
	Identifier string `json:"identifier,omitempty"`
	// Reason is a short machine-readable reason code when Status is not Pass.
	Reason string `json:"reason,omitempty"`
}

// SPFResult is the outcome of SPF evaluation on MAIL FROM / HELO.
type SPFResult struct {
	// Status is Pass | Fail | SoftFail | Neutral | None | TempError | PermError.
	Status AuthStatus `json:"status"`
	// From is the address whose domain was checked (RFC 5321.MailFrom, or
	// the HELO name if MAIL FROM was null).
	From string `json:"from,omitempty"`
	// HELO is the domain supplied on the SMTP HELO/EHLO command.
	HELO string `json:"helo,omitempty"`
	// ClientIP is the peer's IP at the time the check ran.
	ClientIP string `json:"client_ip,omitempty"`
	// Reason carries the RFC 7208 explain string or an internal reason code.
	Reason string `json:"reason,omitempty"`
}

// DMARCResult is the evaluated DMARC outcome for one message.
type DMARCResult struct {
	// Status is Pass | Fail. DMARC does not emit other values; a missing
	// policy produces AuthNone in this field.
	Status AuthStatus `json:"status"`
	// Policy is the advertised p= (or sp= for subdomains).
	Policy DMARCPolicy `json:"policy"`
	// Disposition is the action that was actually applied (possibly
	// overridden by pct= sampling or local policy).
	Disposition DMARCDisposition `json:"disposition"`
	// SPFAligned reports whether SPF both passed and aligned.
	SPFAligned bool `json:"spf_aligned"`
	// DKIMAligned reports whether at least one DKIM signature passed and
	// aligned.
	DKIMAligned bool `json:"dkim_aligned"`
	// HeaderFrom is the RFC 5322.From domain used for alignment.
	HeaderFrom string `json:"header_from,omitempty"`
	// OrgDomain is the Organizational Domain derived from HeaderFrom.
	OrgDomain string `json:"org_domain,omitempty"`
	// Reason is a short machine-readable reason code.
	Reason string `json:"reason,omitempty"`
}

// ARCResult is the chain-of-custody verdict for forwarded / mailing-list
// mail. See RFC 8617.
type ARCResult struct {
	// Status is Pass | Fail | None.
	Status AuthStatus `json:"status"`
	// Chain is the number of ARC-Seal instances (i= values) observed.
	Chain int `json:"chain"`
	// Reason is a short machine-readable reason code.
	Reason string `json:"reason,omitempty"`
}

// AuthResults is the consolidated inbound authentication verdict for one
// message. Every field is a typed enum; callers never parse the wire
// Authentication-Results header.
type AuthResults struct {
	// DKIM is one result per DKIM-Signature header that was evaluated.
	DKIM []DKIMResult `json:"dkim,omitempty"`
	// SPF is the outcome of SPF evaluation for the envelope sender.
	SPF SPFResult `json:"spf"`
	// DMARC is the DMARC evaluation using DKIM + SPF results.
	DMARC DMARCResult `json:"dmarc"`
	// ARC is the verified ARC chain, if any.
	ARC ARCResult `json:"arc"`
	// Raw preserves the original Authentication-Results header content
	// verbatim so ARC sealing can rely on it on forward and so tools that
	// need to re-parse can.
	Raw string `json:"raw,omitempty"`
}
