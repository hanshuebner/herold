package protosmtp

import (
	"context"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// DeliveryStatus enumerates the per-recipient verdict the outbound Client
// returns for one delivery attempt. The queue uses these to decide whether
// the row transitions to done (Success), schedules a retry (Transient), or
// emits a failure DSN (Permanent). Unknown is the zero value and must not
// leak to callers; the Client always sets a concrete status before returning.
type DeliveryStatus int

const (
	// DeliveryUnknown is the zero value; the Client never returns it.
	DeliveryUnknown DeliveryStatus = iota
	// DeliverySuccess indicates the remote MX accepted the message with a
	// 2xx final reply after DATA / BDAT LAST.
	DeliverySuccess
	// DeliveryTransient indicates a 4xx reply, a temporary network error,
	// or an exhausted MX list with the last failure being temporary. The
	// queue should reschedule the attempt.
	DeliveryTransient
	// DeliveryPermanent indicates a 5xx reply or a policy gate (MTA-STS
	// enforce mismatch, DANE TLSA mismatch, REQUIRETLS unsatisfied) that
	// should bounce the recipient rather than retry.
	DeliveryPermanent
)

// String returns the lower-case identifier used in logs / metrics labels.
func (s DeliveryStatus) String() string {
	switch s {
	case DeliverySuccess:
		return "success"
	case DeliveryTransient:
		return "transient"
	case DeliveryPermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// DeliveryRequest is the queue → Deliverer call shape: one outbound
// attempt for one recipient, signed and ready to send. The queue passes
// the already-DKIM-signed bytes; the Client does not re-sign.
//
// MailFrom and RcptTo are wire-form addresses without angle brackets;
// the empty MailFrom is the null reverse-path used for DSNs (RFC 5321 §4.5.5).
type DeliveryRequest struct {
	// MailFrom is the envelope sender (RFC 5321 §3.3). Empty string means
	// null reverse-path; the Client emits "MAIL FROM:<>" verbatim.
	MailFrom string
	// RcptTo is the single envelope recipient for this attempt.
	RcptTo string
	// Message is the full RFC 5322 message bytes the Client transmits in
	// DATA / BDAT. Lines are CRLF-terminated; the Client dot-stuffs as needed.
	Message []byte
	// MessageID is the RFC 5322 Message-ID for log correlation. Empty
	// when unavailable.
	MessageID string
	// REQUIRETLS, when true, opts the message into RFC 8689 enforcement:
	// the Client refuses to send unless STARTTLS is negotiated AND the
	// chosen TLS path satisfies whatever policy applies (DANE / MTA-STS
	// enforce). Unset implies opportunistic TLS rules.
	REQUIRETLS bool
	// SMTPUTF8 hints that the envelope or message may contain non-ASCII;
	// the Client emits SMTPUTF8 on MAIL FROM if the remote advertises it.
	SMTPUTF8 bool
	// EightBitMIME hints that the message contains 8-bit body bytes; the
	// Client emits BODY=8BITMIME if the remote advertises 8BITMIME.
	EightBitMIME bool
	// EnvID and Notify are RFC 3461 DSN parameters propagated onto the
	// outbound MAIL FROM / RCPT TO when the remote advertises DSN.
	EnvID  string
	Notify string
}

// DeliveryOutcome is the Deliverer's per-recipient result. The queue maps
// outcomes onto its row state; the Status alone determines retry policy
// and the diagnostic fields surface in logs / DSNs.
type DeliveryOutcome struct {
	// Status is the verdict.
	Status DeliveryStatus
	// SMTPCode is the final 3-digit reply (e.g. 250, 451, 550). Zero when
	// no SMTP exchange happened (network failure, policy gate before EHLO).
	SMTPCode int
	// EnhancedCode is the optional RFC 3463 enhanced status (e.g. "5.1.1");
	// empty when the remote did not advertise ENHANCEDSTATUSCODES or the
	// failure happened before EHLO.
	EnhancedCode string
	// Diagnostic is a short human-readable description suitable for the
	// DSN diagnostic-code field and admin logs. Always set on failure.
	Diagnostic string
	// MXHost is the MX target the Client connected to (or attempted to
	// connect to last on failure). Empty when MX resolution itself failed.
	MXHost string
	// TLSUsed reports whether the message was transmitted over a TLS
	// connection. When true, TLSPolicy describes the policy that gated it.
	TLSUsed bool
	// TLSPolicy is one of "none", "opportunistic", "mta_sts", "dane".
	// Surfaced for the herold_smtp_outbound_tls_usage_total{policy} metric
	// the queue agent emits.
	TLSPolicy string
	// AttemptedAt is the clock-derived time the attempt began. Used by
	// the queue for retry scheduling and TLS-RPT records.
	AttemptedAt time.Time
}

// MTASTSPolicyMode enumerates the three policy modes RFC 8461 §3.2 defines.
type MTASTSPolicyMode int

const (
	// MTASTSModeNone means no policy is published or in effect.
	MTASTSModeNone MTASTSPolicyMode = iota
	// MTASTSModeTesting means the policy exists but failures must not
	// block delivery (RFC 8461 §5).
	MTASTSModeTesting
	// MTASTSModeEnforce means policy violations MUST cause the delivery
	// to fail.
	MTASTSModeEnforce
)

// String returns the wire-form token used in policy files and logs.
func (m MTASTSPolicyMode) String() string {
	switch m {
	case MTASTSModeTesting:
		return "testing"
	case MTASTSModeEnforce:
		return "enforce"
	default:
		return "none"
	}
}

// MTASTSPolicy is a parsed RFC 8461 §3.2 policy. MX entries may be
// "*"-prefixed wildcards; PolicyID is the value the operator publishes
// as id= in the _mta-sts.<domain> TXT record (the Client uses it for
// cache invalidation, not for matching).
type MTASTSPolicy struct {
	Mode      MTASTSPolicyMode
	MX        []string
	MaxAge    time.Duration
	PolicyID  string
	FetchedAt time.Time
}

// Match reports whether host satisfies any MX pattern in the policy.
// Comparison is case-insensitive on the canonical lower-case host; a
// pattern beginning with "*." matches one DNS label at the corresponding
// position (RFC 8461 §4.1).
func (p *MTASTSPolicy) Match(host string) bool {
	if p == nil {
		return false
	}
	host = canonicaliseHost(host)
	for _, pat := range p.MX {
		if mtaSTSMatch(canonicaliseHost(pat), host) {
			return true
		}
	}
	return false
}

// MTASTSCache is the policy cache the outbound Client consults at delivery
// time. Implementations fetch the policy from `_mta-sts.<domain>` TXT plus
// `https://mta-sts.<domain>/.well-known/mta-sts.txt` and cache by max_age
// up to the 31-day spec ceiling. Lookup MUST NOT block forever; callers
// pass a ctx with a deadline.
type MTASTSCache interface {
	// Lookup returns the cached or freshly-fetched MTA-STS policy for
	// domain. A non-nil policy with Mode==None is equivalent to the
	// domain having no policy. err is reserved for transport-level
	// failures that should be treated as TempError; the absence of a
	// policy is not an error.
	Lookup(ctx context.Context, domain string) (*MTASTSPolicy, error)
}

// TLSRPTReporter is the per-failure ingestion point used by the outbound
// Client when a TLS or DANE failure occurs against a domain that publishes
// `_smtp._tls.<domain>` TXT. The autodns/TLS-RPT agent batches these into
// daily RUA payloads.
type TLSRPTReporter interface {
	// Append records one TLS / DANE failure. The Client passes a fully
	// populated store.TLSRPTFailure with PolicyDomain, ReceivingMTAHostname,
	// FailureType, FailureCode, and FailureDetailJSON set. Append must
	// honour ctx cancellation.
	Append(ctx context.Context, f store.TLSRPTFailure) error
}
