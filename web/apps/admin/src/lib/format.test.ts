/**
 * Tests for the locale-aware formatting helpers in format.ts.
 *
 * Locale is browser-resolved (undefined is passed to Intl APIs), so these
 * tests use structural assertions rather than exact locale-specific strings.
 * The exception is the 24-hour clock guarantee: hour12:false is always set on
 * hour-bearing format presets, so no AM/PM marker ever appears regardless of
 * locale.
 *
 * Relative-time assertions use pattern matching rather than exact strings
 * because Intl.RelativeTimeFormat output varies by locale and ICU version.
 * We verify structural correctness: the number appears and the output is
 * non-empty.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import {
  formatRelative,
  formatAbsolute,
  formatDateOnly,
  DATE_TIME_SHORT,
  DATE_TIME_WITH_SECONDS,
} from './format';

// Pin time so relative-time calculations are deterministic.
const FIXED_NOW = new Date('2026-06-28T12:00:00.000Z').getTime();

afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// formatRelative
// ---------------------------------------------------------------------------

describe('formatRelative', () => {
  it('returns empty string for null', () => {
    expect(formatRelative(null)).toBe('');
  });

  it('returns empty string for undefined', () => {
    expect(formatRelative(undefined)).toBe('');
  });

  it('returns empty string for empty string', () => {
    expect(formatRelative('')).toBe('');
  });

  it('returns the raw value for an unparseable string', () => {
    expect(formatRelative('not-a-date')).toBe('not-a-date');
  });

  it('returns a non-empty string for a recent past timestamp (seconds)', () => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    const thirtySecondsAgo = new Date(FIXED_NOW - 30_000).toISOString();
    const result = formatRelative(thirtySecondsAgo);
    expect(result.length).toBeGreaterThan(0);
    // Should contain the digit 30 or the word "now" for auto mode.
    expect(result).toMatch(/30|now/);
  });

  it('returns a non-empty string for a past timestamp in minutes', () => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    const fiveMinutesAgo = new Date(FIXED_NOW - 5 * 60_000).toISOString();
    const result = formatRelative(fiveMinutesAgo);
    expect(result.length).toBeGreaterThan(0);
    expect(result).toMatch(/5/);
  });

  it('returns a non-empty string for a past timestamp in hours', () => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    const threeHoursAgo = new Date(FIXED_NOW - 3 * 3_600_000).toISOString();
    const result = formatRelative(threeHoursAgo);
    expect(result.length).toBeGreaterThan(0);
    expect(result).toMatch(/3/);
  });

  it('returns a non-empty string for a past timestamp in days', () => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    const fiveDaysAgo = new Date(FIXED_NOW - 5 * 86_400_000).toISOString();
    const result = formatRelative(fiveDaysAgo);
    expect(result.length).toBeGreaterThan(0);
    expect(result).toMatch(/5/);
  });

  it('returns a non-empty string for a future timestamp', () => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    const inTwoHours = new Date(FIXED_NOW + 2 * 3_600_000).toISOString();
    const result = formatRelative(inTwoHours);
    expect(result.length).toBeGreaterThan(0);
    expect(result).toMatch(/2/);
  });

  it('does not contain hardcoded English fallback strings', () => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    const tenMinutesAgo = new Date(FIXED_NOW - 10 * 60_000).toISOString();
    const result = formatRelative(tenMinutesAgo);
    // The old hand-rolled formatters produced "10m ago" — make sure we no
    // longer produce that specific format.
    expect(result).not.toBe('10m ago');
  });
});

// ---------------------------------------------------------------------------
// formatAbsolute
// ---------------------------------------------------------------------------

// Use a noon-UTC timestamp so the calendar date is stable in any time zone.
const ISO_NOON_UTC = '2026-06-28T12:00:00.000Z';

describe('formatAbsolute', () => {
  it('returns empty string for null', () => {
    expect(formatAbsolute(null)).toBe('');
  });

  it('returns empty string for undefined', () => {
    expect(formatAbsolute(undefined)).toBe('');
  });

  it('returns the raw value for an unparseable string', () => {
    expect(formatAbsolute('bad')).toBe('bad');
  });

  it('returns a non-empty string for a valid ISO timestamp', () => {
    const result = formatAbsolute(ISO_NOON_UTC);
    expect(result.length).toBeGreaterThan(0);
  });

  it('includes the year in the default output', () => {
    const result = formatAbsolute(ISO_NOON_UTC);
    expect(result).toContain('2026');
  });

  it('does not include AM or PM (hour12:false enforced on all hour-bearing presets)', () => {
    // hour12:false is set on DATE_TIME_SHORT and DATE_TIME_WITH_SECONDS so
    // no AM/PM marker appears regardless of locale. en-US without this flag
    // would produce "12:00 PM".
    expect(formatAbsolute(ISO_NOON_UTC, DATE_TIME_WITH_SECONDS)).not.toMatch(/AM|PM/i);
    expect(formatAbsolute(ISO_NOON_UTC, DATE_TIME_SHORT)).not.toMatch(/AM|PM/i);
  });

  it('uses browser locale, not a hardcoded de-DE locale', () => {
    // The formatters pass undefined as the locale argument so the browser's
    // navigator.language is honoured. In a Node/en-US environment the short
    // month for June is "Jun"; a de-DE environment would produce "Juni".
    // Asserting the absence of "Juni" confirms no hardcoded German locale.
    const result = formatAbsolute(ISO_NOON_UTC, DATE_TIME_SHORT);
    expect(result).not.toContain('Juni');
  });

  it('accepts custom format options (DATE_TIME_SHORT)', () => {
    const result = formatAbsolute(ISO_NOON_UTC, DATE_TIME_SHORT);
    expect(result.length).toBeGreaterThan(0);
    expect(result).toContain('2026');
  });

  it('accepts custom format options (DATE_TIME_WITH_SECONDS)', () => {
    const result = formatAbsolute(ISO_NOON_UTC, DATE_TIME_WITH_SECONDS);
    expect(result.length).toBeGreaterThan(0);
    expect(result).toContain('2026');
  });
});

// ---------------------------------------------------------------------------
// formatDateOnly
// ---------------------------------------------------------------------------

describe('formatDateOnly', () => {
  it('returns empty string for null', () => {
    expect(formatDateOnly(null)).toBe('');
  });

  it('returns empty string for undefined', () => {
    expect(formatDateOnly(undefined)).toBe('');
  });

  it('returns the raw value for an unparseable string', () => {
    expect(formatDateOnly('bad')).toBe('bad');
  });

  it('returns a non-empty string for a valid ISO date', () => {
    const result = formatDateOnly(ISO_NOON_UTC);
    expect(result.length).toBeGreaterThan(0);
  });

  it('includes the year in the output', () => {
    const result = formatDateOnly(ISO_NOON_UTC);
    expect(result).toContain('2026');
  });

  it('uses browser locale, not a hardcoded de-DE locale', () => {
    // In a Node/en-US environment the short month for June is "Jun"; a de-DE
    // environment would produce "Juni". Asserting the absence of "Juni"
    // confirms no hardcoded German locale.
    const result = formatDateOnly(ISO_NOON_UTC);
    expect(result).not.toContain('Juni');
  });
});
