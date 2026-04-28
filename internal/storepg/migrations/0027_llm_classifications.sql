-- 0027_llm_classifications.sql — per-message LLM classification records
-- (REQ-FILT-66 / REQ-FILT-216 / G14 LLM-transparency server-side contract).
--
-- Mirrors storesqlite/migrations/0027_llm_classifications.sql; column types
-- map BIGINT/DOUBLE PRECISION/BOOLEAN per the established Wave 2 pattern.

CREATE TABLE llm_classifications (
  message_id               BIGINT   NOT NULL PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  principal_id             BIGINT   NOT NULL REFERENCES principals(id) ON DELETE CASCADE,

  spam_verdict             TEXT,
  spam_confidence          DOUBLE PRECISION,
  spam_reason              TEXT,
  spam_prompt_applied      TEXT,
  spam_model               TEXT,
  spam_classified_at_us    BIGINT,

  category_assigned        TEXT,
  category_prompt_applied  TEXT,
  category_model           TEXT,
  category_classified_at_us BIGINT
);

CREATE INDEX idx_llm_classifications_principal
  ON llm_classifications(principal_id);

ALTER TABLE jmap_categorisation_config
  ADD COLUMN guardrail TEXT NOT NULL DEFAULT '';
