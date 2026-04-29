package emailsubmission

import (
	"context"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// Submitter is the narrow seam EmailSubmission/set uses to dispatch a
// new submission into the outbound queue and to cancel a pending one.
// Production wires *queue.Queue, which satisfies the interface; tests
// use a fake that records the Submission shape and returns an
// EnvelopeID synchronously.
//
// Defining a method-level interface (instead of taking *queue.Queue
// directly) keeps the test seam local — the queue package does not
// need to know about JMAP, and JMAP tests don't need a real queue
// scheduler.
//
// Cancel implements REQ-PROTO-58 / REQ-FLOW-63: EmailSubmission/set {
// destroy } before sendAt MUST atomically remove the submission's
// queue rows. The first return is the count of rows the queue removed
// (still cancellable); the second is the count already in-flight (the
// deliverer has the wire and the destroy is a no-op for those rows —
// the JMAP layer surfaces this back to the client as a setError with
// type "alreadyInflight").
type Submitter interface {
	Submit(ctx context.Context, sub queue.Submission) (queue.EnvelopeID, error)
	Cancel(ctx context.Context, env queue.EnvelopeID) (cancelled, inflight int, err error)
}

// ExternalSubmitter is the narrow seam EmailSubmission/set uses to dispatch
// a new submission through an external SMTP relay when the Identity has an
// external submission configuration. Production wires *extsubmit.Submitter;
// tests inject a fake. The interface carries only Submit: there is no Cancel
// for external submissions (REQ-AUTH-EXT-SUBMIT-05: no undo once the wire
// transaction has completed).
type ExternalSubmitter interface {
	Submit(ctx context.Context, sub store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome
}

// ExternalRouter provides the HasExternalSubmission and SubmissionConfig
// predicates so EmailSubmission/set can decide whether to route through the
// external submitter. Production wires *identity.Store; tests inject a stub.
type ExternalRouter interface {
	HasExternalSubmission(ctx context.Context, pid store.PrincipalID, identityJMAPID string) bool
	SubmissionConfig(ctx context.Context, pid store.PrincipalID, identityJMAPID string) (store.IdentitySubmission, error)
	BumpIdentityPushState(ctx context.Context, pid store.PrincipalID) error
}

// queueAsSubmitter adapts *queue.Queue to the Submitter interface.
// Trivial pass-through; declared so the registration code can hand
// either the real queue or a test fake into the handler set without
// branching.
type queueAsSubmitter struct{ q *queue.Queue }

func (a queueAsSubmitter) Submit(ctx context.Context, sub queue.Submission) (queue.EnvelopeID, error) {
	return a.q.Submit(ctx, sub)
}

func (a queueAsSubmitter) Cancel(ctx context.Context, env queue.EnvelopeID) (cancelled, inflight int, err error) {
	return a.q.Cancel(ctx, env)
}
