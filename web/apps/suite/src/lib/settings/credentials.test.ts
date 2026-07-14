/**
 * Unit tests for the unified active-credentials store (issue #224,
 * credentials.svelte.ts).
 *
 * Verifies that load()/revoke() delegate to the shared REST client
 * (get/del), that revoke() always re-fetches from the server rather than
 * mutating `items` client-side, and that the `current` derived getter
 * picks out the is_current row.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

const { mockGet, mockDel, MockUnauthenticatedError } = vi.hoisted(() => {
  class MockUnauthenticatedError extends Error {
    status = 401;
    problemType: string | null;
    constructor(message = 'unauthenticated', problemType: string | null = null) {
      super(message);
      this.name = 'UnauthenticatedError';
      this.problemType = problemType;
    }
  }
  return { mockGet: vi.fn(), mockDel: vi.fn(), MockUnauthenticatedError };
});

vi.mock('../api/client', () => ({
  get: mockGet,
  del: mockDel,
  UnauthenticatedError: MockUnauthenticatedError,
}));

import { credentials, type CredentialDTO } from './credentials.svelte';

function makeItem(overrides: Partial<CredentialDTO> = {}): CredentialDTO {
  return {
    kind: 'session',
    id: 's-1',
    created_at: '2026-07-01T10:00:00Z',
    is_current: false,
    ...overrides,
  };
}

describe('credentials store: load()', () => {
  beforeEach(() => {
    mockGet.mockReset();
    mockDel.mockReset();
    credentials.items = [];
    credentials.status = 'idle';
    credentials.errorMessage = null;
  });

  it('fetches GET /api/v1/auth/credentials and populates items', async () => {
    const items = [
      makeItem({ kind: 'session', id: 's-1', is_current: true }),
      makeItem({ kind: 'device_token', id: '7', label: 'My phone', expires_at: undefined }),
      makeItem({ kind: 'oauth2_grant', id: 'fam-1', label: 'Herold Android', client_id: 'android-client' }),
    ];
    mockGet.mockResolvedValue({ items, next: null });

    await credentials.load();

    expect(mockGet).toHaveBeenCalledWith('/api/v1/auth/credentials');
    expect(credentials.items).toEqual(items);
    expect(credentials.status).toBe('ready');
    expect(credentials.errorMessage).toBeNull();
  });

  it('defaults to an empty list when the server omits items', async () => {
    mockGet.mockResolvedValue({ items: undefined, next: null });
    await credentials.load();
    expect(credentials.items).toEqual([]);
    expect(credentials.status).toBe('ready');
  });

  it('sets loading status while the request is in flight', async () => {
    let resolveFn!: (v: unknown) => void;
    mockGet.mockReturnValue(new Promise((resolve) => (resolveFn = resolve)));
    const promise = credentials.load();
    expect(credentials.status).toBe('loading');
    resolveFn({ items: [], next: null });
    await promise;
    expect(credentials.status).toBe('ready');
  });

  it('sets errorMessage and status=error on a non-auth failure', async () => {
    mockGet.mockRejectedValue(new Error('network down'));
    await credentials.load();
    expect(credentials.status).toBe('error');
    expect(credentials.errorMessage).toBe('network down');
    expect(credentials.items).toEqual([]);
  });

  it('leaves status at loading (not error) on UnauthenticatedError -- the global 401 handler owns the transition', async () => {
    mockGet.mockRejectedValue(new MockUnauthenticatedError());
    await credentials.load();
    // load() returns early on 401 without touching status/errorMessage
    // further -- the shared client's global _onUnauthenticated callback
    // (registered by auth.svelte.ts) already handled the forced-login
    // transition before this catch block runs.
    expect(credentials.status).toBe('loading');
    expect(credentials.errorMessage).toBeNull();
  });
});

describe('credentials store: revoke()', () => {
  beforeEach(() => {
    mockGet.mockReset();
    mockDel.mockReset();
    credentials.items = [];
    credentials.status = 'idle';
    credentials.errorMessage = null;
  });

  it('DELETEs the credential by kind/id, then re-fetches the list', async () => {
    mockDel.mockResolvedValue(undefined);
    const refreshed = [makeItem({ kind: 'session', id: 's-1', is_current: true })];
    mockGet.mockResolvedValue({ items: refreshed, next: null });

    await credentials.revoke('device_token', '7');

    expect(mockDel).toHaveBeenCalledWith('/api/v1/auth/credentials/device_token/7');
    expect(mockGet).toHaveBeenCalledOnce();
    // The list reflects the server's re-fetch, not a client-side splice.
    expect(credentials.items).toEqual(refreshed);
  });

  it('URL-encodes the id path segment', async () => {
    mockDel.mockResolvedValue(undefined);
    mockGet.mockResolvedValue({ items: [], next: null });
    await credentials.revoke('oauth2_grant', 'fam/with slash');
    expect(mockDel).toHaveBeenCalledWith(
      '/api/v1/auth/credentials/oauth2_grant/fam%2Fwith%20slash',
    );
  });

  it('propagates a DELETE failure without re-fetching', async () => {
    mockDel.mockRejectedValue(new Error('boom'));
    await expect(credentials.revoke('session', 's-1')).rejects.toThrow('boom');
    expect(mockGet).not.toHaveBeenCalled();
  });

  it('re-fetch after revoking the current session may itself 401 -- revoke() does not throw in that case', async () => {
    mockDel.mockResolvedValue(undefined);
    mockGet.mockRejectedValue(new MockUnauthenticatedError());
    await expect(credentials.revoke('session', 's-1')).resolves.toBeUndefined();
  });
});

describe('credentials store: current getter', () => {
  beforeEach(() => {
    credentials.items = [];
  });

  it('returns the row whose is_current is true', () => {
    credentials.items = [
      makeItem({ kind: 'session', id: 's-1', is_current: false }),
      makeItem({ kind: 'session', id: 's-2', is_current: true }),
    ];
    expect(credentials.current?.id).toBe('s-2');
  });

  it('returns undefined when no row is current', () => {
    credentials.items = [makeItem({ kind: 'device_token', id: '1', is_current: false })];
    expect(credentials.current).toBeUndefined();
  });
});
