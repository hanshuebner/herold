package queue

import (
	"context"
	"errors"
	"time"
)

// ErrConflict is returned by Submit when the supplied IdempotencyKey
// already names a prior submission. The first return value carries the
// existing EnvelopeID so the caller can map the duplicate request to
// the prior submission's receipt.
var ErrConflict = errors.New("queue: idempotent submission already enqueued")

// DeliveryStatus classifies a single delivery attempt's outcome.
//
// Success is terminal (CompleteQueueItem with success=true). Permanent
// is terminal-failure (CompleteQueueItem with success=false; DSN if
// requested). Transient causes the scheduler to RescheduleQueueItem
// using the configured RetryPolicy until the schedule is exhausted, at
// which point the orchestrator escalates to Permanent and emits a
// failure DSN if NOTIFY=FAILURE was requested.
type DeliveryStatus uint8

// DeliveryStatus values.
const (
	// DeliveryStatusUnknown is the zero value and must not be returned
	// by a Deliverer.
	DeliveryStatusUnknown DeliveryStatus = iota
	// DeliveryStatusSuccess indicates the receiving MTA accepted the
	// message (2xx). Terminal.
	DeliveryStatusSuccess
	// DeliveryStatusPermanent indicates the receiving MTA refused the
	// message (5xx) or a non-retryable policy condition fired.
	// Terminal.
	DeliveryStatusPermanent
	// DeliveryStatusTransient indicates a temporary failure (4xx,
	// network error, DNS-temp). The orchestrator reschedules.
	DeliveryStatusTransient
	// DeliveryStatusHold indicates an operator-initiated hold should be
	// applied to the row. Used when the deliverer recognises a policy
	// disposition that suspends delivery without a 4xx/5xx vocabulary
	// token (e.g. MTA-STS evaluation pending).
	DeliveryStatusHold
)

// String returns the canonical lowercase token for s. Used in metric
// labels and log fields; not persisted.
func (s DeliveryStatus) String() string {
	switch s {
	case DeliveryStatusSuccess:
		return "success"
	case DeliveryStatusPermanent:
		return "permanent"
	case DeliveryStatusTransient:
		return "transient"
	case DeliveryStatusHold:
		return "hold"
	default:
		return "unknown"
	}
}

// PolicyHints carries the pre-resolved security policy state the
// Deliverer applies to a single attempt. The orchestrator does not
// inspect these fields; they exist so the deliverer can be built
// against a stable shape while MTA-STS / DANE wiring lands in parallel.
type PolicyHints struct {
	// MTASTSEnforce, when true, instructs the deliverer to refuse
	// plaintext fallback for this destination.
	MTASTSEnforce bool
	// DANEPresent, when true, indicates DNSSEC-authenticated TLSA
	// records exist for the MX hostname; the deliverer must validate
	// the peer certificate against them.
	DANEPresent bool
	// REQUIRETLS mirrors the per-submission flag; carried here so the
	// deliverer has every TLS-policy input in one struct.
	REQUIRETLS bool
}

// DeliveryRequest is the orchestrator's call to the wire-side
// Deliverer. The orchestrator decides MailFrom, Recipient, and the
// fully-built message bytes; the deliverer chooses MX, dials, and
// reports per-recipient outcome.
type DeliveryRequest struct {
	// MailFrom is the SMTP MAIL FROM (reverse-path). Empty string
	// encodes the null sender ("<>" on the wire) used by DSNs.
	MailFrom string
	// Recipient is the single SMTP RCPT TO (forward-path) for this
	// delivery. The orchestrator never batches multiple recipients into
	// one DeliveryRequest; one queue row is one DeliveryRequest.
	Recipient string
	// Message is the fully-rendered message bytes (post-signing if the
	// caller asked for signing). Includes headers + CRLF + body.
	Message []byte
	// REQUIRETLS mirrors RFC 8689: when true the deliverer must not
	// fall back to plaintext.
	REQUIRETLS bool
	// PolicyHints carries pre-resolved MTA-STS / DANE hints. May be
	// the zero value when no hints are available.
	PolicyHints PolicyHints
}

// DeliveryOutcome is the result of one Deliver call. Status drives the
// orchestrator's transition; Code/EnhancedCode/Detail are persisted
// into the queue row's last_error column and surfaced in DSNs.
type DeliveryOutcome struct {
	// Status classifies the outcome.
	Status DeliveryStatus
	// Code is the SMTP reply code (0 when no SMTP exchange occurred).
	Code int
	// EnhancedCode is the RFC 3463 enhanced status code, e.g. "5.1.1".
	EnhancedCode string
	// Detail is a short, human-readable diagnostic suitable for a DSN
	// "Diagnostic-Code:" field (RFC 3464 §2.3.6). Truncate long server
	// banners at the source.
	Detail string
}

