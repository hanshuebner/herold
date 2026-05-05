/**
 * Wave 3.14 — email reactions unit tests.
 *
 * Test 1: toggling a reaction patches the reactions map optimistically
 *         and reconciles on success (REQ-MAIL-171).
 * Test 2: cross-server confirmation appears for list mail with > 5
 *         recipients on first reaction (REQ-MAIL-191).
 *
 * The JMAP client and auth singleton are mocked so these tests run
 * without a live server.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { mail } from './store.svelte';
import {
  reactionConfirm,
  _internals_forTest as confirmInternals,
} from './reaction-confirm.svelte';
import type { Email } from './types';

// ── JMAP client mock ──────────────────────────────────────────────────────
vi.mock('../jmap/client', () => {
  let batchImpl:
    | (() => Promise<{ responses: unknown[]; sessionState: string }>)
    | (() => { responses: unknown[]; sessionState: string })
    | null = null;

  const jmap = {
    batch: vi.fn(async (builder: (b: unknown) => void) => {
      // Build a minimal builder that records calls but returns nothing;
      // the real response is provided by setNextBatchResult.
      const calls: unknown[] = [];
      builder({
        call: (_name: string, _args: unknown, _using: string[]) => {
          calls.push({ name: _name, args: _args });
          return {
            ref: (path: string) => ({
              resultOf: `c${calls.length - 1}`,
              name: _name,
              path,
            }),
          };
        },
      });
      if (batchImpl) return batchImpl();
      return { responses: [], sessionState: 'state-1' };
    }),
    hasCapability: vi.fn(() => true),
    downloadUrl: vi.fn(() => null),
  };

  return {
    jmap,
    strict: (responses: unknown[]) => {
      for (const r of responses) {
        if (Array.isArray(r) && r[0] === 'error') {
          throw new Error((r[1] as { description?: string }).description ?? 'method error');
        }
      }
      return responses;
    },
    __setBatchImpl: (impl: typeof batchImpl) => {
      batchImpl = impl;
    },
  };
});

// ── Auth mock ─────────────────────────────────────────────────────────────
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'account-1',
        'urn:ietf:params:jmap:submission': 'account-1',
      },
      capabilities: {
        'urn:ietf:params:jmap:mail': {},
        'https://netzhansa.com/jmap/email-reactions': {},
      },
      apiUrl: '/jmap',
      downloadUrl: '/jmap/download/{accountId}/{blobId}/{name}?accept={type}',
      uploadUrl: '/jmap/upload/{accountId}/',
      eventSourceUrl: '/jmap/eventsource/',
      username: 'test@example.com',
      accounts: {},
      state: 'sess-1',
    },
    principalId: 'principal-alice',
    errorMessage: null,
    needsStepUp: false,
    loadMe: vi.fn(),
    bootstrap: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

// ── Sync mock ─────────────────────────────────────────────────────────────
vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(),
    start: vi.fn(),
    stop: vi.fn(),
  },
}));

// ── Toast mock ────────────────────────────────────────────────────────────
vi.mock('../toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    undo: vi.fn(),
    current: null,
  },
}));

// ── Helper: inject an email into the store cache directly ─────────────────

function makeEmail(partial: Partial<Email> & { id: string }): Email {
  return {
    threadId: 't-1',
    mailboxIds: { 'mb-inbox': true },
    keywords: {},
    from: [{ name: 'Alice', email: 'alice@example.com' }],
    to: [{ name: 'Bob', email: 'bob@example.com' }],
    subject: 'Test subject',
    preview: 'Preview text',
    receivedAt: '2026-04-28T10:00:00Z',
    hasAttachment: false,
    blobId: 'blob-stub',
    reactions: null,
    ...partial,
  };
}

function seedEmail(email: Email): void {
  const next = new Map((mail as unknown as { emails: Map<string, Email> }).emails);
  next.set(email.id, email);
  (mail as unknown as { emails: Map<string, Email> }).emails = next;
}

// Helper to get the current email from the store
function getEmail(id: string): Email | undefined {
  return (mail as unknown as { emails: Map<string, Email> }).emails.get(id);
}

// ── get the mock module ───────────────────────────────────────────────────
async function getJmapMock() {
  return (await import('../jmap/client')) as unknown as {
    jmap: ReturnType<typeof vi.fn>;
    strict: (responses: unknown[]) => unknown[];
    __setBatchImpl: (
      impl:
        | (() => Promise<{ responses: unknown[]; sessionState: string }>)
        | (() => { responses: unknown[]; sessionState: string })
        | null,
    ) => void;
  };
}

// ─────────────────────────────────────────────────────────────────────────
// Test suite 1: optimistic reaction toggle
// ─────────────────────────────────────────────────────────────────────────

describe('toggleReaction — optimistic patch and reconciliation', () => {
  const EMAIL_ID = 'email-react-1';
  const MY_ID = 'principal-alice';

  beforeEach(() => {
    seedEmail(
      makeEmail({
        id: EMAIL_ID,
        reactions: null,
      }),
    );
  });

  it('adds the reactor optimistically before the server responds', async () => {
    const mock = await getJmapMock();
    // Delay the server response so we can observe the optimistic state.
    let resolveServer!: (v: { responses: unknown[]; sessionState: string }) => void;
    const serverPromise = new Promise<{ responses: unknown[]; sessionState: string }>(
      (res) => {
        resolveServer = res;
      },
    );
    mock.__setBatchImpl(() => serverPromise);

    const togglePromise = mail.toggleReaction(EMAIL_ID, '\u{1F44D}', MY_ID);

    // Optimistic state should be applied synchronously (before await).
    const optimistic = getEmail(EMAIL_ID);
    expect(optimistic?.reactions).toBeDefined();
    expect(optimistic?.reactions?.['\u{1F44D}']).toContain(MY_ID);

    // Resolve the server response with a successful update.
    resolveServer({
      responses: [
        [
          'Email/set',
          { updated: { [EMAIL_ID]: null }, notUpdated: null },
          'c0',
        ],
      ],
      sessionState: 'state-2',
    });

    await togglePromise;

    // After reconciliation the reaction should still be present.
    const reconciled = getEmail(EMAIL_ID);
    expect(reconciled?.reactions?.['\u{1F44D}']).toContain(MY_ID);

    mock.__setBatchImpl(null);
  });

  it('removes an existing reaction when the user already reacted (toggle off)', async () => {
    seedEmail(
      makeEmail({
        id: EMAIL_ID,
        reactions: { '\u{1F44D}': [MY_ID, 'principal-bob'] },
      }),
    );

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'Email/set',
          { updated: { [EMAIL_ID]: null }, notUpdated: null },
          'c0',
        ],
      ],
      sessionState: 'state-2',
    }));

    await mail.toggleReaction(EMAIL_ID, '\u{1F44D}', MY_ID);

    const after = getEmail(EMAIL_ID);
    // Alice's entry removed; Bob's stays.
    expect(after?.reactions?.['\u{1F44D}']).not.toContain(MY_ID);
    expect(after?.reactions?.['\u{1F44D}']).toContain('principal-bob');

    mock.__setBatchImpl(null);
  });

  it('reverts the optimistic patch when the server returns forbidden', async () => {
    const { toast } = (await import('../toast/toast.svelte')) as unknown as {
      toast: { show: ReturnType<typeof vi.fn> };
    };
    toast.show.mockClear();

    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'Email/set',
          {
            notUpdated: {
              [EMAIL_ID]: { type: 'forbidden', description: 'Not allowed' },
            },
          },
          'c0',
        ],
      ],
      sessionState: 'state-2',
    }));

    await mail.toggleReaction(EMAIL_ID, '\u{1F44D}', MY_ID);

    // Reactions should be reverted to null (original state).
    const after = getEmail(EMAIL_ID);
    const r = after?.reactions;
    // Either null or empty — no entry for the emoji.
    const hasEntry = r && r['\u{1F44D}'] && r['\u{1F44D}'].includes(MY_ID);
    expect(hasEntry).toBeFalsy();

    // A toast error should have been shown.
    expect(toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );

    mock.__setBatchImpl(null);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// Test suite 2: cross-server confirmation for list mail > 5 recipients
// ─────────────────────────────────────────────────────────────────────────

describe('cross-server reaction confirmation (REQ-MAIL-191)', () => {
  const { isListConfirmed, saveListConfirmed } = confirmInternals;

  beforeEach(() => {
    // Clear any localStorage state from prior tests.
    try {
      localStorage.clear();
    } catch {
      // Ignore in environments without localStorage.
    }
    // Reset pending state.
    (reactionConfirm as unknown as { pending: null }).pending = null;
  });

  it('does not show confirmation for non-list mail', () => {
    const onProceed = vi.fn();
    const onAbort = vi.fn();

    const needed = reactionConfirm.needsConfirm({
      listId: null,
      totalRecipients: 10,
      emailId: 'e-1',
      emoji: '\u{1F44D}',
      onProceed,
      onAbort,
    });

    expect(needed).toBe(false);
    expect(reactionConfirm.pending).toBeNull();
    expect(onProceed).not.toHaveBeenCalled();
  });

  it('does not show confirmation when recipient count is <= 5', () => {
    const needed = reactionConfirm.needsConfirm({
      listId: '<mylist.example.com>',
      totalRecipients: 5,
      emailId: 'e-1',
      emoji: '\u{1F44D}',
      onProceed: vi.fn(),
      onAbort: vi.fn(),
    });

    expect(needed).toBe(false);
    expect(reactionConfirm.pending).toBeNull();
  });

  it('shows confirmation for list mail with > 5 recipients on first reaction', () => {
    const onProceed = vi.fn();
    const onAbort = vi.fn();

    const needed = reactionConfirm.needsConfirm({
      listId: '<mylist.example.com>',
      totalRecipients: 6,
      emailId: 'e-1',
      emoji: '\u{1F44D}',
      onProceed,
      onAbort,
    });

    expect(needed).toBe(true);
    expect(reactionConfirm.pending).not.toBeNull();
    expect(reactionConfirm.pending?.totalRecipients).toBe(6);
    expect(reactionConfirm.pending?.listId).toBe('<mylist.example.com>');
  });

  it('calls onProceed and clears pending when confirmed', () => {
    const onProceed = vi.fn();
    const onAbort = vi.fn();

    reactionConfirm.needsConfirm({
      listId: '<mylist.example.com>',
      totalRecipients: 8,
      emailId: 'e-2',
      emoji: '\u{2764}\u{FE0F}',
      onProceed,
      onAbort,
    });

    reactionConfirm.pending?.onConfirm(false);

    expect(onProceed).toHaveBeenCalledOnce();
    expect(reactionConfirm.pending).toBeNull();
  });

  it('calls onAbort and clears pending when cancelled', () => {
    const onProceed = vi.fn();
    const onAbort = vi.fn();

    reactionConfirm.needsConfirm({
      listId: '<mylist.example.com>',
      totalRecipients: 8,
      emailId: 'e-3',
      emoji: '\u{1F525}',
      onProceed,
      onAbort,
    });

    reactionConfirm.pending?.onCancel();

    expect(onAbort).toHaveBeenCalledOnce();
    expect(reactionConfirm.pending).toBeNull();
    expect(onProceed).not.toHaveBeenCalled();
  });

  it('persists the dont-ask-again choice and skips confirmation on subsequent reactions', () => {
    const listId = '<mylist.example.com>';

    // First reaction — confirm with "don't ask again".
    reactionConfirm.needsConfirm({
      listId,
      totalRecipients: 7,
      emailId: 'e-4',
      emoji: '\u{1F44D}',
      onProceed: vi.fn(),
      onAbort: vi.fn(),
    });
    reactionConfirm.pending?.onConfirm(true);

    // isListConfirmed should now return true.
    expect(isListConfirmed(listId)).toBe(true);

    // Second reaction — should skip the confirmation entirely.
    const onProceed2 = vi.fn();
    const needed2 = reactionConfirm.needsConfirm({
      listId,
      totalRecipients: 7,
      emailId: 'e-5',
      emoji: '\u{1F389}',
      onProceed: onProceed2,
      onAbort: vi.fn(),
    });

    expect(needed2).toBe(false);
    expect(reactionConfirm.pending).toBeNull();
  });

  it('saveListConfirmed / isListConfirmed round-trip', () => {
    const id = '<another-list.example.com>';
    expect(isListConfirmed(id)).toBe(false);
    saveListConfirmed(id);
    expect(isListConfirmed(id)).toBe(true);
  });
});
