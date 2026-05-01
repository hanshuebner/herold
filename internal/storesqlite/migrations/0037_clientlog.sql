-- 0037_clientlog.sql — ring-buffer table for client-side log events
-- (REQ-OPS-206, REQ-OPS-206a, REQ-OPS-219).
--
-- Two slices ("auth" and "public") share the same table; each row
-- carries its slice name so eviction and read queries can filter
-- independently.  The canonical pagination order is id DESC (newest
-- first) matching REQ-ADM-230.
--
-- Forward-only.

CREATE TABLE clientlog (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  slice         TEXT    NOT NULL,
  server_ts     INTEGER NOT NULL,  -- Unix microseconds (server arrival)
  client_ts     INTEGER NOT NULL,  -- Unix microseconds (browser wall clock)
  clock_skew_ms INTEGER NOT NULL,  -- signed: server_ts_ms - client_ts_ms
  app           TEXT    NOT NULL,
  kind          TEXT    NOT NULL,
  level         TEXT    NOT NULL,
  user_id       TEXT,
  session_id    TEXT,
  page_id       TEXT    NOT NULL,
  request_id    TEXT,
  route         TEXT,
  build_sha     TEXT    NOT NULL,
  ua            TEXT    NOT NULL,
  msg           TEXT    NOT NULL,
  stack         TEXT,
  payload_json  TEXT    NOT NULL
) STRICT;

CREATE INDEX clientlog_slice_id
  ON clientlog(slice, id DESC);

CREATE INDEX clientlog_request_id
  ON clientlog(request_id)
  WHERE request_id IS NOT NULL;

CREATE INDEX clientlog_session_client_ts
  ON clientlog(session_id, client_ts)
  WHERE session_id IS NOT NULL;

CREATE INDEX clientlog_user_server_ts
  ON clientlog(user_id, server_ts DESC)
  WHERE user_id IS NOT NULL;
