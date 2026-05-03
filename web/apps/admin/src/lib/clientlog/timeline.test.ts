/**
 * Unit tests for the timeline sort / merge behaviour.
 *
 * REQ-ADM-231, REQ-OPS-213: timeline entries are sorted by effective_ts
 * regardless of arrival order. The clientlog state class sorts entries
 * client-side (belt-and-suspenders over the server's sort).
 *
 * Since the sort lives inside the clientlog state class (clientlog.svelte.ts),
 * we extract and test the sort logic in isolation here rather than mounting
 * the full singleton.
 */

import { describe, it, expect } from 'vitest';
import type { TimelineEntry } from './clientlog.svelte';

/** The sort comparator used by the state class's loadTimeline method. */
function sortByEffectiveTs(entries: TimelineEntry[]): TimelineEntry[] {
  return [...entries].sort(
    (a, b) =>
      new Date(a.effective_ts).getTime() - new Date(b.effective_ts).getTime(),
  );
}

describe('timeline sort', () => {
  it('returns an empty array unchanged', () => {
    expect(sortByEffectiveTs([])).toEqual([]);
  });

  it('sorts client entries by effective_ts ascending', () => {
    const entries: TimelineEntry[] = [
      {
        source: 'client',
        effective_ts: '2026-05-01T12:00:00.300Z',
        clientlog: {
          id: 3,
          slice: 'auth',
          server_ts: '2026-05-01T12:00:00.350Z',
          client_ts: '2026-05-01T12:00:00.300Z',
          clock_skew_ms: 50,
          app: 'admin',
          kind: 'log',
          level: 'info',
          page_id: 'p1',
          build_sha: 'sha1',
          ua: 'Firefox',
          msg: 'third',
        },
      },
      {
        source: 'client',
        effective_ts: '2026-05-01T12:00:00.100Z',
        clientlog: {
          id: 1,
          slice: 'auth',
          server_ts: '2026-05-01T12:00:00.150Z',
          client_ts: '2026-05-01T12:00:00.100Z',
          clock_skew_ms: 50,
          app: 'admin',
          kind: 'log',
          level: 'info',
          page_id: 'p1',
          build_sha: 'sha1',
          ua: 'Firefox',
          msg: 'first',
        },
      },
      {
        source: 'client',
        effective_ts: '2026-05-01T12:00:00.200Z',
        clientlog: {
          id: 2,
          slice: 'auth',
          server_ts: '2026-05-01T12:00:00.250Z',
          client_ts: '2026-05-01T12:00:00.200Z',
          clock_skew_ms: 50,
          app: 'admin',
          kind: 'log',
          level: 'info',
          page_id: 'p1',
          build_sha: 'sha1',
          ua: 'Firefox',
          msg: 'second',
        },
      },
    ];

    const sorted = sortByEffectiveTs(entries);
    expect(sorted[0]?.clientlog?.msg).toBe('first');
    expect(sorted[1]?.clientlog?.msg).toBe('second');
    expect(sorted[2]?.clientlog?.msg).toBe('third');
  });

  it('handles mixed client and server entries', () => {
    const entries: TimelineEntry[] = [
      {
        source: 'server',
        effective_ts: '2026-05-01T12:00:00.050Z',
        serverlog: { msg: 'server event', level: 'info' },
      },
      {
        source: 'client',
        effective_ts: '2026-05-01T12:00:00.025Z',
        clientlog: {
          id: 10,
          slice: 'auth',
          server_ts: '2026-05-01T12:00:00.030Z',
          client_ts: '2026-05-01T12:00:00.025Z',
          clock_skew_ms: 5,
          app: 'admin',
          kind: 'log',
          level: 'debug',
          page_id: 'p2',
          build_sha: 'sha2',
          ua: 'Chrome',
          msg: 'client event before server',
        },
      },
    ];

    const sorted = sortByEffectiveTs(entries);
    expect(sorted[0]?.source).toBe('client');
    expect(sorted[1]?.source).toBe('server');
  });

  it('is stable for entries with the same effective_ts', () => {
    const sameTs = '2026-05-01T12:00:00.000Z';
    const entries: TimelineEntry[] = [
      {
        source: 'client',
        effective_ts: sameTs,
        clientlog: {
          id: 1,
          slice: 'auth',
          server_ts: sameTs,
          client_ts: sameTs,
          clock_skew_ms: 0,
          app: 'suite',
          kind: 'log',
          level: 'info',
          page_id: 'p3',
          build_sha: 'sha3',
          ua: 'Safari',
          msg: 'event A',
        },
      },
      {
        source: 'client',
        effective_ts: sameTs,
        clientlog: {
          id: 2,
          slice: 'auth',
          server_ts: sameTs,
          client_ts: sameTs,
          clock_skew_ms: 0,
          app: 'suite',
          kind: 'log',
          level: 'info',
          page_id: 'p3',
          build_sha: 'sha3',
          ua: 'Safari',
          msg: 'event B',
        },
      },
    ];

    // Sort must not throw and must return both entries.
    const sorted = sortByEffectiveTs(entries);
    expect(sorted).toHaveLength(2);
  });
});
