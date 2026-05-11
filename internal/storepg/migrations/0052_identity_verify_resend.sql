-- 0052_identity_verify_resend.sql -- per-identity resend rate-limit
-- bookkeeping for the user-initiated verification resend path
-- (REQ-IDENT-36).
--
-- Mirrors storesqlite/migrations/0052_identity_verify_resend.sql. Column
-- types map BIGINT <-> INTEGER per the established pattern. See the
-- sqlite migration for the design rationale.
--
-- Forward-only.

ALTER TABLE jmap_identities
  ADD COLUMN verify_last_issued_at_us BIGINT;
ALTER TABLE jmap_identities
  ADD COLUMN verify_window_started_at_us BIGINT;
ALTER TABLE jmap_identities
  ADD COLUMN verify_window_count BIGINT NOT NULL DEFAULT 0;
