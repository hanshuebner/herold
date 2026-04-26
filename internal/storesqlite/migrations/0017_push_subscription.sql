-- 0017_push_subscription.sql -- Phase 3 Wave 3.8a:
-- REQ-PROTO-120..122 (JMAP PushSubscription datatype + VAPID key
-- mgmt + capability advertisement; outbound dispatch lands in 3.8b).
-- Mirrors storepg 0017. Forward-only.
--
-- One new table: push_subscription. Per-principal rows; clients
-- (browser service workers / mobile push tokens) register one row
-- per (principal, browser-instance). The row carries:
--
--   * the standard RFC 8620 §7.2 fields: device_client_id, url,
--     keys.p256dh + keys.auth, expires, types (subscribed JMAP
--     datatypes as a comma-separated list), verification_code (the
--     server-minted nonce echoed by the client to confirm receipt)
--     and verified (bool, flips true on successful echo).
--
--   * tabard's REQ-PROTO-121 extensions: notification_rules_json
--     (opaque blob; parsed by the 3.8c rules engine), quiet_hours_*
--     columns, and vapid_key_at_registration (the VAPID public key
--     the client used at subscribe time so a key rotation that
--     invalidates older subscriptions is mechanically detectable).
--
-- The keys.p256dh / keys.auth pair is stored as raw bytes because
-- the dispatcher (3.8b) uses them as RFC 8291 ECDH input directly.
-- The wire form (base64url) is handled at the JMAP serializer.
--
-- One additive jmap_states column (push_subscription_state) bumped
-- on every successful PushSubscription/set so JMAP clients can
-- track their own subscription churn through the standard /changes
-- pattern. The dispatcher does NOT need this column.

CREATE TABLE push_subscription (
  id                            INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id                  INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  device_client_id              TEXT    NOT NULL,
  url                           TEXT    NOT NULL,
  p256dh                        BLOB    NOT NULL,
  auth                          BLOB    NOT NULL,
  expires_at_us                 INTEGER,
  types_csv                     TEXT    NOT NULL DEFAULT '',
  verification_code             TEXT    NOT NULL DEFAULT '',
  verified                      INTEGER NOT NULL DEFAULT 0,
  vapid_key_at_registration     TEXT    NOT NULL DEFAULT '',
  notification_rules_json       BLOB,
  quiet_hours_start_local       INTEGER,
  quiet_hours_end_local         INTEGER,
  quiet_hours_tz                TEXT    NOT NULL DEFAULT '',
  created_at_us                 INTEGER NOT NULL,
  updated_at_us                 INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_push_subscription_principal
  ON push_subscription(principal_id);

ALTER TABLE jmap_states
  ADD COLUMN push_subscription_state INTEGER NOT NULL DEFAULT 0;
