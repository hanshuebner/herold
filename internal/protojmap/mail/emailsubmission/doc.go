// Package emailsubmission implements the JMAP EmailSubmission datatype
// per RFC 8621 §5: EmailSubmission/get, /changes, /query, /queryChanges,
// /set.
//
// EmailSubmission/set create dispatches into the outbound queue
// (REQ-PROTO-42): the handler resolves the referenced Email blob,
// looks up the Identity's signing domain, and calls
// queue.Queue.Submit. The returned EnvelopeID becomes the
// EmailSubmission row id; clients poll EmailSubmission/get to observe
// queue state transitions (`undoStatus` is rendered from the per-row
// store.QueueState).
//
// The capability URI is `urn:ietf:params:jmap:submission` per RFC 8621
// §1.1; we register it alongside the JMAP Mail capability so a
// client's `using` array can name either.
package emailsubmission
