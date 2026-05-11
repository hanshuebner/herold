-- 0052_identity_verify_resend.sql -- per-identity resend rate-limit
-- bookkeeping for the user-initiated verification resend path
-- (REQ-IDENT-36).
--
-- Rationale: the rate limit has two components:
--   (a) cooldown -- a resend within `resend_cooldown_seconds` (default
--       60s) of the most recent token issue is rejected.
--   (b) daily cap -- the count of token issues for this Identity in the
--       trailing 24h must stay strictly below `resend_daily_cap`
--       (default 5).
--
-- Both components are per-Identity, not per-Principal (REQ-IDENT-36).
-- We track the bookkeeping on the identity row itself so a single
-- atomic write covers token rotation and counter update.
--
-- Columns added:
--   verify_last_issued_at_us       unix-micros instant the most recent
--                                  verification token was issued. NULL
--                                  before any issuance (or after a
--                                  successful verify clears state).
--                                  Used by the cooldown check.
--   verify_window_started_at_us    unix-micros instant the active 24h
--                                  resend-counter window began. NULL
--                                  before any issuance. Used together
--                                  with verify_window_count to enforce
--                                  the daily cap.
--   verify_window_count            count of issuances inside the active
--                                  window. INTEGER NOT NULL DEFAULT 0
--                                  so existing rows do not have to be
--                                  retro-fitted with a value.
--
-- The columns are NOT cleared by MarkIdentityVerified -- they survive a
-- successful verify so a user re-running through an erroneous "verify
-- another identity" path cannot escape the daily cap by burning the
-- first verification and immediately re-creating. The GC pass that
-- destroys unverified rows (REQ-IDENT-35) drops the columns naturally
-- by deleting the row.
--
-- Forward-only. Mirrors storepg 0052.

ALTER TABLE jmap_identities
  ADD COLUMN verify_last_issued_at_us INTEGER;
ALTER TABLE jmap_identities
  ADD COLUMN verify_window_started_at_us INTEGER;
ALTER TABLE jmap_identities
  ADD COLUMN verify_window_count INTEGER NOT NULL DEFAULT 0;
