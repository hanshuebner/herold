-- 0004_phase2.sql — Phase 2 outbound + mail security + admin schema.
--
-- Mirrors storesqlite/migrations/0004_phase2.sql: nine new tables
-- backing the Wave 2.1 subsystems. Postgres idioms applied where
-- helpful (BIGINT GENERATED ALWAYS AS IDENTITY, BOOLEAN, BYTEA,
-- partial / unique indexes); column shapes stay isomorphic with
-- SQLite so the migration tool copies row-by-row. _us BIGINT
-- timestamps remain the canonical wire form for the application
-- layer.

CREATE TABLE queue (
  id                  BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id        BIGINT  REFERENCES principals(id) ON DELETE SET NULL,
  mail_from           TEXT    NOT NULL,
  rcpt_to             TEXT    NOT NULL,
  envelope_id         TEXT    NOT NULL,
  body_blob_hash      TEXT    NOT NULL,
  headers_blob_hash   TEXT    NOT NULL DEFAULT '',
  state               INTEGER NOT NULL,
  attempts            INTEGER NOT NULL DEFAULT 0,
  last_attempt_at_us  BIGINT  NOT NULL DEFAULT 0,
  next_attempt_at_us  BIGINT  NOT NULL DEFAULT 0,
  last_error          TEXT    NOT NULL DEFAULT '',
  dsn_notify_flags    INTEGER NOT NULL DEFAULT 0,
  dsn_ret             INTEGER NOT NULL DEFAULT 0,
  dsn_envid           TEXT    NOT NULL DEFAULT '',
  dsn_orcpt           TEXT    NOT NULL DEFAULT '',
  idempotency_key     TEXT,
  created_at_us       BIGINT  NOT NULL
);

CREATE INDEX idx_queue_state_next_attempt
  ON queue(state, next_attempt_at_us);
