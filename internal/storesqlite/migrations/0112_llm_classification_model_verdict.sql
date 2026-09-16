-- 0112_llm_classification_model_verdict.sql -- server-side resolution of
-- an inconsistent verdict (re #396, second round): flagging alone (0111)
-- left every ham verdict whose own reported spam_signals named a
-- decisive spam trait still delivered to the Inbox. spam_verdict now
-- holds the verdict herold actually applied (Spam/Suspect when a
-- decisive signal resolved it); spam_model_verdict holds the plugin's
-- own original verdict when that resolution happened, NULL otherwise
-- (including every row classified before this fix, and every row where
-- no resolution occurred).
--
-- Forward-only. Mirrors storepg 0112.

ALTER TABLE llm_classifications
  ADD COLUMN spam_model_verdict TEXT;
