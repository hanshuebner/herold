// Package protoevents implements the typed event dispatcher that fans
// events into event-publisher plugins (REQ-EVT).
//
// Two paths produce events:
//
//  1. Operational events emitted directly by subsystems (auth success
//     and failure, queue retry, ACME cert lifecycle, DKIM rotation,
//     plugin lifecycle, webhook delivery failures). Producers call
//     Emit; the dispatcher fans out asynchronously through a bounded
//     in-process channel. This path is best-effort by design — see
//     REQ-EVT-13: the bounded channel protects the hot path from
//     plugin slowness.
//
//  2. Mail-flow events derived from the per-principal change feed
//     (mail.received and friends). The dispatcher polls
//     store.ReadChangeFeed per configured principal, transforms each
//     row into the matching mail-flow Event, and feeds the result
//     into the same fan-out path. The change-feed cursor lives in
//     the cursors table keyed "events.changefeed.<principal-id>", so
//     a restart resumes from the last emitted change.
//
// The dispatcher does NOT persist events in a queue. It is not a
// durable event log; that is the change feed's job. Critical events
// MUST flow through the change-feed path.
//
// Ownership: http-api-implementor.
package protoevents
