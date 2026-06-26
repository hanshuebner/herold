/**
 * Tests for the MailStore.reset() method (Bug 1 fix — issue #42).
 *
 * Coverage:
 *   1. After reset(), mailboxes is empty
 *   2. After reset(), emails is empty
 *   3. After reset(), threads is empty
 *   4. After reset(), identities is empty
 *   5. After reset(), listLoadStatus is 'idle'
 *   6. After reset(), searchHistory is []
 *   7. After reset(), emailState and mailboxState are null
 *   8. After reset(), loadMailboxes is not skipped (size === 0 guard passes)
 *   9. After reset(), searchHistory reads from the account-scoped key (not
 *      the old account's key) after hydrateSearchHistory is called
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// ── Auth mock ─────────────────────────────────────────────────────────────

let mockSession: { primaryAccounts: Record<string, string>; username: string } | null = null;

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    get session() {
      return mockSession;
    },
    get status() {
      return mockSession ? 'ready' : 'unauthenticated';
    },
  },
  registerAccountResetCallback: vi.fn(),
}));

// ── JMAP client mock ───────────────────────────────────────────────────────

const mockBatch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch: (...args: unknown[]) => mockBatch(...args) },
  strict: vi.fn(),
}));

// ── Sync mock ─────────────────────────────────────────────────────────────

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn() },
}));

// ── toast mock ────────────────────────────────────────────────────────────

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

// ── i18n mock ────────────────────────────────────────────────────────────

vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));

// ── Settings mock ─────────────────────────────────────────────────────────

vi.mock('../settings/settings.svelte', () => ({
  settings: { desktopNotifEnabled: false },
}));

// ── Sounds mock ────────────────────────────────────────────────────────────

vi.mock('../notifications/sounds.svelte', () => ({
  sounds: { play: vi.fn() },
}));

// ── Cue gates mock ────────────────────────────────────────────────────────

vi.mock('../notifications/cue-gates', () => ({
  shouldPlayMailCue: vi.fn(() => false),
}));

// ── Router mock ───────────────────────────────────────────────────────────

vi.mock('../router/router.svelte', () => ({
  router: { parts: [] },
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

// ─────────────────────────────────────────────────────────────────────────

beforeEach(() => {
  mockSession = null;
  Object.keys(localStorageData).forEach((k) => delete localStorageData[k]);
  localStorageMock.getItem.mockClear();
  localStorageMock.setItem.mockClear();
  mockBatch.mockReset();
});

// Lazy import after mocks are set up.
async function importMail() {
  const mod = await import('./store.svelte');
  return mod;
}

describe('MailStore.reset()', () => {
  it('empties mailboxes, emails, threads, identities after being populated', async () => {
    mockSession = {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-alice' },
      username: 'alice@example.com',
    };
    const { mail } = await importMail();

    // Inject some state as if alice was logged in.
    mail.mailboxes = new Map([['mbx1', { id: 'mbx1', name: 'Inbox', role: 'inbox' } as never]]);
    mail.emails = new Map([['em1', { id: 'em1' } as never]]);
    mail.threads = new Map([['th1', { id: 'th1', emailIds: [] } as never]]);
    mail.identities = new Map([['id1', { id: 'id1' } as never]]);
    mail.emailState = 'state-alice';
    mail.mailboxState = 'mstate-alice';
    mail.listLoadStatus = 'ready' as never;
    mail.searchHistory = ['alice-search'];

    mail.reset();

    expect(mail.mailboxes.size).toBe(0);
    expect(mail.emails.size).toBe(0);
    expect(mail.threads.size).toBe(0);
    expect(mail.identities.size).toBe(0);
    expect(mail.emailState).toBeNull();
    expect(mail.mailboxState).toBeNull();
    expect(mail.listLoadStatus).toBe('idle');
    expect(mail.searchHistory).toEqual([]);
  });

  it('resets list and search state', async () => {
    const { mail } = await importMail();

    mail.listEmailIds = ['em1', 'em2'];
    mail.listFocusedIndex = 3;
    mail.listSelectedIds = new Set(['em1']);
    mail.searchQuery = 'foo';
    mail.searchEmailIds = ['em1'];
    mail.searchLoadStatus = 'ready' as never;
    mail.searchFocusedIndex = 1;

    mail.reset();

    expect(mail.listEmailIds).toEqual([]);
    expect(mail.listFocusedIndex).toBe(-1);
    expect(mail.listSelectedIds.size).toBe(0);
    expect(mail.searchQuery).toBe('');
    expect(mail.searchEmailIds).toEqual([]);
    expect(mail.searchLoadStatus).toBe('idle');
    expect(mail.searchFocusedIndex).toBe(-1);
  });

  it('after reset mailboxes.size === 0 so the loadMailboxes guard does not skip', async () => {
    const { mail } = await importMail();

    mail.mailboxes = new Map([['mbx1', { id: 'mbx1' } as never]]);
    expect(mail.mailboxes.size).toBeGreaterThan(0); // would skip load

    mail.reset();

    expect(mail.mailboxes.size).toBe(0); // guard now passes
  });
});

describe('hydrateSearchHistory() account scoping', () => {
  it('reads search history from the correct account namespace after login', async () => {
    const { mail } = await importMail();

    // Simulate alice's history stored under her scoped key.
    mockSession = { primaryAccounts: {}, username: 'alice@example.com' };
    localStorageData['herold.suite.alice@example.com.mail.search.history'] = JSON.stringify([
      'alice-query',
    ]);

    mail.hydrateSearchHistory();
    expect(mail.searchHistory).toEqual(['alice-query']);
  });

  it('does not read another account history after reset', async () => {
    const { mail } = await importMail();

    // Alice's history is persisted.
    localStorageData['herold.suite.alice@example.com.mail.search.history'] = JSON.stringify([
      'alice-query',
    ]);
    // Bob is now logged in.
    mockSession = { primaryAccounts: {}, username: 'bob@example.com' };

    // After reset and hydrate for bob, bob sees no history.
    mail.reset();
    mail.hydrateSearchHistory();
    expect(mail.searchHistory).toEqual([]);
  });
});
