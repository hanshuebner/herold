-- 0012_chat.sql — Phase-2 Wave 2.8: chat subsystem (REQ-CHAT-*).
-- Mirrors storesqlite 0012. Postgres idioms applied where helpful
-- (BIGINT IDENTITY, BOOLEAN, BYTEA); column shapes stay isomorphic
-- with SQLite so the migration tool copies row-by-row. Forward-only.
--
-- See storesqlite/migrations/0012_chat.sql for the full design notes
-- (naming split for message_chat_state vs email_state, JSON shape of
-- reactions_json / attachments_json, system-message convention,
-- DeletePrincipal cascade rules).

CREATE TABLE chat_conversations (
  id                       BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind                     TEXT    NOT NULL,
  name                     TEXT,
  topic                    TEXT,
  created_by_principal_id  BIGINT  NOT NULL REFERENCES principals(id),
  created_at_us            BIGINT  NOT NULL,
  updated_at_us            BIGINT  NOT NULL,
  last_message_at_us       BIGINT,
  message_count            BIGINT  NOT NULL DEFAULT 0,
  is_archived              BOOLEAN NOT NULL DEFAULT FALSE,
  modseq                   BIGINT  NOT NULL DEFAULT 0
);

CREATE INDEX idx_chat_conversations_last_message
  ON chat_conversations(last_message_at_us DESC);

CREATE TABLE chat_memberships (
  id                       BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  conversation_id          BIGINT  NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
  principal_id             BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  role                     TEXT    NOT NULL,
  joined_at_us             BIGINT  NOT NULL,
  last_read_message_id     BIGINT,
  is_muted                 BOOLEAN NOT NULL DEFAULT FALSE,
  mute_until_us            BIGINT,
  notifications_setting    TEXT    NOT NULL DEFAULT 'all',
  modseq                   BIGINT  NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX idx_chat_memberships_unique
  ON chat_memberships(conversation_id, principal_id);
CREATE INDEX idx_chat_memberships_principal
  ON chat_memberships(principal_id);

CREATE TABLE chat_messages (
  id                       BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  conversation_id          BIGINT  NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
  sender_principal_id      BIGINT  REFERENCES principals(id) ON DELETE SET NULL,
  is_system                BOOLEAN NOT NULL DEFAULT FALSE,
  body_text                TEXT,
  body_html                TEXT,
  body_format              TEXT    NOT NULL DEFAULT 'text',
  reply_to_message_id      BIGINT  REFERENCES chat_messages(id) ON DELETE SET NULL,
  reactions_json           BYTEA,
  attachments_json         BYTEA,
  metadata_json            BYTEA,
  edited_at_us             BIGINT,
  deleted_at_us            BIGINT,
  created_at_us            BIGINT  NOT NULL,
  modseq                   BIGINT  NOT NULL DEFAULT 0
);

CREATE INDEX idx_chat_messages_conversation
  ON chat_messages(conversation_id, created_at_us);
CREATE INDEX idx_chat_messages_modseq
  ON chat_messages(conversation_id, modseq);
CREATE INDEX idx_chat_messages_sender
  ON chat_messages(sender_principal_id);

CREATE TABLE chat_blocks (
  blocker_principal_id     BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  blocked_principal_id     BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at_us            BIGINT  NOT NULL,
  reason                   TEXT,
  PRIMARY KEY (blocker_principal_id, blocked_principal_id)
);

CREATE INDEX idx_chat_blocks_blocked
  ON chat_blocks(blocked_principal_id);

ALTER TABLE jmap_states ADD COLUMN conversation_state   BIGINT NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN message_chat_state   BIGINT NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN membership_state     BIGINT NOT NULL DEFAULT 0;
