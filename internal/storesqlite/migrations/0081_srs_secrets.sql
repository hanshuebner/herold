-- 0081_srs_secrets.sql -- keyed-MAC secrets for SRS (Sender Rewriting
-- Scheme) return-path rewriting on forwarded mail (issue #204, building on
-- #181's external-target aliases and #63's Sieve redirect, both of which
-- ride the shared queueForward path in internal/protosmtp/deliver.go).
--
-- One row per secret. Rotation is additive: inserting a new secret makes it
-- the signing secret (highest id); older rows stay so an SRS address signed
-- before a rotation still verifies until it ages out on its own SRS0
-- timestamp. Secret is stored in plaintext, mirroring the dkim_keys
-- (migration 0004) precedent -- the store itself is the trust boundary.
--
-- Forward-only.

CREATE TABLE srs_secrets (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  secret        BLOB    NOT NULL,
  created_at_us INTEGER NOT NULL
) STRICT;
