-- 0061_blob_part_index.sql -- per-blob serialised MIME part index (re #46).
-- Mirrors storesqlite/migrations/0061_blob_part_index.sql.
-- Postgres idioms applied (BIGINT for integers, TEXT for opaque payload).
--
-- Column semantics identical to the SQLite mirror; see that file for
-- the full rationale.
--
-- Forward-only.

CREATE TABLE blob_part_index (
    blob_hash      TEXT    PRIMARY KEY,
    index_version  BIGINT  NOT NULL,
    parts_json     TEXT    NOT NULL,
    computed_at_us BIGINT  NOT NULL
);
