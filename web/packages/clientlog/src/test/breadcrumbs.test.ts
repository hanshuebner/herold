/**
 * Tests for the breadcrumb ring buffer (REQ-CLOG-21).
 *
 * Covers:
 *   - Ring wraps at 32 entries (oldest evicted).
 *   - snapshot() returns ordered copy.
 *   - url_path allowlist: no query strings (tested via the flush.urlPathOnly helper).
 */

import { describe, it, expect, beforeEach } from 'vitest';
import {
  recordRoute,
  recordFetch,
  recordConsole,
  _resetForTest,
  snapshot,
} from '../breadcrumbs.js';
import { urlPathOnly } from '../flush.js';

beforeEach(() => {
  _resetForTest();
});

describe('breadcrumb ring buffer', () => {
  it('snapshot is empty on fresh state', () => {
    expect(snapshot()).toEqual([]);
  });

  it('records a route breadcrumb', () => {
    recordRoute('/mail/inbox');
    const crumbs = snapshot();
    expect(crumbs).toHaveLength(1);
    expect(crumbs[0]).toMatchObject({ kind: 'route', route: '/mail/inbox' });
    expect(typeof crumbs[0]!.ts).toBe('string');
  });

  it('records a fetch breadcrumb', () => {
    recordFetch('POST', '/jmap', 200);
    const crumbs = snapshot();
    expect(crumbs[0]).toMatchObject({
      kind: 'fetch',
      method: 'POST',
      url_path: '/jmap',
      status: 200,
    });
  });

  it('records a console breadcrumb', () => {
    recordConsole('warn', 'something went wrong');
    const crumbs = snapshot();
    expect(crumbs[0]).toMatchObject({ kind: 'console', level: 'warn', msg: 'something went wrong' });
  });

  it('holds up to 32 entries', () => {
    for (let i = 0; i < 32; i++) recordRoute(`/route-${i}`);
    expect(snapshot()).toHaveLength(32);
  });

  it('evicts oldest entry when ring wraps past 32', () => {
    for (let i = 0; i < 33; i++) recordRoute(`/route-${i}`);
    const crumbs = snapshot();
    expect(crumbs).toHaveLength(32);
    // Oldest is /route-1 (route-0 was evicted)
    expect(crumbs[0]).toMatchObject({ kind: 'route', route: '/route-1' });
    expect(crumbs[31]).toMatchObject({ kind: 'route', route: '/route-32' });
  });

  it('returns a fresh copy on each snapshot call', () => {
    recordRoute('/a');
    const snap1 = snapshot();
    recordRoute('/b');
    const snap2 = snapshot();
    expect(snap1).toHaveLength(1);
    expect(snap2).toHaveLength(2);
  });
});

describe('urlPathOnly -- REQ-CLOG-10 query stripping', () => {
  it('strips query string from absolute URL', () => {
    expect(urlPathOnly('https://example.com/path?foo=bar')).toBe('/path');
  });

  it('strips query string from relative URL', () => {
    expect(urlPathOnly('/api/jmap?method=get')).toBe('/api/jmap');
  });

  it('strips fragment from URL', () => {
    expect(urlPathOnly('/page#section')).toBe('/page');
  });

  it('strips both query and fragment', () => {
    expect(urlPathOnly('/path?q=1#frag')).toBe('/path');
  });

  it('returns path unchanged when no query or fragment', () => {
    expect(urlPathOnly('/api/v1/clientlog')).toBe('/api/v1/clientlog');
  });
});
