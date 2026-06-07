-- 0056_jmap_states_file_share.sql -- per-principal JMAP FileShare state
-- counter for the attachment-share offload feature (REQ-SHARE-40..44).
--
-- The column is bumped via IncrementJMAPState(JMAPStateKindFileShare) on
-- every FileShare/set create, update, or destroy so clients can track
-- drift via FileShare/changes.
--
-- The column defaults to 0 so existing rows are valid post-migration.
--
-- Forward-only.

ALTER TABLE jmap_states ADD COLUMN file_share_state INTEGER NOT NULL DEFAULT 0;
