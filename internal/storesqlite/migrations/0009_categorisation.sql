-- 0009_categorisation.sql — per-account LLM categorisation config
-- (REQ-FILT-200..221).
--
-- One row per principal. Endpoint, model, api_key_env, and
-- timeout_sec are nullable so a per-account row can override only the
-- knobs the user cares about; the categoriser falls back to operator
-- defaults at call time when these are NULL. prompt and
-- category_set_json are required and seeded with the documented
-- defaults (REQ-FILT-201/210/211) on first read.
--
-- The row cascades on principal delete to mirror every other
-- principal-scoped table in this schema. Forward-only; the
-- migrations table records 0009 as applied.

CREATE TABLE jmap_categorisation_config (
  principal_id      INTEGER NOT NULL PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  prompt            TEXT    NOT NULL,
  category_set_json BLOB    NOT NULL,
  endpoint_url      TEXT,
  model             TEXT,
  api_key_env       TEXT,
  timeout_sec       INTEGER NOT NULL DEFAULT 5,
  enabled           INTEGER NOT NULL DEFAULT 1,
  updated_at_us     INTEGER NOT NULL
) STRICT;
