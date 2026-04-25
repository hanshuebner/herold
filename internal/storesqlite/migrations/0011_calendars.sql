-- 0011_calendars.sql — Phase-2 Wave 2.7: JMAP for Calendars
-- (REQ-PROTO-54, RFC 8984 JSCalendar + the JMAP-Calendars binding draft).
--
-- Two new tables (calendars, calendar_events) plus two additive
-- jmap_states columns (calendar_state, calendar_event_state). Mirrors
-- the storepg 0011 migration of the same name; column shapes stay
-- isomorphic so the backup / migration tooling moves rows row-for-row
-- across backends.
--
-- Storage strategy for the JSCalendar Event object: we persist the
-- full RFC 8984 object verbatim as JSON in
-- calendar_events.jscalendar_json. A handful of denormalised columns
-- (start_us, end_us, is_recurring, rrule_json, summary,
-- organizer_email, status) carry the values JMAP queries filter and
-- sort on so the read paths do not have to JSON-parse every row. The
-- protojmap serializer (parallel agent B in Wave 2.7) is the sole
-- producer of those columns: it parses the JSCalendar, derives the
-- denormalised fields, and hands the store a fully-populated
-- CalendarEvent. Future RFC 8984 additions land in the JSON blob
-- without a schema change; only when a new field needs to be
-- filterable does it earn a column.
--
-- IsDefault enforcement: a partial unique index pins at most one
-- is_default=1 row per principal. The Go layer flips the previous
-- default to 0 inside the same tx when a caller marks a different
-- calendar as default; the index is the safety net.
--
-- Forward-only.

CREATE TABLE calendars (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id    INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name            TEXT    NOT NULL,
  description     TEXT    NOT NULL DEFAULT '',
  color_hex       TEXT,
  sort_order      INTEGER NOT NULL DEFAULT 0,
  is_subscribed   INTEGER NOT NULL DEFAULT 1,
  is_default      INTEGER NOT NULL DEFAULT 0,
  is_visible      INTEGER NOT NULL DEFAULT 1,
  time_zone_id    TEXT    NOT NULL DEFAULT '',
  rights_mask     INTEGER NOT NULL DEFAULT 0,
  created_at_us   INTEGER NOT NULL,
  updated_at_us   INTEGER NOT NULL,
  modseq          INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_calendars_principal ON calendars(principal_id);
CREATE UNIQUE INDEX idx_calendars_default
  ON calendars(principal_id) WHERE is_default = 1;

CREATE TABLE calendar_events (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  calendar_id      INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  principal_id     INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  uid              TEXT    NOT NULL,
  jscalendar_json  BLOB    NOT NULL,
  start_us         INTEGER NOT NULL,
  end_us           INTEGER NOT NULL,
  is_recurring     INTEGER NOT NULL DEFAULT 0,
  rrule_json       BLOB,
  summary          TEXT    NOT NULL DEFAULT '',
  organizer_email  TEXT,
  status           TEXT    NOT NULL DEFAULT '',
  created_at_us    INTEGER NOT NULL,
  updated_at_us    INTEGER NOT NULL,
  modseq           INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_calendar_events_calendar          ON calendar_events(calendar_id);
CREATE INDEX idx_calendar_events_principal_start   ON calendar_events(principal_id, start_us);
CREATE UNIQUE INDEX idx_calendar_events_uid        ON calendar_events(calendar_id, uid);
CREATE INDEX idx_calendar_events_modseq            ON calendar_events(calendar_id, modseq);
CREATE INDEX idx_calendar_events_organizer         ON calendar_events(principal_id, organizer_email)
  WHERE organizer_email IS NOT NULL;

ALTER TABLE jmap_states ADD COLUMN calendar_state       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN calendar_event_state INTEGER NOT NULL DEFAULT 0;
