// Package mailreact builds and enqueues outbound reaction emails for
// cross-server emoji reaction propagation (REQ-FLOW-100..103,
// REQ-PROTO-103).
//
// When a JMAP Email/set adds a reaction to a message that has recipients on
// non-local domains, BuildAndEnqueue constructs the RFC 5322 reaction email
// (multipart/alternative text+html body, X-Herold-Reaction-* headers,
// threading via In-Reply-To/References) and drops one queue item per
// external recipient via store.Metadata.EnqueueMessage.  Local recipients
// are skipped — they see the reaction natively through the Email.reactions
// state push.
//
// Removal (reaction delete) does NOT propagate cross-server per REQ-FLOW-103.
package mailreact
