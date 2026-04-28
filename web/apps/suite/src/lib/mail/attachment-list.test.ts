/**
 * Unit tests for G16 inline-image count in "Download all (N)" (REQ-ATT-41).
 *
 * Verifies:
 *   - totalCount = attachments + inline images.
 *   - The chip strip surfaces both kinds.
 *   - The "Download all" button label reflects the combined count.
 */
import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import AttachmentList from './AttachmentList.svelte';
import type { Email, EmailBodyPart } from './types';

// Stub auth so accountId is available.
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct1' },
    },
  },
}));

// Stub JMAP client — downloadUrl returns a predictable string.
vi.mock('../jmap/client', () => ({
  jmap: {
    downloadUrl: ({ blobId, name }: { blobId: string; name: string }) =>
      `/jmap/download/acct1/${blobId}/${encodeURIComponent(name)}`,
  },
}));

// Stub i18n — pass-through for the keys used in AttachmentList.
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, unknown>): string => {
    const map: Record<string, string> = {
      'att.attachments': `${args?.count} attachment`,
      'att.attachments.other': `${args?.count} attachments`,
      'att.inlineImages': 'Inline images',
      'att.downloadAll': `Download all (${args?.count})`,
      'att.attachmentsOnly': 'Attachments only',
      'att.download': 'Download',
      'att.noUrl': 'No URL',
    };
    return map[key] ?? key;
  },
}));

// Stub zipBlobsAsDownload so "Download all" click doesn't try to fetch.
vi.mock('./download-zip', () => ({
  zipBlobsAsDownload: vi.fn().mockResolvedValue(undefined),
}));

function makeEmail(parts: Partial<EmailBodyPart>[]): Email {
  const attachments: EmailBodyPart[] = parts.map((p, i) => ({
    partId: `p${i}`,
    blobId: `b${i}`,
    size: 1024,
    type: 'image/png',
    charset: null,
    disposition: null,
    name: `file${i}.png`,
    cid: null,
    ...p,
  }));
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: 'Test',
    preview: '',
    receivedAt: '2026-04-28T00:00:00Z',
    hasAttachment: true,
    attachments,
  } as unknown as Email;
}

describe('AttachmentList: inline images count in Download all', () => {
  it('shows no "Download all" when there is only one attachment', () => {
    const email = makeEmail([{ disposition: 'attachment', name: 'file.pdf', type: 'application/pdf' }]);
    render(AttachmentList, { email });
    expect(screen.queryByText(/Download all/i)).toBeNull();
  });

  it('shows "Download all (N)" where N = attachments + inline images', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'attachment', name: 'data.csv', type: 'text/csv' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    // Should show "Download all (3)"
    expect(screen.getByText('Download all (3)')).toBeInTheDocument();
  });

  it('shows the "Inline images" section header when inline parts exist', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('Inline images')).toBeInTheDocument();
  });

  it('shows "Attachments only" secondary action when both kinds are present', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('Attachments only')).toBeInTheDocument();
  });

  it('does NOT show "Attachments only" when there are no inline images', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'a.pdf', type: 'application/pdf' },
      { disposition: 'attachment', name: 'b.pdf', type: 'application/pdf' },
    ]);
    render(AttachmentList, { email });
    expect(screen.queryByText('Attachments only')).toBeNull();
  });

  it('renders a chip for each inline image', () => {
    const email = makeEmail([
      { disposition: 'inline', name: 'img1.png', cid: 'c1@h.test', type: 'image/png' },
      { disposition: 'inline', name: 'img2.jpg', cid: 'c2@h.test', type: 'image/jpeg' },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('img1.png')).toBeInTheDocument();
    expect(screen.getByText('img2.jpg')).toBeInTheDocument();
  });

  it('renders nothing when email has no attachments', () => {
    const email = makeEmail([]);
    const { container } = render(AttachmentList, { email });
    expect(container.querySelector('section')).toBeNull();
  });

  it('uses fallback name for inline images without a name', () => {
    const email = makeEmail([
      { disposition: 'inline', name: null, cid: 'c1@h.test', type: 'image/png', blobId: 'b0' },
    ]);
    render(AttachmentList, { email });
    // fallback = "inline-1.png"
    expect(screen.getByText('inline-1.png')).toBeInTheDocument();
  });
});
