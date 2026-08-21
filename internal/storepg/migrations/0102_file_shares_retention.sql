-- 0102_file_shares_retention.sql -- per-share retention chosen at
-- create time (issue #290). Mirrors storesqlite 0102.
--
-- Adds retention_us to file_shares: the lifetime the owner selected in
-- the picker at create time, in microseconds. The pending -> active
-- transition (ConfirmFileShare) applies this value instead of the
-- fixed default_ttl, clamped to max_ttl.
--
-- retention_us = 0 means "unset". Every pre-existing row gets 0 by the
-- column default (no back-fill); ConfirmFileShare falls back to
-- Config.DefaultTTL for those rows, preserving today's behaviour for
-- shares created before this migration.

ALTER TABLE file_shares ADD COLUMN retention_us BIGINT NOT NULL DEFAULT 0;
