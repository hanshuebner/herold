-- 0086_mailing_list_unsubscribe.sql -- adds mailing_list.unsubscribe_enabled
-- (epic #184, docs/design/server/requirements/28-mailing-lists.md
-- REQ-MLIST-56..59). Mirrors
-- internal/storesqlite/migrations/0086_mailing_list_unsubscribe.sql; see
-- that file for the full rationale.
--
-- Forward-only.

ALTER TABLE mailing_list ADD COLUMN unsubscribe_enabled BOOLEAN NOT NULL DEFAULT TRUE;
