import { describe, it, expect } from 'vitest';
import { formatRowDate, formatRowDateTime } from './row-date';

// Local-time constructors so the cases hold in any runner timezone.
const now = new Date(2026, 9, 23, 13, 42, 0);
const iso = (y: number, mo: number, d: number, h: number, mi: number) =>
  new Date(y, mo, d, h, mi, 0).toISOString();

describe('formatRowDate (re #529)', () => {
  it('shows the time for a message received earlier today', () => {
    expect(formatRowDate(iso(2026, 9, 23, 0, 10), 'en-GB', now)).toBe('00:10');
    expect(formatRowDate(iso(2026, 9, 23, 13, 41), 'en-GB', now)).toBe('13:41');
  });
  it('keeps the date for 23:30 the previous local day', () => {
    expect(formatRowDate(iso(2026, 9, 22, 23, 30), 'en-GB', now)).toBe('22 Oct');
  });
  it('treats just-after-midnight correctly with a fixed clock at 00:05', () => {
    const early = new Date(2026, 9, 23, 0, 5, 0);
    expect(formatRowDate(iso(2026, 9, 23, 0, 1), 'en-GB', early)).toBe('00:01');
    expect(formatRowDate(iso(2026, 9, 22, 23, 59), 'en-GB', early)).toBe('22 Oct');
  });
  it('same month/day in an earlier year is not today and carries the year', () => {
    expect(formatRowDate(iso(2025, 9, 23, 13, 0), 'en-GB', now)).toBe('23 Oct 2025');
  });
  it('honours the locale for the time', () => {
    expect(formatRowDate(iso(2026, 9, 23, 13, 5), 'en-US', now)).toMatch(/1:05\sPM/);
  });
});

describe('formatRowDateTime reading-pane header (re #529)', () => {
  it('carries the date and the time for a message received today', () => {
    const out = formatRowDateTime(iso(2026, 9, 23, 7, 28), 'en-GB');
    expect(out).toContain('23');
    expect(out).toContain('Oct');
    expect(out).toContain('2026');
    expect(out).toContain('07:28');
  });
  it('carries the year for an earlier-year message', () => {
    expect(formatRowDateTime(iso(2025, 0, 5, 9, 0), 'en-GB')).toContain('2025');
  });
});
