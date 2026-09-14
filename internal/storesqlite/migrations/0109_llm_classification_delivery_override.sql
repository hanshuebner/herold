-- 0109_llm_classification_delivery_override.sql -- delivery-override note
-- on the LLM transparency record (REQ-FILT-02a / REQ-FLT-16, issue #382).
--
-- delivery_override is non-NULL exactly when a user filter's "never
-- classify as spam" action kept a spam/suspect-verdict message out of
-- Junk: the value is "filter:<rule name or id>", naming the ManagedRule
-- responsible, so the reader can show "classifier said spam, delivered
-- by your filter" (REQ-FILT-66). NULL for every existing row and for any
-- message no override applied to.
--
-- Forward-only. Mirrors storepg 0109.

ALTER TABLE llm_classifications
  ADD COLUMN delivery_override TEXT;
