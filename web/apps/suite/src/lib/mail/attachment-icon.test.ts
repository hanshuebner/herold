/**
 * Unit tests for the attachmentIcon helper.
 */
import { describe, it, expect } from 'vitest';
import { attachmentIcon } from './attachment-icon';
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
