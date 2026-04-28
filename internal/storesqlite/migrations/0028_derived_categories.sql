-- 0028_derived_categories.sql — per-account derived category list
-- (REQ-FILT-217: the prompt is the single source of truth for categories;
-- the server persists the most recent classifier response's category list
-- here so the suite can render tabs without waiting for the next delivery).
--
-- derived_categories_json is a JSON array of lowercase ASCII dash-separated
-- name strings. NULL means no successful classifier call has occurred since
-- the most recent prompt change. Bounded to 64 entries x 200 bytes per name
-- (enforced by application code, not a DB constraint).
--
-- UpdateCategorisationConfig clears this column to NULL whenever the prompt
-- changes (REQ-FILT-217 invalidation rule). SetDerivedCategories updates only
-- this column without touching the rest of the config row.

ALTER TABLE jmap_categorisation_config
  ADD COLUMN derived_categories_json TEXT;
