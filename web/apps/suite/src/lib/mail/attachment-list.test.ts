/**
 * Unit tests for the mail attachment card strip.
 *
 * REQ-MAIL-21: inline images (disposition=inline) must NOT appear in the
 * attachment card strip — they belong to the rendered body and a duplicate
 * card is the wrong UX, even when cid resolution failed at render time.
 *
 * REQ-MAIL-23: image and PDF cards render a "View" affordance that opens
 * the shared Lightbox component (see lib/preview/Lightbox.svelte).
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import AttachmentList from './AttachmentList.svelte';
import { zipBlobsAsDownload } from './download-zip';
import type { Email, EmailBodyPart } from './types';

// Stub auth so accountId is available.
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct1' },
    },
  },
  registerAccountResetCallback: vi.fn(),
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
      'att.aria.open': `Open ${args?.name ?? ''}`,
      'att.aria.download': `Download ${args?.name ?? ''}`,
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

describe('AttachmentList: inline images stay out of the card strip (REQ-MAIL-21)', () => {
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

describe('AttachmentList: undecodable inline images get a chip (issue #269, REQ-ATT-26)', () => {
  it('does not chip an inline image absent from undecodableInlineUrls (regression: REQ-MAIL-21)', () => {
    const email = makeEmail([
      {
        disposition: 'inline',
        name: 'logo.png',
        cid: 'logo@h.test',
        type: 'image/png',
        blobId: 'b0',
      },
    ]);
    const { container } = render(AttachmentList, {
      email,
      undecodableInlineUrls: new Set<string>(),
    });
    expect(container.querySelector('section')).toBeNull();
    expect(screen.queryByText('logo.png')).toBeNull();
  });

  it('chips an inline image whose resolved URL is in undecodableInlineUrls', () => {
    const email = makeEmail([
      {
        disposition: 'inline',
        name: 'PastedGraphic-2.tiff',
        cid: 'sig@h.test',
        type: 'image/tiff',
        blobId: 'b0',
      },
    ]);
    const url = '/jmap/download/acct1/b0/PastedGraphic-2.tiff';
    const { container } = render(AttachmentList, {
      email,
      undecodableInlineUrls: new Set([url]),
    });
    expect(container.querySelector('section')).toBeInTheDocument();
    expect(screen.getByText('PastedGraphic-2.tiff')).toBeInTheDocument();
  });

  it('a message with only one undecodable inline image still gets a discoverable download chip', () => {
    // The exact issue #269 reproduction shape: one inline TIFF part, no
    // regular attachments — the chip strip must still render.
    const email = makeEmail([
      {
        disposition: 'inline',
        name: 'PastedGraphic-2.tiff',
        cid: 'sig@h.test',
        type: 'image/tiff',
        blobId: 'b0',
      },
    ]);
    const url = '/jmap/download/acct1/b0/PastedGraphic-2.tiff';
    render(AttachmentList, { email, undecodableInlineUrls: new Set([url]) });
    const downloadIcon = document.querySelector('.download-icon') as HTMLAnchorElement | null;
    expect(downloadIcon).toBeInTheDocument();
    expect(downloadIcon?.getAttribute('href')).toBe(url);
  });

  it('mixes a chipped undecodable inline image with a normal (unchipped) inline image', () => {
    const email = makeEmail([
      {
        disposition: 'inline',
        name: 'PastedGraphic-2.tiff',
        cid: 'sig@h.test',
        type: 'image/tiff',
        blobId: 'b0',
      },
      {
        disposition: 'inline',
        name: 'ok.png',
        cid: 'ok@h.test',
        type: 'image/png',
        blobId: 'b1',
      },
    ]);
    const tiffUrl = '/jmap/download/acct1/b0/PastedGraphic-2.tiff';
    render(AttachmentList, { email, undecodableInlineUrls: new Set([tiffUrl]) });
    expect(screen.getByText('PastedGraphic-2.tiff')).toBeInTheDocument();
    expect(screen.queryByText('ok.png')).toBeNull();
  });
});

describe('AttachmentList: Gmail-style card type badges', () => {
  it('renders a red PDF badge with label "PDF" for application/pdf', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'report.pdf', type: 'application/pdf', size: 50_000 },
    ]);
    const { container } = render(AttachmentList, { email });
    const badge = container.querySelector('.badge-label');
    expect(badge).toBeInTheDocument();
    expect(badge?.textContent).toBe('PDF');
    // Icon area should have the red background variable set.
    const iconArea = container.querySelector('.card-icon') as HTMLElement | null;
    expect(iconArea?.style.getPropertyValue('--icon-bg')).toBe('#da1e28');
  });

  it('renders an <img> thumbnail for a small image attachment', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'photo.jpg', type: 'image/jpeg', size: 50_000 },
    ]);
    const { container } = render(AttachmentList, { email });
    const thumb = container.querySelector('img.thumb');
    expect(thumb).toBeInTheDocument();
    // src should be the JMAP download URL
    expect(thumb?.getAttribute('src')).toContain('/jmap/download/acct1/');
  });

  it('renders a blue DOC badge for the .docx MIME type', () => {
    const email = makeEmail([
      {
        disposition: 'attachment',
        name: 'document.docx',
        type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
        size: 20_000,
      },
    ]);
    const { container } = render(AttachmentList, { email });
    const badge = container.querySelector('.badge-label');
    expect(badge).toBeInTheDocument();
    expect(badge?.textContent).toBe('DOC');
    const iconArea = container.querySelector('.card-icon') as HTMLElement | null;
    expect(iconArea?.style.getPropertyValue('--icon-bg')).toBe('#0043ce');
  });

  it('renders a generic FILE badge for an unrecognised MIME type', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'data.bin', type: 'application/octet-stream', size: 4096 },
    ]);
    const { container } = render(AttachmentList, { email });
    const badge = container.querySelector('.badge-label');
    expect(badge).toBeInTheDocument();
    expect(badge?.textContent).toBe('FILE');
  });
});

describe('AttachmentList: card structure', () => {
  it('shows filename in the card', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'my-report.pdf', type: 'application/pdf', size: 1024 },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByText('my-report.pdf')).toBeInTheDocument();
  });

  it('the card button has an aria-label of "Open {name}"', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'invoice.pdf', type: 'application/pdf', size: 2048 },
    ]);
    render(AttachmentList, { email });
    expect(screen.getByRole('button', { name: 'Open invoice.pdf' })).toBeInTheDocument();
  });

  it('the dog-ear decoration element is present', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'file.pdf', type: 'application/pdf', size: 1024 },
    ]);
    const { container } = render(AttachmentList, { email });
    expect(container.querySelector('.dog-ear')).toBeInTheDocument();
  });
});

describe('AttachmentList: hover overlay View + Download buttons', () => {
  it('overlay contains a View affordance for viewable parts', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'pic.png', type: 'image/png', size: 100_000 },
    ]);
    render(AttachmentList, { email });
    const overlay = document.querySelector('.card-overlay');
    expect(overlay).toBeInTheDocument();
    const viewIcon = overlay?.querySelector('.view-icon');
    expect(viewIcon).toBeInTheDocument();
  });

  it('overlay contains a Download link for parts with a URL', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf', size: 50_000 },
    ]);
    render(AttachmentList, { email });
    const downloadIcon = document.querySelector('.download-icon');
    expect(downloadIcon).toBeInTheDocument();
    expect(downloadIcon?.getAttribute('download')).toBe('doc.pdf');
  });

  it('overlay has no View icon for non-previewable parts', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'data.zip', type: 'application/zip', size: 10_000 },
    ]);
    render(AttachmentList, { email });
    const viewIcon = document.querySelector('.view-icon');
    expect(viewIcon).toBeNull();
  });

  it('download link aria-label says "Download {name}"', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'invoice.pdf', type: 'application/pdf', size: 5000 },
    ]);
    render(AttachmentList, { email });
    const link = document.querySelector('.download-icon') as HTMLAnchorElement | null;
    expect(link?.getAttribute('aria-label')).toBe('Download invoice.pdf');
  });
});

describe('AttachmentList: clicking the card opens the lightbox (REQ-MAIL-23)', () => {
  it('clicking a viewable card renders the Lightbox component', async () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'photo.png', type: 'image/png', size: 100_000 },
    ]);
    render(AttachmentList, { email });
    const card = screen.getByRole('button', { name: 'Open photo.png' });
    await fireEvent.click(card);
    // Lightbox adds a role="dialog" to the DOM.
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('clicking a non-viewable card does not open the lightbox', async () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'data.zip', type: 'application/zip', size: 50_000 },
    ]);
    render(AttachmentList, { email });
    const card = screen.getByRole('button', { name: 'Open data.zip' });
    await fireEvent.click(card);
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});

describe('AttachmentList: View action (REQ-MAIL-23)', () => {
  it('overlay view icon exists for image attachments', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'pic.png', type: 'image/png', size: 100_000 },
    ]);
    render(AttachmentList, { email });
    expect(document.querySelector('.view-icon')).toBeInTheDocument();
  });

  it('overlay view icon exists for PDF attachments', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf', size: 50_000 },
    ]);
    render(AttachmentList, { email });
    expect(document.querySelector('.view-icon')).toBeInTheDocument();
  });

  it('does not render view icon for non-previewable types', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'data.zip', type: 'application/zip', size: 1024 },
    ]);
    render(AttachmentList, { email });
    expect(document.querySelector('.view-icon')).toBeNull();
  });
});

describe('AttachmentList: Download all bulk action', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('shows no "Download all" when there is only one attachment', () => {
    const email = makeEmail([{ disposition: 'attachment', name: 'file.pdf', type: 'application/pdf' }]);
    render(AttachmentList, { email });
    expect(screen.queryByText(/Download all/i)).toBeNull();
  });

  it('shows no "Download all" when there is only one visible attachment plus inline parts', () => {
    // 1 regular + 1 inline: the bulk bar should NOT appear since only 1 tile is visible.
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    expect(screen.queryByText(/Download all/i)).toBeNull();
  });

  it('shows "Download all (N)" counting only visible (non-inline) attachments', () => {
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'attachment', name: 'data.csv', type: 'text/csv' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    // Header shows 2 tiles; button must match: "Download all (2)", not (3).
    expect(screen.getByText('Download all (2)')).toBeInTheDocument();
  });

  it('does NOT show an "Attachments only" secondary button', () => {
    // The secondary button was removed: "Download all" is already attachments-only.
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf' },
      { disposition: 'attachment', name: 'data.csv', type: 'text/csv' },
      { disposition: 'inline', name: 'photo.png', cid: 'img1@h.test', type: 'image/png' },
    ]);
    render(AttachmentList, { email });
    expect(screen.queryByText('Attachments only')).toBeNull();
  });

  it('clicking "Download all" zips exactly the visible attachments — inline parts excluded', async () => {
    // Regression guard: the download set must equal the label count (N), not N+inline.
    const email = makeEmail([
      { disposition: 'attachment', name: 'doc.pdf', type: 'application/pdf', blobId: 'b0' },
      { disposition: 'attachment', name: 'data.csv', type: 'text/csv', blobId: 'b1' },
      { disposition: 'inline', name: 'logo.png', cid: 'logo@test', type: 'image/png', blobId: 'b2' },
    ]);
    render(AttachmentList, { email });

    const btn = screen.getByText('Download all (2)');
    await fireEvent.click(btn);

    await waitFor(() => {
      expect(zipBlobsAsDownload).toHaveBeenCalledOnce();
    });

    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const [parts] = vi.mocked(zipBlobsAsDownload).mock.calls[0]!;
    // Exactly 2 entries — the 2 regular attachments.
    expect(parts).toHaveLength(2);
    // The inline blob (b2) must NOT appear in the zip manifest.
    const urls = (parts as Array<{ url: string }>).map((p) => p.url);
    expect(urls.some((u) => u.includes('b2'))).toBe(false);
  });

  it('renders nothing when email has no attachments', () => {
    const email = makeEmail([]);
    const { container } = render(AttachmentList, { email });
    expect(container.querySelector('section')).toBeNull();
  });
});
