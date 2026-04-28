/**
 * ProseMirror schema for chat compose per REQ-CHAT-21.
 *
 * Lighter than the mail compose schema: no headings, blockquotes,
 * horizontal rules, or tables. Supported marks: bold, italic, underline,
 * strikethrough, inline code, links. Supported blocks: paragraph, code_block,
 * bullet_list, ordered_list, list_item. Inline images are allowed (for
 * paste/drag-and-drop upload per REQ-CHAT-22/23).
 */

import { Schema, type MarkSpec, type NodeSpec } from 'prosemirror-model';
import { addListNodes } from 'prosemirror-schema-list';
import { schema as basicSchema } from 'prosemirror-schema-basic';

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

const strikethroughMark: MarkSpec = {
  parseDOM: [
    { tag: 's' },
    { tag: 'del' },
    {
      style: 'text-decoration',
      getAttrs: (value) => (value === 'line-through' ? null : false),
    },
  ],
  toDOM() {
    return ['s', 0];
  },
};

// Start from basicSchema nodes but keep only the nodes we want.
// basicSchema has: doc, paragraph, blockquote, horizontal_rule, heading,
//   code_block, text, image, hard_break.
// We want: doc, paragraph, code_block, text, image, hard_break.
// After addListNodes: bullet_list, ordered_list, list_item are added.
const filteredNodes = basicSchema.spec.nodes
  .remove('blockquote')
  .remove('horizontal_rule')
  .remove('heading');

const chatNodes = addListNodes(filteredNodes, 'paragraph block*', 'block');

const chatMarks = basicSchema.spec.marks
  .addToEnd('underline', underlineMark)
  .addToEnd('strikethrough', strikethroughMark);

export const chatSchema = new Schema({
  nodes: chatNodes,
  marks: chatMarks,
});

/** Node specs exported for consumers that need to check node types. */
export const chatNodes_ = {
  bulletList: chatSchema.nodes.bullet_list!,
  orderedList: chatSchema.nodes.ordered_list!,
  listItem: chatSchema.nodes.list_item!,
  codeBlock: chatSchema.nodes.code_block!,
};

export const chatMarks_ = {
  strong: chatSchema.marks.strong!,
  em: chatSchema.marks.em!,
  underline: chatSchema.marks.underline!,
  strikethrough: chatSchema.marks.strikethrough!,
  code: chatSchema.marks.code!,
  link: chatSchema.marks.link!,
};
