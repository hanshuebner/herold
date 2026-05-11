-- 0051_tagged_address_dismissals.sql — per-suffix "don't ask me again"
-- markers for the SPA banner gate (REQ-TAG-10, REQ-TAG-60..62).
--
-- Mirrors storesqlite/migrations/0051_tagged_address_dismissals.sql.
-- Column types map BIGINT <-> INTEGER per the established backend pattern.
-- The dismissal cap (500 per principal, REQ-TAG-11) is enforced in the
-- store helper, not the schema.
--
-- Forward-only.

CREATE TABLE tagged_address_dismissals (
  principal_id      BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  base_identity_id  TEXT    NOT NULL REFERENCES jmap_identities(id) ON DELETE CASCADE,
  suffix            TEXT    NOT NULL,
  dismissed_at_us   BIGINT  NOT NULL,
  PRIMARY KEY (principal_id, base_identity_id, suffix)
);
