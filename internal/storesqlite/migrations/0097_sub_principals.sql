-- 0097_sub_principals.sql -- sub-account substrate (issue #227,
-- REQ-SUBACCT-01..11). A sub-principal is a `principals` row owned by a
-- parent individual principal: same table, same Mailbox/Identity/Sieve/
-- state-string machinery every principal already has, distinguished by
-- kind = 4 (store.PrincipalKindSubAccount) and a non-NULL
-- parent_principal_id.
--
-- parent_principal_id is NULL for every ordinary principal (individual,
-- group, service) and set to the owning parent's id for a sub-principal.
-- ON DELETE CASCADE: deleting the parent row cascades to delete its
-- sub-principals (REQ-SUBACCT-06), which in turn cascades (via the
-- existing principal_id foreign keys on mailboxes/messages/etc.) to the
-- sub-account's own mail. The application-level DeletePrincipal also
-- walks owned sub-principals explicitly to release blob refcounts and
-- wipe state_changes/audit_log/queue rows before the cascade fires (raw
-- SQL FK cascade alone does not run that bookkeeping).
--
-- A sub-principal's mail is never counted in its own used_bytes column:
-- InsertMessage / ReplaceMessageBody / the delete and expunge paths
-- resolve the quota-owning principal (self, unless parent_principal_id
-- is set) before touching used_bytes, so a sub-account's storage always
-- counts against the parent (REQ-SUBACCT-05); the sub-principal's own
-- quota_bytes/used_bytes columns stay zero.
--
-- Mirrors storepg/migrations/0097_sub_principals.sql. Forward-only.

ALTER TABLE principals
  ADD COLUMN parent_principal_id INTEGER REFERENCES principals(id) ON DELETE CASCADE;

CREATE INDEX idx_principals_parent
  ON principals(parent_principal_id)
  WHERE parent_principal_id IS NOT NULL;
