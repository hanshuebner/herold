/**
 * Tests for the inline composer image resize handles (issue #296):
 *   - capImageDimensions: the pure aspect-ratio-preserving cap helper.
 *   - getEditorContentWidth: content-width probe with a test-environment
 *     fallback.
 *   - the image NodeView's insert-time cap (fires on the rendered <img>'s
 *     load event) and its corner-handle drag-to-resize behaviour, both
 *     driven against a real mounted EditorView.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { NodeSelection, Selection } from 'prosemirror-state';
import type { EditorView } from 'prosemirror-view';
import {
  applyImage,
  capImageDimensions,
  createComposeEditor,
  DEFAULT_EDITOR_CONTENT_WIDTH,
  getEditorContentWidth,
  MIN_IMAGE_DISPLAY_WIDTH,
} from './editor';

describe('capImageDimensions', () => {
  it('returns null when the image already fits within maxWidth', () => {
    expect(capImageDimensions({ width: 400, height: 300 }, 560)).toBeNull();
  });

  it('returns null for a degenerate natural size', () => {
    expect(capImageDimensions({ width: 0, height: 300 }, 560)).toBeNull();
    expect(capImageDimensions({ width: 400, height: 0 }, 560)).toBeNull();
  });

  it('scales an oversized image down to maxWidth, preserving aspect ratio', () => {
    const capped = capImageDimensions({ width: 4000, height: 3000 }, 500);
    expect(capped).toEqual({ width: 500, height: 375 });
  });

  it('rounds the scaled height to the nearest integer', () => {
    const capped = capImageDimensions({ width: 1000, height: 333 }, 300);
    expect(capped!.width).toBe(300);
    expect(capped!.height).toBe(100);
  });
});

describe('getEditorContentWidth', () => {
  it('falls back to DEFAULT_EDITOR_CONTENT_WIDTH when the editor has no live layout', () => {
    const host = document.createElement('div');
    const view = { dom: host } as unknown as EditorView;
    expect(getEditorContentWidth(view)).toBe(DEFAULT_EDITOR_CONTENT_WIDTH);
  });

  it('subtracts left/right padding from clientWidth when the editor has real layout', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    host.style.paddingLeft = '20px';
    host.style.paddingRight = '20px';
    Object.defineProperty(host, 'clientWidth', { value: 600, configurable: true });
    const view = { dom: host } as unknown as EditorView;
    expect(getEditorContentWidth(view)).toBe(560);
    host.remove();
  });
});

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

/** Fire the <img>'s load event with the given natural pixel dimensions. */
function fireImgLoad(img: HTMLImageElement, width: number, height: number): void {
  Object.defineProperty(img, 'naturalWidth', { value: width, configurable: true });
  Object.defineProperty(img, 'naturalHeight', { value: height, configurable: true });
  img.dispatchEvent(new Event('load'));
}

function findImagePos(v: EditorView): number {
  let pos = -1;
  v.state.doc.descendants((n, p) => {
    if (n.type.name === 'image') pos = p;
  });
  return pos;
}

function selectImage(v: EditorView, pos: number): void {
  v.dispatch(v.state.tr.setSelection(NodeSelection.create(v.state.doc, pos)));
}

afterEach(() => {
  view?.destroy();
  view = null;
});

describe('image NodeView -- insert-time cap (issue #296)', () => {
  it('caps an oversized image to the editor content width on first load', () => {
    const v = mountEditor();
    Object.defineProperty(v.dom, 'clientWidth', { value: 560, configurable: true });
    applyImage(v, 'blob:test/full-res', 'photo.jpg');

    const img = v.dom.querySelector('img')!;
    fireImgLoad(img, 4000, 3000);

    const node = v.state.doc.nodeAt(findImagePos(v))!;
    expect(node.attrs.width).toBe(560);
    expect(node.attrs.height).toBe(420);
    expect(img.style.width).toBe('560px');
    expect(img.style.height).toBe('420px');
  });

  it('leaves a small image at its natural size (no cap needed)', () => {
    const v = mountEditor();
    Object.defineProperty(v.dom, 'clientWidth', { value: 560, configurable: true });
    applyImage(v, 'blob:test/icon', 'icon.png');

    const img = v.dom.querySelector('img')!;
    fireImgLoad(img, 64, 64);

    const node = v.state.doc.nodeAt(findImagePos(v))!;
    expect(node.attrs.width).toBeNull();
    expect(node.attrs.height).toBeNull();
  });

  it('does not override a size already restored from a saved draft', () => {
    const v = mountEditor();
    Object.defineProperty(v.dom, 'clientWidth', { value: 560, configurable: true });
    const imageType = v.state.schema.nodes.image!;
    const node = imageType.create({
      src: 'cid:a@herold.local',
      alt: 'x',
      width: 200,
      height: 150,
    });
    v.dispatch(v.state.tr.replaceSelectionWith(node, false));

    const img = v.dom.querySelector('img')!;
    fireImgLoad(img, 4000, 3000);

    const restored = v.state.doc.nodeAt(findImagePos(v))!;
    expect(restored.attrs.width).toBe(200);
    expect(restored.attrs.height).toBe(150);
  });

  it('only caps once per image element (a later load, e.g. a proxy-swapped src, does not re-cap)', () => {
    const v = mountEditor();
    Object.defineProperty(v.dom, 'clientWidth', { value: 560, configurable: true });
    applyImage(v, 'blob:test/full-res', 'photo.jpg');

    const img = v.dom.querySelector('img')!;
    fireImgLoad(img, 4000, 3000);
    expect(v.state.doc.nodeAt(findImagePos(v))!.attrs.width).toBe(560);

    // A second load of the same DOM element (e.g. a swapped src re-fetching)
    // must not run the cap logic again.
    fireImgLoad(img, 200, 100);
    const node = v.state.doc.nodeAt(findImagePos(v))!;
    expect(node.attrs.width).toBe(560);
    expect(node.attrs.height).toBe(420);
  });
});

