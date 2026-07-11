-- 0078_push_subscription_fcm.sql -- re #200 (FCM transport in the push
-- gateway). Mirrors storesqlite 0078. Forward-only.
--
-- Two additive columns on push_subscription:
--
--   * transport   -- discriminates the outbound delivery mechanism.
--     'webpush' (default, matches every existing row) or 'fcm'. The
--     Go side is store.PushTransport; empty is normalized to
--     'webpush' for rows that predate this column, but the DEFAULT
--     guarantees new inserts always carry an explicit value.
--   * fcm_token   -- the Firebase Cloud Messaging registration token
--     for transport='fcm' rows. Empty for 'webpush' rows, which keep
--     using url/p256dh/auth as before.
--
-- FCM subscriptions store empty url/p256dh/auth (already a supported
-- shape: RFC 8620 §7.2 keys-less Web Push subscriptions store empty
-- p256dh/auth today), so no existing NOT NULL constraint needs
-- relaxing.

ALTER TABLE push_subscription
  ADD COLUMN transport TEXT NOT NULL DEFAULT 'webpush';

ALTER TABLE push_subscription
  ADD COLUMN fcm_token TEXT NOT NULL DEFAULT '';
