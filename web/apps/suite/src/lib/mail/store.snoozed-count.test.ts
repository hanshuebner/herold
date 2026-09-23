/**
 * Issue #471: the sidebar's Snoozed badge has no cached
 * `Mailbox.totalEmails` to read from (the virtual folder spans every
 * mailbox), so it is refreshed via its own `Email/query
 * { calculateTotal: true }` round trip. Covers:
 *   - the filter shape sent on the wire (flat, Junk/Trash-excluded
 *     $snoozed hasKeyword -- REQ-SRC-06/REQ-PERF-INDEX-10).
 *   - snoozedCount is populated from the response's `total`.
 *   - a successful snoozeEmail / unsnoozeEmail triggers a refresh.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email, Mailbox } from './types';

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch, session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn(), hasCapability: vi.fn(() => false) },
  strict: (r: unknown[]) => r,
}));

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()) },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acc1' },
    },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({ router: { parts: [], getParam: () => null } }));
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k },
  localeTag: () => 'en',
}));

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: 'test',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
    snoozedUntil: null,
    ...overrides,
  } as Email;
}

function makeMailbox(overrides: Partial<Mailbox> & Pick<Mailbox, 'id'>): Mailbox {
  return {
    name: overrides.id,
    role: null,
    parentId: null,
    sortOrder: 0,
    totalEmails: 0,
    unreadEmails: 0,
    totalThreads: 0,
    unreadThreads: 0,
    ...overrides,
  } as Mailbox;
}

describe('refreshSnoozedCount (re #471)', () => {
  beforeEach(() => {
    batch.mockReset();
    vi.resetModules();
  });

  it('queries a flat, Junk/Trash-excluded $snoozed filter with calculateTotal and no limit', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map<string, Mailbox>([
      ['mbx-junk', makeMailbox({ id: 'mbx-junk', role: 'junk' })],
    ]);

    let capturedArgs: Record<string, unknown> | undefined;
    batch.mockImplementation(async (builder: unknown) => {
      const api = {
        call: (name: string, args: Record<string, unknown>) => {
          capturedArgs = args;
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      };
      (builder as (b: unknown) => void)(api);
      return { responses: [['Email/query', { total: 3 }, 'c0']], using: new Set<string>() };
    });

    await mail.refreshSnoozedCount();

    expect(capturedArgs).toEqual({
      accountId: 'acc1',
      filter: { hasKeyword: '$snoozed', inMailboxOtherThan: ['mbx-junk'] },
      limit: 0,
      calculateTotal: true,
    });
    expect(mail.snoozedCount).toBe(3);
  });

  it('defaults to 0 when the response carries no total', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map();
    batch.mockImplementation(async (builder: unknown) => {
      const api = { call: (name: string) => ({ ref: () => ({ resultOf: 'c0', name, path: '' }) }) };
      (builder as (b: unknown) => void)(api);
      return { responses: [['Email/query', {}, 'c0']], using: new Set<string>() };
    });

    await mail.refreshSnoozedCount();

    expect(mail.snoozedCount).toBe(0);
  });

  it('a successful snoozeEmail refreshes snoozedCount', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map();
    mail.emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 't1' })],
    ]);

    let snoozedQueryCount = 0;
    batch.mockImplementation(async (builder: unknown) => {
      const calls: { name: string; args: Record<string, unknown> }[] = [];
      const api = {
        call: (name: string, args: Record<string, unknown>) => {
          calls.push({ name, args });
          return { ref: () => ({ resultOf: `c${calls.length - 1}`, name, path: '' }) };
        },
      };
      (builder as (b: unknown) => void)(api);
      const responses: Array<[string, unknown, string]> = calls.map((c, i) => {
        if (c.name === 'Email/set') {
          return [c.name, { newState: 's2', updated: { e1: {} } }, `c${i}`];
        }
        // Email/query (the mailbox-count refresh and the snoozed-count refresh).
        snoozedQueryCount++;
        return [c.name, { total: 1 }, `c${i}`];
      });
      return { responses, using: new Set<string>() };
    });

    await mail.snoozeEmail('e1', new Date('2026-05-01T09:00:00Z'));
    await new Promise((r) => setTimeout(r, 0));

    expect(mail.snoozedCount).toBe(1);
    expect(snoozedQueryCount).toBeGreaterThan(0);
  });

  it('a successful unsnoozeEmail refreshes snoozedCount', async () => {
    const { mail } = await import('./store.svelte');
    mail.mailboxes = new Map();
    mail.emails = new Map<string, Email>([
      ['e1', makeEmail({ id: 'e1', threadId: 't1', snoozedUntil: '2026-05-01T09:00:00Z' })],
    ]);

    batch.mockImplementation(async (builder: unknown) => {
      const calls: { name: string; args: Record<string, unknown> }[] = [];
      const api = {
        call: (name: string, args: Record<string, unknown>) => {
          calls.push({ name, args });
          return { ref: () => ({ resultOf: `c${calls.length - 1}`, name, path: '' }) };
        },
      };
      (builder as (b: unknown) => void)(api);
      const responses: Array<[string, unknown, string]> = calls.map((c, i) => {
        if (c.name === 'Email/set') {
          return [c.name, { newState: 's2', updated: { e1: {} } }, `c${i}`];
        }
        return [c.name, { total: 0 }, `c${i}`];
      });
      return { responses, using: new Set<string>() };
    });

    await mail.unsnoozeEmail('e1');
    await new Promise((r) => setTimeout(r, 0));

    expect(mail.snoozedCount).toBe(0);
  });
});
