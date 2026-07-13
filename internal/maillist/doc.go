// Package maillist implements Stage 1 of hosted mailing lists (issue
// #183, docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-*,
// docs/design/server/architecture/14-mailing-lists.md). It owns list
// expansion (turning one post to a list's posting_address into one
// outbound queue row per active/each member) and the fan-out shaping
// (List-* headers, ARC-seal, idempotent subject tag, Auto-Submitted)
// applied at enqueue time without ever rewriting the persisted inbound
// blob.
//
// The package depends only on the metadata store's mailing-list surface
// (internal/store), the mailparse header accessor, mailauth's
// authentication-result type, and two narrow interfaces (Submitter,
// Sealer) that its caller (internal/protosmtp) satisfies with the real
// outbound queue and mailarc.Sealer. This keeps maillist ignorant of
// SMTP session state and of the queue's concrete implementation.
package maillist
