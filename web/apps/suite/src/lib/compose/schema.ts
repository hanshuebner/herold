/**
 * ProseMirror schema for tabard's compose body.
 *
 * The schema is the contract for what tabard sends and (eventually) what
 * tabard accepts inbound after sanitisation — see
 * docs/architecture/04-rendering.md and docs/implementation/01-tech-stack.md.
 *
 * Starts from prosemirror-schema-basic + prosemirror-schema-list and adds
 * an underline mark (basic doesn't include it). The image node is
 * removed: inline images flow through the attachment / Blob/upload path,
 * not the editor's image insertion.
 */

import { Schema, type MarkSpec } from 'prosemirror-model';
import { schema as basicSchema } from 'prosemirror-schema-basic';
import { addListNodes } from 'prosemirror-schema-list';

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

const baseNodes = basicSchema.spec.nodes.remove('image');
const nodes = addListNodes(baseNodes, 'paragraph block*', 'block');
const marks = basicSchema.spec.marks.addToEnd('underline', underlineMark);

export const composeSchema = new Schema({ nodes, marks });
