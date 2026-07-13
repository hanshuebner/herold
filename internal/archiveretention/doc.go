// Package archiveretention implements the periodic sweep that enforces
// each mailing list's configured archive retention bound (epic #187,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-74): an
// age bound (ArchiveRetentionDays), a count bound
// (ArchiveRetentionMaxMessages), or both. The worker follows the same
// pattern as internal/trashretention: a Run(ctx) loop that calls Tick(ctx)
// on each sweep interval, pages through mailing lists that have an
// archive mailbox configured, and calls ExpungeMessages on the messages
// that fall outside the bound. Expunging decrements the message's blob
// refcount through the normal store path (internal/store's
// InsertMessage/ExpungeMessages atomic ref-counting, REQ-STORE-12); a
// blob still referenced by a live archive message -- whether because it
// is within the retention bound or because another mailbox (a member's
// own delivered copy, or another list's archive) still holds it -- is
// never collected, since GC only ever acts on a blob whose refcount has
// reached zero.
package archiveretention
