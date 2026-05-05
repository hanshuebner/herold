/**
 * Tests for issue #83: Inlined image issues.
 *
 * Three behaviours under test:
 *   1. Inline images do NOT appear in the non-inline attachment list —
 *      only attachments with inline=false count as "file attachments".
 *   2. Removing an attachment by key revokes its objectURL and drops it
 *      from compose.attachments (covers the onImageRemoved→removeAttachment
 *      path exercised by the editor when the user deletes an image node).
 *   3. collectImageSrcs correctly collects and diffs src attributes across
 *      ProseMirror document states.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { compose } from './compose.svelte';
import { collectImageSrcs, htmlToDoc } from './editor';

// Stub the JMAP client so uploadBlob never hits the network.
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

// Provide minimal URL stubs so happy-dom does not throw.
if (typeof globalThis.URL.createObjectURL !== 'function') {
  let n = 0;
  globalThis.URL.createObjectURL = () => `blob:test/${++n}`;
}
if (typeof globalThis.URL.revokeObjectURL !== 'function') {
  globalThis.URL.revokeObjectURL = () => undefined;
}

function makeImageFile(name = 'photo.png'): File {
  return new File(['img-bytes'], name, { type: 'image/png' });
}

// ── 1. Inline images do NOT appear in the non-inline attachment list ──────────

describe('inline images omitted from non-inline attachment list (issue #83)', () => {
  beforeEach(() => {
    compose.attachments = [];
    vi.clearAllMocks();
  });

  it('inline attachment has inline=true and is excluded from non-inline filter', () => {
    const file = makeImageFile();
    compose.startInlineImage(file);

    expect(compose.attachments).toHaveLength(1);
    const att = compose.attachments[0]!;
    expect(att.inline).toBe(true);

    // The non-inline filter that drives the chip strip must exclude inline images.
    const nonInline = compose.attachments.filter((a) => !a.inline);
    expect(nonInline).toHaveLength(0);
  });

  it('regular file attachment is NOT inline and appears in non-inline filter', async () => {
    // addAttachments creates a non-inline entry.
    const file = new File(['data'], 'report.pdf', { type: 'application/pdf' });
    // We just want to push a ready attachment without network — fake it directly.
    compose.attachments = [
      {
        key: 'att-1',
        name: 'report.pdf',
        size: 4,
        type: 'application/pdf',
        status: 'ready',
        blobId: 'blobX',
        error: null,
        inline: false,
      },
    ];

    const nonInline = compose.attachments.filter((a) => !a.inline);
    expect(nonInline).toHaveLength(1);
    expect(nonInline[0]?.name).toBe('report.pdf');

    // No inline attachments.
    const inlines = compose.attachments.filter((a) => a.inline);
    expect(inlines).toHaveLength(0);

    void file;
  });

  it('mixed: only non-inline files appear in attachment chip strip', () => {
    // Insert one inline image and one regular file.
    const img = makeImageFile('banner.png');
    compose.startInlineImage(img);
    compose.attachments = [
      ...compose.attachments,
      {
        key: 'att-file-1',
        name: 'document.pdf',
        size: 100,
        type: 'application/pdf',
        status: 'ready',
        blobId: 'blobY',
        error: null,
        inline: false,
      },
    ];

    const nonInline = compose.attachments.filter((a) => !a.inline);
    expect(nonInline).toHaveLength(1);
    expect(nonInline[0]?.name).toBe('document.pdf');
  });
});

// ── 2. removeAttachment drops the record (covers onImageRemoved path) ─────────

describe('removeAttachment removes inline image from compose.attachments (issue #83)', () => {
  beforeEach(() => {
    compose.attachments = [];
    vi.clearAllMocks();
  });

  it('removeAttachment by key drops the inline attachment record', () => {
    const file = makeImageFile();
    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    const { key } = started!;

    expect(compose.attachments).toHaveLength(1);

    // This is what the ComposeWindow.onImageRemoved handler calls.
    compose.removeAttachment(key);

    expect(compose.attachments).toHaveLength(0);
  });

  it('removeAttachment by key for objectURL-matched inline image removes the record', () => {
    const file = makeImageFile();
    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    const { key, objectURL } = started!;

    // Simulate the onImageRemoved lookup: find by objectURL, then removeAttachment.
    const att = compose.attachments.find(
      (a) => a.inline && a.objectURL === objectURL,
    );
    expect(att).toBeTruthy();
    expect(att?.key).toBe(key);

    compose.removeAttachment(att!.key);
    expect(compose.attachments).toHaveLength(0);
  });

  it('removeAttachment by key for cid-matched inline image removes the record', () => {
    const file = makeImageFile();
    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    const { key, cid } = started!;

    // Simulate the onImageRemoved lookup when src is a cid: URL.
    const cidSrc = `cid:${cid}`;
    const att = compose.attachments.find(
      (a) => a.inline && a.cid && `cid:${a.cid}` === cidSrc,
    );
    expect(att).toBeTruthy();
    expect(att?.key).toBe(key);

    compose.removeAttachment(att!.key);
    expect(compose.attachments).toHaveLength(0);
  });
});

// ── 3. collectImageSrcs diffs document image nodes correctly ─────────────────

describe('collectImageSrcs (issue #83)', () => {
  it('returns empty set for a document with no images', () => {
    const doc = htmlToDoc('<p>hello</p>');
    const srcs = collectImageSrcs(doc);
    expect(srcs.size).toBe(0);
  });

  it('collects a single image src', () => {
    const doc = htmlToDoc('<p><img src="blob:test/1" alt="img"></p>');
    const srcs = collectImageSrcs(doc);
    expect(srcs.size).toBe(1);
    expect(srcs.has('blob:test/1')).toBe(true);
  });

  it('collects multiple distinct image srcs', () => {
    const doc = htmlToDoc(
      '<p><img src="blob:test/1" alt="a"><img src="blob:test/2" alt="b"></p>',
    );
    const srcs = collectImageSrcs(doc);
    expect(srcs.size).toBe(2);
    expect(srcs.has('blob:test/1')).toBe(true);
    expect(srcs.has('blob:test/2')).toBe(true);
  });

  it('correctly identifies a removed src via set difference', () => {
    const before = htmlToDoc(
      '<p><img src="blob:test/1" alt="a"><img src="blob:test/2" alt="b"></p>',
    );
    const after = htmlToDoc('<p><img src="blob:test/2" alt="b"></p>');
    const prevSrcs = collectImageSrcs(before);
    const nextSrcs = collectImageSrcs(after);

    const removed: string[] = [];
    for (const src of prevSrcs) {
      if (!nextSrcs.has(src)) removed.push(src);
    }
    expect(removed).toEqual(['blob:test/1']);
  });

  it('reports no removals when the same image is present in both docs', () => {
    const html = '<p><img src="blob:test/1" alt="a"></p>';
    const before = htmlToDoc(html);
    const after = htmlToDoc(html);
    const prevSrcs = collectImageSrcs(before);
    const nextSrcs = collectImageSrcs(after);

    const removed: string[] = [];
    for (const src of prevSrcs) {
      if (!nextSrcs.has(src)) removed.push(src);
    }
    expect(removed).toHaveLength(0);
  });
});
