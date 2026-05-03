/**
 * Tests for the recipient hover-card resolver (REQ-MAIL-46f).
 *
 * Coverage:
 *   - peekPerson returns null on cache miss
 *   - peekPerson returns the cached payload synchronously
 *   - resolvePerson writes contact / principal flags into the cache
 *   - mergePhones de-duplicates by normalised number
 *   - pickName falls back through contact → principal → captured → local-part
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// ── localStorage stub ─────────────────────────────────────────────────────

const lsStore: Record<string, string> = {};

vi.stubGlobal('localStorage', {
  getItem: (k: string) => lsStore[k] ?? null,
  setItem: (k: string, v: string) => { lsStore[k] = v; },
  removeItem: (k: string) => { delete lsStore[k]; },
  clear: () => { Object.keys(lsStore).forEach((k) => delete lsStore[k]); },
});

// ── Mocks: avatar resolver and JMAP client ────────────────────────────────

vi.mock('./avatar-resolver.svelte', async () => {
  // Use a real cache map shared across the test so writeCacheEntryFields +
  // readCacheEntry behave like the production module while keeping the
  // network-touching `resolve` mocked out.
  const cache = new Map<string, {
    url: string | null;
    ts: number;
    name?: string | null;
    phones?: { type: string; number: string }[];
    contactId?: string | null;
    principalId?: string | null;
  }>();
  return {
    resolve: vi.fn(async (email: string) => {
      const e = cache.get(email.toLowerCase().trim());
      return e?.url ?? null;
    }),
    readCacheEntry: (email: string) => {
      const e = cache.get(email.toLowerCase().trim());
      return e ?? null;
    },
    writeCacheEntryFields: (
      email: string,
      fields: Record<string, unknown>,
    ) => {
      const k = email.toLowerCase().trim();
      const prev = cache.get(k) ?? { url: null, ts: 0 };
      cache.set(k, { ...prev, ...fields, ts: Date.now() });
    },
    _testCache: cache,
  };
});

vi.mock('../jmap/client', () => ({
  jmap: {
    batch: vi.fn(async () => ({ responses: [] })),
    hasCapability: vi.fn(() => false),
    downloadUrl: vi.fn(() => null),
  },
  strict: vi.fn(),
}));

vi.mock('../jmap/types', () => ({
  Capability: {
    Core: 'urn:ietf:params:jmap:core',
    Mail: 'urn:ietf:params:jmap:mail',
    Contacts: 'urn:ietf:params:jmap:contacts',
    Calendars: 'urn:ietf:params:jmap:calendars',
    HeroldChat: 'https://netzhansa.com/jmap/chat',
  },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: { session: null },
}));

vi.mock('../contacts/store.svelte', () => ({
  contacts: {
    status: 'ready',
    suggestions: [
      { id: 'c1', name: 'Jane Doe', email: 'jane@example.com' },
    ],
    load: vi.fn(),
  },
}));

// ── tests ─────────────────────────────────────────────────────────────────

describe('person-resolver', () => {
  beforeEach(() => {
    Object.keys(lsStore).forEach((k) => delete lsStore[k]);
    vi.clearAllMocks();
  });

  it('peekPerson returns null on cache miss', async () => {
    const { peekPerson } = await import('./person-resolver.svelte');
    expect(peekPerson('nobody@example.com')).toBeNull();
  });

  it('peekPerson returns the cached entry when available', async () => {
    const mod = await import('./avatar-resolver.svelte') as unknown as {
      writeCacheEntryFields: (e: string, f: Record<string, unknown>) => void;
    };
    mod.writeCacheEntryFields('jane@example.com', {
      url: 'https://cdn.example.com/jane.jpg',
      name: 'Jane Doe',
      phones: [{ type: 'mobile', number: '+1 555 0100' }],
      contactId: 'c1',
      principalId: null,
    });
    const { peekPerson } = await import('./person-resolver.svelte');
    const p = peekPerson('Jane@Example.com');
    expect(p).not.toBeNull();
    expect(p?.email).toBe('jane@example.com');
    expect(p?.displayName).toBe('Jane Doe');
    expect(p?.contactId).toBe('c1');
    expect(p?.phones).toHaveLength(1);
  });

  it('resolvePerson populates contactId from the contacts store', async () => {
    const { resolvePerson } = await import('./person-resolver.svelte');
    const r = await resolvePerson('Jane@example.com', [], 'Jane Doe');
    expect(r.email).toBe('jane@example.com');
    expect(r.contactId).toBe('c1');
    expect(r.displayName).toBe('Jane Doe');
  });

  it('resolvePerson falls back to local-part when no contact / principal / name', async () => {
    const { resolvePerson } = await import('./person-resolver.svelte');
    const r = await resolvePerson('stranger@example.com', [], null);
    expect(r.email).toBe('stranger@example.com');
    expect(r.displayName).toBe('stranger');
    expect(r.contactId).toBeNull();
    expect(r.principalId).toBeNull();
  });

  it('mergePhones drops duplicates ignoring whitespace', async () => {
    const { _internals_forTest } = await import('./person-resolver.svelte');
    const merged = _internals_forTest.mergePhones(
      [{ type: 'work', number: '+1 555 0100' }],
      [
        { type: 'mobile', number: '+15550100' },
        { type: 'home', number: '+1 555 0200' },
      ],
    );
    expect(merged).toHaveLength(2);
    expect(merged[0]?.type).toBe('work');
    expect(merged[1]?.number).toBe('+1 555 0200');
  });

  it('pickName prefers earlier candidates', async () => {
    const { _internals_forTest } = await import('./person-resolver.svelte');
    expect(_internals_forTest.pickName('Jane', null, 'jane@example.com')).toBe('Jane');
    expect(_internals_forTest.pickName(null, undefined, 'jane@example.com')).toBe('jane');
    expect(_internals_forTest.pickName('  ', null, 'jane@example.com')).toBe('jane');
  });
});
