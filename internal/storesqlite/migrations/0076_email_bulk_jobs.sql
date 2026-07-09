-- 0076_email_bulk_jobs.sql -- whole-mailbox async bulk-mutation jobs
-- (issue #149/#161, REQ-PROTO-40..48 vendor extension
-- https://netzhansa.com/jmap/email-bulk-mutation).
--
-- `Email/setByQuery` resolves its filter to a match estimate only and
-- acks immediately (REQ-PERF-DEADLINE); the row created here is the
-- background worker's resumable state and the JMAP `EmailBulkJob/get`
-- backing store.
--
-- target_ids_json holds the full ordered list of matched message ids,
-- resolved once by the worker (off the request path) and never
-- re-queried afterwards -- a patch that removes a message from its own
-- filter match (e.g. archive/move) must not let position-based
-- re-querying skip or repeat rows. `processed` is both the count of
-- attempted targets and the index into target_ids_json the next batch
-- starts at, so resuming after a crash is just "re-read the row and
-- keep going".
--
-- failures_json is a JSON array of {id, error} pairs -- kept as one
-- column (rather than parallel arrays) so a partial write can never
-- desync the two lists.
--
-- No FK to messages(id): a target message may be expunged by the user
-- while the job is in flight (surfaces as a per-message failure, not a
-- schema violation).
--
-- Excluded from herold diag backup: ephemeral operational state, not
-- user mail data (see internal/diag/backup/manifest.go TableNames).
--
-- Forward-only.

CREATE TABLE email_bulk_jobs (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id     INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  filter_json      TEXT    NOT NULL DEFAULT '',
  patch_json       TEXT    NOT NULL,
  status           TEXT    NOT NULL DEFAULT 'running'
                            CHECK(status IN ('running','done','partial','failed')),
  matched_estimate INTEGER NOT NULL DEFAULT 0,
  total            INTEGER NOT NULL DEFAULT -1,   -- -1 = targets not yet resolved
  processed        INTEGER NOT NULL DEFAULT 0,
  target_ids_json  TEXT    NOT NULL DEFAULT '',
  failures_json    TEXT    NOT NULL DEFAULT '[]',
  error_message    TEXT    NOT NULL DEFAULT '',
  created_at_us    INTEGER NOT NULL,
  updated_at_us    INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_email_bulk_jobs_principal
  ON email_bulk_jobs(principal_id, id DESC);

-- Worker poll: find running jobs, oldest first.
CREATE INDEX idx_email_bulk_jobs_status_id
  ON email_bulk_jobs(status, id)
  WHERE status = 'running';

-- Per-principal JMAP state counter (REQ-PROTO-40..48 vendor extension).
-- Bumped once per processed batch so EventSource push tells the client
-- to re-fetch EmailBulkJob/get without per-message churn.
ALTER TABLE jmap_states ADD COLUMN email_bulk_job_state INTEGER NOT NULL DEFAULT 0;
