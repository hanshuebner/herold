-- 0009_categorisation.sql — per-account LLM categorisation config
-- (REQ-FILT-200..221).
--
-- Mirrors storesqlite/migrations/0009_categorisation.sql; column
-- types map BIGINT/BYTEA/BOOLEAN per the established Wave 2 pattern.

CREATE TABLE jmap_categorisation_config (
  principal_id      BIGINT  NOT NULL PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  prompt            TEXT    NOT NULL,
  category_set_json BYTEA   NOT NULL,
  endpoint_url      TEXT,
  model             TEXT,
  api_key_env       TEXT,
  timeout_sec       INTEGER NOT NULL DEFAULT 5,
  enabled           BOOLEAN NOT NULL DEFAULT TRUE,
  updated_at_us     BIGINT  NOT NULL
);
