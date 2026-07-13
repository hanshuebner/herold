-- 0086_mailing_list_unsubscribe.sql -- adds mailing_list.unsubscribe_enabled
-- (epic #184, docs/design/server/requirements/28-mailing-lists.md
-- REQ-MLIST-56..59). Gates the List-Unsubscribe / RFC 8058 one-click
-- header pair and the token-authorised self-service management page on
-- a per-list toggle, the same way arc_seal (migration 0085) gates ARC
-- sealing.
--
-- DEFAULT TRUE mirrors arc_seal's own migration default: every list that
-- existed before this migration starts emitting List-Unsubscribe on its
-- very next fan-out, a purely additive (never harmful) behaviour change.
-- Rows inserted through store.Metadata.InsertMailingList after this
-- migration always carry the caller's own explicit value regardless of
-- the column default (see store.MailingList.UnsubscribeEnabled's doc
-- comment).
--
-- Forward-only.

ALTER TABLE mailing_list ADD COLUMN unsubscribe_enabled INTEGER NOT NULL DEFAULT 1;
