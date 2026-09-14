-- 0108_identity_aliases.sql — additional email addresses ("aliases")
-- an Identity accepts as itself for reply-sender selection
-- (REQ-IDENT-01, re #387).
--
-- jmap_identity_aliases holds an ordered list of alias addresses per
-- persisted Identity row. The synthesised default identity (no
-- jmap_identities row) cannot carry aliases; that restriction is
-- enforced by the application layer, not this schema.
--
-- Columns:
--   identity_id   FK to jmap_identities(id) ON DELETE CASCADE. Deleting
--                 the owning Identity removes every alias row with it,
--                 so an alias never outlives its Identity.
--   principal_id  denormalized copy of jmap_identities.principal_id,
--                 written by the store at insert time and kept in
--                 step by RebindJMAPIdentityPrincipal. Present so the
--                 cross-identity uniqueness index below can be
--                 enforced within this one table — SQLite/Postgres
--                 CHECK constraints and plain UNIQUE indexes cannot
--                 reference a second table.
--   address       the alias addr-spec, stored verbatim (case as
--                 supplied). Matching is case-insensitive via the
--                 lower(address) expression in the index and in the
--                 store's own pre-write checks.
--   position      zero-based ordinal within the identity's alias
--                 list; preserves the caller's ordering on read-back.
--
-- Constraints:
--   PRIMARY KEY (identity_id, position) — at most one alias per
--   ordinal slot per identity.
--   UNIQUE index on (principal_id, lower(address)) — no address may
--   be claimed as an alias twice within one principal. The store
--   layer additionally checks a candidate alias against every
--   identity's primary address in the same principal, and a
--   candidate primary address against every existing alias (and vice
--   versa) — an invariant a single-table index cannot express.
--
-- Forward-only. Mirrors storepg 0108.

CREATE TABLE jmap_identity_aliases (
  identity_id   TEXT    NOT NULL REFERENCES jmap_identities(id) ON DELETE CASCADE,
  principal_id  INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  address       TEXT    NOT NULL,
  position      INTEGER NOT NULL,
  PRIMARY KEY (identity_id, position)
) STRICT;

CREATE UNIQUE INDEX idx_jmap_identity_aliases_principal_address
  ON jmap_identity_aliases(principal_id, lower(address));
