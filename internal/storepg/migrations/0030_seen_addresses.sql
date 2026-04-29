-- 0030_seen_addresses.sql — per-principal seen-addresses history (REQ-MAIL-11e..m).
-- Mirrors storesqlite 0030. Postgres idioms applied (BIGINT IDENTITY, BOOLEAN).
-- Column shapes stay isomorphic with SQLite so the migration tool copies row-by-row.
-- Forward-only.

CREATE TABLE seen_addresses (
  id               BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id     BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  email            TEXT    NOT NULL,
  display_name     TEXT    NOT NULL DEFAULT '',
  first_seen_at_us BIGINT  NOT NULL DEFAULT 0,
  last_used_at_us  BIGINT  NOT NULL DEFAULT 0,
  send_count       BIGINT  NOT NULL DEFAULT 0,
  received_count   BIGINT  NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX idx_seen_addresses_principal_email
  ON seen_addresses(principal_id, email);

CREATE INDEX idx_seen_addresses_principal_last_used
  ON seen_addresses(principal_id, last_used_at_us DESC);

ALTER TABLE jmap_states ADD COLUMN seen_address_state BIGINT NOT NULL DEFAULT 0;
