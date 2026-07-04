/**
 * Unit tests for the device-local IndexedDB debug ring (issue #83).
 *
 * Coverage:
 *   1. appendEvent adds a record readable via readAll
 *   2. Records are returned in insertion order (oldest first)
 *   3. Cap enforcement: at MAX_RECORDS the oldest record is pruned
 *   4. After overflow only MAX_RECORDS records remain
 *   5. setEnabled / getEnabled round-trip
 *   6. setEnabled(false) after setEnabled(true) reads back false
 *   7. clear() removes all events but leaves the meta store intact
 *   8. formatRecord produces the expected plain-text line
 *   9. formatRecord omits payload field when absent
 *  10. appendEvent with a non-string payload serialises it to JSON
 *
 * fake-indexeddb replaces the global indexedDB with a spec-compliant
 * in-memory implementation so IDB transactions behave correctly in the
 * Node.js/happy-dom environment.  A fresh IDBFactory is installed before
 * each test via vi.stubGlobal so every test starts from an empty DB.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import {
  appendEvent,
  readAll,
  clear,
  getEnabled,
  setEnabled,
  formatRecord,
  MAX_RECORDS,
  type DebugRecord,
} from './debug-ring';

// Install a fresh fake-indexeddb instance before each test.
// debug-ring.ts opens the DB via the global `indexedDB`; patching the global
// here means every call to openDb() within the same test uses the same
// fresh in-memory DB.
beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory());
});

// ── Ring append + readAll ──────────────────────────────────────────────────

describe('appendEvent + readAll', () => {
  it('returns an empty array when the ring is empty', async () => {
    const records = await readAll();
    expect(records).toHaveLength(0);
  });

  it('stores a record and returns it via readAll', async () => {
    await appendEvent('sw', 'info', 'sw.push', { kind: 'mail' });
    const records = await readAll();
    expect(records).toHaveLength(1);
    const r = records[0]!;
    expect(r.ctx).toBe('sw');
    expect(r.level).toBe('info');
    expect(r.msg).toBe('sw.push');
  });

  it('returns records in insertion order (oldest first)', async () => {
    await appendEvent('sw', 'info', 'first', {});
    await appendEvent('page', 'debug', 'second', {});
    await appendEvent('sw', 'warn', 'third', {});
    const records = await readAll();
    expect(records).toHaveLength(3);
    expect(records[0]!.msg).toBe('first');
    expect(records[1]!.msg).toBe('second');
    expect(records[2]!.msg).toBe('third');
  });

  it('stores a ts field that is a valid ISO string', async () => {
    const before = new Date().toISOString();
    await appendEvent('sw', 'info', 'ts-test');
    const after = new Date().toISOString();
    const records = await readAll();
    const ts = records[0]!.ts;
    expect(ts >= before).toBe(true);
    expect(ts <= after).toBe(true);
  });

  it('stores a string payload verbatim', async () => {
    await appendEvent('sw', 'info', 'msg', 'raw-string');
    const records = await readAll();
    expect(records[0]!.payload).toBe('raw-string');
  });

  it('serialises an object payload to JSON', async () => {
    await appendEvent('sw', 'info', 'msg', { count: 3, path: '/#/mail' });
    const records = await readAll();
    expect(records[0]!.payload).toBe('{"count":3,"path":"/#/mail"}');
  });

  it('omits the payload field when none is supplied', async () => {
    await appendEvent('sw', 'info', 'msg');
    const records = await readAll();
    expect('payload' in records[0]!).toBe(false);
  });
});

// ── Cap enforcement ────────────────────────────────────────────────────────

describe('ring cap enforcement', () => {
  it('prunes the oldest record when count reaches MAX_RECORDS', async () => {
    // Fill the ring to capacity.
    for (let i = 0; i < MAX_RECORDS; i++) {
      await appendEvent('sw', 'info', `entry-${i}`);
    }
    let records = await readAll();
    expect(records).toHaveLength(MAX_RECORDS);
    expect(records[0]!.msg).toBe('entry-0');

    // One more write should evict entry-0.
    await appendEvent('sw', 'info', 'entry-overflow');
    records = await readAll();

    expect(records).toHaveLength(MAX_RECORDS);
    expect(records[0]!.msg).toBe('entry-1');
    expect(records[records.length - 1]!.msg).toBe('entry-overflow');
  });

  it('keeps exactly MAX_RECORDS after repeated overflow', async () => {
    for (let i = 0; i < MAX_RECORDS + 10; i++) {
      await appendEvent('sw', 'info', `e${i}`);
    }
    const records = await readAll();
    expect(records).toHaveLength(MAX_RECORDS);
  });
});

// ── enabled flag ───────────────────────────────────────────────────────────

describe('setEnabled / getEnabled', () => {
  it('returns false by default (fresh DB)', async () => {
    expect(await getEnabled()).toBe(false);
  });

  it('persists true and reads it back', async () => {
    await setEnabled(true);
    expect(await getEnabled()).toBe(true);
  });

  it('persists false after true and reads it back', async () => {
    await setEnabled(true);
    await setEnabled(false);
    expect(await getEnabled()).toBe(false);
  });
});

// ── clear ──────────────────────────────────────────────────────────────────

describe('clear', () => {
  it('removes all events from the ring', async () => {
    await appendEvent('sw', 'info', 'a');
    await appendEvent('page', 'debug', 'b');
    await clear();
    expect(await readAll()).toHaveLength(0);
  });

  it('does not affect the enabled flag', async () => {
    await setEnabled(true);
    await appendEvent('sw', 'info', 'x');
    await clear();
    expect(await getEnabled()).toBe(true);
    expect(await readAll()).toHaveLength(0);
  });
});

// ── formatRecord ───────────────────────────────────────────────────────────

describe('formatRecord', () => {
  it('formats a record without payload as ts  ctx  level  msg', () => {
    const r: DebugRecord = {
      ts: '2026-07-04T12:00:00.000Z',
      ctx: 'sw',
      level: 'info',
      msg: 'sw.openApp.focus.ok',
    };
    const line = formatRecord(r);
    expect(line).toBe('2026-07-04T12:00:00.000Z  sw  info   sw.openApp.focus.ok');
  });

  it('appends payload when present', () => {
    const r: DebugRecord = {
      ts: '2026-07-04T12:00:00.000Z',
      ctx: 'sw',
      level: 'warn',
      msg: 'sw.openApp.focus.threw',
      payload: '{"name":"InvalidAccessError"}',
    };
    const line = formatRecord(r);
    expect(line).toContain('{"name":"InvalidAccessError"}');
  });

  it('pads the level field to 5 characters', () => {
    const r: DebugRecord = {
      ts: '2026-07-04T12:00:00.000Z',
      ctx: 'page',
      level: 'debug',
      msg: 'page.event',
    };
    const line = formatRecord(r);
    // "debug" is exactly 5 chars; padEnd(5) leaves it unchanged.
    expect(line).toContain('  debug  ');
  });

  it('omits payload field when absent', () => {
    const r: DebugRecord = {
      ts: '2026-07-04T12:00:00.000Z',
      ctx: 'sw',
      level: 'info',
      msg: 'sw.push',
    };
    const parts = formatRecord(r).split('  ');
    // 4 parts: ts, ctx, level, msg.
    expect(parts).toHaveLength(4);
  });

  it('produces exactly 5 parts when payload is present', () => {
    const r: DebugRecord = {
      ts: '2026-07-04T12:00:00.000Z',
      ctx: 'sw',
      level: 'info',
      msg: 'sw.push',
      payload: '{"kind":"mail"}',
    };
    const parts = formatRecord(r).split('  ');
    expect(parts).toHaveLength(5);
    expect(parts[4]).toBe('{"kind":"mail"}');
  });
});
