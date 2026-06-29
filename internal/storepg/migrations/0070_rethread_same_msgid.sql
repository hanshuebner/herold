-- 0070_rethread_same_msgid.sql -- repair fragmented threads caused by
-- duplicate message-id copies (Sent + delivered). (re #88, REQ-STORE-40)
-- Mirrors storesqlite 0070.

UPDATE messages
SET thread_id = (
    SELECT CASE WHEN m2.thread_id = 0 THEN m2.id ELSE m2.thread_id END
    FROM messages m2
    WHERE m2.principal_id = messages.principal_id
      AND m2.env_message_id = messages.env_message_id
      AND m2.env_message_id != ''
    ORDER BY m2.id ASC
    LIMIT 1
)
WHERE env_message_id != ''
  AND (
    SELECT COUNT(*)
    FROM messages m2
    WHERE m2.principal_id = messages.principal_id
      AND m2.env_message_id = messages.env_message_id
  ) > 1
  AND (CASE WHEN thread_id = 0 THEN id ELSE thread_id END) != (
    SELECT CASE WHEN m2.thread_id = 0 THEN m2.id ELSE m2.thread_id END
    FROM messages m2
    WHERE m2.principal_id = messages.principal_id
      AND m2.env_message_id = messages.env_message_id
      AND m2.env_message_id != ''
    ORDER BY m2.id ASC
    LIMIT 1
  );
