-- 0014_inbound_attachment_policy.sql — Phase 3 Wave 3.5c Track B:
-- REQ-FLOW-ATTPOL-01. Mirrors storesqlite 0014. Forward-only.
--
-- See storesqlite/migrations/0014_inbound_attachment_policy.sql for
-- the full design notes.

CREATE TABLE inbound_attpol_recipient (
  address       TEXT PRIMARY KEY,
  policy        TEXT   NOT NULL,
  reject_text   TEXT   NOT NULL DEFAULT '',
  updated_at_us BIGINT NOT NULL
);

CREATE TABLE inbound_attpol_domain (
  domain        TEXT PRIMARY KEY,
  policy        TEXT   NOT NULL,
  reject_text   TEXT   NOT NULL DEFAULT '',
  updated_at_us BIGINT NOT NULL
);
