package protosmtp

import (
	"context"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// SubmissionQueue is the seam between the SMTP submission listener and
// the outbound queue (Wave 3.1.6). When a non-local RCPT TO arrives on
// a SubmissionSTARTTLS / SubmissionImplicitTLS listener and the session
// is authenticated, the DATA-phase loop calls Submit so the message
// flows through the same outbound pipeline JMAP EmailSubmission and the
// HTTP send API already use post-Wave-3.1.5.
//
// The interface is the narrow shape *queue.Queue exposes; the SMTP
// package keeps it as an interface so the queue package's importer
// graph stays one-way (queue → store, not protosmtp → queue at compile
// time). Production wiring constructs a *queue.Queue and passes it to
// SetSubmissionQueue; the queue package satisfies the interface
// directly.
type SubmissionQueue interface {
	Submit(ctx context.Context, msg queue.Submission) (queue.EnvelopeID, error)
}

// SyntheticDispatch carries the per-message inputs for a synthetic-
// recipient webhook delivery.  It mirrors protowebhook.SyntheticDispatch
// without importing protowebhook here; the protosmtp ↔ protowebhook
// dependency is uni-directional via this seam (protowebhook does not
// import protosmtp; protosmtp does not import protowebhook).
type SyntheticDispatch struct {
	// Domain is the recipient's domain (lowercased). Used to filter
	// matching subscriptions.
	Domain string
	// Recipient is the synthetic RCPT TO address.
	Recipient string
	// MailFrom is the SMTP MAIL FROM (reverse-path).
	MailFrom string
	// RouteTag is the directory.resolve_rcpt plugin's correlation token.
	RouteTag string
	// BlobHash is the canonical content hash of the persisted body.
	BlobHash string
	// Size is the body byte count.
	Size int64
	// Parsed is the parsed message (re-used so the dispatcher does not
	// re-parse for every subscription).
	Parsed mailparse.Message
}

// WebhookDispatcher is the seam between the SMTP DATA-phase loop and
// the protowebhook subsystem for synthetic-recipient deliveries
// (Wave 3.5c-Z, REQ-DIR-RCPT-07 + REQ-HOOK-02). It bypasses the change
// feed because synthetic deliveries lack a messages-row.
//
// The protowebhook package adapts itself onto this seam; production
// wiring sets it via SetWebhookDispatcher.
type WebhookDispatcher interface {
	// MatchingSyntheticHooks returns the active webhook subscriptions
	// that match a synthetic recipient on the supplied domain. Empty
	// slice when no subscriber is configured (the operator hasn't
	// wired up the recipe yet, or the domain has no hooks).
	MatchingSyntheticHooks(ctx context.Context, domain string) []store.Webhook
	// DispatchSynthetic enqueues one webhook delivery per supplied
	// subscription. The dispatcher owns the goroutine lifetime via its
	// own bounded semaphore + parent ctx; per-delivery failures are
	// logged and metered inside the package, NOT surfaced here.
	DispatchSynthetic(ctx context.Context, in SyntheticDispatch, hooks []store.Webhook) error
}
