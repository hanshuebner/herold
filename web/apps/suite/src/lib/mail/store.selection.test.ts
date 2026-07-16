/**
 * Coverage for shift-click range selection in the mail list store
 * (re #202): anchor tracking via `toggleSelected`/`selectRowClick`, forward
 * and backward range extension, and anchor reset on `clearSelection`.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), hasCapability: vi.fn(() => true) },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: {},
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-1' },
      apiUrl: '/jmap',
      downloadUrl: '/jmap/dl/{accountId}/{blobId}/{name}?accept={type}',
      uploadUrl: '/jmap/upload/{accountId}/',
      eventSourceUrl: '/jmap/eventsource/',
      username: 'alice@example.local',
      accounts: {},
      state: 'sess-1',
    },
    principalId: 'principal-alice',
    errorMessage: null,
    needsStepUp: false,
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()), start: vi.fn(), stop: vi.fn() },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../router/router.svelte', () => ({
  router: {
    parts: [],
    matches: vi.fn(() => false),
    navigate: vi.fn(),
    getParam: vi.fn(() => null),
    setParam: vi.fn(),
  },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (key: string) => key },
  localeTag: () => 'en-US',
}));

// eslint-disable-next-line @typescript-eslint/no-explicit-any
let mail: any;

beforeEach(async () => {
  vi.resetModules();
  vi.clearAllMocks();
  const mod = await import('./store.svelte');
  mail = mod.mail;
  mail.listEmailIds = ['a', 'b', 'c', 'd', 'e'];
});

describe('mail list selection: anchor + shift-click range (re #202)', () => {
  it('toggleSelected sets the clicked row as the anchor', () => {
    mail.toggleSelected('b');
    expect(mail.listSelectedIds).toEqual(new Set(['b']));
    expect(mail.listSelectAnchorId).toBe('b');
  });

  it('selectRowClick without shiftKey behaves like a plain toggle and moves the anchor', () => {
    mail.toggleSelected('a');
    mail.selectRowClick('c', false, mail.listEmailIds);
    expect(mail.listSelectedIds).toEqual(new Set(['a', 'c']));
    expect(mail.listSelectAnchorId).toBe('c');
  });

  it('shift-click selects the forward range from the anchor, inclusive', () => {
    mail.toggleSelected('b'); // anchor = b
    mail.selectRowClick('d', true, mail.listEmailIds);
    expect(mail.listSelectedIds).toEqual(new Set(['b', 'c', 'd']));
    // Anchor stays put so a further shift-click keeps extending from it.
    expect(mail.listSelectAnchorId).toBe('b');
  });

  it('shift-click selects the backward range when the clicked row precedes the anchor', () => {
    mail.toggleSelected('d'); // anchor = d
    mail.selectRowClick('b', true, mail.listEmailIds);
    expect(mail.listSelectedIds).toEqual(new Set(['b', 'c', 'd']));
    expect(mail.listSelectAnchorId).toBe('d');
  });

  it('a second shift-click from the same anchor re-extends rather than unioning ranges', () => {
    mail.toggleSelected('a'); // anchor = a
    mail.selectRowClick('c', true, mail.listEmailIds); // range a..c
    expect(mail.listSelectedIds).toEqual(new Set(['a', 'b', 'c']));
    mail.selectRowClick('b', true, mail.listEmailIds); // shrink to a..b
    expect(mail.listSelectedIds).toEqual(new Set(['a', 'b']));
  });

  it('shift-click with no prior anchor falls back to selecting only the clicked row', () => {
    mail.selectRowClick('c', true, mail.listEmailIds);
    expect(mail.listSelectedIds).toEqual(new Set(['c']));
    expect(mail.listSelectAnchorId).toBe('c');
  });

  it('clearSelection resets the anchor as well as the selection set', () => {
    mail.toggleSelected('b');
    mail.clearSelection();
    expect(mail.listSelectedIds.size).toBe(0);
    expect(mail.listSelectAnchorId).toBeNull();

    // A subsequent shift-click has no anchor to extend from.
    mail.selectRowClick('d', true, mail.listEmailIds);
    expect(mail.listSelectedIds).toEqual(new Set(['d']));
  });
});

describe('mail list selection: pruneSelectionToRendered (re #202)', () => {
  it('drops ids that are no longer rendered', () => {
    mail.selectRowClick('b', true, mail.listEmailIds);
    mail.selectRowClick('d', true, mail.listEmailIds); // range b..d
    expect(mail.listSelectedIds).toEqual(new Set(['b', 'c', 'd']));

    // A category-tab switch or background refresh narrows the rendered set
    // to just 'c' without going through clearSelection.
    mail.pruneSelectionToRendered(['a', 'c']);
    expect(mail.listSelectedIds).toEqual(new Set(['c']));
  });

  it('is a no-op when everything selected is still rendered', () => {
    mail.toggleSelected('b');
    const before = mail.listSelectedIds;
    mail.pruneSelectionToRendered(['a', 'b', 'c']);
    expect(mail.listSelectedIds).toBe(before); // same Set reference: no reactive write
  });

  it('does not prune while listWholeMailboxSelected is true', () => {
    mail.toggleSelected('b');
    mail.listWholeMailboxSelected = true;
    mail.pruneSelectionToRendered(['z']);
    expect(mail.listSelectedIds).toEqual(new Set(['b']));
  });

  it('is a no-op on an empty selection', () => {
    const before = mail.listSelectedIds;
    mail.pruneSelectionToRendered(['a']);
    expect(mail.listSelectedIds).toBe(before);
  });
});
