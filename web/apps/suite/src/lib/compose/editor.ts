/**
 * Non-Svelte ProseMirror helpers — schema-aware doc / HTML / text
 * conversions, mark / list-active queries, and editor-view construction.
 *
 * The Svelte wrapper (RichEditor.svelte) keeps a thin reactive bridge for
 * the toolbar; everything imperative lives here.
 */

import {
  EditorState,
  type Transaction,
  type EditorState as EditorStateType,
} from 'prosemirror-state';
import { EditorView } from 'prosemirror-view';
import {
  DOMParser as PMDOMParser,
  DOMSerializer,
  type Node,
  type MarkType,
} from 'prosemirror-model';
import { keymap } from 'prosemirror-keymap';
import {
  baseKeymap,
  toggleMark,
  wrapIn,
  setBlockType,
} from 'prosemirror-commands';
import { history, undo, redo } from 'prosemirror-history';
import {
  splitListItem,
  liftListItem,
  sinkListItem,
  wrapInList,
} from 'prosemirror-schema-list';

import { composeSchema } from './schema';

export interface ActiveState {
  strong: boolean;
  em: boolean;
  underline: boolean;
  code: boolean;
  link: boolean;
  bulletList: boolean;
  orderedList: boolean;
  blockquote: boolean;
}

export const EMPTY_ACTIVE: ActiveState = {
  strong: false,
  em: false,
  underline: false,
  code: false,
  link: false,
  bulletList: false,
  orderedList: false,
  blockquote: false,
};

/** Parse an HTML fragment into a ProseMirror Doc using the compose schema. */
export function htmlToDoc(html: string): Node {
  const trimmed = html.trim();
  if (!trimmed) {
    const top = composeSchema.topNodeType.createAndFill();
    if (!top) throw new Error('compose schema cannot create empty doc');
    return top;
  }
  const wrapper = document.createElement('div');
  wrapper.innerHTML = trimmed;
  return PMDOMParser.fromSchema(composeSchema).parse(wrapper);
}

/** Serialise a ProseMirror Doc to an HTML string. */
export function docToHtml(doc: Node): string {
  const fragment = DOMSerializer.fromSchema(composeSchema).serializeFragment(
    doc.content,
  );
  const wrapper = document.createElement('div');
  wrapper.appendChild(fragment);
  return wrapper.innerHTML;
}

/** Plain-text projection of a ProseMirror Doc — joins inline content per block. */
export function docToText(doc: Node): string {
  // textBetween joins block boundaries with the given separator.
  return doc.textBetween(0, doc.content.size, '\n', ' ').trim();
}

function isMarkActive(state: EditorStateType, mark: MarkType): boolean {
  const { from, $from, to, empty } = state.selection;
  if (empty) {
    const stored = state.storedMarks ?? $from.marks();
    return Boolean(mark.isInSet(stored));
  }
  return state.doc.rangeHasMark(from, to, mark);
}

function isInBlockNode(state: EditorStateType, name: string): boolean {
  const node = state.selection.$from.node(state.selection.$from.depth - 0);
  return node.type.name === name;
}

function isInsideListType(state: EditorStateType, name: string): boolean {
  const $from = state.selection.$from;
  for (let d = $from.depth; d > 0; d--) {
    if ($from.node(d).type.name === name) return true;
  }
  return false;
}

/** Compute toolbar-active state from an EditorState. */
export function computeActive(state: EditorStateType): ActiveState {
  const m = composeSchema.marks;
  const n = composeSchema.nodes;
  return {
    strong: isMarkActive(state, m.strong!),
    em: isMarkActive(state, m.em!),
    underline: isMarkActive(state, m.underline!),
    code: isMarkActive(state, m.code!),
    link: isMarkActive(state, m.link!),
    bulletList: isInsideListType(state, n.bullet_list!.name),
    orderedList: isInsideListType(state, n.ordered_list!.name),
    blockquote: isInsideListType(state, n.blockquote!.name),
  };
}

/**
 * Build the editor's keymap. Mod = Cmd on macOS, Ctrl elsewhere
 * (prosemirror-keymap handles the platform check internally).
 */
