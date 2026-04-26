-- 0021_apikey_from_constraints.sql -- Phase 3 REQ-SEND-12 / REQ-FLOW-41:
-- Per-key from-address constraints (REQ-SEND-30).
-- Mirrors storepg 0021. Forward-only.
--
-- Adds two columns:
--
--   * allowed_from_addresses_json -- JSON array of addr-spec strings;
--                                    NULL / '[]' means no constraint.
--   * allowed_from_domains_json   -- JSON array of domain strings;
--                                    NULL / '[]' means no constraint.

ALTER TABLE api_keys ADD COLUMN allowed_from_addresses_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_keys ADD COLUMN allowed_from_domains_json   TEXT NOT NULL DEFAULT '[]';
