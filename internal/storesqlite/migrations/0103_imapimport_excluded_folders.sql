-- 0103_imapimport_excluded_folders.sql -- per-account no-sync folder list
-- (issue #303/#305). Mirrors storepg 0103.
--
-- Adds imapimport_account.excluded_folders_json: a JSON array of upstream
-- folder names (verbatim, case-sensitive, matching the folder_map
-- convention) that the worker never syncs. An excluded folder gets no
-- cursor row and no message_state rows. Column-only migration on the
-- table added by migration 0057; every pre-existing row defaults to '[]'
-- (no exclusions), preserving today's behaviour.

ALTER TABLE imapimport_account ADD COLUMN excluded_folders_json TEXT NOT NULL DEFAULT '[]';
