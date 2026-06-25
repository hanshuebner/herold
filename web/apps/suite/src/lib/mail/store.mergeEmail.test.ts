/**
 * Unit tests for the mergeEmailListFetch helper (re #31 residual).
 *
 * A list-properties fetch (EMAIL_LIST_PROPERTIES) does not include
 * bodyValues / htmlBody / textBody / attachments / etc. When it writes
 * its result into the email cache it must not overwrite an existing
 * body-complete entry, because the thread reader would regress to
 * "(no body)" until the user reloads.
 */

import { describe, it, expect, vi } from 'vitest';
import type { Email, EmailBodyValue, EmailBodyPart } from './types';

// Mock all heavy store dependencies so the module loads cleanly.
vi.mock('../jmap/sync.svelte', () => ({ sync: { on: vi.fn(() => vi.fn()) } }));
vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
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

const { mergeEmailListFetch } = await import('./store.svelte');

// Minimal list-properties email (no bodyValues).
function makeListEmail(overrides: Partial<Email> = {}): Email {
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: { mb1: true },
    keywords: {},
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    subject: 'Hello',
    preview: 'Hello world',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: '',
    ...overrides,
  };
}

// Body-complete email (has bodyValues).
function makeBodyEmail(overrides: Partial<Email> = {}): Email {
  const bodyVal: EmailBodyValue = { value: '<p>Hello</p>', isEncodingProblem: false, isTruncated: false };
  const part: EmailBodyPart = { partId: 'p1', blobId: 'b1', size: 12, type: 'text/html', charset: 'utf-8', disposition: null, name: null, cid: null };
  return {
    ...makeListEmail(),
    bodyValues: { p1: bodyVal },
    htmlBody: [part],
    textBody: [],
    attachments: [],
    blobId: 'blob-1',
    cc: [{ name: 'Bob', email: 'bob@example.test' }],
    bcc: null,
    replyTo: null,
    sentAt: '2026-01-01T00:00:00Z',
    messageId: ['<abc@example.test>'],
    inReplyTo: null,
    references: null,
    reactions: { '+1': ['p-alice'] },
    ...overrides,
  };
}

describe('mergeEmailListFetch (re #31 residual)', () => {
  it('returns incoming as-is when it has bodyValues (body-complete fetch)', () => {
    const bodyEmail = makeBodyEmail({ id: 'e1' });
    const existing = makeListEmail({ id: 'e1' });
    const result = mergeEmailListFetch(existing, bodyEmail);
    expect(result).toBe(bodyEmail);
  });

  it('returns incoming as-is when there is no existing cache entry', () => {
    const listEmail = makeListEmail();
    const result = mergeEmailListFetch(undefined, listEmail);
    expect(result).toBe(listEmail);
  });

  it('returns incoming as-is when existing has no bodyValues either', () => {
    const listEmail = makeListEmail({ subject: 'Updated subject' });
    const existing = makeListEmail({ subject: 'Old subject' });
    const result = mergeEmailListFetch(existing, listEmail);
    expect(result).toBe(listEmail);
  });

  it('preserves bodyValues when incoming is a list-only update', () => {
    const existing = makeBodyEmail();
    const incoming = makeListEmail({ subject: 'Updated subject', mailboxIds: { mb2: true } });
    const result = mergeEmailListFetch(existing, incoming);
    // Fresh metadata from incoming.
    expect(result.subject).toBe('Updated subject');
    expect(result.mailboxIds).toEqual({ mb2: true });
    // Body fields preserved from existing.
    expect(result.bodyValues).toEqual(existing.bodyValues);
  });

  it('preserves htmlBody, textBody, attachments from existing', () => {
    const existing = makeBodyEmail();
    const incoming = makeListEmail();
    const result = mergeEmailListFetch(existing, incoming);
    expect(result.htmlBody).toEqual(existing.htmlBody);
    expect(result.textBody).toEqual(existing.textBody);
    expect(result.attachments).toEqual(existing.attachments);
  });

  it('preserves cc, bcc, replyTo, sentAt, messageId from existing', () => {
    const existing = makeBodyEmail();
    const incoming = makeListEmail();
    const result = mergeEmailListFetch(existing, incoming);
    expect(result.cc).toEqual(existing.cc);
    expect(result.bcc).toEqual(existing.bcc);
    expect(result.replyTo).toEqual(existing.replyTo);
    expect(result.sentAt).toEqual(existing.sentAt);
    expect(result.messageId).toEqual(existing.messageId);
  });

  it('preserves reactions and blobId from existing', () => {
    const existing = makeBodyEmail();
    const incoming = makeListEmail({ blobId: '' });
    const result = mergeEmailListFetch(existing, incoming);
    expect(result.reactions).toEqual(existing.reactions);
    expect(result.blobId).toBe(existing.blobId);
  });

  it('uses fresh keywords and mailboxIds from incoming even when preserving body', () => {
    const existing = makeBodyEmail({ keywords: { '$seen': true }, mailboxIds: { mb1: true } });
    const incoming = makeListEmail({ keywords: {}, mailboxIds: { mb2: true } });
    const result = mergeEmailListFetch(existing, incoming);
    expect(result.keywords).toEqual({});
    expect(result.mailboxIds).toEqual({ mb2: true });
    // Body still preserved.
    expect(result.bodyValues).toEqual(existing.bodyValues);
  });
});
