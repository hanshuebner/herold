-- 0115_message_references.sql -- reverse-reference index for REQ-STORE-40
-- late-ancestor thread merging (re #485). Mirrors storesqlite 0115.

CREATE TABLE message_references (
  principal_id          BIGINT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  message_id            BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  referenced_message_id TEXT   NOT NULL,
  PRIMARY KEY (message_id, referenced_message_id)
);

CREATE INDEX idx_message_references_principal_referenced
  ON message_references(principal_id, referenced_message_id);

WITH RECURSIVE
  hdrs(id, principal_id, hdr) AS (
    SELECT id, principal_id, env_in_reply_to FROM messages WHERE env_in_reply_to <> ''
    UNION ALL
    SELECT id, principal_id, env_references FROM messages WHERE env_references <> ''
  ),
  toks(id, principal_id, tok, rest) AS (
    SELECT id, principal_id, NULL::text, hdr FROM hdrs
    UNION ALL
    SELECT id, principal_id,
           substr(rest, position('<' in rest) + 1, position('>' in rest) - position('<' in rest) - 1),
           substr(rest, position('>' in rest) + 1)
      FROM toks
     WHERE position('<' in rest) > 0 AND position('>' in rest) > position('<' in rest)
  )
INSERT INTO message_references (principal_id, message_id, referenced_message_id)
SELECT DISTINCT principal_id, id, LOWER(TRIM(tok))
  FROM toks
 WHERE tok IS NOT NULL AND TRIM(tok) <> '';
