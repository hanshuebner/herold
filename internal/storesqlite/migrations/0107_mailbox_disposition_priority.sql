-- 0107_mailbox_disposition_priority.sql -- category disposition and
-- priority on mailboxes (issue #333, ADR-0004,
-- docs/design/web/requirements/05-categorisation.md REQ-CAT-01..11).
--
-- disposition classifies how a category (a label, modelled as a
-- mailbox) renders in a client's inbox lanes: one of 'none', 'pinned',
-- 'bundled', 'daily', 'weekly', 'filed'. The enum is validated by the
-- Go layer (store.ValidMailboxDisposition), matching the precedent set
-- by ingest_source (migration 0105) and delivery_disposition
-- (migration 0090). 'none' (the NOT NULL DEFAULT) means a plain
-- mailbox with no special inbox rendering, preserving current
-- behaviour for every existing row.
--
-- priority is a mailbox's position in its principal's user-ordered
-- ranked-label list; NULL means unranked. Lower values rank higher.
-- store.Metadata.ReorderMailboxPriority keeps the set of non-NULL
-- values dense (0..n-1) per principal.
--
-- Forward-only. Mirrors storepg 0107.

ALTER TABLE mailboxes ADD COLUMN disposition TEXT NOT NULL DEFAULT 'none';
ALTER TABLE mailboxes ADD COLUMN priority INTEGER;
