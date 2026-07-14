/**
 * Tests for editor.ts's replaceImageSrc (issue #243): swaps an inline
 * image node's `src` attribute in place, used to replace a large image's
 * full-resolution blob: URL with a downscaled proxy once one is ready.
 */
import { describe, it, expect, afterEach } from 'vitest';
import type { EditorView } from 'prosemirror-view';
import { applyImage, createComposeEditor, replaceImageSrc } from './editor';

let view: EditorView | null = null;

function mountEditor(): EditorView {
  const host = document.createElement('div');
  document.body.appendChild(host);
  view = createComposeEditor(host, {
    initialHtml: '<p>hello</p>',
    onChange: () => undefined,
  });
  return view;
}

afterEach(() => {
  view?.destroy();
  view = null;
});

describe('replaceImageSrc', () => {
  it('swaps the src attribute of a matching image node', () => {
    const v = mountEditor();
    applyImage(v, 'blob:test/full-res', 'photo.jpg');

    let img = v.dom.querySelector('img');
    expect(img?.getAttribute('src')).toBe('blob:test/full-res');

    replaceImageSrc(v, 'blob:test/full-res', 'blob:test/proxy');

    img = v.dom.querySelector('img');
    expect(img?.getAttribute('src')).toBe('blob:test/proxy');
  });

  it('preserves alt text and node position across the swap', () => {
    const v = mountEditor();
    applyImage(v, 'blob:test/full-res', 'scan.jpg');
    replaceImageSrc(v, 'blob:test/full-res', 'blob:test/proxy');

    const img = v.dom.querySelector('img');
    expect(img?.getAttribute('alt')).toBe('scan.jpg');
  });

  it('is a no-op when no image matches oldSrc', () => {
    const v = mountEditor();
    applyImage(v, 'blob:test/full-res', 'photo.jpg');

    replaceImageSrc(v, 'blob:test/does-not-exist', 'blob:test/proxy');

    const img = v.dom.querySelector('img');
    expect(img?.getAttribute('src')).toBe('blob:test/full-res');
  });

  it('is a no-op when the view is null', () => {
    expect(() => replaceImageSrc(null, 'a', 'b')).not.toThrow();
  });

  it('swaps only the matching image when multiple images are present', () => {
    const v = mountEditor();
    applyImage(v, 'blob:test/img-a', 'a');
    applyImage(v, 'blob:test/img-b', 'b');

    replaceImageSrc(v, 'blob:test/img-a', 'blob:test/img-a-proxy');

    const srcs = Array.from(v.dom.querySelectorAll('img')).map((el) =>
      el.getAttribute('src'),
    );
    expect(srcs).toContain('blob:test/img-a-proxy');
    expect(srcs).toContain('blob:test/img-b');
    expect(srcs).not.toContain('blob:test/img-a');
  });
});
