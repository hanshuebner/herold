/**
 * Regression test (re #507): undo-cancelling a send must not leave the
 * Email sitting in Sent. EmailSubmission/create applies the Drafts -> Sent
 * move and clears $draft as an implicit onSuccessUpdateEmail patch at
 * submission-creation time (REQ-MAIL-14, REQ-OPT-11), before the undo
 * window elapses. The undo callback destroys the EmailSubmission but, pre-
 * fix, never reverted that patch -- the pins below assert the undo callback
 * now:
 *   1. Issues EmailSubmission/set { destroy } first (unchanged).
 *   2. Issues a follow-up Email/set that moves the Email back out of Sent
 *      and into Drafts and restores $draft.
 *   3. Re-opens the composer with draftId pointing at that same Email id,
 *      so a later send() updates the row in place instead of creating a
 *      fresh draft (which is what produced the duplicate Sent copy).
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
  // Mirrors the real strict(): throws on the first 'error' invocation so
  // the revert-fails test case exercises the actual catch path.
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

vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    primaryIdentity,
    drafts: { id: 'drafts1' },
    sent: { id: 'sent1' },
    archive: { id: 'archive1' },
    identities: new Map([[primaryIdentity.id, primaryIdentity]]),
    mailboxes: new Map([['drafts1', {}]]),
    loadMailboxes: vi.fn(),
    loadIdentities: vi.fn(),
  },
}));

vi.mock('../mail/sub-accounts.svelte', () => ({
  subAccounts: { find: vi.fn().mockReturnValue(undefined) },
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

describe('compose undo callback restores the Sent copy to Drafts (re #507)', () => {
  beforeEach(() => {
    batchMock.mockReset();
    toastShow.mockReset();
  });

  it('issues destroy, then a Drafts-restoring Email/set, then re-opens with the same draft id', async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
    compose.openWith({
      to: 'dest@remote.test',
      subject: 'undo me',
      body: 'body text',
    });

    // Call 1: the initial send's batch (Email/set create + EmailSubmission/set create).
    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      expect(calls.map((c) => c.name)).toEqual(['Email/set', 'EmailSubmission/set']);
      return Promise.resolve({
        responses: [
          ['Email/set', { created: { draft1: { id: 'email-42' } } }, 'c0'],
          ['EmailSubmission/set', { created: { sub1: { id: 'sub-99' } } }, 'c1'],
        ],
      });
    });

    await compose.send();

    // The "Message sent" toast carries the undo callback.
    expect(toastShow).toHaveBeenCalledTimes(1);
    const toastArgs = toastShow.mock.calls[0]![0] as { undo?: () => Promise<void> };
    expect(toastArgs.undo).toBeTypeOf('function');

    // Call 2: EmailSubmission/set { destroy }.
    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      expect(calls).toHaveLength(1);
      expect(calls[0]!.name).toBe('EmailSubmission/set');
      expect(calls[0]!.args.destroy).toEqual(['sub-99']);
      return Promise.resolve({
        responses: [['EmailSubmission/set', { destroyed: ['sub-99'] }, 'c0']],
      });
    });

    // Call 3: the Drafts-restoring Email/set.
    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      expect(calls).toHaveLength(1);
      const call = calls[0]!;
      expect(call.name).toBe('Email/set');
      const update = (call.args.update as Record<string, Record<string, unknown>>)['email-42'];
      expect(update, 'the revert must target the Email id the submission sent').toBeDefined();
      // Moves out of Sent, back into Drafts, and restores $draft.
      expect(update!['mailboxIds/sent1']).toBeNull();
      expect(update!['mailboxIds/drafts1']).toBe(true);
      expect(update!['keywords/$draft']).toBe(true);
      return Promise.resolve({
        responses: [['Email/set', { updated: { 'email-42': null } }, 'c0']],
      });
    });

    await toastArgs.undo!();

    // Exactly 3 batches total: send, destroy, revert.
    expect(batchMock).toHaveBeenCalledTimes(3);

    // No "Could not cancel send" error toast.
    expect(toastShow).toHaveBeenCalledTimes(1);

    // The composer re-opened editing the SAME Email id the submission had
    // sent, so a re-send updates that row instead of creating a new draft
    // (the duplicate-Sent-copy mechanism this issue reports).
    expect(compose.editingDraftId).toBe('email-42');
    expect(compose.to).toBe('dest@remote.test');
    expect(compose.subject).toBe('undo me');
  });

  it('still re-opens the composer (without pinning a draft id) when the Drafts-restore Email/set fails', async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
    compose.openWith({
      to: 'dest2@remote.test',
      subject: 'undo me too',
      body: 'body text',
    });

    batchMock.mockImplementationOnce(() =>
      Promise.resolve({
        responses: [
          ['Email/set', { created: { draft1: { id: 'email-77' } } }, 'c0'],
          ['EmailSubmission/set', { created: { sub1: { id: 'sub-77' } } }, 'c1'],
        ],
      }),
    );
    await compose.send();
    const toastArgs = toastShow.mock.calls[0]![0] as { undo?: () => Promise<void> };

    // destroy succeeds...
    batchMock.mockImplementationOnce(() =>
      Promise.resolve({
        responses: [['EmailSubmission/set', { destroyed: ['sub-77'] }, 'c0']],
      }),
    );
    // ...but the revert Email/set fails.
    batchMock.mockImplementationOnce(() =>
      Promise.resolve({
        responses: [
          ['error', { type: 'serverFail', description: 'boom' }, 'c0'],
        ],
      }),
    );

    await toastArgs.undo!();

    // The failed revert is swallowed (best-effort) -- no "Could not cancel
    // send" toast, since the destroy itself (the thing that actually
    // mattered to the user -- stopping delivery) succeeded.
    expect(toastShow).toHaveBeenCalledTimes(1);
    // editingDraftId stays null: the Email never actually moved back to
    // Drafts, so a resend must create a fresh draft rather than updating a
    // row still sitting in Sent.
    expect(compose.editingDraftId).toBeNull();
    expect(compose.to).toBe('dest2@remote.test');
  });
});
