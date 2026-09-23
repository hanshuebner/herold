-- 0115_message_references.sql -- reverse-reference index for REQ-STORE-40
-- late-ancestor thread merging (re #485).
--
-- One row per (message, Message-ID it names in its own In-Reply-To or
-- References header), written by insertMessageTx alongside the messages
-- row and refreshed by ReplaceMessageBody when a draft's headers are
-- rewritten. The index on (principal_id, referenced_message_id) turns
-- "which already-stored messages of this principal name the message I am
-- inserting right now as an ancestor" into a single indexed lookup.
-- Before this migration that lookup was a LOWER(...) LIKE '%...%' scan of
-- every message row for the principal, paid on every insert that carries
-- a Message-ID (IMAP import, SMTP delivery, JMAP Email/set all pay it) --
-- an EXPLAIN QUERY PLAN showed an index seek on principal_id followed by
-- a linear scan of every remaining row.
--
-- referenced_message_id is a normalised Message-ID (mailparse.
-- NormalizeMessageID: lowercase, angle brackets stripped) with no FK --
-- the referenced message frequently does not exist yet at the time this
-- row is written, which is exactly the race REQ-STORE-40 exists to
-- resolve once it eventually arrives.
--
-- Backfill: extracts every "<...>" token from every existing message's
-- env_in_reply_to/env_references via a recursive CTE that walks each
-- header string bracket-pair by bracket-pair -- the same extraction rule
-- mailparse.ParseReferences applies in Go, reimplemented in SQL because
-- migrations are plain SQL with no per-row Go hook. This is a one-time
-- pass over the existing (message x reference) population, not a
-- per-request cost; at the current production scale (a few thousand
-- messages) it completes in well under a second. A store with tens of
-- millions of messages would need a background/staged backfill instead
-- of an inline one; that threshold has not been reached, so the simple
-- inline backfill is preferred over adding per-principal
-- backfilled/not-backfilled bookkeeping and a dual lookup path for a cost
-- this migration does not actually have to pay.
--
-- Forward-only. Mirrors storepg 0115.

CREATE TABLE message_references (
  principal_id          INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  message_id            INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  referenced_message_id TEXT    NOT NULL,
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
    SELECT id, principal_id, NULL, hdr FROM hdrs
    UNION ALL
    SELECT id, principal_id,
           substr(rest, instr(rest, '<') + 1, instr(rest, '>') - instr(rest, '<') - 1),
           substr(rest, instr(rest, '>') + 1)
      FROM toks
     WHERE instr(rest, '<') > 0 AND instr(rest, '>') > instr(rest, '<')
  )
INSERT INTO message_references (principal_id, message_id, referenced_message_id)
SELECT DISTINCT principal_id, id, LOWER(TRIM(tok))
  FROM toks
 WHERE tok IS NOT NULL AND TRIM(tok) <> '';
