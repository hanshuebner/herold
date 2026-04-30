/**
 * Tests for the tiered avatar resolver (avatar-resolver.svelte.ts).
 *
 * Coverage:
 *   1. Tier order: own identity wins over Face header.
 *   2. Tier order: own identity wins over Gravatar.
 *   3. Tier order within tier 2: Face header wins over Gravatar.
 *   4. Gravatar used when Face absent and toggle on.
 *   5. null returned when Gravatar 404s.
 *   6. Cache keyed by lowercased, trimmed email (coalesces duplicates).
 *   7. Cache survives a JSON round-trip.
 *   8. Toggle off: no Gravatar URL returned.
 *   9. Toggle off: no Face URL returned.
 *  10. _invalidateCacheEntry removes only the target key.
 *  11. clearAvatarCache empties memory cache.
 *
 * Uses vi.mock to stub the network-facing helpers so no real requests
 * are issued.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Identity } from './types';

// ── localStorage stub ─────────────────────────────────────────────────────

const lsStore: Record<string, string> = {};

vi.stubGlobal('localStorage', {
  getItem: (k: string) => lsStore[k] ?? null,
  setItem: (k: string, v: string) => { lsStore[k] = v; },
  removeItem: (k: string) => { delete lsStore[k]; },
  clear: () => { Object.keys(lsStore).forEach((k) => delete lsStore[k]); },
});

// ── crypto.subtle stub ────────────────────────────────────────────────────

// Return deterministic bytes so SHA-256 produces reproducible hex.
vi.stubGlobal('crypto', {
  subtle: {
    digest: async (_algo: string, data: ArrayBuffer) => {
      const result = new Uint8Array(32);
      const src = new Uint8Array(data);
      result.set(src.slice(0, 32));
      return result.buffer;
    },
  },
});

// ── identity-avatar stub ─────────────────────────────────────────────────

vi.mock('./identity-avatar', () => ({
  identityAvatarUrl: (identity: Identity) => {
    if ((identity as Identity & { avatarBlobId?: string }).avatarBlobId) {
      return `https://jmap.example.com/blob/${(identity as Identity & { avatarBlobId?: string }).avatarBlobId}`;
    }
    return null;
  },
}));

// ── email-metadata-avatar stub ────────────────────────────────────────────

const mockTryFetchGravatar = vi.fn(async (_url: string): Promise<boolean> => false);
const mockDecodeFaceHeader = vi.fn((_b64: string): string | null => null);
const mockGravatarUrl = vi.fn(async (email: string): Promise<string> =>
  `https://www.gravatar.com/avatar/stub-${email}?s=128&d=404`,
);

vi.mock('./email-metadata-avatar', () => ({
  gravatarUrl: (...args: [string]) => mockGravatarUrl(...args),
  tryFetchGravatar: (...args: [string]) => mockTryFetchGravatar(...args),
  decodeFaceHeader: (...args: [string]) => mockDecodeFaceHeader(...args),
}));

// ── helper ────────────────────────────────────────────────────────────────

type IdentityWithBlob = Identity & { avatarBlobId?: string };

function makeIdentity(email: string, avatarBlobId?: string): IdentityWithBlob {
  return {
    id: `id-${email}`,
    name: email,
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    avatarBlobId,
  };
}

// ── dynamic import so each test gets a fresh module ───────────────────────

type ResolverModule = typeof import('./avatar-resolver.svelte');

async function freshModule(): Promise<ResolverModule> {
  vi.resetModules();
  return import('./avatar-resolver.svelte');
}

// ── tests ─────────────────────────────────────────────────────────────────

describe('avatar-resolver', () => {
  beforeEach(() => {
    // Clear localStorage between tests.
    Object.keys(lsStore).forEach((k) => delete lsStore[k]);
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.resetModules();
  });

  it('tier 1: own identity with avatarBlobId wins over Face header', async () => {
    const { resolve } = await freshModule();
    const identity = makeIdentity('me@example.com', 'blob-abc');
    mockDecodeFaceHeader.mockReturnValue('blob:face-url');

    const url = await resolve('me@example.com', [identity] as Identity[], {
      face: 'base64data',
    });

    expect(url).toBe('https://jmap.example.com/blob/blob-abc');
    expect(mockDecodeFaceHeader).not.toHaveBeenCalled();
  });

  it('tier 1: own identity with avatarBlobId wins over Gravatar', async () => {
    const { resolve } = await freshModule();
    const identity = makeIdentity('me@example.com', 'blob-abc');
    mockTryFetchGravatar.mockResolvedValue(true);

    const url = await resolve('me@example.com', [identity] as Identity[]);

    expect(url).toBe('https://jmap.example.com/blob/blob-abc');
    expect(mockTryFetchGravatar).not.toHaveBeenCalled();
  });

  it('tier 2a: Face header wins over Gravatar when identity has no blob', async () => {
    const { resolve } = await freshModule();
    const identity = makeIdentity('other@example.com'); // no blob
    mockDecodeFaceHeader.mockReturnValue('blob:face-url');
    mockTryFetchGravatar.mockResolvedValue(true);

    const url = await resolve(
      'other@example.com',
      [identity] as Identity[],
      { face: 'base64data' },
    );

    expect(url).toBe('blob:face-url');
    expect(mockTryFetchGravatar).not.toHaveBeenCalled();
  });

  it('tier 2b: Gravatar used when Face absent', async () => {
    const { resolve } = await freshModule();
    mockTryFetchGravatar.mockResolvedValue(true);

    const url = await resolve('someone@example.com', []);

    expect(url).toMatch(/gravatar\.com/);
    expect(mockTryFetchGravatar).toHaveBeenCalledOnce();
  });

  it('tier 3: returns null when Gravatar 404s', async () => {
    const { resolve } = await freshModule();
    mockTryFetchGravatar.mockResolvedValue(false);

    const url = await resolve('nobody@example.com', []);

    expect(url).toBeNull();
  });

  it('cache keyed by lowercased, trimmed email — coalesces duplicates', async () => {
    const { resolve } = await freshModule();
    mockTryFetchGravatar.mockResolvedValue(true);

    const url1 = await resolve('  User@Example.COM  ', []);
    const url2 = await resolve('user@example.com', []);

    expect(url1).toEqual(url2);
    // Second call is a cache hit — Gravatar called only once.
    expect(mockTryFetchGravatar).toHaveBeenCalledOnce();
  });

  it('cache null entry: Gravatar not re-fetched within TTL', async () => {
    const { resolve } = await freshModule();
    mockTryFetchGravatar.mockResolvedValue(false);

    await resolve('absent@example.com', []);
    await resolve('absent@example.com', []);

    expect(mockTryFetchGravatar).toHaveBeenCalledOnce();
  });

  it('cache survives JSON round-trip in localStorage', async () => {
    const { resolve, _setMemCache, _getMemCache } = await freshModule();
    mockTryFetchGravatar.mockResolvedValue(true);

    await resolve('cached@example.com', []);

    const raw = lsStore['herold:avatar:cache'];
    expect(raw).toBeDefined();
    const parsed: Record<string, { url: string | null; ts: number }> = JSON.parse(raw!);
    const cachedEntry = parsed['cached@example.com'];
    expect(cachedEntry).toBeDefined();
    expect(typeof cachedEntry?.url).toBe('string');
    expect(typeof cachedEntry?.ts).toBe('number');

    // Inject the parsed cache back (simulates page reload restoring from LS).
    _setMemCache(parsed);

    // Second resolve should hit memory cache, not Gravatar.
    const url2 = await resolve('cached@example.com', []);
    expect(url2).toEqual(cachedEntry?.url);
    expect(mockTryFetchGravatar).toHaveBeenCalledOnce();
    void _getMemCache; // used to verify type
  });

  it('toggle off: Gravatar not called even when it would resolve', async () => {
    const { resolve, setAvatarEmailMetadataEnabled } = await freshModule();
    setAvatarEmailMetadataEnabled(false);
    mockTryFetchGravatar.mockResolvedValue(true);

    const url = await resolve('someone@example.com', []);

    expect(url).toBeNull();
    expect(mockTryFetchGravatar).not.toHaveBeenCalled();
  });

  it('toggle off: Face header not decoded even when present', async () => {
    const { resolve, setAvatarEmailMetadataEnabled } = await freshModule();
    setAvatarEmailMetadataEnabled(false);
    mockDecodeFaceHeader.mockReturnValue('blob:face-url');

    const url = await resolve('someone@example.com', [], { face: 'base64' });

    expect(url).toBeNull();
    expect(mockDecodeFaceHeader).not.toHaveBeenCalled();
  });

  it('_invalidateCacheEntry removes only the target key', async () => {
    const { _setMemCache, _getMemCache, _invalidateCacheEntry } =
      await freshModule();
    _setMemCache({
      'a@example.com': { url: 'https://example.com/a.jpg', ts: Date.now() },
      'b@example.com': { url: null, ts: Date.now() },
    });

    _invalidateCacheEntry('A@example.com');

    const cache = _getMemCache();
    expect(cache['a@example.com']).toBeUndefined();
    expect(cache['b@example.com']).toBeDefined();
  });

  it('clearAvatarCache empties the memory cache', async () => {
    const { _setMemCache, _getMemCache, clearAvatarCache } =
      await freshModule();
    _setMemCache({
      'a@example.com': { url: 'https://example.com/a.jpg', ts: Date.now() },
    });

    clearAvatarCache();

    const cache = _getMemCache();
    expect(Object.keys(cache)).toHaveLength(0);
  });
});
