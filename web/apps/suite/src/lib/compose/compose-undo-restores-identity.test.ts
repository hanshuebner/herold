/**
 * Regression test (re #508): undo-cancelling a send must restore the From
 * identity the message was actually composed/sent under, and (for a
 * sub-account-scoped compose) the scope itself -- not silently fall back to
 * the primary identity / primary account. send()'s undo-callback openWith(...)
 * call, pre-fix, omitted both `identity` and `scopeAccountId`, so openWith's
 * own defaults (`args.identity ?? null`, `args.scopeAccountId ?? null`) reset
 * both to the primary-account fallback -- the pins below assert the undo
 * callback now passes the identity and scope that were active when send()
 * captured its snapshot.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Identity } from '../mail/types';

interface CapturedCall {
  name: string;
  args: Record<string, unknown>;
}

const batchMock = vi.fn();

/** Minimal stand-in for jmap's BatchBuilder: records every b.call(...). */
function captureCalls(builder: (b: {
  call: (name: string, args: Record<string, unknown>) => { ref: () => unknown };
}) => void): CapturedCall[] {
  const calls: CapturedCall[] = [];
  builder({
    call: (name, args) => {
      calls.push({ name, args });
      return { ref: () => ({}) };
    },
  });
  return calls;
}

vi.mock('../jmap/client', () => ({
  jmap: {
    maxUploadSize: null,
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn().mockReturnValue(null),
    batch: (...args: unknown[]) => batchMock(...args),
  },
  strict: (responses: [string, { type: string; description?: string }, string][]) => {
    for (const [name, args] of responses) {
      if (name === 'error') throw new Error(args.description ?? args.type);
    }
    return responses;
  },
}));

const primaryIdentity: Identity = {
  id: 'default',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
};

const workIdentity: Identity = {
  id: '800002',
  name: 'Alice (working-external)',
  email: 'alice-work@foreign.example',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: true,
};

const subIdentity: Identity = {
  id: '800101',
  name: 'Vorsitz',
  email: 'vorsitz@classic-computing.example',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: true,
};

vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    primaryIdentity,
    drafts: { id: 'drafts1' },
    sent: { id: 'sent1' },
    archive: { id: 'archive1' },
    identities: new Map([[primaryIdentity.id, primaryIdentity], [workIdentity.id, workIdentity]]),
    mailboxes: new Map([['drafts1', {}]]),
    loadMailboxes: vi.fn(),
    loadIdentities: vi.fn(),
  },
}));

const subAccountMailboxes = [
  { id: 'sub-drafts1', role: 'drafts' },
  { id: 'sub-sent1', role: 'sent' },
  { id: 'sub-archive1', role: 'archive' },
];

vi.mock('../mail/sub-accounts.svelte', () => ({
  subAccounts: {
    find: vi.fn((id: string) =>
      id === 'sub1' ? { id: 'sub1', mailboxes: subAccountMailboxes } : undefined,
    ),
  },
}));

vi.mock('../settings/settings.svelte', () => ({
  settings: { undoWindowSec: 5 },
}));

const toastShow = vi.fn();
vi.mock('../toast/toast.svelte', () => ({
  toast: { show: (...args: unknown[]) => toastShow(...args) },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  localeTag: () => 'en',
}));

describe('compose undo callback restores the From identity and scope (re #508)', () => {
  beforeEach(() => {
    batchMock.mockReset();
    toastShow.mockReset();
  });

  it('carries the non-default From identity through openWith on undo', async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
    compose.openWith({
      to: 'dest@remote.test',
      subject: 'undo identity',
      body: 'body text',
      identity: workIdentity,
    });
    expect(compose.selectedIdentity).toEqual(workIdentity);

    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      // The submission must use the selected identity, not the primary one.
      const subCall = calls.find((c) => c.name === 'EmailSubmission/set')!;
      const created = (subCall.args.create as Record<string, Record<string, unknown>>).sub1!;
      expect(created.identityId).toBe(workIdentity.id);
      return Promise.resolve({
        responses: [
          ['Email/set', { created: { draft1: { id: 'email-1' } } }, 'c0'],
          ['EmailSubmission/set', { created: { sub1: { id: 'sub-1' } } }, 'c1'],
        ],
      });
    });

    await compose.send();
    const toastArgs = toastShow.mock.calls[0]![0] as { undo?: () => Promise<void> };

    // Undo re-triggers selectedIdentity reset via close(); the picker must
    // not be left pointing at the primary identity before openWith runs.
    batchMock.mockImplementationOnce(() =>
      Promise.resolve({ responses: [['EmailSubmission/set', { destroyed: ['sub-1'] }, 'c0']] }),
    );
    batchMock.mockImplementationOnce(() =>
      Promise.resolve({ responses: [['Email/set', { updated: { 'email-1': null } }, 'c0']] }),
    );

    await toastArgs.undo!();

    expect(compose.selectedIdentity).toEqual(workIdentity);
    expect(compose.to).toBe('dest@remote.test');
  });

  it('carries scopeAccountId (and the scoped identity) through openWith on undo', async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
    compose.openWith({
      to: 'dest@remote.test',
      subject: 'undo scope',
      body: 'body text',
      identity: subIdentity,
      scopeAccountId: 'sub1',
    });
    expect(compose.scopeAccountId).toBe('sub1');

    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      const setCall = calls.find((c) => c.name === 'Email/set')!;
      // The initial send must target the sub-account, not the primary one.
      expect(setCall.args.accountId).toBe('sub1');
      return Promise.resolve({
        responses: [
          ['Email/set', { created: { draft1: { id: 'email-2' } } }, 'c0'],
          ['EmailSubmission/set', { created: { sub1: { id: 'sub-2' } } }, 'c1'],
        ],
      });
    });

    await compose.send();
    const toastArgs = toastShow.mock.calls[0]![0] as { undo?: () => Promise<void> };

    batchMock.mockImplementationOnce(() =>
      Promise.resolve({ responses: [['EmailSubmission/set', { destroyed: ['sub-2'] }, 'c0']] }),
    );
    // The Drafts-restoring Email/set after destroy must also target the
    // sub-account -- the Email being restored lives in its Drafts mailbox,
    // not the primary account's.
    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      const setCall = calls.find((c) => c.name === 'Email/set')!;
      expect(setCall.args.accountId).toBe('sub1');
      return Promise.resolve({ responses: [['Email/set', { updated: { 'email-2': null } }, 'c0']] });
    });

    await toastArgs.undo!();

    expect(compose.scopeAccountId).toBe('sub1');
    expect(compose.selectedIdentity).toEqual(subIdentity);

    // A resend from the reopened composer must still target the sub-account
    // (pre-fix this failed with "email is not visible to the caller" because
    // the resend ran against the primary account against a draft row that
    // lives only in the sub-account).
    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      const setCall = calls.find((c) => c.name === 'Email/set')!;
      expect(setCall.args.accountId).toBe('sub1');
      return Promise.resolve({
        responses: [
          ['Email/set', { updated: { 'email-2': null } }, 'c0'],
          ['EmailSubmission/set', { created: { sub1: { id: 'sub-3' } } }, 'c1'],
        ],
      });
    });
    await compose.send();
  });
});
