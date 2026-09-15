-- 0111_llm_classification_signals.sql -- structured spam/ham signals and
-- the ham-verdict-vs-spam-signals inconsistency marker on the LLM
-- classification transparency record (re #396).
--
-- Mirrors storesqlite/migrations/0111_llm_classification_signals.sql.

ALTER TABLE llm_classifications
  ADD COLUMN spam_signals_json TEXT;

ALTER TABLE llm_classifications
  ADD COLUMN spam_ham_signals_json TEXT;

ALTER TABLE llm_classifications
  ADD COLUMN spam_inconsistent BOOLEAN;
