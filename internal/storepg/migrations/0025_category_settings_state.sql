-- 0025_category_settings_state.sql -- Wave 3.13:
-- REQ-CAT-50 (https://netzhansa.com/jmap/categorise) CategorySettings
-- JMAP state counter.
--
-- Mirrors storesqlite/migrations/0025_category_settings_state.sql.
-- Postgres idiom: BIGINT, NOT NULL DEFAULT 0.

ALTER TABLE jmap_states
  ADD COLUMN category_settings_state BIGINT NOT NULL DEFAULT 0;
