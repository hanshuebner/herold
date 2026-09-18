-- 0113_imapimport_own_addresses.sql -- per-account own-address list (re
-- #396, third round). See
-- storesqlite/migrations/0113_imapimport_own_addresses.sql for the full
-- rationale.

ALTER TABLE imapimport_account ADD COLUMN own_addresses_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE imapimport_account ADD COLUMN learned_addresses_json TEXT;
ALTER TABLE imapimport_account ADD COLUMN addresses_learned_at BIGINT;
