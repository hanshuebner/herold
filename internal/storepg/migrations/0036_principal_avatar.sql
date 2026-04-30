-- 0036_principal_avatar.sql — promote per-Identity avatar to the
-- principal row so cross-user lookups (chat member display, mail thread
-- header avatars) can resolve the picture (REQ-SET-03b, REQ-MAIL-44).
--
-- The default Identity is synthesised from the principal row at read
-- time. Until 0035 the Identity/set { id: 'default', avatarBlobId } write
-- path landed in an in-process overlay only — invisible to other users
-- and lost on restart. This migration moves the canonical home of the
-- default-identity avatar onto principals so:
--
--   * `Principal/get` (chat capability) can return `avatarBlobId` for
--     any hosted address the caller already knows about.
--   * Restart preserves the picture without dragging the per-Identity
--     overlay table into the synthesised-default code path.
--
-- Forward-only.

ALTER TABLE principals
  ADD COLUMN avatar_blob_hash TEXT;
ALTER TABLE principals
  ADD COLUMN avatar_blob_size BIGINT NOT NULL DEFAULT 0;
ALTER TABLE principals
  ADD COLUMN xface_enabled BOOLEAN NOT NULL DEFAULT FALSE;
