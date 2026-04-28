-- 0027_llm_classifications.sql — per-message LLM classification records
-- (REQ-FILT-66 / REQ-FILT-216 / G14 LLM-transparency server-side contract).
--
-- Stores spam-classification verdict/confidence/reason/model/prompt-as-applied
-- and categorisation assignment/prompt-as-applied/model per delivered message.
-- Written once at delivery; overwritten on re-classification (REQ-FILT-220).
--
-- Guardrails are NEVER stored here; only the user-visible prompt portion is
-- persisted (REQ-FILT-67).
--
-- The guardrail column is added to jmap_categorisation_config so operators
-- can move abuse-prevention preambles out of the user-visible prompt into a
-- separately-stored field that the transparency endpoints never return.

-- Per-message LLM classification records.
CREATE TABLE llm_classifications (
  message_id               INTEGER NOT NULL PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  principal_id             INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,

  -- Spam classification sub-record (all nullable: absent when classifier not run).
  spam_verdict             TEXT,
  spam_confidence          REAL,
  spam_reason              TEXT,
  spam_prompt_applied      TEXT,
  spam_model               TEXT,
  spam_classified_at_us    INTEGER,

  -- Categorisation sub-record (all nullable: absent when categoriser not run).
  category_assigned        TEXT,
  category_prompt_applied  TEXT,
  category_model           TEXT,
  category_classified_at_us INTEGER
) STRICT;

CREATE INDEX idx_llm_classifications_principal
  ON llm_classifications(principal_id);

-- Operator guardrail column on the categorisation config table.
-- Default empty string: existing rows have no guardrail (REQ-FILT-67
-- migration rule: existing prompt values stay in prompt; guardrail defaults
-- to empty so operators must consciously add them).
ALTER TABLE jmap_categorisation_config
  ADD COLUMN guardrail TEXT NOT NULL DEFAULT '';
