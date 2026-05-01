/**
 * Tests for the payload allowlist (REQ-CLOG-10).
 *
 * The allowlist is enforced at two levels:
 *   1. Breadcrumbs carry only: ts, kind, route?, method?, url_path?, status?, msg?
 *   2. url_path never contains a query string.
 *
 * These tests assert that the schema types and the flush layer enforce this
 * -- no message bodies, contact data, search queries, URL query strings, etc.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { installFakeClock, installFakeFetch, installFakeBeacon, installFakeUuid } from './fakes.js';
import type { FakeClock, FakeFetch, FakeBeacon, FakeUuid } from './fakes.js';
import { install } from '../index.js';
import { _resetForTest } from '../breadcrumbs.js';
import type { FullEvent, Breadcrumb } from '../schema.js';

let clock: FakeClock;
let fakeFetch: FakeFetch;
let fakeBeacon: FakeBeacon;
let fakeUuid: FakeUuid;

beforeEach(() => {
  clock = installFakeClock();
  fakeFetch = installFakeFetch();
  fakeBeacon = installFakeBeacon();
  fakeUuid = installFakeUuid();
  _resetForTest();
});

afterEach(() => {
  clock.uninstall();
  fakeFetch.uninstall();
  fakeBeacon.uninstall();
  fakeUuid.uninstall();
});

describe('breadcrumb field allowlist', () => {
  it('fetch breadcrumbs carry only the allowed fields', async () => {
    const instance = install({
      app: 'suite',
      buildSha: 'sha',
      endpoints: { authenticated: '/api/v1/clientlog', anonymous: '/api/v1/clientlog/public' },
      isAuthenticated: () => true,
      livetailUntil: () => null,
      telemetryEnabled: () => true,
      bootstrap: { enabled: true, batch_max_events: 100, batch_max_age_ms: 5000, queue_cap: 200, telemetry_enabled_default: true },
    });

    // Record a fetch breadcrumb (via recordFetch helper, not inline -- the
    // important thing is what appears in the wire payload for error events).
    const { recordFetch } = await import('../breadcrumbs.js');
    recordFetch('GET', '/jmap', 200);

    // Trigger an error so the breadcrumb snapshot is attached.
    console.error('test error for breadcrumb check');

    clock.advance(5000);
    await Promise.resolve();

    // Find the error event.
    const call = fakeFetch.calls.find((c) => c.url === '/api/v1/clientlog');
    expect(call).toBeDefined();
    const body = JSON.parse(call!.body) as { events: FullEvent[] };
    const errEvent = body.events.find((e) => e.kind === 'error');
    expect(errEvent).toBeDefined();

    if (errEvent?.breadcrumbs) {
      for (const crumb of errEvent.breadcrumbs) {
        // Only allowed fields per REQ-OPS-202 breadcrumb shape plus kind-specific extras.
        // console breadcrumbs add 'level' (explicitly in the union type in architecture).
        const keys = Object.keys(crumb);
        const allowed = new Set(['ts', 'kind', 'route', 'method', 'url_path', 'status', 'msg', 'level']);
        for (const k of keys) {
          expect(allowed.has(k), `unexpected breadcrumb field: ${k}`).toBe(true);
        }
        // url_path must not contain a query string
        if ('url_path' in crumb) {
          const fc = crumb as Extract<Breadcrumb, { kind: 'fetch' }>;
          expect(fc.url_path).not.toContain('?');
          expect(fc.url_path).not.toContain('#');
        }
      }
    }

    instance.shutdown();
  });
});

describe('narrow schema on anonymous endpoint', () => {
  it('does not include session_id, breadcrumbs, or request_id in anon events', async () => {
    const instance = install({
      app: 'suite',
      buildSha: 'sha',
      endpoints: { authenticated: '/api/v1/clientlog', anonymous: '/api/v1/clientlog/public' },
      isAuthenticated: () => false, // anon
      livetailUntil: () => null,
      telemetryEnabled: () => true,
      bootstrap: { enabled: true, batch_max_events: 100, batch_max_age_ms: 5000, queue_cap: 200, telemetry_enabled_default: true },
    });

    console.error('anon error');
    clock.advance(5000);
    await Promise.resolve();

    const call = fakeFetch.calls.find((c) => c.url === '/api/v1/clientlog/public');
    expect(call).toBeDefined();
    const body = JSON.parse(call!.body) as { events: Record<string, unknown>[] };
    const ev = body.events.find((e) => e['kind'] === 'error');
    expect(ev).toBeDefined();
    expect(ev!['session_id']).toBeUndefined();
    expect(ev!['breadcrumbs']).toBeUndefined();
    expect(ev!['request_id']).toBeUndefined();

    instance.shutdown();
  });
});
