-- 0029_derived_categories_epoch.sql — epoch column for optimistic-locking of
-- derived_categories_json (REQ-FILT-217).
--
-- Without this column, a classifier call that reads the config before a
-- prompt-change transaction commits can call SetDerivedCategories afterwards
-- and overwrite the NULL the prompt-change wrote. The epoch guard makes that
-- stale write a no-op: UpdateCategorisationConfig increments the epoch on
-- every prompt change; SetDerivedCategories adds WHERE derived_categories_epoch = $n
-- so only the call that read the same epoch wins.
--
-- Default 0 so existing rows (no successful classifier call yet) start at
-- epoch 0; the first prompt-change bumps to 1.

ALTER TABLE jmap_categorisation_config
  ADD COLUMN derived_categories_epoch BIGINT NOT NULL DEFAULT 0;
