package emailsubmission

import (
	"context"

	"github.com/hanshuebner/herold/internal/queue"
)

// Submitter is the narrow seam EmailSubmission/set uses to dispatch a
// new submission into the outbound queue. Production wires
// *queue.Queue, which satisfies the interface; tests use a fake that
// records the Submission shape and returns an EnvelopeID synchronously.
//
// Defining a method-level interface (instead of taking *queue.Queue
// directly) keeps the test seam local — the queue package does not
// need to know about JMAP, and JMAP tests don't need a real queue
// scheduler.
type Submitter interface {
	Submit(ctx context.Context, sub queue.Submission) (queue.EnvelopeID, error)
}

// queueAsSubmitter adapts *queue.Queue to the Submitter interface.
// Trivial pass-through; declared so the registration code can hand
// either the real queue or a test fake into the handler set without
// branching.
type queueAsSubmitter struct{ q *queue.Queue }

func (a queueAsSubmitter) Submit(ctx context.Context, sub queue.Submission) (queue.EnvelopeID, error) {
	return a.q.Submit(ctx, sub)
}