CREATE INDEX idx_queue_envelope ON queue(envelope_id);
CREATE INDEX idx_queue_principal_state ON queue(principal_id, state);
CREATE UNIQUE INDEX idx_queue_idempotency
  ON queue(idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE dkim_keys (
  id              BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  domain          TEXT    NOT NULL,
  selector        TEXT    NOT NULL,
  algorithm       INTEGER NOT NULL,
  private_key_pem TEXT    NOT NULL,
  public_key_b64  TEXT    NOT NULL,
  status          INTEGER NOT NULL,
  created_at_us   BIGINT  NOT NULL,
  rotated_at_us   BIGINT  NOT NULL DEFAULT 0,
  UNIQUE(domain, selector)
);

CREATE INDEX idx_dkim_keys_domain_status ON dkim_keys(domain, status);

CREATE TABLE acme_accounts (
  id                BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  directory_url     TEXT    NOT NULL,
  contact_email     TEXT    NOT NULL,
  account_key_pem   TEXT    NOT NULL,
  kid               TEXT    NOT NULL DEFAULT '',
  created_at_us     BIGINT  NOT NULL,
  UNIQUE(directory_url, contact_email)
);

CREATE TABLE acme_orders (
  id              BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  account_id      BIGINT  NOT NULL REFERENCES acme_accounts(id) ON DELETE CASCADE,
  hostnames_json  TEXT    NOT NULL,
  status          INTEGER NOT NULL,
  order_url       TEXT    NOT NULL DEFAULT '',
  finalize_url    TEXT    NOT NULL DEFAULT '',
  certificate_url TEXT    NOT NULL DEFAULT '',
  challenge_type  INTEGER NOT NULL DEFAULT 0,
  updated_at_us   BIGINT  NOT NULL,
  error           TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_acme_orders_status_updated
  ON acme_orders(status, updated_at_us);

CREATE TABLE acme_certs (
  hostname         TEXT    PRIMARY KEY,
  chain_pem        TEXT    NOT NULL,
  private_key_pem  TEXT    NOT NULL,
  not_before_us    BIGINT  NOT NULL,
  not_after_us     BIGINT  NOT NULL,
  issuer           TEXT    NOT NULL DEFAULT '',
  order_id         BIGINT  REFERENCES acme_orders(id) ON DELETE SET NULL
);

CREATE INDEX idx_acme_certs_not_after ON acme_certs(not_after_us);

CREATE TABLE webhooks (
  id                  BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  owner_kind          INTEGER NOT NULL,
  owner_id            TEXT    NOT NULL,
  target_url          TEXT    NOT NULL,
  hmac_secret         BYTEA   NOT NULL,
  delivery_mode       INTEGER NOT NULL,
  retry_policy_json   TEXT    NOT NULL DEFAULT '',
  active              BOOLEAN NOT NULL DEFAULT TRUE,
  created_at_us       BIGINT  NOT NULL,
  updated_at_us       BIGINT  NOT NULL
);

CREATE INDEX idx_webhooks_owner ON webhooks(owner_kind, owner_id);
CREATE INDEX idx_webhooks_active ON webhooks(active);

CREATE TABLE dmarc_reports_raw (
  id              BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  received_at_us  BIGINT  NOT NULL,
  reporter_email  TEXT    NOT NULL,
  reporter_org    TEXT    NOT NULL,
  report_id       TEXT    NOT NULL,
  domain          TEXT    NOT NULL,
  date_begin_us   BIGINT  NOT NULL,
  date_end_us     BIGINT  NOT NULL,
  xml_blob_hash   TEXT    NOT NULL,
  parsed_ok       BOOLEAN NOT NULL DEFAULT TRUE,
  parse_error     TEXT    NOT NULL DEFAULT '',
  UNIQUE(reporter_org, report_id)
);

CREATE INDEX idx_dmarc_reports_domain_date
  ON dmarc_reports_raw(domain, date_begin_us);

CREATE TABLE dmarc_rows (
  id              BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  report_id       BIGINT  NOT NULL REFERENCES dmarc_reports_raw(id) ON DELETE CASCADE,
  source_ip       TEXT    NOT NULL,
  count           BIGINT  NOT NULL,
  disposition     INTEGER NOT NULL,
  spf_aligned     BOOLEAN NOT NULL DEFAULT FALSE,
  dkim_aligned    BOOLEAN NOT NULL DEFAULT FALSE,
  spf_result      TEXT    NOT NULL DEFAULT '',
  dkim_result     TEXT    NOT NULL DEFAULT '',
  header_from     TEXT    NOT NULL,
  envelope_from   TEXT    NOT NULL DEFAULT '',
  envelope_to     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_dmarc_rows_report ON dmarc_rows(report_id);
CREATE INDEX idx_dmarc_rows_header_disp ON dmarc_rows(header_from, disposition);

CREATE TABLE mailbox_acl (
  id            BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  mailbox_id    BIGINT  NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
  principal_id  BIGINT  REFERENCES principals(id) ON DELETE CASCADE,
  rights_mask   BIGINT  NOT NULL,
  granted_by    BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at_us BIGINT  NOT NULL
);

CREATE UNIQUE INDEX idx_mailbox_acl_unique_principal
  ON mailbox_acl(mailbox_id, principal_id) WHERE principal_id IS NOT NULL;
CREATE UNIQUE INDEX idx_mailbox_acl_unique_anyone
  ON mailbox_acl(mailbox_id) WHERE principal_id IS NULL;
CREATE INDEX idx_mailbox_acl_principal_mailbox
  ON mailbox_acl(principal_id, mailbox_id);

CREATE TABLE jmap_states (
  principal_id              BIGINT  PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  mailbox_state             BIGINT  NOT NULL DEFAULT 0,
  email_state               BIGINT  NOT NULL DEFAULT 0,
  thread_state              BIGINT  NOT NULL DEFAULT 0,
  identity_state            BIGINT  NOT NULL DEFAULT 0,
  email_submission_state    BIGINT  NOT NULL DEFAULT 0,
  vacation_response_state   BIGINT  NOT NULL DEFAULT 0,
  updated_at_us             BIGINT  NOT NULL
);

CREATE TABLE tlsrpt_failures (
  id                      BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  recorded_at_us          BIGINT  NOT NULL,
  policy_domain           TEXT    NOT NULL,
  receiving_mta_hostname  TEXT    NOT NULL,
  failure_type            INTEGER NOT NULL,
  failure_code            TEXT    NOT NULL DEFAULT '',
  failure_detail_json     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_tlsrpt_failures_domain_at
  ON tlsrpt_failures(policy_domain, recorded_at_us);
