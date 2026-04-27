-- 0020_coach.sql -- Phase 3 Wave 3.10:
-- REQ-PROTO-110..112 (ShortcutCoachStat JMAP datatype).
-- Mirrors storepg 0020. Forward-only.
--
-- Backing table: coach_events. One row per invocation event that
-- the suite flushes to herold.  The stat aggregates (keyboardCount14d,
-- mouseCount14d, keyboardCount90d, mouseCount90d, lastKeyboardAt,
-- lastMouseAt) are computed at read time by aggregating events within
-- the relevant sliding window — no rolled-up counters that drift.
--
-- Two additional columns live directly on the stat: dismissCount and
-- dismissUntil.  These are not window-aggregated (they accumulate
-- monotonically / are set explicitly by the client), so they get their
-- own table: coach_dismiss.  Combining them with coach_events would
-- complicate the aggregation query.
--
-- GC: a nightly pass (see internal/admin/server.go) deletes
-- coach_events rows with occurred_at < now - 90 days.
--
-- Privacy: no foreign reads. Only the owning principal can touch
-- their own rows; herold enforces this at the JMAP handler level
-- (REQ-PROTO-112 / REQ-COACH-04).
--
-- One new jmap_states column: shortcut_coach_state, bumped on every
-- successful ShortcutCoachStat/set.

CREATE TABLE coach_events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  action       TEXT    NOT NULL,
  input_method TEXT    NOT NULL CHECK (input_method IN ('keyboard','mouse')),
  event_count  INTEGER NOT NULL DEFAULT 1,
  occurred_at  INTEGER NOT NULL,  -- Unix microseconds; client-supplied
  recorded_at  INTEGER NOT NULL   -- Unix microseconds; server-assigned
) STRICT;

CREATE INDEX idx_coach_events_lookup
  ON coach_events(principal_id, action, occurred_at);

-- coach_dismiss stores per-(principal, action) dismissal counters
-- and the optional suppression deadline.  These are not windowed.
CREATE TABLE coach_dismiss (
  principal_id  INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  action        TEXT    NOT NULL,
  dismiss_count INTEGER NOT NULL DEFAULT 0,
  dismiss_until INTEGER,   -- Unix microseconds, nullable
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (principal_id, action)
) STRICT;

ALTER TABLE jmap_states
  ADD COLUMN shortcut_coach_state INTEGER NOT NULL DEFAULT 0;
