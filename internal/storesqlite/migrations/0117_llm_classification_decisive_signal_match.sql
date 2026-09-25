-- 0117_llm_classification_decisive_signal_match.sql -- transparency for
-- the server-side decisive-signal resolution's normalization step (re
-- #489): a model may report a decisive trait under a synonym
-- (e.g. "unsolicited_marketing_pitch") that internal/spam normalises
-- onto the canonical decisive-signal name
-- (e.g. "unsolicited_bulk_marketing") before matching. spam_signals
-- keeps the model's own reported names unchanged; this column records
-- the canonical name the normalized match actually resolved against, so
-- a reader of the transparency record (Email/llmInspect, herold spam
-- show) can see what fired even when it does not appear verbatim among
-- the persisted spam_signals. NULL exactly when spam_model_verdict is
-- NULL (no decisive-signal resolution happened), including every row
-- written before this column existed.
--
-- Forward-only. Mirrors storepg 0117.

ALTER TABLE llm_classifications
  ADD COLUMN spam_decisive_signal_match TEXT;
