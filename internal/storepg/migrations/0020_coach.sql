-- 0020_coach.sql -- Phase 3 Wave 3.10:
-- REQ-PROTO-110..112 (ShortcutCoachStat JMAP datatype).
-- Mirrors storesqlite 0020. Forward-only.
--
-- Postgres idioms: BIGINT IDENTITY, TIMESTAMPTZ stored as Unix
-- microseconds (BIGINT) for cross-backend parity, BOOLEAN for
-- coach_dismiss.dismiss_until nullable.

CREATE TABLE coach_events (
  id           BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  action       TEXT    NOT NULL,
  input_method TEXT    NOT NULL CHECK (input_method IN ('keyboard','mouse')),
  event_count  INTEGER NOT NULL DEFAULT 1,
  occurred_at  BIGINT  NOT NULL,
  recorded_at  BIGINT  NOT NULL
);

CREATE INDEX idx_coach_events_lookup
  ON coach_events(principal_id, action, occurred_at);

CREATE TABLE coach_dismiss (
  principal_id  BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  action        TEXT    NOT NULL,
  dismiss_count INTEGER NOT NULL DEFAULT 0,
  dismiss_until BIGINT,
  updated_at    BIGINT  NOT NULL,
  PRIMARY KEY (principal_id, action)
);

ALTER TABLE jmap_states
  ADD COLUMN shortcut_coach_state BIGINT NOT NULL DEFAULT 0;
