/**
 * Tests for the proxy-vs-outbound decoupling in the inline image insertion
 * path (issue #243): a large pasted/dropped/picked image's editor display
 * is swapped to a downscaled proxy once one is generated, while the JMAP
 * blob upload -- and therefore the outbound cid: part -- always carries the
 * original, full-resolution File untouched.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import type { EditorView } from 'prosemirror-view';
import { compose } from './compose.svelte';
import { applyImage, createComposeEditor, replaceImageSrc } from './editor';

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

let blobUrlSeq = 0;
globalThis.URL.createObjectURL = vi.fn();
globalThis.URL.revokeObjectURL = vi.fn();

// Several tests below spy on document.createElement (to stub a fake
// canvas). vi.clearAllMocks() in each beforeEach only clears call data,
// not the spy itself -- without a restore, spies stack across tests and
// `document.createElement.bind(document)` captured by the NEXT stub
// re-wraps the previous (already-replaced) implementation instead of the
// real one, blowing the call stack on non-canvas tags.
afterEach(() => {
  vi.restoreAllMocks();
});

function makeLargeImageFile(name = 'scan.jpg'): File {
  return new File(['full-resolution-bytes'], name, { type: 'image/jpeg' });
}

function makeSmallImageFile(name = 'icon.png'): File {
  return new File(['small-bytes'], name, { type: 'image/png' });
}

/** Stub createImageBitmap + canvas so generateImageProxy succeeds for any file. */
function stubProxyGeneration(dimensions: { width: number; height: number }) {
  const close = vi.fn();
  (globalThis as { createImageBitmap?: unknown }).createImageBitmap = vi.fn(
    async () => ({ ...dimensions, close }),
  );
  const realCreateElement = document.createElement.bind(document);
  const fakeCanvas = {
    width: 0,
    height: 0,
    getContext: () => ({ drawImage: vi.fn() }),
    toBlob: (cb: (b: Blob | null) => void, type: string) => {
      cb(new Blob(['proxy-bytes'], { type }));
    },
  };
  vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
    if (tag === 'canvas') return fakeCanvas as unknown as HTMLCanvasElement;
    return realCreateElement(tag);
  });
}

/** No createImageBitmap at all -- generateImageProxy always declines. */
function stubNoProxySupport() {
  delete (globalThis as { createImageBitmap?: unknown }).createImageBitmap;
}

async function getUploadBlob() {
  const { jmap } = await import('../jmap/client');
  return jmap.uploadBlob as ReturnType<typeof vi.fn>;
}

