-- 0093_oidc_autoprovision_domain.sql -- REQ-AUTH-56 OIDC first-login
-- auto-provisioning (issue #230).
--
-- oidc_providers.auto_provision_domain: the local domain a principal
-- auto-provisioned by this provider is created under. The provisioned
-- principal's local-part is taken from the verified `email` claim; the
-- domain always comes from this operator-configured column, never from
-- the claim, so an untrusted IdP cannot pick which local domain it lands
-- a new principal in. auto_provision itself (the per-provider opt-in,
-- off by default) was already added by an earlier migration; this column
-- was the missing piece to make it usable -- REQ-AUTH-56 also permits a
-- "config-specified template", but this deployment implements the
-- generated-local-email alternative the requirement names as the other
-- option: local-part from the claim, domain from this column.
--
-- Forward-only.

ALTER TABLE oidc_providers ADD COLUMN auto_provision_domain TEXT NOT NULL DEFAULT '';
