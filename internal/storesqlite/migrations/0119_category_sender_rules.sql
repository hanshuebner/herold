-- 0119_category_sender_rules.sql -- per-sender category rules (issue #555).
--
-- sender_rules_json is a JSON array of {"sender": "<lower-cased From
-- address>", "category": "<category name>"} objects, at most one per
-- sender. A From-address match assigns the rule's category without a
-- classifier call. NULL means the account has no rules.

ALTER TABLE jmap_categorisation_config
  ADD COLUMN sender_rules_json TEXT;