describe('image NodeView -- selection shows resize handles (issue #296)', () => {
  it('renders four corner handles only while the image node is selected', () => {
    const v = mountEditor();
    applyImage(v, 'blob:test/photo', 'photo.jpg');

    expect(v.dom.querySelectorAll('.cq-image-handle')).toHaveLength(0);

    const pos = findImagePos(v);
    selectImage(v, pos);

    expect(v.dom.querySelectorAll('.cq-image-handle')).toHaveLength(4);
    expect(v.dom.querySelector('.cq-image-view.cq-image-selected')).not.toBeNull();

    // Moving the selection elsewhere (deselecting the image) removes them.
    v.dispatch(v.state.tr.setSelection(Selection.atStart(v.state.doc)));
    expect(v.dom.querySelectorAll('.cq-image-handle')).toHaveLength(0);
    expect(v.dom.querySelector('.cq-image-view.cq-image-selected')).toBeNull();
  });
});

describe('image NodeView -- drag-to-resize a handle (issue #296)', () => {
  it('scales the image and commits width/height on mouseup, preserving aspect ratio', () => {
    const v = mountEditor();
    Object.defineProperty(v.dom, 'clientWidth', { value: 560, configurable: true });
    applyImage(v, 'blob:test/photo', 'photo.jpg');
    const img = v.dom.querySelector('img')!;
    fireImgLoad(img, 400, 300); // below the cap threshold -- starts at natural size

    const pos = findImagePos(v);
    selectImage(v, pos);

    const handle = v.dom.querySelector<HTMLElement>('[data-testid="image-resize-handle-se"]')!;
    handle.dispatchEvent(new MouseEvent('mousedown', { clientX: 100, bubbles: true }));
    window.dispatchEvent(new MouseEvent('mousemove', { clientX: 150 }));
    window.dispatchEvent(new MouseEvent('mouseup', { clientX: 150 }));

    const node = v.state.doc.nodeAt(pos)!;
    expect(node.attrs.width).toBe(450);
    expect(node.attrs.height).toBe(338); // 450 / (400/300), rounded
  });

  it('shrinks the image when dragging the se handle left', () => {
    const v = mountEditor();
    Object.defineProperty(v.dom, 'clientWidth', { value: 560, configurable: true });
    applyImage(v, 'blob:test/photo', 'photo.jpg');
    const img = v.dom.querySelector('img')!;
    fireImgLoad(img, 400, 400);

    const pos = findImagePos(v);
    selectImage(v, pos);

    const handle = v.dom.querySelector<HTMLElement>('[data-testid="image-resize-handle-se"]')!;
    handle.dispatchEvent(new MouseEvent('mousedown', { clientX: 200, bubbles: true }));
    window.dispatchEvent(new MouseEvent('mousemove', { clientX: 100 }));
    window.dispatchEvent(new MouseEvent('mouseup', { clientX: 100 }));

    const node = v.state.doc.nodeAt(pos)!;
    expect(node.attrs.width).toBe(300);
    expect(node.attrs.height).toBe(300);
  });

  it('clamps the drag result to MIN_IMAGE_DISPLAY_WIDTH', () => {
    const v = mountEditor();
    Object.defineProperty(v.dom, 'clientWidth', { value: 560, configurable: true });
    applyImage(v, 'blob:test/photo', 'photo.jpg');
    const img = v.dom.querySelector('img')!;
    fireImgLoad(img, 400, 400);

    const pos = findImagePos(v);
    selectImage(v, pos);

    const handle = v.dom.querySelector<HTMLElement>('[data-testid="image-resize-handle-se"]')!;
    handle.dispatchEvent(new MouseEvent('mousedown', { clientX: 200, bubbles: true }));
    window.dispatchEvent(new MouseEvent('mousemove', { clientX: -1000 }));
    window.dispatchEvent(new MouseEvent('mouseup', { clientX: -1000 }));

    const node = v.state.doc.nodeAt(pos)!;
    expect(node.attrs.width).toBe(MIN_IMAGE_DISPLAY_WIDTH);
  });
});
