/**
 * formatWakeTime is the shared "wake time relative to now" formatter
 * used by the SnoozePicker preview, the snooze confirmation toast, and
 * the Snoozed folder's row label (re #471). Pins `now` so the
 * relative-day math is deterministic across CI runs and time zones.
 */

import { describe, it, expect } from 'vitest';
import { formatWakeTime } from './snooze-format';

function at(year: number, month: number, day: number, hour = 9, minute = 0): Date {
  // month is 1-based for ergonomics in tests.
  return new Date(year, month - 1, day, hour, minute, 0, 0);
}

describe('formatWakeTime (re #471)', () => {
  it('shows just the time for a target later today', () => {
    const now = at(2026, 4, 28, 9);
    const target = at(2026, 4, 28, 15, 30);
    expect(formatWakeTime(target, now)).not.toMatch(/tomorrow/i);
    // localeTag() pins the default English locale to en-GB (24h clock,
    // see localeTag's docstring), so a 15:30 target renders as "15:30".
    expect(formatWakeTime(target, now)).toMatch(/15:30/);
  });

  it('appends "tomorrow" for a target the next calendar day', () => {
    const now = at(2026, 4, 28, 9);
    const target = at(2026, 4, 29, 8, 0);
    expect(formatWakeTime(target, now)).toMatch(/tomorrow/i);
  });

  it('shows the weekday for a target 2-6 days out', () => {
    const now = at(2026, 4, 28, 9); // Tuesday
    const target = at(2026, 5, 1, 9); // Friday, 3 days out
    const label = formatWakeTime(target, now);
    expect(label).toMatch(/Friday/);
  });

  it('shows month/day for a target a week or more out', () => {
    const now = at(2026, 4, 28, 9);
    const target = at(2026, 5, 12, 8, 0);
    const label = formatWakeTime(target, now);
    expect(label).toMatch(/May/);
    expect(label).toMatch(/12/);
    expect(label).not.toMatch(/tomorrow/i);
  });
});
