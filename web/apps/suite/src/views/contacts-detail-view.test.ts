/**
 * ContactsDetailView.svelte component tests (re #198).
 *
 * "Alle Mails mit dieser Person" must link to the path-segment form the
 * mail search route reads (`#/mail/search/<encoded query>` via
 * `router.parts[2]`), not the `?q=` querystring form the route ignores.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

vi.mock('../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

vi.mock('../lib/contacts/store.svelte', () => ({
  contacts: { reload: vi.fn(async () => undefined) },
}));

vi.mock('../lib/contacts/list-store.svelte', () => ({
  contactsListStore: { init: vi.fn(async () => undefined), setGroup: vi.fn() },
}));

vi.mock('../lib/contacts/groups.svelte', () => ({
  groupsStore: {
    groups: [],
    getGroupsForContact: vi.fn(() => []),
    addMember: vi.fn(async () => true),
    removeMember: vi.fn(async () => true),
  },
}));

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    parts: ['contacts', '1'],
    navigate: vi.fn(),
    matches: vi.fn(() => false),
    getParam: vi.fn(() => null),
    setParam: vi.fn(),
  },
}));

vi.mock('../lib/auth/auth.svelte', () => ({
  auth: {
    principalId: 'p1',
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:contacts': 'acct1' },
      capabilities: {},
    },
  },
}));

const CARD_ROSENTHAL = {
  id: '1',
  '@type': 'Card',
  version: '1.0',
  uid: 'uid-1',
  kind: 'individual',
  name: {
    full: 'Andreas Rosenthal',
    components: [
      { kind: 'given', value: 'Andreas' },
      { kind: 'surname', value: 'Rosenthal' },
    ],
  },
  emails: {
    e1: { address: 'andreasrosenthal9@gmail.com' },
    e2: { address: 'andreas.rosenthal@lamberti.at' },
    e3: { address: 'rosenthal@deepick.eu' },
  },
};

vi.mock('../lib/jmap/client', () => ({
  jmap: {
    batch: vi.fn(async (configure: (b: { call: (...args: unknown[]) => { ref: (p: string) => unknown } }) => void) => {
      const calls: unknown[][] = [];
      configure({ call: (...args: unknown[]) => { calls.push(args); return { ref: () => undefined }; } });
      const [method] = calls[0] as [string, Record<string, unknown>];
      if (method === 'Contact/get') {
        return { responses: [['Contact/get', { list: [CARD_ROSENTHAL] }, 'c1']], sessionState: 's1' };
      }
      // Contact/query duplicate-detection probe -- no other candidates.
      return { responses: [['Contact/query', { ids: [] }, 'c1']], sessionState: 's1' };
    }),
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn(() => null),
  },
}));

vi.mock('../lib/jmap/types', () => ({
  Capability: { Contacts: 'urn:ietf:params:jmap:contacts' },
}));

import ContactsDetailView from './ContactsDetailView.svelte';

describe('ContactsDetailView -- "Alle Mails mit dieser Person" link (re #198)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('builds the mail-search path-segment URL, not the ignored ?q= querystring', async () => {
    render(ContactsDetailView);

    const link = await screen.findByText('All mail with this person');
    const href = link.getAttribute('href') ?? '';

    // The route (MailView.svelte / GlobalBar.svelte) reads the query from
    // router.parts[2] -- a `/mail/search/<query>` path segment -- and never
    // parses a `?q=` querystring, so the link must use that shape.
    expect(href.startsWith('#/mail/search/')).toBe(true);
    expect(href).not.toContain('?q=');

    const encodedQuery = href.slice('#/mail/search/'.length);
    const decoded = decodeURIComponent(encodedQuery);
    expect(decoded).toBe(
      'andreasrosenthal9@gmail.com OR andreas.rosenthal@lamberti.at OR rosenthal@deepick.eu',
    );
  });
});
