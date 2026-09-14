-- 0108_identity_aliases.sql — additional email addresses ("aliases")
-- an Identity accepts as itself for reply-sender selection
-- (REQ-IDENT-01, re #387).
--
-- Mirrors storesqlite/migrations/0108_identity_aliases.sql. Column
-- types map BIGINT <-> INTEGER per the established backend pattern.
--
-- Forward-only.

CREATE TABLE jmap_identity_aliases (
  identity_id   TEXT    NOT NULL REFERENCES jmap_identities(id) ON DELETE CASCADE,
  principal_id  BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  address       TEXT    NOT NULL,
  position      INTEGER NOT NULL,
  PRIMARY KEY (identity_id, position)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_jmap_identity_aliases_principal_address
  ON jmap_identity_aliases(principal_id, lower(address));
