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

const nodes = addListNodes(basicSchema.spec.nodes, 'paragraph block*', 'block');
const marks = basicSchema.spec.marks.addToEnd('underline', underlineMark);

export const composeSchema = new Schema({ nodes, marks });
