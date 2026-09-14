-- 0109_llm_classification_delivery_override.sql -- delivery-override note
-- on the LLM transparency record (REQ-FILT-02a / REQ-FLT-16, issue #382).
--
-- Mirrors storesqlite/migrations/0109_llm_classification_delivery_override.sql.

ALTER TABLE llm_classifications
  ADD COLUMN delivery_override TEXT;
