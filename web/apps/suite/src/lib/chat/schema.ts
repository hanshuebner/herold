/**
 * ProseMirror schema for chat compose per REQ-CHAT-21.
 *
 * Lighter than the mail compose schema: no headings, blockquotes,
 * horizontal rules, or tables. Supported marks: bold, italic, underline,
 * strikethrough, inline code, links. Supported blocks: paragraph, code_block,
 * bullet_list, ordered_list, list_item. Inline images are allowed (for
 * paste/drag-and-drop upload per REQ-CHAT-22/23).
 *
 * The link mark overrides prosemirror-schema-basic's default to add
 * target="_blank" and rel="noopener noreferrer" on serialization, and
 * restricts accepted hrefs to http:// and https:// only.
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

/**
 * Safe link mark: http/https only, serializes with target="_blank" and
 * rel="noopener noreferrer". Other schemes (mailto, javascript, data, etc.)
 * are rejected at parse time and stripped by the input rules.
 */
const safeLinkMark: MarkSpec = {
  attrs: {
    href: { validate: 'string' },
    title: { default: null, validate: 'string|null' },
  },
  inclusive: false,
  parseDOM: [
    {
      tag: 'a[href]',
      getAttrs(dom) {
        const href = (dom as HTMLElement).getAttribute('href') ?? '';
        if (!/^https?:\/\//i.test(href)) return false;
        return {
          href,
          title: (dom as HTMLElement).getAttribute('title'),
        };
      },
    },
  ],
  toDOM(node) {
    const { href, title } = node.attrs as { href: string; title: string | null };
    return [
      'a',
      {
        href,
        ...(title ? { title } : {}),
        target: '_blank',
        rel: 'noopener noreferrer',
      },
      0,
    ];
  },
};

const chatMarks = basicSchema.spec.marks
  .update('link', safeLinkMark)
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
