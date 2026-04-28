-- 0025_category_settings_state.sql -- Wave 3.13:
-- REQ-CAT-50 (https://netzhansa.com/jmap/categorise) CategorySettings
-- JMAP state counter.
--
-- The per-account category set and classifier prompt are already stored
-- in jmap_categorisation_config (migration 0009). This migration adds
-- the matching JMAP state counter to jmap_states so CategorySettings/get
-- and CategorySettings/set can return and advance a state string per the
-- standard JMAP pattern. Forward-only; no drop or alter on other tables.

ALTER TABLE jmap_states
  ADD COLUMN category_settings_state INTEGER NOT NULL DEFAULT 0;