describe('inline image proxy swap (issue #243)', () => {
  beforeEach(() => {
    compose.attachments = [];
    vi.clearAllMocks();
    (globalThis.URL.createObjectURL as ReturnType<typeof vi.fn>).mockImplementation(
      () => `blob:test/${++blobUrlSeq}`,
    );
    compose.setSwapInlineImageSrcFn(null);
  });

  it('swaps the editor src to a downscaled proxy for a large image, decoupled from the uploaded bytes', async () => {
    stubProxyGeneration({ width: 4000, height: 3000 });
    const swapFn = vi.fn();
    compose.setSwapInlineImageSrcFn(swapFn);

    const uploadBlob = await getUploadBlob();
    uploadBlob.mockResolvedValueOnce({ blobId: 'blob-server-1' });

    const file = makeLargeImageFile();
    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    const fullResSrc = started!.objectURL;

    // Complete the (unrelated) JMAP upload with the ORIGINAL file, same as
    // the caller always does -- this must carry the full-resolution bytes
    // regardless of what the editor is displaying.
    await compose.uploadInlineImage(started!.key, file);
    expect(uploadBlob).toHaveBeenCalledWith(
      expect.objectContaining({ body: file }),
    );

    // The proxy generation is fire-and-forget from startInlineImage; wait
    // for the swap callback to fire.
    await vi.waitFor(() => expect(swapFn).toHaveBeenCalled());

    expect(swapFn).toHaveBeenCalledTimes(1);
    const [oldSrc, newSrc] = swapFn.mock.calls[0]!;
    expect(oldSrc).toBe(fullResSrc);
    expect(newSrc).not.toBe(fullResSrc);
    expect(newSrc).toMatch(/^blob:/);

    // The attachment record's display src is now the proxy, while the
    // upload that already completed carried the original File untouched.
    const att = compose.attachments.find((a) => a.key === started!.key);
    expect(att?.objectURL).toBe(newSrc);
    expect(att?.blobId).toBe('blob-server-1');
  });

  it('does not swap the src for a small image (below the downscale threshold)', async () => {
    stubProxyGeneration({ width: 400, height: 300 });
    const swapFn = vi.fn();
    compose.setSwapInlineImageSrcFn(swapFn);

    const started = compose.startInlineImage(makeSmallImageFile());
    expect(started).not.toBeNull();

    // Give any pending microtasks a chance to run; there should be nothing
    // to wait for since generateImageProxy resolves null synchronously-ish
    // for small images.
    await new Promise((r) => setTimeout(r, 20));

    expect(swapFn).not.toHaveBeenCalled();
    const att = compose.attachments.find((a) => a.key === started!.key);
    expect(att?.objectURL).toBe(started!.objectURL);
  });

  it('does not swap when the attachment was removed before the proxy finished generating', async () => {
    let resolveBitmap!: (v: { width: number; height: number; close: () => void }) => void;
    (globalThis as { createImageBitmap?: unknown }).createImageBitmap = vi.fn(
      () => new Promise((res) => { resolveBitmap = res; }),
    );
    const realCreateElement = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      if (tag === 'canvas') {
        return {
          width: 0,
          height: 0,
          getContext: () => ({ drawImage: vi.fn() }),
          toBlob: (cb: (b: Blob | null) => void, type: string) =>
            cb(new Blob(['proxy-bytes'], { type })),
        } as unknown as HTMLCanvasElement;
      }
      return realCreateElement(tag);
    });

    const swapFn = vi.fn();
    compose.setSwapInlineImageSrcFn(swapFn);

    const started = compose.startInlineImage(makeLargeImageFile());
    expect(started).not.toBeNull();

    // Remove the attachment (as if the user deleted the image, or upload
    // failed and the caller retracted the placeholder) before the bitmap
    // decode resolves.
    compose.removeAttachment(started!.key);

    resolveBitmap({ width: 4000, height: 3000, close: vi.fn() });
    await new Promise((r) => setTimeout(r, 20));

    expect(swapFn).not.toHaveBeenCalled();
  });

  it('never swaps when generateImageProxy has no createImageBitmap support', async () => {
    stubNoProxySupport();
    const swapFn = vi.fn();
    compose.setSwapInlineImageSrcFn(swapFn);

    const started = compose.startInlineImage(makeLargeImageFile());
    expect(started).not.toBeNull();
    await new Promise((r) => setTimeout(r, 20));

    expect(swapFn).not.toHaveBeenCalled();
    const att = compose.attachments.find((a) => a.key === started!.key);
    expect(att?.objectURL).toBe(started!.objectURL);
  });
});

/**
 * Regression coverage for a real bug caught only via live-browser
 * verification (issue #243): wiring `setSwapInlineImageSrcFn` to a real
 * EditorView (as ComposeWindow.svelte / ThreadInlineComposer.svelte do)
 * dispatches a live ProseMirror transaction when the proxy swaps in.
 * editor.ts's docChanged image-removal diff (`onImageRemoved`) saw the
 * pre-swap src disappear and, because the attachment record still carried
 * that stale src at the moment the transaction dispatched, ComposeWindow's
 * `onImageRemoved` handler matched it and deleted the whole attachment --
 * mistaking an internal src rename for the user deleting the image. Fixed
 * by patching the attachment's objectURL before dispatching the swap.
 *
 * These tests wire the real editor.ts pieces (a mounted EditorView,
 * `replaceImageSrc`, and the same `onImageRemoved` matching logic
 * ComposeWindow.svelte uses) so a regression here fails a unit test
 * instead of only surfacing in the browser.
 */
