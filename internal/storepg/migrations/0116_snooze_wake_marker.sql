-- 0116_snooze_wake_marker.sql -- wake marker for the JMAP snooze
-- extension (issue #469). Mirrors storesqlite 0116.

ALTER TABLE messages ADD COLUMN snooze_woke_at_us BIGINT;
ALTER TABLE messages ADD COLUMN snooze_woke_for_us BIGINT;
