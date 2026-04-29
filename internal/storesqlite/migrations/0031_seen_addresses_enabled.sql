-- 0031_seen_addresses_enabled.sql — per-principal seen-addresses-enabled flag (REQ-SET-15).
-- Stored in the principals table as a nullable integer boolean.
-- NULL / 1 means enabled (default-on); 0 means disabled.
-- Forward-only.

ALTER TABLE principals ADD COLUMN seen_addresses_enabled INTEGER NOT NULL DEFAULT 1;
