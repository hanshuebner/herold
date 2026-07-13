-- 0087_queue_header_overlay.sql -- adds queue.header_overlay (issue #184,
-- REQ-MLIST-11 fan-out blob-dedup regression).
--
-- REQ-MLIST-56 (List-Unsubscribe) added a per-member header pair, and the
-- S2.3 implementation spliced it into the fan-out copy's body before
-- Submit, which broke REQ-MLIST-11: each member's distinct token produced
-- a distinct persisted body blob, so blob storage grew O(members) per
-- post instead of staying at one shared blob.
--
-- header_overlay carries the small per-recipient header block (a few
-- hundred bytes: List-Unsubscribe + List-Unsubscribe-Post) directly on
-- the queue row. Queue.deliver prepends it to the shared body blob, still
-- unsigned, as the message streams to the wire -- the persisted body
-- blob stays list-wide-identical and therefore shared across every
-- fan-out copy. Empty for every submission path that has no per-recipient
-- header variation (the overwhelming majority of rows).
--
-- Forward-only.

ALTER TABLE queue ADD COLUMN header_overlay TEXT NOT NULL DEFAULT '';
