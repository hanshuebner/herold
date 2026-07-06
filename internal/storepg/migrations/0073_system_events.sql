-- 0073_system_events.sql — bounded ring-buffer table for system-initiated
-- operational events (REQ-ADM-304, re #142).
--
-- System-initiated mail-flow events (SMTP acceptance, recipient resolution,
-- SES inbound receipt, webhook dispatch outcomes) belong here, not in the
-- audit_log table. The audit log is the security/compliance record for
-- actor-initiated actions; this table is operational telemetry.
--
-- Bounded by row-count and age via EvictSystemEvents (called by a background
-- sweeper). The `domain` column enables REQ-ADM-307 scope filtering so
-- domain-scoped operators see only events for their managed domains.
--
-- Excluded from herold diag backup by default (--include-system-events
-- opts in).
--
-- Forward-only.

CREATE TABLE system_events (
  id           BIGSERIAL PRIMARY KEY,
  ts           BIGINT  NOT NULL,           -- Unix microseconds (UTC)
  action       TEXT    NOT NULL,           -- dotted action identifier
  actor_id     TEXT    NOT NULL DEFAULT '',
  subject      TEXT    NOT NULL DEFAULT '',
  remote_addr  TEXT    NOT NULL DEFAULT '',
  outcome      INTEGER NOT NULL,           -- 1=success, 2=failure
  message      TEXT    NOT NULL DEFAULT '',
  domain       TEXT    NOT NULL DEFAULT '', -- for REQ-ADM-307 scope filter
  metadata_json TEXT   NOT NULL DEFAULT '{}'
);

-- Primary pagination index: newest-first by id.
CREATE INDEX idx_system_events_ts
  ON system_events(ts DESC);

-- Filtered list by action (admin Events view action filter).
CREATE INDEX idx_system_events_action_ts
  ON system_events(action, ts DESC);

-- Domain-scoped list (REQ-ADM-307): only rows with a non-empty domain.
CREATE INDEX idx_system_events_domain_ts
  ON system_events(domain, ts DESC)
  WHERE domain != '';
