/**
 * ProseMirror schema for the suite's compose body.
 *
 * The schema is the contract for what the suite sends and (eventually) what
 * the suite accepts inbound after sanitisation — see
 * docs/architecture/04-rendering.md and docs/implementation/01-tech-stack.md.
 *
 * Starts from prosemirror-schema-basic + prosemirror-schema-list, adds
 * an underline mark, and keeps the image node so inline images uploaded
 * via the toolbar Insert image action (issue #20) can be edited as
 * part of the document. The image node serialises to an `<img>` tag
 * whose `src` is a `cid:<content-id>` reference; the corresponding
 * inline part is added to the outbound message at send time.
 *
 * The image node also carries a display `width`/`height` (issue #296),
 * set by the resize-handle NodeView in editor.ts. Both are parsed from
 * the `<img>` tag's `width`/`height` attributes or its inline
 * `style="width:...;height:...;"` (style takes precedence, matching the
 * DOM's own cascade) and are serialised back onto the outbound `<img>` as
 * both the attributes and an inline style, so the display size holds
 * whichever of the two a given mail client honours.
 */

import { Schema, type MarkSpec, type NodeSpec } from 'prosemirror-model';
import { schema as basicSchema } from 'prosemirror-schema-basic';
import { addListNodes } from 'prosemirror-schema-list';

export interface ImageAttrs {
  src: string;
  alt: string | null;
  title: string | null;
  width: number | null;
  height: number | null;
}

const underlineMark: MarkSpec = {
  parseDOM: [
    { tag: 'u' },
    {
      style: 'text-decoration',
      getAttrs: (value) => (value === 'underline' ? null : false),
    },
  ],
  toDOM() {
    return ['u', 0];
  },
};

/**
 * Parse a CSS length such as `"320px"` or a bare `"320"` into a positive
 * integer pixel count, or `null` when the value is absent, non-numeric, or
 * given in a non-pixel unit (`%`, `em`, ...) that a fixed attr can't carry.
 */
function parseDimension(value: string | null | undefined): number | null {
  if (!value) return null;
  const match = /^(\d+(?:\.\d+)?)(px)?$/.exec(value.trim());
  if (!match) return null;
  const n = Math.round(Number(match[1]));
  return Number.isFinite(n) && n > 0 ? n : null;
}

const imageNodeSpec: NodeSpec = {
  inline: true,
  attrs: {
    src: { validate: 'string' },
    alt: { default: null, validate: 'string|null' },
    title: { default: null, validate: 'string|null' },
    width: { default: null, validate: 'number|null' },
    height: { default: null, validate: 'number|null' },
  },
  group: 'inline',
  draggable: true,
  parseDOM: [
    {
      tag: 'img[src]',
      getAttrs(dom: HTMLElement) {
        const style = (dom as HTMLElement).style;
        const width = parseDimension(style?.width) ?? parseDimension(dom.getAttribute('width'));
        const height =
          parseDimension(style?.height) ?? parseDimension(dom.getAttribute('height'));
        return {
          src: dom.getAttribute('src'),
          title: dom.getAttribute('title'),
          alt: dom.getAttribute('alt'),
          width,
          height,
        };
      },
    },
  ],
  toDOM(node) {
    const { src, alt, title, width, height } = node.attrs as ImageAttrs;
    const style = width != null && height != null ? `width:${width}px;height:${height}px;` : null;
    return ['img', { src, alt, title, width, height, style }];
  },
};

const nodes = addListNodes(basicSchema.spec.nodes, 'paragraph block*', 'block').update(
  'image',
  imageNodeSpec,
);
const marks = basicSchema.spec.marks.addToEnd('underline', underlineMark);

export const composeSchema = new Schema({ nodes, marks });
