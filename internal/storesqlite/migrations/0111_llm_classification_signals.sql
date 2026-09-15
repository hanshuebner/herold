-- 0111_llm_classification_signals.sql -- structured spam/ham signals and
-- the ham-verdict-vs-spam-signals inconsistency marker on the LLM
-- classification transparency record (re #396).
--
-- spam_signals_json / spam_ham_signals_json hold the classifier's
-- reported spam_signals/ham_signals lists as JSON arrays of strings (the
-- same encode-as-TEXT pattern jmap_categorisation_config.derived_
-- categories_json already uses). NULL for every existing row and for any
-- message whose classifier response carried neither key.
--
-- spam_inconsistent is 1 when spam_verdict = 'ham' while spam_signals_json
-- names at least one spam signal: the classifier's own stated reasoning
-- contradicted its verdict (the motivating report: a cold unsolicited
-- marketing pitch scored ham while its reason text named every spam
-- criterion the prompt lists). NULL for every existing row and for a
-- category-only write (mirroring spam_confidence's nullable, set-only-
-- with-the-spam-sub-record convention); the server never re-derives this
-- for a row classified before the fix landed.
--
-- Forward-only. Mirrors storepg 0111.

ALTER TABLE llm_classifications
  ADD COLUMN spam_signals_json TEXT;

ALTER TABLE llm_classifications
  ADD COLUMN spam_ham_signals_json TEXT;

ALTER TABLE llm_classifications
  ADD COLUMN spam_inconsistent INTEGER;
