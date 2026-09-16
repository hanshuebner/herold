-- 0112_llm_classification_model_verdict.sql -- server-side resolution of
-- an inconsistent verdict (re #396, second round). See
-- storesqlite/migrations/0112_llm_classification_model_verdict.sql.

ALTER TABLE llm_classifications
  ADD COLUMN spam_model_verdict TEXT;
