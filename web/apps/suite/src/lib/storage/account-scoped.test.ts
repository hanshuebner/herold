/**
 * Tests for the account-scoped localStorage helper.
 *
 * Coverage:
 *   1. accountKey uses 'anon' namespace pre-auth (session null)
 *   2. accountKey uses the logged-in username when a session is active
 *   3. readAccountJson returns fallback when no item exists
 *   4. readAccountJson reads from the current account's namespace
 *   5. writeAccountJson writes to the current account's namespace
 *   6. removeAccountItem removes from the current account's namespace
 *   7. No cross-account reads: account A's data is not visible to account B
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { accountKey, readAccountJson, writeAccountJson, removeAccountItem } from './account-scoped';

// ── Auth mock ─────────────────────────────────────────────────────────────

let mockSession: { username: string } | null = null;

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    get session() {
      return mockSession;
    },
  },
  registerAccountResetCallback: vi.fn(),
}));

// ── localStorage mock ────────────────────────────────────────────────────

const localStorageData: Record<string, string> = {};
const localStorageMock = {
  getItem: vi.fn((key: string) => localStorageData[key] ?? null),
  setItem: vi.fn((key: string, value: string) => {
    localStorageData[key] = value;
  }),
  removeItem: vi.fn((key: string) => {
    delete localStorageData[key];
  }),
};
vi.stubGlobal('localStorage', localStorageMock);

beforeEach(() => {
  mockSession = null;
  Object.keys(localStorageData).forEach((k) => delete localStorageData[k]);
  localStorageMock.getItem.mockClear();
  localStorageMock.setItem.mockClear();
  localStorageMock.removeItem.mockClear();
});

// ─────────────────────────────────────────────────────────────────────────

describe('accountKey', () => {
  it('uses anon namespace pre-auth', () => {
    mockSession = null;
    expect(accountKey('foo.bar')).toBe('herold.suite.anon.foo.bar');
  });

  it('uses the logged-in username when a session is set', () => {
    mockSession = { username: 'alice@example.com' };
    expect(accountKey('foo.bar')).toBe('herold.suite.alice@example.com.foo.bar');
  });

  it('produces distinct keys for different accounts', () => {
    mockSession = { username: 'alice@example.com' };
    const aliceKey = accountKey('search.history');
    mockSession = { username: 'bob@example.com' };
    const bobKey = accountKey('search.history');
    expect(aliceKey).not.toBe(bobKey);
  });
});

describe('readAccountJson', () => {
  it('returns fallback when item does not exist', () => {
    mockSession = { username: 'alice@example.com' };
    const result = readAccountJson<string[]>('mail.search.history', []);
    expect(result).toEqual([]);
  });

  it('reads from the current account namespace', () => {
    mockSession = { username: 'alice@example.com' };
    localStorageData['herold.suite.alice@example.com.mail.search.history'] = JSON.stringify([
      'query1',
    ]);
    const result = readAccountJson<string[]>('mail.search.history', []);
    expect(result).toEqual(['query1']);
  });

  it('does not read another account namespace', () => {
    // Alice's history is stored under her key.
    localStorageData['herold.suite.alice@example.com.mail.search.history'] = JSON.stringify([
      'alice-query',
    ]);
    // Bob is logged in.
    mockSession = { username: 'bob@example.com' };
    const result = readAccountJson<string[]>('mail.search.history', []);
    // Bob must not see Alice's history.
    expect(result).toEqual([]);
  });

  it('returns fallback on JSON parse error', () => {
    mockSession = { username: 'alice@example.com' };
    localStorageData['herold.suite.alice@example.com.bad'] = 'not-json{{{';
    const result = readAccountJson<number>('bad', 42);
    expect(result).toBe(42);
  });
});

describe('writeAccountJson', () => {
  it('writes to the current account namespace', () => {
    mockSession = { username: 'alice@example.com' };
    writeAccountJson('mail.search.history', ['q1', 'q2']);
    expect(
      localStorageData['herold.suite.alice@example.com.mail.search.history'],
    ).toBe(JSON.stringify(['q1', 'q2']));
  });

  it('does not write to another account namespace', () => {
    mockSession = { username: 'alice@example.com' };
    writeAccountJson('x', 1);
    mockSession = { username: 'bob@example.com' };
    writeAccountJson('x', 2);
    expect(localStorageData['herold.suite.alice@example.com.x']).toBe('1');
    expect(localStorageData['herold.suite.bob@example.com.x']).toBe('2');
  });
});

describe('removeAccountItem', () => {
  it('removes from the current account namespace', () => {
    mockSession = { username: 'alice@example.com' };
    localStorageData['herold.suite.alice@example.com.mail.search.history'] = '["q"]';
    removeAccountItem('mail.search.history');
    expect(
      localStorageData['herold.suite.alice@example.com.mail.search.history'],
    ).toBeUndefined();
  });

  it('does not remove another account entry', () => {
    localStorageData['herold.suite.alice@example.com.x'] = '"alice"';
    localStorageData['herold.suite.bob@example.com.x'] = '"bob"';
    mockSession = { username: 'alice@example.com' };
    removeAccountItem('x');
    expect(localStorageData['herold.suite.alice@example.com.x']).toBeUndefined();
    // Bob's key untouched.
    expect(localStorageData['herold.suite.bob@example.com.x']).toBe('"bob"');
  });
});
