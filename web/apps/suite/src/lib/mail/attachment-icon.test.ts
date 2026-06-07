/**
 * Unit tests for the attachmentIcon and attachmentBadge helpers.
 */
import { describe, it, expect } from 'vitest';
import { attachmentIcon, attachmentBadge } from './attachment-icon';
import type { EmailBodyPart } from './types';

function makePart(overrides: Partial<EmailBodyPart>): EmailBodyPart {
  return {
    partId: 'p1',
    blobId: 'b1',
    size: 1024,
    type: 'application/octet-stream',
    charset: null,
    disposition: null,
    name: 'file.bin',
    cid: null,
    ...overrides,
  };
}

describe('attachmentIcon: image parts', () => {
  it('returns thumbnail for a small image', () => {
    const icon = attachmentIcon(makePart({ type: 'image/png', size: 50_000 }));
    expect(icon.kind).toBe('thumbnail');
  });

  it('returns thumbnail for image exactly at cap (2 MB)', () => {
    const cap = 2 * 1024 * 1024;
    const icon = attachmentIcon(makePart({ type: 'image/jpeg', size: cap }));
    expect(icon.kind).toBe('thumbnail');
  });

  it('returns IMG badge for image over 2 MB', () => {
    const overCap = 2 * 1024 * 1024 + 1;
    const icon = attachmentIcon(makePart({ type: 'image/png', size: overCap }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('IMG');
    }
  });

  it('returns IMG badge when size is 0', () => {
    const icon = attachmentIcon(makePart({ type: 'image/gif', size: 0 }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('IMG');
    }
  });
});

describe('attachmentIcon: PDF', () => {
  it('returns red PDF badge', () => {
    const icon = attachmentIcon(makePart({ type: 'application/pdf', size: 500_000 }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('PDF');
      expect(icon.bg).toBe('#da1e28');
    }
  });
});

describe('attachmentIcon: Word documents', () => {
  it('returns blue DOC badge for application/msword', () => {
    const icon = attachmentIcon(makePart({ type: 'application/msword' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('DOC');
      expect(icon.bg).toBe('#0043ce');
    }
  });

  it('returns blue DOC badge for .docx MIME type', () => {
    const icon = attachmentIcon(makePart({
      type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('DOC');
    }
  });
});

describe('attachmentIcon: spreadsheets', () => {
  it('returns green XLS badge for application/vnd.ms-excel', () => {
    const icon = attachmentIcon(makePart({ type: 'application/vnd.ms-excel' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('XLS');
      expect(icon.bg).toBe('#198038');
    }
  });

  it('returns green XLS badge for .xlsx MIME type', () => {
    const icon = attachmentIcon(makePart({
      type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('XLS');
    }
  });
});

describe('attachmentIcon: archive types', () => {
  it('returns ZIP badge for application/zip', () => {
    const icon = attachmentIcon(makePart({ type: 'application/zip' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('ZIP');
    }
  });

  it('returns ZIP badge for application/x-7z-compressed', () => {
    const icon = attachmentIcon(makePart({ type: 'application/x-7z-compressed' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('ZIP');
    }
  });

  it('returns ZIP badge for application/x-tar', () => {
    const icon = attachmentIcon(makePart({ type: 'application/x-tar' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('ZIP');
    }
  });

  it('returns ZIP badge for application/gzip', () => {
    const icon = attachmentIcon(makePart({ type: 'application/gzip' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('ZIP');
    }
  });
});

describe('attachmentIcon: fallback', () => {
  it('returns FILE badge for octet-stream', () => {
    const icon = attachmentIcon(makePart({ type: 'application/octet-stream' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('FILE');
    }
  });

  it('returns FILE badge for unknown MIME type', () => {
    const icon = attachmentIcon(makePart({ type: 'application/x-custom-thing' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('FILE');
    }
  });

  it('returns FILE badge for empty type string', () => {
    const icon = attachmentIcon(makePart({ type: '' }));
    expect(icon.kind).toBe('badge');
    if (icon.kind === 'badge') {
      expect(icon.label).toBe('FILE');
    }
  });
});

// ── attachmentBadge ──────────────────────────────────────────────────────────

describe('attachmentBadge: image types', () => {
  it('maps image/png to IMG with teal background', () => {
    const b = attachmentBadge('image/png', 'photo.png');
    expect(b.label).toBe('IMG');
    expect(b.bg).toBe('#007d79');
  });

  it('maps image/jpeg to IMG', () => {
    const b = attachmentBadge('image/jpeg', 'photo.jpg');
    expect(b.label).toBe('IMG');
  });
});

describe('attachmentBadge: document types', () => {
  it('maps application/pdf to PDF with red background', () => {
    const b = attachmentBadge('application/pdf', 'doc.pdf');
    expect(b.label).toBe('PDF');
    expect(b.bg).toBe('#da1e28');
  });

  it('maps application/msword to DOC with blue background', () => {
    const b = attachmentBadge('application/msword', 'doc.doc');
    expect(b.label).toBe('DOC');
    expect(b.bg).toBe('#0043ce');
  });

  it('maps .docx MIME type to DOC', () => {
    const b = attachmentBadge(
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
      'doc.docx',
    );
    expect(b.label).toBe('DOC');
  });

  it('maps application/vnd.ms-excel to XLS with green background', () => {
    const b = attachmentBadge('application/vnd.ms-excel', 'sheet.xls');
    expect(b.label).toBe('XLS');
    expect(b.bg).toBe('#198038');
  });

  it('maps .xlsx MIME type to XLS', () => {
    const b = attachmentBadge(
      'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      'sheet.xlsx',
    );
    expect(b.label).toBe('XLS');
  });
});

describe('attachmentBadge: archive types', () => {
  it('maps application/zip to ZIP', () => {
    const b = attachmentBadge('application/zip', 'archive.zip');
    expect(b.label).toBe('ZIP');
    expect(b.bg).toBe('#697077');
  });

  it('maps application/x-7z-compressed to ZIP', () => {
    const b = attachmentBadge('application/x-7z-compressed', 'archive.7z');
    expect(b.label).toBe('ZIP');
  });

  it('maps application/x-tar to ZIP', () => {
    const b = attachmentBadge('application/x-tar', 'archive.tar');
    expect(b.label).toBe('ZIP');
  });

  it('maps application/gzip to ZIP', () => {
    const b = attachmentBadge('application/gzip', 'archive.gz');
    expect(b.label).toBe('ZIP');
  });
});

describe('attachmentBadge: fallback', () => {
  it('maps application/octet-stream to FILE with gray background', () => {
    const b = attachmentBadge('application/octet-stream', 'file.bin');
    expect(b.label).toBe('FILE');
    expect(b.bg).toBe('#697077');
  });

  it('maps empty content type to FILE', () => {
    const b = attachmentBadge('', 'file.bin');
    expect(b.label).toBe('FILE');
  });

  it('maps unknown MIME type to FILE', () => {
    const b = attachmentBadge('application/x-custom-thing', 'file.xyz');
    expect(b.label).toBe('FILE');
  });
});
