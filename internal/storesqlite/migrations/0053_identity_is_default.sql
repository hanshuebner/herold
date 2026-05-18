-- 0053_identity_is_default.sql -- per-principal default-identity pointer
-- for the herold JMAP Identity.isDefault extension property (REQ-IDENT-70).
--
-- The JMAP Identity object gains a herold-namespaced boolean property
-- isDefault. Exactly one identity per principal is the default; the
-- frontend's From picker and compose-default selector key off it
-- (web REQ-SET-IDENT-04).
--
-- Representation: a single is_default flag on each persisted
-- jmap_identities row. The single-default invariant is enforced by the
-- store's SetDefaultJMAPIdentity, which in one transaction clears the
-- flag on every row owned by the principal and then sets it on the
-- chosen row.
--
-- The synthesised "default" identity (wire id "default") has no row in
-- this table. It is the principal's default exactly when NO persisted
-- row owned by the principal carries is_default = 1. This keeps the
-- invariant symmetric: zero-or-one row flagged, and the synthesised
-- default fills the gap when zero rows are flagged. Setting
-- isDefault:false on whatever row is currently default therefore falls
-- back to the synthesised default automatically.
--
-- INTEGER NOT NULL DEFAULT 0 so existing rows are non-default and do
-- not have to be retro-fitted with a value; existing principals keep
-- the synthesised default as their default.
--
-- Forward-only. Mirrors storepg 0053.

ALTER TABLE jmap_identities
  ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_jmap_identities_principal_default
  ON jmap_identities(principal_id, is_default);
