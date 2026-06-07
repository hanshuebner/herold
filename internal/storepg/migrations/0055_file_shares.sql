-- 0055_file_shares.sql -- Attachment shares lifecycle table.
-- (REQ-SHARE-01..23, REQ-SHARE-10). Forward-only. Mirrors storesqlite 0055.
-- Column types map BIGINT <-> INTEGER per the established backend pattern.
--
-- Each row represents one owner-scoped, TTL-bounded share of a
-- content-addressed blob. The id column IS the capability token (>= 128
-- bits of CSPRNG entropy, URL-safe base64url); possessing the URL is the
-- only credential that authorises download.
--
-- Columns:
--   id               TEXT pk -- capability token (REQ-SHARE-11b).
--   principal_id     BIGINT FK -> principals(id) ON DELETE CASCADE.
--   blob_hash        TEXT NOT NULL -- BLAKE3 hex of the shared blob.
--   blob_size        BIGINT NOT NULL -- byte length of the blob.
--   filename         TEXT NOT NULL -- presented to recipient.
--   content_type     TEXT NOT NULL -- served verbatim on download.
--   created_at_us    BIGINT NOT NULL -- row insert instant, unix micros.
--   expires_at_us    BIGINT NOT NULL -- absolute expiry, unix micros.
--   max_downloads    BIGINT NULL -- nil = unlimited.
--   download_count   BIGINT NOT NULL DEFAULT 0.
--   password_hash    TEXT NULL -- Argon2id PHC string; NULL = no password.
--   state            TEXT NOT NULL CHECK(state IN ('pending','active','revoked')).
--   last_downloaded_at_us BIGINT NULL -- instant of most recent download.
--   revoked_at_us    BIGINT NULL -- instant of revocation; NULL if not revoked.

CREATE TABLE file_shares (
  id                      TEXT   PRIMARY KEY,
  principal_id            BIGINT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  blob_hash               TEXT   NOT NULL,
  blob_size               BIGINT NOT NULL,
  filename                TEXT   NOT NULL,
  content_type            TEXT   NOT NULL,
  created_at_us           BIGINT NOT NULL,
  expires_at_us           BIGINT NOT NULL,
  max_downloads           BIGINT,
  download_count          BIGINT NOT NULL DEFAULT 0,
  password_hash           TEXT,
  state                   TEXT   NOT NULL CHECK(state IN ('pending','active','revoked')),
  last_downloaded_at_us   BIGINT,
  revoked_at_us           BIGINT
);

CREATE INDEX idx_file_shares_principal_created
  ON file_shares(principal_id, created_at_us DESC);

CREATE INDEX idx_file_shares_state_expires
  ON file_shares(state, expires_at_us);
