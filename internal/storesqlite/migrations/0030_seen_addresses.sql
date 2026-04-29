-- 0030_seen_addresses.sql — per-principal seen-addresses history (REQ-MAIL-11e..m)
--
-- The seen_addresses table stores a sliding window of recently-used email
-- addresses per principal for recipient autocomplete. The server-side cap of
-- 500 rows per principal is enforced in Go (UpsertSeenAddress deletes the
-- oldest-by-last_used_at row when the count exceeds 500).
--
-- Indices:
--   unique (principal_id, email)        — prevents duplicate entries per principal.
--   (principal_id, last_used_at DESC)   — efficient eviction sort and ListByPrincipal.
--
-- jmap_states gets a new seen_address_state column to carry the per-principal
-- JMAP state counter for SeenAddress/get, /changes, /set.
--
-- Forward-only.

CREATE TABLE seen_addresses (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id     INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  email            TEXT    NOT NULL,
  display_name     TEXT    NOT NULL DEFAULT '',
  first_seen_at_us INTEGER NOT NULL DEFAULT 0,
  last_used_at_us  INTEGER NOT NULL DEFAULT 0,
  send_count       INTEGER NOT NULL DEFAULT 0,
  received_count   INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE UNIQUE INDEX idx_seen_addresses_principal_email
  ON seen_addresses(principal_id, email);

CREATE INDEX idx_seen_addresses_principal_last_used
  ON seen_addresses(principal_id, last_used_at_us DESC);

ALTER TABLE jmap_states ADD COLUMN seen_address_state INTEGER NOT NULL DEFAULT 0;
