-- 0012_chat.sql — Phase-2 Wave 2.8: chat subsystem (REQ-CHAT-*).
-- Architecture in docs/architecture/08-chat.md; spec in
-- docs/requirements/14-chat.md. The schema mirrors the storepg 0012
-- migration of the same name; column shapes stay isomorphic so the
-- backup / migration tooling moves rows row-for-row across backends.
--
-- Five new tables (chat_conversations, chat_memberships, chat_messages,
-- chat_blocks) plus three additive jmap_states columns
-- (conversation_state, message_chat_state, membership_state).
--
-- Naming: we deliberately use message_chat_state rather than
-- message_state on jmap_states. JMAP exposes a single Email datatype
-- whose state-string the existing email_state column tracks; the chat
-- Message datatype is conceptually distinct (per the architecture doc:
-- "chat-Message; distinct from email-Email"). A separate chat-side
-- counter keeps Email/changes and Message/changes from accidentally
-- sharing a state string. Same reason there is an EntityKindChatMessage
-- distinct from EntityKindEmail in the Go layer.
--
-- Reactions are stored as a JSON BLOB on chat_messages.reactions_json
-- (shape {"<emoji>": [principal_id, ...]}). The Go layer caps reactions
-- at 100 distinct emojis x 200 reactors per emoji; CHECK constraints
-- in SQL would fragment the schema across SQLite vs Postgres so we
-- enforce in code instead.
--
-- Attachments JSON: an array of {blob_hash, content_type, filename,
-- size}. Each blob_hash references a row the mail subsystem already
-- refcounts via blob_refs; chat-message creators are responsible for
-- incrementing those refcounts the same way mail messages do.
--
-- System messages: is_system = 1, sender_principal_id = NULL,
-- body_text carries a small phrase ("Alice started a video call"),
-- metadata_json carries the structured payload.
--
-- DeletePrincipal cascade: chat_blocks and chat_memberships are bound
-- to principals(id) via ON DELETE CASCADE so the existing
-- DeletePrincipal path sweeps them automatically. chat_messages.
-- sender_principal_id is ON DELETE SET NULL so historical messages
-- survive sender deletion as system-message-less rows.
--
-- Forward-only.

CREATE TABLE chat_conversations (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  kind                     TEXT    NOT NULL,                       -- "dm" | "space"
  name                     TEXT,                                   -- spaces have a name; DMs are computed from members
  topic                    TEXT,                                   -- spaces; brief description
  created_by_principal_id  INTEGER NOT NULL REFERENCES principals(id),
  created_at_us            INTEGER NOT NULL,
  updated_at_us            INTEGER NOT NULL,
  last_message_at_us       INTEGER,                                -- denorm for sort-by-recent
  message_count            INTEGER NOT NULL DEFAULT 0,             -- denorm
  is_archived              INTEGER NOT NULL DEFAULT 0,
  modseq                   INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_chat_conversations_last_message
  ON chat_conversations(last_message_at_us DESC);

CREATE TABLE chat_memberships (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id          INTEGER NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
  principal_id             INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  role                     TEXT    NOT NULL,                       -- "member" | "admin" | "owner"
  joined_at_us             INTEGER NOT NULL,
  last_read_message_id     INTEGER,                                -- per-principal read pointer
  is_muted                 INTEGER NOT NULL DEFAULT 0,
  mute_until_us            INTEGER,                                -- nullable; specific-time mute
  notifications_setting    TEXT    NOT NULL DEFAULT 'all',         -- "all" | "mentions" | "none"
  modseq                   INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE UNIQUE INDEX idx_chat_memberships_unique
  ON chat_memberships(conversation_id, principal_id);
CREATE INDEX idx_chat_memberships_principal
  ON chat_memberships(principal_id);

CREATE TABLE chat_messages (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id          INTEGER NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
  sender_principal_id      INTEGER REFERENCES principals(id) ON DELETE SET NULL,
  is_system                INTEGER NOT NULL DEFAULT 0,             -- e.g. call-started, member-joined, member-left
  body_text                TEXT,                                   -- plain-text rendering for FTS / fallback
  body_html                TEXT,                                   -- HTML rendering for clients
  body_format              TEXT    NOT NULL DEFAULT 'text',        -- "text" | "markdown" | "html"
  reply_to_message_id      INTEGER REFERENCES chat_messages(id) ON DELETE SET NULL,
  reactions_json           BLOB,                                   -- JSON: {emoji -> [principal_ids]}
  attachments_json         BLOB,                                   -- JSON array; each {blob_hash, content_type, filename, size}
  metadata_json            BLOB,                                   -- system-message payload (call ids, etc.)
  edited_at_us             INTEGER,                                -- nullable; not-edited when null
  deleted_at_us            INTEGER,                                -- soft-delete; not-deleted when null
  created_at_us            INTEGER NOT NULL,
  modseq                   INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_chat_messages_conversation
  ON chat_messages(conversation_id, created_at_us);
CREATE INDEX idx_chat_messages_modseq
  ON chat_messages(conversation_id, modseq);
CREATE INDEX idx_chat_messages_sender
  ON chat_messages(sender_principal_id);

CREATE TABLE chat_blocks (
  blocker_principal_id     INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  blocked_principal_id     INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at_us            INTEGER NOT NULL,
  reason                   TEXT,
  PRIMARY KEY (blocker_principal_id, blocked_principal_id)
) STRICT;

CREATE INDEX idx_chat_blocks_blocked
  ON chat_blocks(blocked_principal_id);

ALTER TABLE jmap_states ADD COLUMN conversation_state   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN message_chat_state   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN membership_state     INTEGER NOT NULL DEFAULT 0;
