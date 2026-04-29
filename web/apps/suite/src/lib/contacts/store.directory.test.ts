/**
 * Tests for the searchDirectory() helper in the contacts store.
 *
 * These tests mock the JMAP client and the capabilities module to verify
 * that Directory/search is called correctly and that soft-fail behaviour
 * works as specified.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// Mock the capabilities module before importing store so that
// hasDirectoryAutocomplete can be controlled per-test.
vi.mock('../auth/capabilities', () => ({
  hasDirectoryAutocomplete: vi.fn(() => false),
  hasExternalSubmission: vi.fn(() => false),
  directoryAutocompleteMode: vi.fn(() => null),
}));

// Mock the JMAP client so no real network calls are made.
vi.mock('../jmap/client', () => ({
  jmap: {
    session: null,
    hasCapability: vi.fn(() => false),
    batch: vi.fn(),
  },
  strict: vi.fn((r: unknown) => r),
}));

// Mock auth so directoryAccountId resolves.
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'account1',
        'https://netzhansa.com/jmap/directory-autocomplete': 'account1',
      },
    },
  },
}));

// Mock seen-addresses so it doesn't blow up during import.
vi.mock('./seen-addresses.svelte', () => ({
  seenAddresses: { entries: [], status: 'idle' },
}));

import { searchDirectory, _internals_forTest } from './store.svelte';
import * as capsMod from '../auth/capabilities';
import * as clientMod from '../jmap/client';
import { Capability } from '../jmap/types';

const { searchDirectory: searchDirectoryInternal } = _internals_forTest;

// Ensure searchDirectory and _internals_forTest.searchDirectory are the same export.
expect(searchDirectory).toBe(searchDirectoryInternal);

const mockHasDirectoryAutocomplete = vi.mocked(capsMod.hasDirectoryAutocomplete);
const mockBatch = vi.mocked(clientMod.jmap.batch);

beforeEach(() => {
  vi.clearAllMocks();
  mockHasDirectoryAutocomplete.mockReturnValue(false);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('searchDirectory: capability gate', () => {
  it('returns [] when capability is absent', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(false);
    const result = await searchDirectory('alice', 8);
    expect(result).toEqual([]);
    expect(mockBatch).not.toHaveBeenCalled();
  });

  it('returns [] for prefix shorter than 2 characters (empty)', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(true);
    const result = await searchDirectory('', 8);
    expect(result).toEqual([]);
    expect(mockBatch).not.toHaveBeenCalled();
  });

  it('returns [] for prefix of length 1', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(true);
    const result = await searchDirectory('a', 8);
    expect(result).toEqual([]);
    expect(mockBatch).not.toHaveBeenCalled();
  });
});

describe('searchDirectory: JMAP call', () => {
  it('calls Directory/search with the directory-autocomplete capability in using', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(true);

    let capturedUsing: string[] = [];
    let capturedMethodName = '';

    mockBatch.mockImplementation(async (builder) => {
      // Intercept the builder callback to capture what the store passes.
      const fakeBatchBuilder = {
        call: (name: string, _args: unknown, using: string[]) => {
          capturedUsing = using;
          capturedMethodName = name;
          return {
            id: 'c0',
            name,
            ref: (path: string) => ({ resultOf: 'c0', name, path }),
          };
        },
      };
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      builder(fakeBatchBuilder as any);
      return {
        responses: [
          [
            'Directory/search',
            { accountId: 'account1', items: [] },
            'c0',
          ],
        ] as [string, unknown, string][],
        sessionState: 'st1',
      };
    });

    await searchDirectory('ali', 8);

    expect(capturedUsing).toContain(Capability.HeroldDirectoryAutocomplete);
    expect(capturedMethodName).toBe('Directory/search');
  });

  it('maps items to ContactSuggestion with dir: prefix', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(true);

    mockBatch.mockResolvedValue({
      responses: [
        [
          'Directory/search',
          {
            accountId: 'account1',
            items: [
              { id: 'p1', email: 'alice@example.com', displayName: 'Alice Example' },
              { id: 'p2', email: 'alicia@example.com', displayName: '' },
            ],
          },
          'c0',
        ],
      ] as [string, unknown, string][],
      sessionState: 'st1',
    });

    const result = await searchDirectory('ali', 8);
    expect(result).toHaveLength(2);
    expect(result[0]).toEqual({
      id: 'dir:p1',
      name: 'Alice Example',
      email: 'alice@example.com',
    });
    expect(result[1]).toEqual({
      id: 'dir:p2',
      name: '',
      email: 'alicia@example.com',
    });
  });

  it('returns [] on JMAP method error (does not throw)', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(true);

    mockBatch.mockResolvedValue({
      responses: [
        ['error', { type: 'serverFail', description: 'internal error' }, 'c0'],
      ] as [string, unknown, string][],
      sessionState: 'st1',
    });

    const result = await searchDirectory('ali', 8);
    expect(result).toEqual([]);
  });

  it('returns [] when batch throws (network / transport error)', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(true);
    mockBatch.mockRejectedValue(new Error('network failure'));

    const result = await searchDirectory('al', 8);
    expect(result).toEqual([]);
  });

  it('respects the limit parameter', async () => {
    mockHasDirectoryAutocomplete.mockReturnValue(true);

    const manyItems = Array.from({ length: 20 }, (_, i) => ({
      id: `p${i}`,
      email: `user${i}@corp.test`,
      displayName: `User ${i}`,
    }));

    mockBatch.mockResolvedValue({
      responses: [
        ['Directory/search', { accountId: 'account1', items: manyItems }, 'c0'],
      ] as [string, unknown, string][],
      sessionState: 'st1',
    });

    const result = await searchDirectory('user', 5);
    expect(result).toHaveLength(5);
  });
});
