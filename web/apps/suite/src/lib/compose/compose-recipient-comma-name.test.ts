/**
 * Regression tests (re #510): a recipient display name containing a
 * comma (or another RFC 5322 "special" character) must survive the
 * compose string round trip that openWith/Undo-restore/reply-prefill
 * all go through -- it must come back as the single original
 * recipient, never split into stray tokens and never dropped.
 *
 * Covers:
 *   1. Undo re-open: send() -> toast undo -> openWith's string
 *      round trip -- the re-opened composer's To must still carry the
 *      one correct recipient, and a resend must submit exactly that
 *      one address (not a bare-name token with no domain).
 *   2. Reply prefill: openReply() on a parent From a comma-display-name
 *      sender must populate To with that one recipient, not leave it
 *      empty.
 *   3. Send-path guard: a stale string-only recipient field that does
 *      not parse to a real address must refuse to send rather than
 *      silently submit a malformed RCPT TO.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Email, Identity } from '../mail/types';
import { recipientToString } from './recipient-parse';

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

describe('Undo re-open preserves a comma display name (re #510)', () => {
  beforeEach(() => {
    batchMock.mockReset();
    toastShow.mockReset();
  });

  it('re-sends the single correct recipient after Undo, not a split/bare token', async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
    compose.openWith({
      to: '',
      subject: 'undo comma name',
      body: 'body text',
    });
    // Mirrors ComposeWindow.svelte's RecipientField onChipsChange handler:
    // a chip picked from autocomplete (or typed via a recognized pattern)
    // sets the structured array and re-derives the string field from it.
    const recipient = { name: 'Surname, Firstname', email: 'user@example.org' };
    compose.toRecipients = [recipient];
    compose.to = [recipient].map(recipientToString).join(', ');
    // The serialized field string must be the quoted form so it survives
    // the Undo round trip below.
    expect(compose.to).toBe('"Surname, Firstname" <user@example.org>');

    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      const subCall = calls.find((c) => c.name === 'EmailSubmission/set')!;
      const created = (subCall.args.create as Record<string, Record<string, unknown>>).sub1!;
      const envelope = created.envelope as { rcptTo: { email: string }[] };
      // Pre-fix this carried [{email:"Surname"},{email:"user@example.org"}].
      expect(envelope.rcptTo).toEqual([{ email: 'user@example.org' }]);
      return Promise.resolve({
        responses: [
          ['Email/set', { created: { draft1: { id: 'email-1' } } }, 'c0'],
          ['EmailSubmission/set', { created: { sub1: { id: 'sub-1' } } }, 'c1'],
        ],
      });
    });

    await compose.send();
    const toastArgs = toastShow.mock.calls[0]![0] as { undo?: () => Promise<void> };

    batchMock.mockImplementationOnce(() =>
      Promise.resolve({ responses: [['EmailSubmission/set', { destroyed: ['sub-1'] }, 'c0']] }),
    );
    batchMock.mockImplementationOnce(() =>
      Promise.resolve({ responses: [['Email/set', { updated: { 'email-1': null } }, 'c0']] }),
    );

    await toastArgs.undo!();

    // The re-opened composer must still carry the one recipient with its
    // display name intact -- not split into "Surname" + the bare address.
    expect(compose.toRecipients).toEqual([
      { name: 'Surname, Firstname', email: 'user@example.org' },
    ]);

    batchMock.mockImplementationOnce((builder) => {
      const calls = captureCalls(builder);
      const subCall = calls.find((c) => c.name === 'EmailSubmission/set')!;
      const created = (subCall.args.create as Record<string, Record<string, unknown>>).sub1!;
      const envelope = created.envelope as { rcptTo: { email: string }[] };
      expect(envelope.rcptTo).toEqual([{ email: 'user@example.org' }]);
      return Promise.resolve({
        responses: [
          ['Email/set', { updated: { 'email-1': null } }, 'c0'],
          ['EmailSubmission/set', { created: { sub1: { id: 'sub-2' } } }, 'c1'],
        ],
      });
    });
    await compose.send();
  });
});

describe('Reply prefill populates To for a comma-display-name sender (re #510)', () => {
  beforeEach(() => {
    batchMock.mockReset();
    toastShow.mockReset();
  });

  function parentFrom(name: string, email: string): Email {
    return {
      id: 'parent-1',
      threadId: 'thread-1',
      mailboxIds: {},
      keywords: {},
      from: [{ name, email }],
      to: [{ name: null, email: primaryIdentity.email }],
      cc: null,
      bcc: null,
      subject: 'Hello',
      sentAt: '2026-10-04T12:00:00Z',
      receivedAt: '2026-10-04T12:00:01Z',
      messageId: ['<parent@example.org>'],
      inReplyTo: null,
      references: null,
      preview: '',
      hasAttachment: false,
      attachments: [],
      bodyValues: {},
      textBody: [],
      htmlBody: [],
    } as unknown as Email;
  }

  it('populates the To chip from a From header with a comma display name', async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
    const parent = parentFrom('Surname, Firstname', 'user@example.org');

    await compose.openReply(parent);

    // Pre-fix this was [] -- the empty-To-field defect from the ticket.
    expect(compose.toRecipients).toEqual([
      { name: 'Surname, Firstname', email: 'user@example.org' },
    ]);
    expect(compose.to).toContain('user@example.org');
  });
});

describe('send() refuses an unparseable leftover recipient (re #510)', () => {
  beforeEach(() => {
    batchMock.mockReset();
    toastShow.mockReset();
  });

  it('sets a visible error and never calls jmap.batch for a bare, domain-less token', async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
    compose.openWith({
      to: 'placeholder@example.org',
      subject: 'guard test',
      body: 'body text',
    });
    // Simulate a stale string-only field (e.g. a snapshot-restore path
    // that only carried the string form): toRecipients empty, but the
    // string holds a token with no @domain.
    compose.toRecipients = [];
    compose.to = 'Surname';

    await compose.send();

    expect(compose.errorMessage).toMatch(/not a valid recipient/i);
    expect(batchMock).not.toHaveBeenCalled();
  });
});
