package extsubmit

import (
	"io"
	"time"
)

// RefreshLeadTime is the fixed duration before OAuth token expiry at which the
// sweeper schedules the next refresh. Using a fixed buffer (rather than a
// percentage of token lifetime) is more predictable across providers and gives
// the sweeper 5 ticks of headroom at the default 60-second interval to retry
// on transient refresh failures.
//
// All code that writes RefreshDue to an IdentitySubmission row must use this
// constant so the two write sites (OAuth callback and sweeper refresh) remain
// consistent (REQ-AUTH-EXT-SUBMIT-05).
const RefreshLeadTime = 5 * time.Minute

// OutcomeState is the result category of one external submission attempt.
type OutcomeState string

const (
	// OutcomeOK means the remote accepted the message (2xx reply to DATA).
	OutcomeOK OutcomeState = "ok"
	// OutcomeAuthFailed means the remote rejected AUTH or an OAuth refresh
	// failed. The caller should record IdentitySubmissionStateAuthFailed.
	OutcomeAuthFailed OutcomeState = "auth-failed"
	// OutcomeUnreachable means a network or DNS failure occurred before the
	// SMTP conversation could begin. The caller should record
	// IdentitySubmissionStateUnreachable.
	OutcomeUnreachable OutcomeState = "unreachable"
	// OutcomePermanent means the remote returned a 5xx on MAIL FROM, RCPT TO,
	// or DATA. Treat as a hard-fail; do not retry.
	OutcomePermanent OutcomeState = "permanent"
	// OutcomeTransient means the remote returned a 4xx on MAIL FROM, RCPT TO,
	// or DATA. Treat as a soft-fail.
	OutcomeTransient OutcomeState = "transient"
)

// Outcome is the result of one external submission attempt.
type Outcome struct {
	// State is the categorical result.
	State OutcomeState
	// Diagnostic is a human-readable description of the result, suitable for
	// surfacing in audit logs and admin UI. It never contains credential
	// material.
	Diagnostic string
	// CorrelationID, when set, is an opaque token that links this outcome to
	// the JMAP EmailSubmission id for audit events (REQ-AUTH-EXT-SUBMIT-09).
	CorrelationID string
	// MTAID is the MTA-supplied queue-id from the server's 2xx DATA reply,
	// or empty on failure.
	MTAID string
}

// Envelope carries the message content for one external submission attempt.
// It corresponds to the RFC 5321 envelope plus the message body.
type Envelope struct {
	// MailFrom is the SMTP reverse-path (MAIL FROM address). Empty string
	// becomes "<>" on the wire.
	MailFrom string
	// RcptTo is the list of SMTP forward-paths (RCPT TO addresses). Must
	// contain at least one entry.
	RcptTo []string
	// Body is the RFC 5322 message bytes. It is read exactly once; the
	// caller must supply a fresh reader for each attempt.
	Body io.Reader
	// CorrelationID is an opaque token propagated into the Outcome, used
	// to link the submission result back to the JMAP EmailSubmission id.
	CorrelationID string
}
