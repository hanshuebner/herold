/**
 * Tests for MailStore.separateIdentity (issue #212, REQ-MAIL-SUB-01/07).
 *
 * Verifies the Identity/set{separated:true} wire shape and the optimistic
 * cache eviction: once separated, the identity moves off the parent
 * account's Identity/get and this method must drop it from mail.identities
 * so the primary Identity list stops showing it immediately.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Identity } from './types';

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()) },
}));
vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
  strict: (r: unknown[]) => r,
}));
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: { capabilities: {}, primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-1' } },
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
vi.mock('../storage/account-scoped', () => ({
  accountKey: (_key: string) => _key,
}));
vi.mock('../debug-ring/debug-ring', () => ({ appendEvent: vi.fn() }));

function makeIdentity(overrides: Partial<Identity> & Pick<Identity, 'id' | 'email'>): Identity {
  return {
    name: overrides.email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    ...overrides,
  };
}

const { mail } = await import('./store.svelte');
const { jmap } = await import('../jmap/client');

describe('MailStore.separateIdentity', () => {
  beforeEach(() => {
    vi.mocked(jmap.batch).mockReset();
  });

  it('sends Identity/set{separated:true} against the parent account and drops the row on success', async () => {
    const target = makeIdentity({ id: '42', email: 'club@example.com' });
    const other = makeIdentity({ id: '7', email: 'other@example.com' });
    mail.identities = new Map([
      [target.id, target],
      [other.id, other],
    ]);

    vi.mocked(jmap.batch).mockImplementation(async (fn) => {
      const calls: [string, unknown, string][] = [];
      fn({
        call: (name: string, args: unknown, _using?: unknown) => {
          calls.push([name, args, 'c0']);
          return { ref: (path: string) => ({ resultOf: 'c0', name, path }) };
        },
      } as never);
      expect(calls[0]?.[0]).toBe('Identity/set');
      expect(calls[0]?.[1]).toEqual({
        accountId: 'acct-1',
        update: { '42': { separated: true } },
      });
      return {
        responses: [
          [
            'Identity/set',
            { updated: { '42': { subAccountId: 'sub-42' } } },
            'c0',
          ],
        ],
        sessionState: 'state-1',
      };
    });

    const subAccountId = await mail.separateIdentity('42');
    expect(subAccountId).toBe('sub-42');
    expect(mail.identities.has('42')).toBe(false);
    expect(mail.identities.has('7')).toBe(true);
  });

  it('throws the server description on notUpdated and keeps the identity cached', async () => {
    const target = makeIdentity({ id: '42', email: 'club@example.com' });
    mail.identities = new Map([[target.id, target]]);

    vi.mocked(jmap.batch).mockImplementation(async (fn) => {
      fn({
        call: () => ({ ref: () => ({}) }),
      } as never);
      return {
        responses: [
          [
            'Identity/set',
            { notUpdated: { '42': { type: 'forbidden', description: 'nope' } } },
            'c0',
          ],
        ],
        sessionState: 'state-1',
      };
    });

    await expect(mail.separateIdentity('42')).rejects.toThrow('nope');
    expect(mail.identities.has('42')).toBe(true);
  });
});
