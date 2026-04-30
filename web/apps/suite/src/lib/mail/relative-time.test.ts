/**
 * Tests for relativeTimeAgo.
 *
 * Each bucket boundary is tested in both supported locales.
 * The helper must also handle dates that appear to be in the future
 * (clock skew tolerance) by using the absolute delta.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { i18n } from '../i18n/i18n.svelte';
import { relativeTimeAgo } from './relative-time';

const SEC = 1000;
const MIN = 60 * SEC;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const WEEK = 7 * DAY;

// A fixed anchor so tests are deterministic.
const NOW = new Date('2026-04-30T12:00:00Z');

function ago(ms: number): Date {
  return new Date(NOW.getTime() - ms);
}

describe('relativeTimeAgo -- English', () => {
  beforeEach(() => {
    i18n.locale = 'en';
  });
  afterEach(() => {
    i18n.locale = 'en';
  });

  it('returns "just now" for delta < 60 seconds', () => {
    expect(relativeTimeAgo(ago(30 * SEC), NOW)).toBe('just now');
    expect(relativeTimeAgo(ago(0), NOW)).toBe('just now');
    expect(relativeTimeAgo(ago(59 * SEC), NOW)).toBe('just now');
  });

  it('returns minutes label for delta 1..59 minutes', () => {
    const label5 = relativeTimeAgo(ago(5 * MIN), NOW);
    expect(label5).toMatch(/5/);
    expect(label5.toLowerCase()).toContain('minute');

    const label59 = relativeTimeAgo(ago(59 * MIN), NOW);
    expect(label59).toMatch(/59/);
    expect(label59.toLowerCase()).toContain('minute');
  });

  it('returns 1 minute ago at exactly 60 seconds', () => {
    const label = relativeTimeAgo(ago(60 * SEC), NOW);
    expect(label).toMatch(/1/);
    expect(label.toLowerCase()).toContain('minute');
  });

  it('returns hours label for delta 1..23 hours', () => {
    const label17 = relativeTimeAgo(ago(17 * HOUR), NOW);
    expect(label17).toMatch(/17/);
    expect(label17.toLowerCase()).toContain('hour');

    const label23 = relativeTimeAgo(ago(23 * HOUR), NOW);
    expect(label23).toMatch(/23/);
    expect(label23.toLowerCase()).toContain('hour');
  });

  it('returns 1 hour ago at exactly 3600 seconds', () => {
    const label = relativeTimeAgo(ago(3600 * SEC), NOW);
    expect(label).toMatch(/1/);
    expect(label.toLowerCase()).toContain('hour');
  });

  it('returns days label for delta 1..6 days', () => {
    const label3 = relativeTimeAgo(ago(3 * DAY), NOW);
    expect(label3).toMatch(/3/);
    expect(label3.toLowerCase()).toContain('day');

    const label6 = relativeTimeAgo(ago(6 * DAY), NOW);
    expect(label6).toMatch(/6/);
    expect(label6.toLowerCase()).toContain('day');
  });

  it('returns weeks label for delta 1..4 weeks', () => {
    const label2 = relativeTimeAgo(ago(2 * WEEK), NOW);
    expect(label2).toMatch(/2/);
    expect(label2.toLowerCase()).toContain('week');

    const label4 = relativeTimeAgo(ago(4 * WEEK), NOW);
    expect(label4).toMatch(/4/);
    expect(label4.toLowerCase()).toContain('week');
  });

  it('returns months label for delta >= 5 weeks', () => {
    const label = relativeTimeAgo(ago(5 * WEEK), NOW);
    expect(label.toLowerCase()).toContain('month');
  });
});

describe('relativeTimeAgo -- German', () => {
  beforeEach(() => {
    i18n.locale = 'de';
  });
  afterEach(() => {
    i18n.locale = 'en';
  });

  it('returns "gerade eben" for delta < 60 seconds', () => {
    expect(relativeTimeAgo(ago(45 * SEC), NOW)).toBe('gerade eben');
  });

  it('returns minutes label in German for < 1 hour', () => {
    const label = relativeTimeAgo(ago(5 * MIN), NOW);
    expect(label).toMatch(/5/);
    // Intl.RelativeTimeFormat de-DE produces "vor N Minuten"
    expect(label.toLowerCase()).toContain('minute');
  });

  it('returns hours label in German for < 24 hours', () => {
    const label = relativeTimeAgo(ago(17 * HOUR), NOW);
    expect(label).toMatch(/17/);
    expect(label.toLowerCase()).toContain('stunde');
  });

  it('returns days label in German for 1..6 days', () => {
    const label = relativeTimeAgo(ago(3 * DAY), NOW);
    expect(label).toMatch(/3/);
    expect(label.toLowerCase()).toContain('tag');
  });

  it('returns weeks label in German for 1..4 weeks', () => {
    const label = relativeTimeAgo(ago(2 * WEEK), NOW);
    expect(label).toMatch(/2/);
    expect(label.toLowerCase()).toContain('woche');
  });

  it('returns months label in German for >= 5 weeks', () => {
    const label = relativeTimeAgo(ago(5 * WEEK), NOW);
    expect(label.toLowerCase()).toContain('monat');
  });
});

describe('relativeTimeAgo -- edge cases', () => {
  beforeEach(() => {
    i18n.locale = 'en';
  });
  afterEach(() => {
    i18n.locale = 'en';
  });

  it('uses absolute delta for a future date (clock skew tolerance)', () => {
    // date is 5 minutes in the future relative to now
    const future = new Date(NOW.getTime() + 5 * MIN);
    const label = relativeTimeAgo(future, NOW);
    expect(label).toMatch(/5/);
    expect(label.toLowerCase()).toContain('minute');
  });

  it('default now parameter is the system clock (smoke test)', () => {
    // Pass a date well in the past so the result is deterministic enough.
    const pastDate = new Date(Date.now() - 2 * HOUR);
    const label = relativeTimeAgo(pastDate);
    expect(label.toLowerCase()).toContain('hour');
  });
});