function composeKeymap() {
  const m = composeSchema.marks;
  const n = composeSchema.nodes;
  return keymap({
    'Mod-z': undo,
    'Mod-y': redo,
    'Mod-Shift-z': redo,
    'Mod-b': toggleMark(m.strong!),
    'Mod-i': toggleMark(m.em!),
    'Mod-u': toggleMark(m.underline!),
    'Mod-`': toggleMark(m.code!),
    Enter: splitListItem(n.list_item!),
    'Mod-[': liftListItem(n.list_item!),
    'Mod-]': sinkListItem(n.list_item!),
  });
}

export function createComposeEditor(
  target: HTMLElement,
  options: {
    initialHtml: string;
    onChange: (state: EditorStateType) => void;
  },
): EditorView {
  const doc = htmlToDoc(options.initialHtml);
  const state = EditorState.create({
    schema: composeSchema,
    doc,
    plugins: [history(), composeKeymap(), keymap(baseKeymap)],
  });
  const view = new EditorView(target, {
    state,
    dispatchTransaction(tr: Transaction) {
      const next = view.state.apply(tr);
      view.updateState(next);
      options.onChange(next);
    },
  });
  return view;
}

// Toolbar action wrappers — call against the live view.
export function exec(
  view: EditorView | null,
  cmd: (state: EditorStateType, dispatch?: (tr: Transaction) => void) => boolean,
): void {
  if (!view) return;
  cmd(view.state, view.dispatch);
  view.focus();
}

export function applyBold(view: EditorView | null): void {
  exec(view, toggleMark(composeSchema.marks.strong!));
}
export function applyItalic(view: EditorView | null): void {
  exec(view, toggleMark(composeSchema.marks.em!));
}
export function applyUnderline(view: EditorView | null): void {
  exec(view, toggleMark(composeSchema.marks.underline!));
}
export function applyBulletList(view: EditorView | null): void {
  exec(view, wrapInList(composeSchema.nodes.bullet_list!));
}
export function applyOrderedList(view: EditorView | null): void {
  exec(view, wrapInList(composeSchema.nodes.ordered_list!));
}
export function applyBlockquote(view: EditorView | null): void {
  exec(view, wrapIn(composeSchema.nodes.blockquote!));
}

/**
 * Apply the link mark across the current selection. If the URL is empty
 * or the selection is empty, this is a no-op.
 */
export function applyLink(view: EditorView | null, href: string): void {
  if (!view) return;
  const trimmed = href.trim();
  if (!trimmed) return;
  const { from, to } = view.state.selection;
  if (from === to) return;
  const mark = composeSchema.marks.link!.create({ href: trimmed });
  const tr = view.state.tr.addMark(from, to, mark);
  view.dispatch(tr);
  view.focus();
}

export function removeLink(view: EditorView | null): void {
  if (!view) return;
  const { from, to } = view.state.selection;
  const tr = view.state.tr.removeMark(from, to, composeSchema.marks.link!);
  view.dispatch(tr);
  view.focus();
}

/**
 * Remove all image nodes whose `src` attribute matches the given URL
 * from the ProseMirror document. Used to retract a placeholder image
 * when its JMAP blob upload fails (issue #74).
 */
export function removeImageBySrc(view: EditorView | null, src: string): void {
  if (!view) return;
  const { doc, tr } = view.state;
  // Collect all positions of image nodes with this src in reverse order
  // so that deleting earlier nodes does not shift positions of later ones.
  const toDelete: Array<{ from: number; to: number }> = [];
  doc.descendants((node, pos) => {
    if (node.type.name === 'image' && node.attrs.src === src) {
      toDelete.push({ from: pos, to: pos + node.nodeSize });
    }
  });
  if (toDelete.length === 0) return;
  // Apply deletes in reverse order so positions stay valid.
  let transaction = tr;
  for (let i = toDelete.length - 1; i >= 0; i--) {
    const { from, to } = toDelete[i]!;
    transaction = transaction.delete(from, to);
  }
  view.dispatch(transaction);
}

/**
 * Insert an image node at the current cursor position. The src is a
 * `cid:<content-id>` URL that points to an inline part attached to the
 * outbound message at send time (issue #20). Alt text describes the
 * image when it can't be displayed.
 */
export function applyImage(
  view: EditorView | null,
  src: string,
  alt: string,
): void {
  if (!view) return;
  const trimmedSrc = src.trim();
  if (!trimmedSrc) return;
  const imageType = composeSchema.nodes.image;
  if (!imageType) return;
  const node = imageType.create({ src: trimmedSrc, alt: alt || null });
  const tr = view.state.tr.replaceSelectionWith(node, false);
  view.dispatch(tr);
  view.focus();
}
