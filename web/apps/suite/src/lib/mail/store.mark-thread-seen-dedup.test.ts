/**
 * Regression coverage for `markThreadSeen`'s dedupe fan-out (re #276).
 *
 * Two independent deliveries of one message (same Message-ID) into the
 * same thread leave the merged thread entry's own `keywords.$seen` union
 * true as soon as ANY copy is read, hiding a genuinely-unread twin behind
 * `unseenDedupedCopyIds`. The explicit "Mark thread read" action
 * (`markThreadSeen(threadId, true)`) must reach that hidden copy too, not
 * just the ids whose merged/raw `$seen` state actually flips.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email } from './types';

// ── Module-level mocks (must be before any dynamic import) ────────────────────

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), hasCapability: vi.fn(() => true) },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
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

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../router/router.svelte', () => ({
  router: { parts: [], matches: vi.fn(() => false), navigate: vi.fn(), getParam: vi.fn(() => null) },
}));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/notifications.svelte', () => ({ notifications: { add: vi.fn() } }));
vi.mock('../compose/compose.svelte', () => ({
  compose: { openReply: vi.fn(), openReplyAll: vi.fn(), openForward: vi.fn(), openDraft: vi.fn() },
}));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  i18n: { t: (key: string) => key },
  localeTag: () => 'en',
}));

// ── Helpers ─────────────────────────────────────────────────────────────────

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: { 'mbx-inbox': true },
    keywords: {},
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    subject: 'Test',
    preview: '',
    receivedAt: '2026-08-07T10:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
    ...overrides,
  };
}

function invocation(name: string, args: unknown, callId = 'c0'): [string, unknown, string] {
  return [name, args, callId];
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('markThreadSeen dedupe fan-out (re #276)', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let mailMod: any;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    jmapMod = await import('../jmap/client');
    mailMod = await import('./store.svelte');
  });

  it('marks the shadowed unseen copy read even though the merged entry already reads seen', async () => {
    const { mail } = mailMod;

    // Two deliveries of the same logical message: 2492-equivalent (larger,
    // already read) and 2493-equivalent (smaller, unread). Both are stored
    // as raw thread members; resolveDeduplicatedThreadEmails collapses them
    // into one rendered entry via threadEmails().
    mail.emails.set(
      'e-2492',
      { ...makeEmail({ id: 'e-2492', threadId: 't2488', keywords: { $seen: true } }), size: 2822 },
    );
    mail.emails.set(
      'e-2493',
      { ...makeEmail({ id: 'e-2493', threadId: 't2488', keywords: {} }), size: 2797 },
    );
    // Both copies share a Message-ID so resolveDeduplicatedThreadEmails
    // merges them.
    mail.emails.get('e-2492')!.messageId = ['<dup@rm1>'];
    mail.emails.get('e-2493')!.messageId = ['<dup@rm1>'];
    mail.committedThreadEmailIds = new Map([['t2488', ['e-2492', 'e-2493']]]);

    // Sanity: the merged view reports seen (truthy-wins union) yet still
    // carries the shadow id.
    const merged = mail.threadEmails('t2488');
    expect(merged).toHaveLength(1);
    expect(merged[0]!.keywords.$seen).toBe(true);
    expect(merged[0]!.unseenDedupedCopyIds).toEqual(['e-2493']);

    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/set', {
          newState: 's2',
          updated: { 'e-2493': {} },
        }),
      ],
    });

    await mail.markThreadSeen('t2488', true);

    // The Email/set call is the first jmap.batch invocation; a second,
    // unmocked call follows from the store's own post-mutation mailbox-
    // count refresh (#refreshMailboxesSoon) and is not under test here.
    expect(jmapMod.jmap.batch).toHaveBeenCalled();
    const builder = vi.mocked(jmapMod.jmap.batch).mock.calls[0][0];
    let sentUpdate: unknown;
    builder({
      call: (_name: string, args: { update: unknown }) => {
        sentUpdate = args.update;
        return { ref: () => ({}) };
      },
    });
    // Only the shadowed copy needed an actual flip: the representative
    // (e-2492) was already seen so it is not itself in the patch, but the
    // fan-out reaches e-2493 regardless.
    expect(sentUpdate).toEqual({ 'e-2493': { 'keywords/$seen': true } });
  });

  it('does not re-issue a call once every copy in the group is already seen', async () => {
    const { mail } = mailMod;

    mail.emails.set(
      'e-a',
      { ...makeEmail({ id: 'e-a', threadId: 't1', keywords: { $seen: true } }), size: 100 },
    );
    mail.emails.set(
      'e-b',
      { ...makeEmail({ id: 'e-b', threadId: 't1', keywords: { $seen: true } }), size: 90 },
    );
    mail.emails.get('e-a')!.messageId = ['<dup@rm1>'];
    mail.emails.get('e-b')!.messageId = ['<dup@rm1>'];
    mail.committedThreadEmailIds = new Map([['t1', ['e-a', 'e-b']]]);

    await mail.markThreadSeen('t1', true);

    expect(jmapMod.jmap.batch).not.toHaveBeenCalled();
  });
});
