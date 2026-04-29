-- 0031_seen_addresses_enabled.sql — per-principal seen-addresses-enabled flag (REQ-SET-15).
-- Mirrors storesqlite 0031. Postgres idioms applied (BOOLEAN).
-- Forward-only.

ALTER TABLE principals ADD COLUMN seen_addresses_enabled BOOLEAN NOT NULL DEFAULT TRUE;