describe('inline image proxy swap does not clobber the attachment record (issue #243)', () => {
  let view: EditorView | null = null;

  function mountEditorWithImageRemovedWiring(): EditorView {
    const host = document.createElement('div');
    document.body.appendChild(host);
    // Mirrors ComposeWindow.svelte's onImageRemoved: matches a removed
    // image's src against the live attachment list by objectURL or cid:,
    // then calls compose.removeAttachment.
    const onImageRemoved = (src: string) => {
      const att = compose.attachments.find(
        (a) => a.inline && (a.objectURL === src || (a.cid && `cid:${a.cid}` === src)),
      );
      if (att) compose.removeAttachment(att.key);
    };
    view = createComposeEditor(host, {
      initialHtml: '<p></p>',
      onChange: () => undefined,
      onImageRemoved,
    });
    return view;
  }

  beforeEach(() => {
    compose.attachments = [];
    vi.clearAllMocks();
    (globalThis.URL.createObjectURL as ReturnType<typeof vi.fn>).mockImplementation(
      () => `blob:test/${++blobUrlSeq}`,
    );
    compose.setSwapInlineImageSrcFn(null);
  });

  afterEach(() => {
    view?.destroy();
    view = null;
  });

  it('keeps the attachment record intact across a proxy swap', async () => {
    stubProxyGeneration({ width: 4000, height: 3000 });
    const v = mountEditorWithImageRemovedWiring();
    compose.setSwapInlineImageSrcFn((oldSrc, newSrc) => replaceImageSrc(v, oldSrc, newSrc));

    const file = makeLargeImageFile();
    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    applyImage(v, started!.objectURL, file.name);

    // Wait for the proxy generation + swap to complete.
    await vi.waitFor(() => {
      const img = v.dom.querySelector('img');
      expect(img?.getAttribute('src')).not.toBe(started!.objectURL);
    });

    // The DOM shows the swapped-in proxy src...
    const img = v.dom.querySelector('img');
    const proxySrc = img?.getAttribute('src');
    expect(proxySrc).toBeTruthy();
    expect(proxySrc).not.toBe(started!.objectURL);

    // ...and the attachment record still exists, updated to match --
    // it must NOT have been deleted by the onImageRemoved diff firing on
    // the internal src rename.
    const att = compose.attachments.find((a) => a.key === started!.key);
    expect(att).toBeDefined();
    expect(att?.objectURL).toBe(proxySrc);
  });

  it('preserves an explicitly resized width/height across a proxy swap (issue #296)', async () => {
    stubProxyGeneration({ width: 4000, height: 3000 });
    const v = mountEditorWithImageRemovedWiring();
    compose.setSwapInlineImageSrcFn((oldSrc, newSrc) => replaceImageSrc(v, oldSrc, newSrc));

    const file = makeLargeImageFile();
    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    applyImage(v, started!.objectURL, file.name);

    // Simulate the user having already resized the image (issue #296)
    // before the proxy generation finishes.
    let pos = -1;
    v.state.doc.descendants((n, p) => {
      if (n.type.name === 'image') pos = p;
    });
    v.dispatch(
      v.state.tr.setNodeAttribute(pos, 'width', 250).setNodeAttribute(pos, 'height', 188),
    );

    await vi.waitFor(() => {
      const img = v.dom.querySelector('img');
      expect(img?.getAttribute('src')).not.toBe(started!.objectURL);
    });

    const img = v.dom.querySelector('img');
    expect(img?.getAttribute('width')).toBe('250');
    expect(img?.getAttribute('height')).toBe('188');

    let node: { attrs: Record<string, unknown> } | null = null;
    v.state.doc.descendants((n) => {
      if (!node && n.type.name === 'image') node = n;
    });
    expect(node!.attrs.width).toBe(250);
    expect(node!.attrs.height).toBe(188);
  });

  it('a real user deletion (Backspace-equivalent removeImageBySrc) still removes the attachment', async () => {
    // Sanity check that the fix did not disable onImageRemoved entirely --
    // an actual removal of the image node must still drop the attachment.
    const v = mountEditorWithImageRemovedWiring();
    compose.setSwapInlineImageSrcFn((oldSrc, newSrc) => replaceImageSrc(v, oldSrc, newSrc));

    const file = makeSmallImageFile(); // below the proxy threshold -- no swap noise
    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    applyImage(v, started!.objectURL, file.name);

    expect(compose.attachments.find((a) => a.key === started!.key)).toBeDefined();

    const { removeImageBySrc } = await import('./editor');
    removeImageBySrc(v, started!.objectURL);

    expect(compose.attachments.find((a) => a.key === started!.key)).toBeUndefined();
  });
});
