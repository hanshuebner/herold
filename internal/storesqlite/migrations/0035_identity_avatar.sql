-- 0035_identity_avatar.sql — per-Identity avatar + outbound X-Face/Face
-- header opt-in (REQ-SET-03b).
--
-- The Suite settings UI lets the user attach an image to one or more
-- Identity rows. The wire-form blob id (BLAKE3 hex hash) is recorded
-- here, alongside the byte size so backups can round-trip the row
-- without re-hashing the blob content. The application layer manages
-- blob refcounts: incRef on set, decRef on clear / replace.
--
-- avatar_blob_size is signed INTEGER for SQLite's STRICT typing; it is
-- always non-negative.
--
-- xface_enabled controls whether outbound submissions from this Identity
-- inject the legacy X-Face: and modern Face: headers derived from the
-- avatar blob. Default 0 (off); the Suite asks before enabling so the
-- user is aware of the size cost.
--
-- Forward-only.

ALTER TABLE jmap_identities
  ADD COLUMN avatar_blob_hash TEXT;
ALTER TABLE jmap_identities
  ADD COLUMN avatar_blob_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jmap_identities
  ADD COLUMN xface_enabled INTEGER NOT NULL DEFAULT 0;
