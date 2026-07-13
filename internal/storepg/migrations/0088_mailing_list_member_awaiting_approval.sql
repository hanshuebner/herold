-- 0088_mailing_list_member_awaiting_approval.sql -- widens
-- mailing_list_member.state to accept 'awaiting-approval' (Stage 3 self-
-- subscription, issue #185, REQ-MLIST-60..63). Mirrors
-- storesqlite/migrations/0088_mailing_list_member_awaiting_approval.sql
-- (which must rebuild the STRICT table because SQLite cannot ALTER a
-- CHECK); Postgres only needs to drop and re-add the auto-named
-- single-column CHECK constraint.
--
-- Forward-only.

ALTER TABLE mailing_list_member
  DROP CONSTRAINT mailing_list_member_state_check;

ALTER TABLE mailing_list_member
  ADD CONSTRAINT mailing_list_member_state_check
  CHECK (state IN ('active', 'suspended', 'unsubscribed', 'pending', 'awaiting-approval'));
