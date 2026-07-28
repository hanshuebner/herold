/**
 * Tests for forwarding carrying over the parent message's regular
 * attachments (re #273). Before the fix, `ComposeStore.openForward` built
 * the forward via `openWith({...})` passing only subject/body/threading
 * context; `openWith` unconditionally reset `attachments = []` and nothing
 * in the forward path read `parent.attachments`, so a forwarded message
 * that itself had attachments was sent with none.
 *
 * `forwardAttachmentsFromParent` is the pure classification + mapping
 * helper (regular attachment -> ready ComposeAttachment referencing the
 * original blobId; inline body images excluded, mirroring
 * AttachmentList.svelte / REQ-MAIL-21). The ComposeStore.openForward
 * describe block below exercises the actual singleton wiring end to end.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { _internals_forTest } from './compose.svelte';
import type { Email, EmailBodyPart } from '../mail/types';

const { forwardAttachmentsFromParent } = _internals_forTest;

function part(overrides: Partial<EmailBodyPart>): EmailBodyPart {
  return {
    partId: null,
    blobId: 'blob-1',
    size: 2200,
    type: 'application/octet-stream',
    charset: null,
    disposition: 'attachment',
    name: 'file.bin',
    cid: null,
    ...overrides,
  };
}

function makeParent(attachments: EmailBodyPart[]): Email {
  return {
    id: 'parent-1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Antonio Hoppe', email: 'antonio@doinstruct.example' }],
    to: [{ name: null, email: 'me@example.local' }],
    subject: 'Einladung: Follow up',
    preview: '',
    receivedAt: '2026-07-16T00:00:00Z',
    hasAttachment: attachments.length > 0,
    attachments,
  } as unknown as Email;
}

describe('forwardAttachmentsFromParent', () => {
  it('carries over regular (non-inline) attachments as ready entries referencing the original blobId', () => {
    const parent = makeParent([
      part({
        blobId: 'blob-ics-1',
        name: '(unnamed)',
        type: 'text/calendar',
        size: 2252,
        disposition: null,
      }),
      part({
        blobId: 'blob-ics-2',
        name: 'invite.ics',
        type: 'application/ics',
        size: 2252,
        disposition: 'attachment',
      }),
    ]);

    const result = forwardAttachmentsFromParent(parent);

    expect(result).toHaveLength(2);
    expect(result[0]).toMatchObject({
      name: '(unnamed)',
      type: 'text/calendar',
      size: 2252,
      status: 'ready',
      blobId: 'blob-ics-1',
      error: null,
    });
    expect(result[1]).toMatchObject({
      name: 'invite.ics',
      type: 'application/ics',
      size: 2252,
      status: 'ready',
      blobId: 'blob-ics-2',
      error: null,
    });
    // Every entry gets a unique key so the UI can render/remove chips.
    expect(new Set(result.map((a) => a.key)).size).toBe(2);
  });

  it('excludes inline images that belong to the quoted body (REQ-MAIL-21)', () => {
    const parent = makeParent([
      part({
        blobId: 'blob-inline',
        name: 'logo.png',
        type: 'image/png',
        disposition: 'inline',
        cid: 'logo@herold.local',
      }),
      part({
        blobId: 'blob-doc',
        name: 'report.pdf',
        type: 'application/pdf',
        disposition: 'attachment',
      }),
    ]);

    const result = forwardAttachmentsFromParent(parent);

    expect(result).toHaveLength(1);
    expect(result[0]?.name).toBe('report.pdf');
  });

  it('skips a regular-disposition part that has no blobId (nothing to reference)', () => {
    const parent = makeParent([
      part({ blobId: null, name: 'ghost.pdf', disposition: 'attachment' }),
    ]);
    expect(forwardAttachmentsFromParent(parent)).toEqual([]);
  });

  it('returns empty for a parent with no attachments', () => {
    expect(forwardAttachmentsFromParent(makeParent([]))).toEqual([]);
  });
});

// ── ComposeStore.openForward integration ──────────────────────────────

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

describe('ComposeStore.openForward (re #273)', () => {
  beforeEach(async () => {
    const { compose } = await import('./compose.svelte');
    compose.close();
  });

  it('seeds compose.attachments with the parent regular attachments as ready chips', async () => {
    const { compose } = await import('./compose.svelte');
    const parent = makeParent([
      part({ blobId: 'blob-ics', name: 'invite.ics', type: 'text/calendar' }),
    ]);

    compose.openForward(parent);

    expect(compose.attachments).toHaveLength(1);
    expect(compose.attachments[0]).toMatchObject({
      name: 'invite.ics',
      type: 'text/calendar',
      status: 'ready',
      blobId: 'blob-ics',
    });
  });

  it('does not add a chip for an inline image referenced by the quoted body', async () => {
    const { compose } = await import('./compose.svelte');
    const parent = makeParent([
      part({
        blobId: 'blob-inline',
        name: 'photo.png',
        type: 'image/png',
        disposition: 'inline',
        cid: 'photo@herold.local',
      }),
    ]);

    compose.openForward(parent);

    expect(compose.attachments).toHaveLength(0);
  });

  it('leaves compose.attachments empty when the parent has none', async () => {
    const { compose } = await import('./compose.svelte');
    compose.openForward(makeParent([]));
    expect(compose.attachments).toHaveLength(0);
  });
});
