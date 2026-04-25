-- 0004_phase2.sql — Phase 2 outbound + mail security + admin schema.
--
-- Wave 2.0 introduces nine new tables behind store.Metadata to back
-- the Wave 2.1 subsystems (queue, outbound SMTP, mail-auth signer,
-- ACME, DNS plugins + autodns, webhooks, DMARC ingest, mailbox ACL,
-- JMAP states, TLS-RPT). The shapes here are kept isomorphic with the
-- Postgres 0004 migration of the same name so the Phase 2 SQLite ↔ PG
-- migration tool can copy row-by-row. Conventions match 0001..0003:
-- BIGINT-equivalent identity columns, _us suffixed unix-micros
-- timestamps, TEXT for hex / opaque-string columns, BLOB / BYTEA for
-- raw bytes.

-- queue: per-recipient outbound mail items.
--
-- One row per (envelope_id, rcpt_to). Per-recipient rows make DSN
-- generation, retry / backoff, and HOLD trivially per-row; the
-- envelope_id column groups them when correlating a multi-RCPT
-- submission's DSN. body_blob_hash references the canonical body in
-- the blob store (refcount incremented on EnqueueMessage,
-- decremented on Complete/Delete). A non-empty headers_blob_hash
-- carries pre-rendered headers (signer commits) — when present the
-- worker streams them verbatim instead of synthesising on each
-- attempt.
CREATE TABLE queue (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id        INTEGER REFERENCES principals(id) ON DELETE SET NULL,
  mail_from           TEXT    NOT NULL,
  rcpt_to             TEXT    NOT NULL,
  envelope_id         TEXT    NOT NULL,
  body_blob_hash      TEXT    NOT NULL,
  headers_blob_hash   TEXT    NOT NULL DEFAULT '',
  state               INTEGER NOT NULL,
  attempts            INTEGER NOT NULL DEFAULT 0,
  last_attempt_at_us  INTEGER NOT NULL DEFAULT 0,
  next_attempt_at_us  INTEGER NOT NULL DEFAULT 0,
  last_error          TEXT    NOT NULL DEFAULT '',
  dsn_notify_flags    INTEGER NOT NULL DEFAULT 0,
  dsn_ret             INTEGER NOT NULL DEFAULT 0,
  dsn_envid           TEXT    NOT NULL DEFAULT '',
  dsn_orcpt           TEXT    NOT NULL DEFAULT '',
  idempotency_key     TEXT,
  created_at_us       INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_queue_state_next_attempt
  ON queue(state, next_attempt_at_us);
CREATE INDEX idx_queue_envelope ON queue(envelope_id);
CREATE INDEX idx_queue_principal_state ON queue(principal_id, state);
CREATE UNIQUE INDEX idx_queue_idempotency
  ON queue(idempotency_key) WHERE idempotency_key IS NOT NULL;

-- dkim_keys: per-domain per-selector signing keys.
CREATE TABLE dkim_keys (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  domain          TEXT    NOT NULL,
  selector        TEXT    NOT NULL,
  algorithm       INTEGER NOT NULL,
  private_key_pem TEXT    NOT NULL,
  public_key_b64  TEXT    NOT NULL,
  status          INTEGER NOT NULL,
  created_at_us   INTEGER NOT NULL,
  rotated_at_us   INTEGER NOT NULL DEFAULT 0,
  UNIQUE(domain, selector)
) STRICT;

CREATE INDEX idx_dkim_keys_domain_status ON dkim_keys(domain, status);

-- acme_accounts: ACME account keys + contact.
CREATE TABLE acme_accounts (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  directory_url     TEXT    NOT NULL,
  contact_email     TEXT    NOT NULL,
  account_key_pem   TEXT    NOT NULL,
  kid               TEXT    NOT NULL DEFAULT '',
  created_at_us     INTEGER NOT NULL,
  UNIQUE(directory_url, contact_email)
) STRICT;

-- acme_orders: in-flight orders. Finalized orders stay for audit;
-- live order state is always here (no shadow table).
CREATE TABLE acme_orders (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id      INTEGER NOT NULL REFERENCES acme_accounts(id) ON DELETE CASCADE,
  hostnames_json  TEXT    NOT NULL,
  status          INTEGER NOT NULL,
  order_url       TEXT    NOT NULL DEFAULT '',
  finalize_url    TEXT    NOT NULL DEFAULT '',
  certificate_url TEXT    NOT NULL DEFAULT '',
  challenge_type  INTEGER NOT NULL DEFAULT 0,
  updated_at_us   INTEGER NOT NULL,
  error           TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_acme_orders_status_updated
  ON acme_orders(status, updated_at_us);

-- acme_certs: active cert material per hostname.
CREATE TABLE acme_certs (
  hostname         TEXT    PRIMARY KEY,
  chain_pem        TEXT    NOT NULL,
  private_key_pem  TEXT    NOT NULL,
  not_before_us    INTEGER NOT NULL,
  not_after_us     INTEGER NOT NULL,
  issuer           TEXT    NOT NULL DEFAULT '',
  order_id         INTEGER REFERENCES acme_orders(id) ON DELETE SET NULL
) STRICT;

CREATE INDEX idx_acme_certs_not_after ON acme_certs(not_after_us);

-- webhooks: mail-arrival subscription rows.
--
-- owner_kind=domain stores the domain name in owner_id; owner_kind=
-- principal stores the stringified PrincipalID. The dispatcher
-- queries by both shapes; storing as a single TEXT column keeps the
-- schema simple at the cost of a small explicit predicate in the
-- "active for domain" lookup.
CREATE TABLE webhooks (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_kind          INTEGER NOT NULL,
  owner_id            TEXT    NOT NULL,
  target_url          TEXT    NOT NULL,
  hmac_secret         BLOB    NOT NULL,
  delivery_mode       INTEGER NOT NULL,
  retry_policy_json   TEXT    NOT NULL DEFAULT '',
  active              INTEGER NOT NULL DEFAULT 1,
  created_at_us       INTEGER NOT NULL,
  updated_at_us       INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_webhooks_owner ON webhooks(owner_kind, owner_id);
CREATE INDEX idx_webhooks_active ON webhooks(active);

-- dmarc_reports_raw: ingested DMARC aggregate report metadata. The
-- XML body lives in the blob store (xml_blob_hash). (reporter_org,
-- report_id) is unique so re-deliveries from the same reporter
-- dedupe at the storage boundary.
CREATE TABLE dmarc_reports_raw (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  received_at_us  INTEGER NOT NULL,
  reporter_email  TEXT    NOT NULL,
  reporter_org    TEXT    NOT NULL,
  report_id       TEXT    NOT NULL,
  domain          TEXT    NOT NULL,
  date_begin_us   INTEGER NOT NULL,
  date_end_us     INTEGER NOT NULL,
  xml_blob_hash   TEXT    NOT NULL,
  parsed_ok       INTEGER NOT NULL DEFAULT 1,
  parse_error     TEXT    NOT NULL DEFAULT '',
  UNIQUE(reporter_org, report_id)
) STRICT;

CREATE INDEX idx_dmarc_reports_domain_date
  ON dmarc_reports_raw(domain, date_begin_us);

-- dmarc_rows: parsed per-record rows from each report.
CREATE TABLE dmarc_rows (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  report_id       INTEGER NOT NULL REFERENCES dmarc_reports_raw(id) ON DELETE CASCADE,
  source_ip       TEXT    NOT NULL,
  count           INTEGER NOT NULL,
  disposition     INTEGER NOT NULL,
  spf_aligned     INTEGER NOT NULL DEFAULT 0,
  dkim_aligned    INTEGER NOT NULL DEFAULT 0,
  spf_result      TEXT    NOT NULL DEFAULT '',
  dkim_result     TEXT    NOT NULL DEFAULT '',
  header_from     TEXT    NOT NULL,
  envelope_from   TEXT    NOT NULL DEFAULT '',
  envelope_to     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_dmarc_rows_report ON dmarc_rows(report_id);
CREATE INDEX idx_dmarc_rows_header_disp ON dmarc_rows(header_from, disposition);

-- mailbox_acl: per-mailbox ACL rows for RFC 4314 + JMAP sharing.
--
-- principal_id NULL encodes the RFC 4314 "anyone" pseudo-row. The
-- partial unique index lets one such row per mailbox coexist with
-- ordinary per-principal rows.
CREATE TABLE mailbox_acl (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  mailbox_id    INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
  principal_id  INTEGER REFERENCES principals(id) ON DELETE CASCADE,
  rights_mask   INTEGER NOT NULL,
  granted_by    INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at_us INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_mailbox_acl_unique_principal
  ON mailbox_acl(mailbox_id, principal_id) WHERE principal_id IS NOT NULL;
CREATE UNIQUE INDEX idx_mailbox_acl_unique_anyone
  ON mailbox_acl(mailbox_id) WHERE principal_id IS NULL;
CREATE INDEX idx_mailbox_acl_principal_mailbox
  ON mailbox_acl(principal_id, mailbox_id);

-- jmap_states: per-principal JMAP-object-scoped state counters. One
-- row per principal; counters increment on every relevant mutation.
-- Created lazily on first access.
CREATE TABLE jmap_states (
  principal_id              INTEGER PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  mailbox_state             INTEGER NOT NULL DEFAULT 0,
  email_state               INTEGER NOT NULL DEFAULT 0,
  thread_state              INTEGER NOT NULL DEFAULT 0,
  identity_state            INTEGER NOT NULL DEFAULT 0,
  email_submission_state    INTEGER NOT NULL DEFAULT 0,
  vacation_response_state   INTEGER NOT NULL DEFAULT 0,
  updated_at_us             INTEGER NOT NULL
) STRICT;

-- tlsrpt_failures: outbound TLS failures awaiting roll-up.
CREATE TABLE tlsrpt_failures (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  recorded_at_us          INTEGER NOT NULL,
  policy_domain           TEXT    NOT NULL,
  receiving_mta_hostname  TEXT    NOT NULL,
  failure_type            INTEGER NOT NULL,
  failure_code            TEXT    NOT NULL DEFAULT '',
  failure_detail_json     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_tlsrpt_failures_domain_at
  ON tlsrpt_failures(policy_domain, recorded_at_us);
