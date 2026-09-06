/**
 * Tests for the compose schema's image node width/height attrs (issue
 * #296): parsed from an `<img>` tag's `width`/`height` attributes or its
 * inline style, and serialised back onto both so a draft round-trips its
 * chosen display size through the HTML serialiser/parser used for draft
 * save/reload (docToHtml / htmlToDoc in editor.ts).
 */
import { describe, it, expect } from 'vitest';
import type { Node } from 'prosemirror-model';
import { docToHtml, htmlToDoc } from './editor';
import { composeSchema, type ImageAttrs } from './schema';

function firstImage(doc: Node): { attrs: ImageAttrs } | null {
  const positions: number[] = [];
  doc.descendants((node, pos) => {
    if (node.type.name === 'image') positions.push(pos);
  });
  if (positions.length === 0) return null;
  const node = doc.nodeAt(positions[0]!);
  return node ? { attrs: node.attrs as ImageAttrs } : null;
}

describe('compose schema image node -- width/height attrs (issue #296)', () => {
  it('has no size by default (schema-basic parity for a plain <img>)', () => {
    const doc = htmlToDoc('<p><img src="cid:a@herold.local" alt="x"></p>');
    const img = firstImage(doc);
    expect(img).not.toBeNull();
    expect(img!.attrs.width).toBeNull();
    expect(img!.attrs.height).toBeNull();
  });

  it('parses width/height from the width/height attributes', () => {
    const doc = htmlToDoc(
      '<p><img src="cid:a@herold.local" alt="x" width="320" height="240"></p>',
    );
    const img = firstImage(doc);
    expect(img!.attrs.width).toBe(320);
    expect(img!.attrs.height).toBe(240);
  });

  it('parses width/height from an inline style', () => {
    const doc = htmlToDoc(
      '<p><img src="cid:a@herold.local" alt="x" style="width:150px;height:100px;"></p>',
    );
    const img = firstImage(doc);
    expect(img!.attrs.width).toBe(150);
    expect(img!.attrs.height).toBe(100);
  });

  it('prefers the inline style over the width/height attributes when both are present', () => {
    const doc = htmlToDoc(
      '<p><img src="cid:a@herold.local" width="999" height="999" style="width:200px;height:150px;"></p>',
    );
    const img = firstImage(doc);
    expect(img!.attrs.width).toBe(200);
    expect(img!.attrs.height).toBe(150);
  });

  it('ignores a non-pixel style width (e.g. percentage)', () => {
    const doc = htmlToDoc('<p><img src="cid:a@herold.local" style="width:50%;"></p>');
    const img = firstImage(doc);
    expect(img!.attrs.width).toBeNull();
  });

  it('serialises width/height onto both the attributes and an inline style', () => {
    const imageType = composeSchema.nodes.image!;
    const node = imageType.create({
      src: 'cid:a@herold.local',
      alt: 'photo',
      width: 300,
      height: 200,
    });
    const doc = composeSchema.topNodeType.create(null, [
      composeSchema.nodes.paragraph!.create(null, node),
    ]);
    const html = docToHtml(doc);
    expect(html).toContain('width="300"');
    expect(html).toContain('height="200"');
    expect(html).toMatch(/style="width:\s*300px;\s*height:\s*200px;?"/);
  });

  it('omits width/height/style entirely when unset', () => {
    const doc = htmlToDoc('<p><img src="cid:a@herold.local" alt="x"></p>');
    const html = docToHtml(doc);
    expect(html).not.toContain('width=');
    expect(html).not.toContain('height=');
    expect(html).not.toContain('style=');
  });

  it('round-trips an explicitly sized image through parse -> serialise -> parse (draft save/reload)', () => {
    const original = '<p><img src="cid:a@herold.local" alt="photo" width="480" height="360"></p>';
    const doc1 = htmlToDoc(original);
    const html = docToHtml(doc1);
    const doc2 = htmlToDoc(html);
    const img = firstImage(doc2);
    expect(img!.attrs.width).toBe(480);
    expect(img!.attrs.height).toBe(360);
    expect(img!.attrs.src).toBe('cid:a@herold.local');
    expect(img!.attrs.alt).toBe('photo');
  });
});
