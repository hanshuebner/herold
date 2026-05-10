-- 0044_state_change_cause.sql -- classify state-change rows by cause.
--
-- See REQ-EXTIMG-BG-INTERNAL-01..03 and
-- docs/design/server/architecture/05-sync-and-state.md "Cause classification".
--
-- The state_changes feed serves two communities of consumers: JMAP /changes
-- and EventSource (which want only mutations the user can observe), and
-- IMAP IDLE / FTS / seen-address indexers (which need every mutation that
-- touched the bytes they index). Until now those needs were conflated;
-- the extimg internalize-worker (REQ-EXTIMG-BG-01) appended Email rows
-- with the same shape as a JMAP /set, and a backlog drain produced one
-- state advance per rewrite -- a sustained burst of /changes round-trips
-- on a 90k-row backlog.
--
-- This migration adds an open-enum `cause` column. v1 values:
--   'user'       -- default; any user-attributable mutation. Existing
--                   rows back-fill to this value.
--   'background' -- in-process worker writes that do not change what
--                   the user can observe (extimg worker only in v1).
--
-- The partial index on cause = 'user' lets the JMAP/EventSource read
-- path (GetMaxChangeSeqForKind, ReadChangeFeed default) skip the
-- background rows without a sequential scan. The pre-existing
-- idx_state_changes_principal_kind_seq is retained: IMAP IDLE / FTS
-- still walk the full feed across all causes.
--
-- Forward-only.

ALTER TABLE state_changes ADD COLUMN cause TEXT NOT NULL DEFAULT 'user';

CREATE INDEX idx_state_changes_principal_kind_seq_user
  ON state_changes(principal_id, entity_kind, seq)
  WHERE cause = 'user';
