-- 0117_llm_classification_decisive_signal_match.sql -- transparency for
-- the server-side decisive-signal resolution's normalization step (re
-- #489). See
-- storesqlite/migrations/0117_llm_classification_decisive_signal_match.sql.

ALTER TABLE llm_classifications
  ADD COLUMN spam_decisive_signal_match TEXT;
