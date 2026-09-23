/**
 * Issue #469 (work item 3): marking a message `$seen` must clear its
 * on-wake indication (`Email.snoozeWokeAt` / `snoozeWokeFor`) in the
 * store's own cache, not just on the server.
 *
 * `#emailSetUpdate` deliberately swallows the EventSource push its own
 * `Email/set` call triggers (`#captureEmailSetNewState`, issue #127 --
 * a spurious refresh on a self-caused change would blank the list on a
 * slow connection). That means the client never learns, via the normal
 * `Email/changes` fold, about a side effect the server made alongside
 * the property the client explicitly set -- and gaining `$seen` clears
 * the wake marker as exactly such a side effect (issue #469's wire
 * contract). `setSeen` and `bulkSetSeen` must therefore front-run that
 * known side effect in their own optimistic patch.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email } from './types';

vi.mock('../jmap/sync.svelte', () => ({ sync: { on: vi.fn(() => vi.fn()) } }));

const batch = vi.fn();
vi.mock('../jmap/client', () => ({
  jmap: { batch, session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acc1',
        'urn:ietf:params:jmap:submission': 'acc1',
      },
    },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));

vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn(), enabled: false } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({ router: { parts: [], getParam: () => null } }));
vi.mock('../settings/settings.svelte', () => ({
  settings: { desktopNotifEnabled: false, isImageAllowed: () => false },
}));
vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { t: (k: string) => k, locale: 'en' },
  localeTag: () => 'en',
}));

function makeEmail(overrides: Partial<Email> & Pick<Email, 'id' | 'threadId'>): Email {
  return {
    mailboxIds: { 'mbx-inbox': true },
    keywords: {},
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    subject: 'Hello',
    preview: 'Hello world',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: '',
    snoozedUntil: null,
    snoozeWakeMailboxId: null,
    snoozeWokeAt: '2026-01-01T09:00:00Z',
    snoozeWokeFor: '2026-01-01T09:00:00Z',
    ...overrides,
  };
}

/** A batch mock that answers every Email/set call with a bare success --
 *  no newState, so #captureEmailSetNewState's dedup path is inert and this
 *  test only exercises the optimistic patch, matching what the store's own
 *  cache holds the instant setSeen/bulkSetSeen returns control. */
function installSuccessBatchMock(): void {
  batch.mockImplementation(async (builder: unknown) => {
    let counter = 0;
    const calls: { name: string }[] = [];
    const api = {
      call: (name: string) => {
        calls.push({ name });
        return { ref: () => ({ resultOf: `c${counter++}`, name, path: '' }) };
      },
    };
    (builder as (b: unknown) => void)(api);
    const responses = calls.map((c, i) => [c.name, { updated: {} }, `c${i}`]);
    return { responses };
  });
}

describe('mail store: snoozeWokeAt/snoozeWokeFor clear on $seen (issue #469)', () => {
  beforeEach(() => {
    batch.mockReset();
    installSuccessBatchMock();
  });

  it('setSeen(id, true) clears the wake marker in the same optimistic patch', async () => {
    const { mail } = await import('./store.svelte');
    mail.emails = new Map([['e1', makeEmail({ id: 'e1', threadId: 't1' })]]);

    await mail.setSeen('e1', true);

    const updated = mail.emails.get('e1')!;
    expect(updated.keywords.$seen).toBe(true);
    expect(updated.snoozeWokeAt).toBeNull();
    expect(updated.snoozeWokeFor).toBeNull();
  });

  it('setSeen(id, false) leaves an absent wake marker alone', async () => {
    const { mail } = await import('./store.svelte');
    mail.emails = new Map([
      [
        'e1',
        makeEmail({
          id: 'e1',
          threadId: 't1',
          keywords: { $seen: true },
          snoozeWokeAt: null,
          snoozeWokeFor: null,
        }),
      ],
    ]);

    await mail.setSeen('e1', false);

    const updated = mail.emails.get('e1')!;
    expect(updated.keywords.$seen).toBeUndefined();
    expect(updated.snoozeWokeAt).toBeNull();
    expect(updated.snoozeWokeFor).toBeNull();
  });

  it('bulkSetSeen(ids, true) clears the wake marker for every affected message', async () => {
    const { mail } = await import('./store.svelte');
    mail.emails = new Map([
      ['e1', makeEmail({ id: 'e1', threadId: 't1' })],
      ['e2', makeEmail({ id: 'e2', threadId: 't2', snoozeWokeAt: null, snoozeWokeFor: null })],
    ]);

    await mail.bulkSetSeen(['e1', 'e2'], true);

    expect(mail.emails.get('e1')!.snoozeWokeAt).toBeNull();
    expect(mail.emails.get('e1')!.snoozeWokeFor).toBeNull();
    expect(mail.emails.get('e1')!.keywords.$seen).toBe(true);
    expect(mail.emails.get('e2')!.keywords.$seen).toBe(true);
  });

  it('setSeen(id, true) restores the wake marker if the server call fails', async () => {
    batch.mockReset();
    batch.mockRejectedValue(new Error('network error'));
    const { mail } = await import('./store.svelte');
    mail.emails = new Map([['e1', makeEmail({ id: 'e1', threadId: 't1' })]]);

    await mail.setSeen('e1', true);

    const reverted = mail.emails.get('e1')!;
    expect(reverted.keywords.$seen).toBeUndefined();
    expect(reverted.snoozeWokeAt).toBe('2026-01-01T09:00:00Z');
    expect(reverted.snoozeWokeFor).toBe('2026-01-01T09:00:00Z');
  });
});
