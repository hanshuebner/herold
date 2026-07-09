-- 0076_email_bulk_jobs.sql -- whole-mailbox async bulk-mutation jobs
-- (issue #149/#161, REQ-PROTO-40..48 vendor extension
-- https://netzhansa.com/jmap/email-bulk-mutation).
-- Mirrors storesqlite/migrations/0076_email_bulk_jobs.sql.
-- Postgres idioms: BIGSERIAL id, BIGINT for counters/timestamps.
--
-- Excluded from herold diag backup: ephemeral operational state, not
-- user mail data (see internal/diag/backup/manifest.go TableNames).
--
-- Forward-only.

CREATE TABLE email_bulk_jobs (
  id               BIGSERIAL PRIMARY KEY,
  principal_id     BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  filter_json      TEXT    NOT NULL DEFAULT '',
  patch_json       TEXT    NOT NULL,
  status           TEXT    NOT NULL DEFAULT 'running'
                            CHECK(status IN ('running','done','partial','failed')),
  matched_estimate BIGINT  NOT NULL DEFAULT 0,
  total            BIGINT  NOT NULL DEFAULT -1,
  processed        BIGINT  NOT NULL DEFAULT 0,
  target_ids_json  TEXT    NOT NULL DEFAULT '',
  failures_json    TEXT    NOT NULL DEFAULT '[]',
  error_message    TEXT    NOT NULL DEFAULT '',
  created_at_us    BIGINT  NOT NULL,
  updated_at_us    BIGINT  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_email_bulk_jobs_principal
  ON email_bulk_jobs(principal_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_email_bulk_jobs_status_id
  ON email_bulk_jobs(status, id)
  WHERE status = 'running';

ALTER TABLE jmap_states ADD COLUMN email_bulk_job_state BIGINT NOT NULL DEFAULT 0;
