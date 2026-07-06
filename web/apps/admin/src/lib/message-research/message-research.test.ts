/**
 * Unit tests for the message-research module helpers.
 *
 * The state class itself is covered by e2e tests (tests/e2e/message-research.spec.ts).
 * Here we test the pure toRFC3339 helper which the state class uses to convert
 * datetime-local input values to RFC3339 before sending them to the server.
 */

import { describe, it, expect } from 'vitest';
import { toRFC3339 } from './message-research.svelte';

describe('toRFC3339', () => {
  it('converts datetime-local input (YYYY-MM-DDTHH:MM) to RFC3339', () => {
    expect(toRFC3339('2024-06-01T12:30')).toBe('2024-06-01T12:30:00Z');
  });

  it('converts date-only input to midnight UTC RFC3339', () => {
    expect(toRFC3339('2024-06-01')).toBe('2024-06-01T00:00:00Z');
  });

  it('handles a datetime-local value with seconds already present', () => {
    // "2024-06-01T12:30" -> appends ":00Z" -> "2024-06-01T12:30:00Z"
    const result = toRFC3339('2024-06-01T12:30');
    expect(result).not.toBeNull();
    expect(result).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
  });

  it('returns null for an unparseable string', () => {
    expect(toRFC3339('not-a-date')).toBeNull();
  });

  it('returns null for an empty string', () => {
    expect(toRFC3339('')).toBeNull();
  });

  it('returns null for a string that is not a valid ISO fragment', () => {
    expect(toRFC3339('99999-99-99')).toBeNull();
  });

  it('produces a Z-terminated string (UTC, no fractional seconds)', () => {
    const result = toRFC3339('2026-01-15T08:00');
    expect(result).toBe('2026-01-15T08:00:00Z');
    expect(result?.endsWith('Z')).toBe(true);
    expect(result?.includes('.')).toBe(false);
  });

  it('handles the earliest valid input date', () => {
    const result = toRFC3339('2000-01-01');
    expect(result).toBe('2000-01-01T00:00:00Z');
  });
});
