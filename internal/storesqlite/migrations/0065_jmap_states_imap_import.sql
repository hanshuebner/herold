-- 0065_jmap_states_imap_import.sql -- per-principal JMAP IMAPImport state
-- counter for the inbound IMAP live-mirror feature (REQ-IMAP-IMP-61).
--
-- The column is bumped via IncrementJMAPState(JMAPStateKindIMAPImport) on
-- every IMAPImport/set create, update, or destroy so clients can track drift
-- via IMAPImport/changes. Until this column existed IMAPImport/get returned a
-- fixed "0" state and /changes was unavailable.
--
-- The column defaults to 0 so existing rows are valid post-migration.
--
-- Forward-only.

ALTER TABLE jmap_states ADD COLUMN imap_import_state INTEGER NOT NULL DEFAULT 0;
