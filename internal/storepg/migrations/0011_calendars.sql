-- 0011_calendars.sql — Phase-2 Wave 2.7: JMAP for Calendars
-- (REQ-PROTO-54, RFC 8984 JSCalendar + the JMAP-Calendars binding
-- draft). Mirrors storesqlite 0011. Postgres idioms applied where
-- helpful (BIGINT IDENTITY, BOOLEAN, BYTEA, partial unique index);
-- column shapes stay isomorphic with SQLite so the migration tool
-- copies row-by-row. Forward-only.

CREATE TABLE calendars (
  id              BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id    BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name            TEXT    NOT NULL,
  description     TEXT    NOT NULL DEFAULT '',
  color_hex       TEXT
    CHECK (color_hex IS NULL OR color_hex ~ '^#[0-9A-Fa-f]{6}$'),
  sort_order      INTEGER NOT NULL DEFAULT 0,
  is_subscribed   BOOLEAN NOT NULL DEFAULT TRUE,
  is_default      BOOLEAN NOT NULL DEFAULT FALSE,
  is_visible      BOOLEAN NOT NULL DEFAULT TRUE,
  time_zone_id    TEXT    NOT NULL DEFAULT '',
  rights_mask     BIGINT  NOT NULL DEFAULT 0,
  created_at_us   BIGINT  NOT NULL,
  updated_at_us   BIGINT  NOT NULL,
  modseq          BIGINT  NOT NULL DEFAULT 0
);

CREATE INDEX idx_calendars_principal ON calendars(principal_id);
CREATE UNIQUE INDEX idx_calendars_default
  ON calendars(principal_id) WHERE is_default = TRUE;

CREATE TABLE calendar_events (
  id               BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  calendar_id      BIGINT  NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  principal_id     BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  uid              TEXT    NOT NULL,
  jscalendar_json  BYTEA   NOT NULL,
  start_us         BIGINT  NOT NULL,
  end_us           BIGINT  NOT NULL,
  is_recurring     BOOLEAN NOT NULL DEFAULT FALSE,
  rrule_json       BYTEA,
  summary          TEXT    NOT NULL DEFAULT '',
  organizer_email  TEXT,
  status           TEXT    NOT NULL DEFAULT '',
  created_at_us    BIGINT  NOT NULL,
  updated_at_us    BIGINT  NOT NULL,
  modseq           BIGINT  NOT NULL DEFAULT 0
);

CREATE INDEX idx_calendar_events_calendar          ON calendar_events(calendar_id);
CREATE INDEX idx_calendar_events_principal_start   ON calendar_events(principal_id, start_us);
CREATE UNIQUE INDEX idx_calendar_events_uid        ON calendar_events(calendar_id, uid);
CREATE INDEX idx_calendar_events_modseq            ON calendar_events(calendar_id, modseq);
CREATE INDEX idx_calendar_events_organizer         ON calendar_events(principal_id, organizer_email)
  WHERE organizer_email IS NOT NULL;

ALTER TABLE jmap_states ADD COLUMN calendar_state       BIGINT NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN calendar_event_state BIGINT NOT NULL DEFAULT 0;
