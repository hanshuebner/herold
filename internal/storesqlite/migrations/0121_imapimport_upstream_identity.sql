-- 0121_imapimport_upstream_identity.sql -- adds the upstream identity
-- columns to imapimport_message_state (issue #561).
--
-- upstream_message_id is the normalised Message-ID (no angle brackets) and
-- upstream_size the RFC822 size of the upstream message the state row
-- (account, folder, uid) was created from. Write-back expunges the upstream
-- message at (folder, uid) only when it positively matches both. Rows
-- written before this migration carry '' / 0 ("identity not recorded") and
-- are never expunged through until the sync pass records a verified
-- identity for them.
--
-- Forward-only.

ALTER TABLE imapimport_message_state ADD COLUMN upstream_message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE imapimport_message_state ADD COLUMN upstream_size INTEGER NOT NULL DEFAULT 0;
