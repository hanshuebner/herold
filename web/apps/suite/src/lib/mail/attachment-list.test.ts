/**
 * Unit tests for the mail attachment chip strip.
 *
 * REQ-MAIL-21: inline images (disposition=inline) must NOT appear in the
 * attachment chip strip — they belong to the rendered body and a duplicate
 * chip is the wrong UX, even when cid resolution failed at render time.
 *
 * REQ-MAIL-23: image and PDF chips render a "View" affordance that opens
 * the shared Lightbox component (see lib/preview/Lightbox.svelte).
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
      'att.downloadAll': `Download all (${args?.count})`,
      'att.attachmentsOnly': 'Attachments only',
      'att.download': 'Download',
      'att.view': 'View',
      'att.noUrl': 'No URL',
      'att.close': 'Close',
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

describe('AttachmentList: inline images stay out of the chip strip (REQ-MAIL-21)', () => {
  it('hides every disposition=inline part, regardless of cid resolution', () => {
    const email = makeEmail([
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    const { container } = render(AttachmentList, { email });
    expect(container.querySelector('section')).toBeNull();
    expect(screen.queryByText('photo.png')).toBeNull();
  });

  it('hides inline parts even with no cid (e.g. malformed inbound MIME)', () => {
    const email = makeEmail([
      { disposition: 'inline', name: 'noncid.png', cid: null, type: 'image/png' },
    ]);
    const { container } = render(AttachmentList, { email });
    expect(container.querySelector('section')).toBeNull();
  });

  it('still renders non-inline (regular) attachments', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('doc.pdf')).toBeInTheDocument();
    expect(screen.queryByText('photo.png')).toBeNull();
  });
});

describe('AttachmentList: View action (REQ-MAIL-23)', () => {
  it('renders a View button on image attachments', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'pic.png', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('View')).toBeInTheDocument();
  });

  it('renders a View button on PDF attachments', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('View')).toBeInTheDocument();
  });

  it('does not render a View button for non-previewable types', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'data.zip', type: 'application/zip' },
    ]);
    render(AttachmentList, { email });
    expect(screen.queryByText('View')).toBeNull();
  });
});

describe('AttachmentList: Download all bulk action', () => {
  it('shows no "Download all" when there is only one attachment', () => {
    const email = makeEmail([{ disposition: 'attachment', name: 'file.pdf', type: 'application/pdf' }]);
    render(AttachmentList, { email });
    expect(screen.queryByText(/Download all/i)).toBeNull();
  });

  it('shows "Download all (N)" counting both attachments and inline parts', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'attachment', name: 'data.csv', type: 'text/csv' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('Download all (3)')).toBeInTheDocument();
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

  it('renders nothing when email has no attachments', () => {
    const email = makeEmail([]);
    const { container } = render(AttachmentList, { email });
    expect(container.querySelector('section')).toBeNull();
  });
});
