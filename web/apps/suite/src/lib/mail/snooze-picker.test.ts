/**
 * snoozeQuickOptions returns the canned snooze targets shown in the
 * picker. The test pins "now" so the relative-time math is
 * deterministic across CI runs.
 */
import { describe, it, expect } from 'vitest';
import { _internals_forTest } from './snooze-picker.svelte';

const { snoozeQuickOptions } = _internals_forTest;

function at(year: number, month: number, day: number, hour = 9): Date {
  // month is 1-based for ergonomics in tests.
  return new Date(year, month - 1, day, hour, 0, 0, 0);
}

describe('snoozeQuickOptions', () => {
  it('returns Tomorrow morning + Next week at minimum', () => {
    const now = at(2026, 4, 28, 9); // Tuesday morning
    const opts = snoozeQuickOptions(now);
    const labels = opts.map((o) => o.label);
    expect(labels).toContain('Tomorrow morning');
    expect(labels).toContain('Next week');
  });

  it('Tomorrow morning is exactly 8:00 the next day', () => {
    const now = at(2026, 4, 28, 9);
    const tomorrow = snoozeQuickOptions(now).find((o) => o.label === 'Tomorrow morning')?.at;
    expect(tomorrow?.getDate()).toBe(29);
    expect(tomorrow?.getHours()).toBe(8);
    expect(tomorrow?.getMinutes()).toBe(0);
  });

  it('Next week is the next Monday at 8:00', () => {
    const now = at(2026, 4, 28, 9); // Tuesday
    const nextWeek = snoozeQuickOptions(now).find((o) => o.label === 'Next week')?.at;
    // Next Monday after Tue 2026-04-28 is 2026-05-04.
    expect(nextWeek?.getFullYear()).toBe(2026);
    expect(nextWeek?.getMonth()).toBe(4); // May (0-indexed)
    expect(nextWeek?.getDate()).toBe(4);
    expect(nextWeek?.getDay()).toBe(1); // Monday
    expect(nextWeek?.getHours()).toBe(8);
  });

  it('omits Later today after 9 pm', () => {
    const lateNight = at(2026, 4, 28, 22);
    const labels = snoozeQuickOptions(lateNight).map((o) => o.label);
    expect(labels).not.toContain('Later today');
  });

  it('omits This weekend when Sat is tomorrow (avoid duplicate with Tomorrow morning)', () => {
    const friday = at(2026, 5, 1, 10); // Friday
    const labels = snoozeQuickOptions(friday).map((o) => o.label);
    expect(labels).not.toContain('This weekend');
    // (saturday 2026-05-02 is exactly 1 day away)
    expect(labels).toContain('Tomorrow morning');
  });

  it('includes This weekend earlier in the week', () => {
    const monday = at(2026, 4, 27, 10);
    const opts = snoozeQuickOptions(monday);
    const weekend = opts.find((o) => o.label === 'This weekend');
    expect(weekend).toBeDefined();
    expect(weekend?.at.getDay()).toBe(6); // Saturday
    expect(weekend?.at.getHours()).toBe(9);
  });
});
