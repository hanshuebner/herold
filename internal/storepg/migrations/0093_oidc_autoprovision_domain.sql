-- 0093_oidc_autoprovision_domain.sql -- REQ-AUTH-56 OIDC first-login
-- auto-provisioning (issue #230). Mirrors
-- storesqlite/migrations/0093_oidc_autoprovision_domain.sql.
--
-- See the sqlite migration for the full design commentary.
--
-- Forward-only.

ALTER TABLE oidc_providers ADD COLUMN auto_provision_domain TEXT NOT NULL DEFAULT '';
