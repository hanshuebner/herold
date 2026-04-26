-- 0017_push_subscription.sql -- Phase 3 Wave 3.8a:
-- REQ-PROTO-120..122 (JMAP PushSubscription datatype + VAPID key
-- mgmt + capability advertisement; outbound dispatch lands in 3.8b).
-- Mirrors storesqlite 0017. Forward-only.
--
-- Postgres idioms applied where helpful (BIGINT IDENTITY, BOOLEAN,
-- BYTEA); column shapes stay isomorphic with SQLite so the migration
-- tool moves rows row-for-row across backends without translation.

CREATE TABLE push_subscription (
  id                            BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id                  BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  device_client_id              TEXT    NOT NULL,
  url                           TEXT    NOT NULL,
  p256dh                        BYTEA   NOT NULL,
  auth                          BYTEA   NOT NULL,
  expires_at_us                 BIGINT,
  types_csv                     TEXT    NOT NULL DEFAULT '',
  verification_code             TEXT    NOT NULL DEFAULT '',
  verified                      BOOLEAN NOT NULL DEFAULT FALSE,
  vapid_key_at_registration     TEXT    NOT NULL DEFAULT '',
  notification_rules_json       BYTEA,
  quiet_hours_start_local       INTEGER,
  quiet_hours_end_local         INTEGER,
  quiet_hours_tz                TEXT    NOT NULL DEFAULT '',
  created_at_us                 BIGINT  NOT NULL,
  updated_at_us                 BIGINT  NOT NULL
);

CREATE INDEX idx_push_subscription_principal
  ON push_subscription(principal_id);

ALTER TABLE jmap_states
  ADD COLUMN push_subscription_state BIGINT NOT NULL DEFAULT 0;
