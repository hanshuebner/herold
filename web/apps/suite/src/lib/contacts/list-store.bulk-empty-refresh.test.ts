/**
 * Issue #222: after a bulk delete removes every currently loaded contact
 * row, the store must re-issue Contact/query to repopulate the list rather
 * than showing the false "Noch keine Kontakte vorhanden" empty state while
 * the server still holds further matching contacts. This is the contacts
 * counterpart of the mail store's bulk-empty-refresh fix (re #148); see
 * ./store.bulk-empty-refresh.test.ts equivalent under lib/mail/.
 *
 * These tests drive the actual ContactsListStore singleton against a mocked
 * jmap client so we can verify that #refillIfEmptied re-issues Contact/query
 * (via #reload -> #loadPage(0, false)) exactly when the destroy empties the
 * visible `rows` window while the list is still 'ready'.
 */

import { vi, describe, it, expect, beforeEach } from 'vitest';

// ── Hoisted mock primitives ──────────────────────────────────────────────────

const { batchMock, capturedFirstCallNames, lastSetDestroyArgs } = vi.hoisted(() => {
  const capturedFirstCallNames: string[] = [];
  const lastSetDestroyArgs: { destroy?: string[] }[] = [];

  const batchMock = vi.fn(
    async (
      builderFn: (b: {
        call: (name: string, args: unknown, using?: unknown) => { ref: () => unknown };
      }) => void,
    ) => {
      const names: string[] = [];
      const argsByIndex: unknown[] = [];
      const mockB = {
        call(name: string, args: unknown) {
          names.push(name);
          argsByIndex.push(args);
          return { ref: () => ({}) };
        },
      };
      builderFn(mockB);
      const first = names[0] ?? '';
      capturedFirstCallNames.push(first);

      if (first === 'Contact/set') {
        const destroy = (argsByIndex[0] as { destroy?: string[] }).destroy ?? [];
        lastSetDestroyArgs.push({ destroy });
        return {
          responses: [['Contact/set', { destroyed: destroy, notDestroyed: {} }, '0']],
        };
      }

      if (first === 'Contact/query') {
        // Fresh server state after the destroy: the "backfill" page.
        return {
          responses: [
            [
              'Contact/query',
              { ids: ['fresh-c1', 'fresh-c2'], total: 2, queryState: 'qs-fresh' },
              '0',
            ],
            [
              'Contact/get',
              {
                list: [
                  { id: 'fresh-c1', name: { full: 'Fresh One' } },
                  { id: 'fresh-c2', name: { full: 'Fresh Two' } },
                ],
              },
              '1',
            ],
          ],
        };
      }

      return { responses: [] };
    },
  );

  return { batchMock, capturedFirstCallNames, lastSetDestroyArgs };
});

vi.mock('../jmap/client', () => ({
  jmap: {
    hasCapability: vi.fn(() => true),
    batch: batchMock,
  },
  Capability: { Contacts: 'urn:ietf:params:jmap:contacts' },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: {
        'urn:ietf:params:jmap:contacts': 'account1',
      },
    },
  },
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => () => {}) },
}));

import { contactsListStore } from './list-store.svelte';
import type { ContactListRow } from './list-store.svelte';

function makeRow(id: string): ContactListRow {
  return { id, displayName: `Contact ${id}`, secondary: '', photoBlobId: null };
}

/** Seed the store as if two 30-row pages (60 total) were already loaded. */
function seedTwoPagesOf60(totalOnServer: number): void {
  const ids = Array.from({ length: 60 }, (_, i) => `c${i}`);
  contactsListStore.rows = ids.map(makeRow);
  contactsListStore.total = totalOnServer;
  contactsListStore.status = 'ready';
  contactsListStore.selectedIds = new Set(ids);
}

beforeEach(() => {
  batchMock.mockClear();
  capturedFirstCallNames.length = 0;
  lastSetDestroyArgs.length = 0;
  contactsListStore.clearSelection();
  contactsListStore.rows = [];
  contactsListStore.total = null;
  contactsListStore.status = 'idle';
});

describe('bulkDelete empty-window refresh (re #222)', () => {
  it('re-runs Contact/query when the whole loaded window is deleted but more contacts remain', async () => {
    seedTwoPagesOf60(200);
    const ids = contactsListStore.rows.map((r) => r.id);

    await contactsListStore.bulkDelete(ids);

    await vi.waitFor(() => {
      expect(capturedFirstCallNames).toContain('Contact/query');
    });
    await vi.waitFor(() => {
      expect(contactsListStore.rows.map((r) => r.id)).toEqual(['fresh-c1', 'fresh-c2']);
    });
    expect(contactsListStore.status).toBe('ready');
  });

  it('does not re-query when only a subset is deleted (window not emptied)', async () => {
    seedTwoPagesOf60(200);
    const ids = contactsListStore.rows.slice(0, 10).map((r) => r.id);

    await contactsListStore.bulkDelete(ids);
    // Flush any fire-and-forget microtasks.
    await new Promise<void>((resolve) => setTimeout(resolve, 20));

    expect(capturedFirstCallNames).not.toContain('Contact/query');
    expect(contactsListStore.rows.length).toBe(50);
  });

  it('falls back to a genuinely empty, non-stuck ready state when the last page is emptied', async () => {
    // Only one page loaded (30 rows) and it is the only remaining page on
    // the server: after the delete, the server truthfully has zero matches.
    const ids = Array.from({ length: 30 }, (_, i) => `p${i}`);
    contactsListStore.rows = ids.map(makeRow);
    contactsListStore.total = 30;
    contactsListStore.status = 'ready';
    contactsListStore.selectedIds = new Set(ids);

    // Override the Contact/query mock behaviour for this one test: no
    // contacts remain server-side.
    batchMock.mockImplementationOnce(async (builderFn) => {
      const names: string[] = [];
      const argsByIndex: unknown[] = [];
      builderFn({
        call: (name: string, args: unknown) => {
          names.push(name);
          argsByIndex.push(args);
          return { ref: () => ({}) };
        },
      });
      const destroy = (argsByIndex[0] as { destroy?: string[] }).destroy ?? [];
      return { responses: [['Contact/set', { destroyed: destroy, notDestroyed: {} }, '0']] };
    });
    batchMock.mockImplementationOnce(async (builderFn) => {
      builderFn({
        call: (name: string) => {
          capturedFirstCallNames.push(name);
          return { ref: () => ({}) };
        },
      });
      return {
        responses: [
          ['Contact/query', { ids: [], total: 0, queryState: 'qs-empty' }, '0'],
          ['Contact/get', { list: [] }, '1'],
        ],
      };
    });

    await contactsListStore.bulkDelete(ids);

    await vi.waitFor(() => {
      expect(contactsListStore.total).toBe(0);
    });
    expect(contactsListStore.rows).toEqual([]);
    expect(contactsListStore.status).toBe('ready');
    expect(contactsListStore.hasMore).toBe(false);
  });
});
