/**
 * Unit tests for the filter encoder / decoder.
 *
 * REQ-ADM-230: filter params must round-trip cleanly through URL params.
 */

import { describe, it, expect } from 'vitest';
import {
  encodeFilters,
  decodeFilters,
  DEFAULT_FILTERS,
} from './filters';
import type { ClientlogFilters } from './filters';

describe('encodeFilters', () => {
  it('includes slice and limit in all cases', () => {
    const p = encodeFilters(DEFAULT_FILTERS);
    expect(p.get('slice')).toBe('auth');
    expect(p.get('limit')).toBe('50');
  });

  it('omits empty optional fields', () => {
    const p = encodeFilters(DEFAULT_FILTERS);
    expect(p.has('app')).toBe(false);
    expect(p.has('kind')).toBe(false);
    expect(p.has('level')).toBe(false);
    expect(p.has('user')).toBe(false);
    expect(p.has('text')).toBe(false);
    expect(p.has('cursor')).toBe(false);
  });

  it('includes non-empty optional fields', () => {
    const f: ClientlogFilters = {
      ...DEFAULT_FILTERS,
      slice: 'public',
      app: 'suite',
      kind: 'error',
      level: 'warn',
      user: 'user-1',
      route: '/mail/inbox',
      text: 'crash',
    };
    const p = encodeFilters(f);
    expect(p.get('slice')).toBe('public');
    expect(p.get('app')).toBe('suite');
    expect(p.get('kind')).toBe('error');
    expect(p.get('level')).toBe('warn');
    expect(p.get('user')).toBe('user-1');
    expect(p.get('route')).toBe('/mail/inbox');
    expect(p.get('text')).toBe('crash');
  });

  it('includes cursor when provided', () => {
    const p = encodeFilters(DEFAULT_FILTERS, 'cursor-abc');
    expect(p.get('cursor')).toBe('cursor-abc');
  });

  it('accepts a custom limit', () => {
    const p = encodeFilters(DEFAULT_FILTERS, undefined, 100);
    expect(p.get('limit')).toBe('100');
  });

  it('converts datetime-local since to RFC3339', () => {
    const f: ClientlogFilters = { ...DEFAULT_FILTERS, since: '2026-05-01T12:00' };
    const p = encodeFilters(f);
    const since = p.get('since');
    expect(since).not.toBeNull();
    // Should be a valid ISO 8601 date string ending in Z.
    expect(since).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
  });

  it('omits since when the datetime is invalid', () => {
    const f: ClientlogFilters = { ...DEFAULT_FILTERS, since: 'not-a-date' };
    const p = encodeFilters(f);
    expect(p.has('since')).toBe(false);
  });
});

describe('decodeFilters', () => {
  it('returns defaults for empty params', () => {
    const out = decodeFilters(new URLSearchParams());
    expect(out.slice).toBe('auth');
    expect(out.app).toBe('');
    expect(out.kind).toBe('');
    expect(out.level).toBe('');
    expect(out.user).toBe('');
    expect(out.text).toBe('');
  });

  it('parses known enum values', () => {
    const p = new URLSearchParams({
      slice: 'public',
      app: 'admin',
      kind: 'vital',
      level: 'debug',
    });
    const out = decodeFilters(p);
    expect(out.slice).toBe('public');
    expect(out.app).toBe('admin');
    expect(out.kind).toBe('vital');
    expect(out.level).toBe('debug');
  });

  it('ignores unknown enum values and uses defaults', () => {
    const p = new URLSearchParams({ slice: 'unknown', app: 'badapp', kind: 'metric' });
    const out = decodeFilters(p);
    expect(out.slice).toBe('auth'); // falls back to default
    expect(out.app).toBe('');
    expect(out.kind).toBe('');
  });

  it('preserves free-text fields as-is', () => {
    const p = new URLSearchParams({ user: 'u-123', text: 'crash in render', route: '/mail' });
    const out = decodeFilters(p);
    expect(out.user).toBe('u-123');
    expect(out.text).toBe('crash in render');
    expect(out.route).toBe('/mail');
  });
});

describe('round-trip', () => {
  it('encodes and decodes without loss', () => {
    const original: ClientlogFilters = {
      slice: 'public',
      app: 'suite',
      kind: 'log',
      level: 'info',
      since: '',
      until: '',
      user: 'alice',
      session_id: 'sess-1',
      request_id: 'req-1',
      route: '/mail/inbox',
      text: 'test',
    };
    const encoded = encodeFilters(original);
    const decoded = decodeFilters(encoded);
    expect(decoded.slice).toBe(original.slice);
    expect(decoded.app).toBe(original.app);
    expect(decoded.kind).toBe(original.kind);
    expect(decoded.level).toBe(original.level);
    expect(decoded.user).toBe(original.user);
    expect(decoded.route).toBe(original.route);
    expect(decoded.text).toBe(original.text);
  });
});