// Deliverer is the wire-side outbound SMTP surface. The orchestrator
// calls Deliver once per queue row claim; the implementation chooses MX,
// dials, optionally negotiates TLS, performs MAIL/RCPT/DATA, and
// translates the receiver's response into a DeliveryOutcome.
//
// The interface is intentionally minimal: a connection-pooled,
// destination-batched implementation is invisible to the orchestrator
// because the orchestrator already groups concurrency by MX hostname
// (see Options.PerHostMax).
type Deliverer interface {
	// Deliver attempts to deliver req. The returned outcome's Status
	// must be one of Success / Permanent / Transient / Hold (never
	// Unknown). A non-nil error is treated as Transient with the
	// error string copied into Detail; implementations should still
	// classify when possible.
	Deliver(ctx context.Context, req DeliveryRequest) (DeliveryOutcome, error)
}

// Signer is satisfied by the mail-auth subsystem (DKIM + optional ARC).
// A nil Signer on Options is allowed and means "no signing"; any
// Submission with Sign=true is enqueued unsigned and the worker
// renders the body verbatim.
//
// The Sign method is pure: given (domain, message), it returns a fresh
// byte slice with the appropriate signature header(s) prepended. The
// orchestrator never mutates message in place.
type Signer interface {
	// Sign returns a signed copy of message for the given signing
	// domain. The returned slice is owned by the caller; implementations
	// must not retain it.
	Sign(ctx context.Context, domain string, message []byte) ([]byte, error)
}

// RetryPolicy encodes the per-attempt backoff schedule. Element i is
// the delay applied between attempt i and attempt i+1; once Attempts
// reaches len(Schedule) the orchestrator escalates the row to permanent
// failure. An empty Schedule means "one attempt only".
//
// The default schedule (DefaultRetrySchedule) approximates RFC 5321
// §4.5.4 spirit: 5m, 15m, 1h, 4h, 12h, 24h, 48h, 96h — total ~7 days.
type RetryPolicy struct {
	// Schedule is the ordered list of delays. nil falls back to the
	// default.
	Schedule []time.Duration
}

// DefaultRetrySchedule is the default backoff schedule. Total duration
// approximates 7 days, which matches the queue-and-delivery design's
// "5-day expiry" guidance with one additional buffer step.
var DefaultRetrySchedule = []time.Duration{
	5 * time.Minute,
	15 * time.Minute,
	1 * time.Hour,
	4 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
	48 * time.Hour,
	96 * time.Hour,
}

// Next returns the delay for the (attempts+1)-th attempt: the duration
// the scheduler waits before re-claiming a deferred row whose
// Attempts == attempts. The second return is false when the schedule
// is exhausted; the caller treats that as permanent failure.
func (p RetryPolicy) Next(attempts int32) (time.Duration, bool) {
	sched := p.Schedule
	if sched == nil {
		sched = DefaultRetrySchedule
	}
	if int(attempts) >= len(sched) {
		return 0, false
	}
	return sched[attempts], true
}

// MaxAttempts returns the maximum number of attempts the policy
// permits before the orchestrator escalates to permanent failure.
func (p RetryPolicy) MaxAttempts() int {
	sched := p.Schedule
	if sched == nil {
		sched = DefaultRetrySchedule
	}
	// Initial attempt + len(Schedule) reschedules.
	return len(sched) + 1
}

// Stats is the snapshot returned by Queue.Stats. The counts mirror
// store.CountQueueByState; Inflight is the live in-process worker
// count (separate from the persisted count, which can lag a transition).
type Stats struct {
	// Queued is the number of rows in QueueStateQueued.
	Queued int
	// Deferred is the number of rows in QueueStateDeferred.
	Deferred int
	// Inflight is the number of rows the store reports as
	// QueueStateInflight (claimed but not yet completed).
	Inflight int
	// InflightWorkers is the count of in-process workers actively
	// running a Deliver call. Bounded by Concurrency.
	InflightWorkers int
	// Done is the cumulative count of successful terminal rows.
	Done int
	// Failed is the cumulative count of permanently-failed rows.
	Failed int
	// Held is the count of operator-held rows.
	Held int
}
