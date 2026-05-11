-- 0051_tagged_address_dismissals.sql — per-suffix "don't ask me again"
-- markers for the SPA banner gate (REQ-TAG-10, REQ-TAG-60..62).
--
-- Rows arrive when the user dismisses the per-message banner for a
-- particular (base Identity, suffix). The banner-gate read (REQ-TAG-40
-- web-side) consults this table together with tagged_address_filters
-- to decide whether to surface a prompt; the inbound delivery pipeline
-- does NOT read this table (REQ-TAG-20 step 3).
--
-- Columns:
--   principal_id     owning principal (FK to principals(id) ON DELETE
--                    CASCADE).
--   base_identity_id FK to jmap_identities(id) ON DELETE CASCADE so
--                    removing the Identity drops every dismissal that
--                    referenced it.
--   suffix           TEXT NOT NULL, lower-cased canonical form
--                    (REQ-TAG-02).
--   dismissed_at_us  unix-micros instant the user pressed dismiss.
--
-- Constraints:
--   PRIMARY KEY (principal_id, base_identity_id, suffix) — idempotent
--   insert per REQ-TAG-60. The dismissal cap (500 per principal,
--   REQ-TAG-11) is enforced in the store helper, not the schema.
--
-- Forward-only. Mirrors storepg 0051.

CREATE TABLE tagged_address_dismissals (
  principal_id      INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  base_identity_id  TEXT    NOT NULL REFERENCES jmap_identities(id) ON DELETE CASCADE,
  suffix            TEXT    NOT NULL,
  dismissed_at_us   INTEGER NOT NULL,
  PRIMARY KEY (principal_id, base_identity_id, suffix)
) STRICT;
