-- 0072_principal_managed_domains.sql
-- Introduces the delegated-operator authorization model (REQ-ADM-307, re #145).
--
-- Creates principal_managed_domains: an association table linking a principal
-- to the set of domains it may administer. Domain-scoped operators hold rows
-- here; super-admins do not need rows (their unrestricted access is derived
-- from the super-admin flag, not from this table).
--
-- Also adds the super-admin marker: PrincipalFlagSuperAdmin = 32 (bit 5 of
-- the flags column). Existing PrincipalFlagAdmin principals (bit 2 = 4) are
-- auto-promoted to super-admin in the same migration so no operator is locked
-- out or changes behaviour on upgrade.

CREATE TABLE principal_managed_domains (
    principal_id INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    domain       TEXT    NOT NULL,
    PRIMARY KEY (principal_id, domain)
);

CREATE INDEX idx_principal_managed_domains_domain
    ON principal_managed_domains (domain);

-- Auto-promote every existing admin principal to super-admin (bit 5 = 32).
UPDATE principals SET flags = flags | 32 WHERE (flags & 4) != 0;
