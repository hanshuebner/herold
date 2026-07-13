// Package dsn parses inbound RFC 3464 delivery status notifications
// (DSNs) -- and the pragmatic "bounce-ish" reports real-world mail
// servers emit instead of a conformant DSN -- and classifies the
// reported failure as hard (permanent, 5.x.x) or soft (transient,
// 4.x.x).
//
// This is the wire-format parser behind the mailing-list VERP bounce
// processor (docs/design/server/requirements/28-mailing-lists.md
// REQ-MLIST-51, issue #184): a message accepted at a hosted list's
// per-member VERP bounce address is handed to Parse to recover the
// classification internal/maillist attributes to that member.
//
// # Structure
//
// A conformant DSN is a multipart/report message (RFC 6522) carrying a
// message/delivery-status part (RFC 3464): a per-message field block
// (Reporting-MTA, Arrival-Date, ...) followed by one or more
// per-recipient field blocks (Original-Recipient, Final-Recipient,
// Action, Status, Diagnostic-Code, ...), each block separated by a
// blank line. Parse locates that part anywhere in the MIME tree (not
// only directly under a multipart/report container, since some
// real-world senders nest or omit the report-type wrapper) and reads
// its field blocks with the same tolerant, structured header reader
// used for RFC 5322 headers elsewhere in this codebase -- never ad-hoc
// substring matching against the raw bytes.
//
// A message with no message/delivery-status part at all (a plain-text
// "Undeliverable" notice from a non-conformant sender, or any other
// mail that merely looks bounce-ish) is not an error: Parse returns a
// Report with Classification set to ClassificationUnknown and an empty
// Recipients slice, so callers degrade gracefully instead of guessing
// from free text.
//
// # Classification is conservative
//
// A wrong hard classification later drives an incorrect membership
// auto-suspend (REQ-MLIST-53/54); a wrong soft or unknown classification
// merely delays that decision. Classify errs toward the cheaper
// mistake: Hard requires an unambiguous, internally consistent 5.x.x
// Action: failed signal; anything less certain classifies Soft or
// Unknown. See classifyOne's doc comment for the exact rule.
package dsn
