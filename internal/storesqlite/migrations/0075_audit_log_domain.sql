-- 0075_audit_log_domain.sql — add domain column to audit_log for REQ-ADM-307
-- operator-scope filtering.
--
-- Domain-scoped operators must see only audit entries for their managed
-- domains (the per-domain slice of the Audit log, REQ-ADM-307). This column
-- stores the mail domain the entry relates to. Existing rows default to ''
-- (no domain context) — domain operators see 0 entries from pre-migration
-- history, which is the correct fail-closed behavior.
--
-- Forward-only.

ALTER TABLE audit_log ADD COLUMN domain TEXT NOT NULL DEFAULT '';

-- Support the REQ-ADM-307 IN-list filter over domain + time cursor.
CREATE INDEX idx_audit_log_domain_id
  ON audit_log(domain, id DESC)
  WHERE domain != '';
