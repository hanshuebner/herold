/**
 * Regression test (re #212 CI flake, job 12019): a scoped compose
 * (SubAccountMailView's composeFromHere, REQ-MAIL-SUB-08) must never
 * silently fall back to mail.primaryIdentity when its own selectedIdentity
 * was not resolved. That fallback's wire id ("default") collides with the
 * sub-account's own synthesized default identity id, so a send scoped to
 * the sub-account under the borrowed identity either serverFails (no
 * external-submission config under "default") or, worse, sends as the
 * wrong identity -- the message never reaches its destination either way.
 * See compose.svelte.ts's send()/persistDraft() comments for the full
 * mechanism.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Identity, Mailbox } from '../mail/types';

const batchMock = vi.fn();

vi.mock('../jmap/client', () => ({
  jmap: {
    maxUploadSize: null,
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn().mockReturnValue(null),
    batch: (...args: unknown[]) => batchMock(...args),
  },
  strict: vi.fn(),
}));

const scopedDraftsMailbox: Mailbox = {
  id: 'drafts-sub1',
  name: 'Drafts',
  role: 'drafts',
  parentId: null,
  sortOrder: 0,
  totalEmails: 0,
  unreadEmails: 0,
} as unknown as Mailbox;

vi.mock('../mail/sub-accounts.svelte', () => ({
  subAccounts: {
    find: vi.fn().mockReturnValue({
      accountId: 'sub1',
      name: 'Vorsitz',
      identity: null,
      mailboxes: [
        {
          id: 'drafts-sub1',
          name: 'Drafts',
          role: 'drafts',
          parentId: null,
          sortOrder: 0,
          totalEmails: 0,
          unreadEmails: 0,
        },
      ],
      unreadThreads: 0,
      loadStatus: 'ready',
      errorMessage: null,
    }),
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

vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    // Non-null on purpose: this identity exists so a test that failed to
    // close the fallback would silently succeed using it.
    primaryIdentity,
    drafts: { id: 'drafts-acct1' },
    identities: new Map(),
    mailboxes: new Map(),
    loadMailboxes: vi.fn(),
    loadIdentities: vi.fn(),
  },
}));

vi.mock('../settings/settings.svelte', () => ({
  settings: { undoWindowSec: 0 },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  localeTag: () => 'en',
}));

void scopedDraftsMailbox; // keep the standalone const for readability; used via the mock above

describe('scoped compose refuses the primary-identity fallback (re #212)', () => {
  beforeEach(async () => {
    batchMock.mockReset();
    const { compose } = await import('./compose.svelte');
    compose.close();
  });

  it('send() sets a visible errorMessage and never calls jmap.batch when scoped with no selectedIdentity', async () => {
    const { compose } = await import('./compose.svelte');
    compose.openWith({
      to: 'dest@remote.test',
      subject: 'test',
      body: 'body',
      identity: null,
      scopeAccountId: 'sub1',
    });
    expect(compose.selectedIdentity).toBeNull();

    await compose.send();

    expect(batchMock).not.toHaveBeenCalled();
    expect(compose.errorMessage).toMatch(/identity/i);
    expect(compose.status).toBe('editing');
  });

  it('persistDraft() returns false and never calls jmap.batch under the same conditions', async () => {
    const { compose } = await import('./compose.svelte');
    compose.openWith({
      to: 'dest@remote.test',
      subject: 'test',
      body: 'body',
      identity: null,
      scopeAccountId: 'sub1',
    });

    const ok = await compose.persistDraft();

    expect(ok).toBe(false);
    expect(batchMock).not.toHaveBeenCalled();
  });

  it('send() proceeds past the identity guard when openWith was given a real identity', async () => {
    const { compose } = await import('./compose.svelte');
    const scopedIdentity: Identity = {
      id: '800101',
      name: 'Vorsitz',
      email: 'vorsitz@classic-computing.example',
      replyTo: null,
      bcc: null,
      textSignature: '',
      htmlSignature: '',
      mayDelete: true,
    };
    compose.openWith({
      to: 'dest@remote.test',
      subject: 'test',
      body: 'body',
      identity: scopedIdentity,
      scopeAccountId: 'sub1',
    });
    expect(compose.selectedIdentity?.id).toBe('800101');

    batchMock.mockResolvedValue({ responses: [] });
    await compose.send();

    // strict() is mocked to a no-op vi.fn() with no return value, so it
    // throws on `responses[0]` destructuring downstream -- irrelevant here;
    // the only thing this test pins down is that the identity guard did
    // NOT short-circuit before jmap.batch was reached.
    expect(batchMock).toHaveBeenCalled();
  });
});
