/**
 * Regression test for the initial-focus selection (re #284).
 *
 * `compose.replyContext.focusRecipient` is the flag ComposeWindow /
 * ThreadInlineComposer read to decide whether the To `RecipientField` or
 * the `RichEditor` body gets initial focus when a compose opens. Before
 * the fix, focus selection keyed off `replyContext.parentId` alone, so
 * forward — which sets `parentId` just like reply / reply-all but leaves
 * `to` empty — was treated identically to reply and focused the body
 * instead of the empty To field.
 *
 * These tests assert the store-level contract only (`openBlank` /
 * `openReply` / `openReplyAll` / `openForward` each produce the expected
 * `focusRecipient` value); the actual DOM focus behaviour is verified
 * against a live browser per the issue's puppeteer evidence, since
 * `autofocus` effects are not observable in happy-dom.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Email } from '../mail/types';

vi.mock('../jmap/client', () => ({
  jmap: {
    maxUploadSize: null,
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn().mockReturnValue(null),
  },
  strict: vi.fn(),
}));

vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    primaryIdentity: null,
    drafts: null,
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

function makeParent(): Email {
  return {
    id: 'parent-1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Antonio Hoppe', email: 'antonio@doinstruct.example' }],
    to: [{ name: null, email: 'me@example.local' }],
    subject: 'Quarterly numbers',
    preview: '',
    receivedAt: '2026-07-16T00:00:00Z',
    hasAttachment: false,
    attachments: [],
  } as unknown as Email;
}

describe('compose.replyContext.focusRecipient (re #284)', () => {
  beforeEach(async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
  });

  it('is false for a fresh compose (openBlank) — To autofocus comes from an unset parentId instead', async () => {
    const { compose } = await import('./compose.svelte');
    compose.openBlank();
    expect(compose.replyContext.parentId).toBeNull();
    expect(compose.replyContext.focusRecipient).toBe(false);
    expect(compose.to).toBe('');
  });

  it('is false for openReply, which pre-populates To from the parent', async () => {
    const { compose } = await import('./compose.svelte');
    await compose.openReply(makeParent());
    expect(compose.replyContext.parentId).toBe('parent-1');
    expect(compose.replyContext.focusRecipient).toBe(false);
    expect(compose.to).not.toBe('');
  });

  it('is false for openReplyAll, which also pre-populates To from the parent', async () => {
    const { compose } = await import('./compose.svelte');
    compose.openReplyAll(makeParent());
    expect(compose.replyContext.parentId).toBe('parent-1');
    expect(compose.replyContext.focusRecipient).toBe(false);
    expect(compose.to).not.toBe('');
  });

  it('is true for openForward, which leaves To empty despite having a parent', async () => {
    const { compose } = await import('./compose.svelte');
    compose.openForward(makeParent());
    expect(compose.replyContext.parentId).toBe('parent-1');
    expect(compose.replyContext.focusRecipient).toBe(true);
    expect(compose.to).toBe('');
  });
});
