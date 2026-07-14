/**
 * Non-Svelte ProseMirror helpers — schema-aware doc / HTML / text
 * conversions, mark / list-active queries, and editor-view construction.
 *
 * The Svelte wrapper (RichEditor.svelte) keeps a thin reactive bridge for
 * the toolbar; everything imperative lives here.
 *
 * Image lifecycle hooks (issue #83):
 *   onImageRemoved — called with the src of each image node that disappears
 *   from the document in a transaction (user Delete/Backspace/Cut). The
 *   compose store uses this to drop the corresponding attachment record so
 *   deleted inline images do not appear in the outbound MIME part list.
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
  chainCommands,
  splitBlock,
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
import { settings, type ComposerEnterMode } from '../settings/settings.svelte';

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
 * Insert a hard_break node at the cursor (re #33).
 * Replaces the selection with a hard_break node from the compose schema.
 */
function insertHardBreak(
  state: EditorStateType,
  dispatch?: (tr: Transaction) => void,
): boolean {
  const br = composeSchema.nodes.hard_break;
  if (!br) return false;
  if (dispatch) dispatch(state.tr.replaceSelectionWith(br.create()).scrollIntoView());
  return true;
}

/**
 * Build the editor's keymap. Mod = Cmd on macOS, Ctrl elsewhere
 * (prosemirror-keymap handles the platform check internally).
 *
 * Mode 'paragraph' (default): Enter = new paragraph (falls through to
 * baseKeymap splitBlock when not in a list), Shift-Enter = hard_break.
 * Mode 'linebreak': Enter = hard_break (after splitListItem), Shift-Enter
 * = new paragraph via splitBlock.
 * In both modes splitListItem takes priority when the caret is inside a
 * list item (re #33).
 */
function composeKeymap(mode: ComposerEnterMode) {
  const m = composeSchema.marks;
  const n = composeSchema.nodes;

  const inListEnter = splitListItem(n.list_item!);

  // In linebreak mode Enter inserts a hard_break even outside lists.
  // In paragraph mode the splitListItem falls through to baseKeymap's
  // Enter chain (splitBlock etc.) when the caret is not in a list.
  const enterCmd =
    mode === 'linebreak'
      ? chainCommands(inListEnter, insertHardBreak)
      : inListEnter;

  const shiftEnterCmd = mode === 'linebreak' ? splitBlock : insertHardBreak;

  return keymap({
    'Mod-z': undo,
    'Mod-y': redo,
    'Mod-Shift-z': redo,
    'Mod-b': toggleMark(m.strong!),
    'Mod-i': toggleMark(m.em!),
    'Mod-u': toggleMark(m.underline!),
    'Mod-`': toggleMark(m.code!),
    Enter: enterCmd,
    'Shift-Enter': shiftEnterCmd,
    'Mod-[': liftListItem(n.list_item!),
    'Mod-]': sinkListItem(n.list_item!),
  });
}

/**
 * Collect all `src` attribute values of image nodes in a document.
 * Used to diff before/after states to detect removed images (issue #83).
 */
export function collectImageSrcs(doc: Node): Set<string> {
  const srcs = new Set<string>();
  doc.descendants((node) => {
    if (node.type.name === 'image') {
      const src = node.attrs.src as string | undefined;
      if (src) srcs.add(src);
    }
  });
  return srcs;
}

/**
 * True when a clipboard-supplied image `src` is safe to keep verbatim in
 * pasted HTML: it already resolves to the herold origin (a `blob:` URL
 * created by this tab, or a same-origin path), a `cid:` reference (the
 * scheme the compose schema itself emits for uploaded inline images), or a
 * `data:` URI (self-contained — no cross-site fetch happens when it
 * renders). Any other URL is a foreign resource that the browser will
 * refuse to load cross-site (issue #242) and must not survive paste.
 */
export function isAllowedPastedImageSrc(src: string): boolean {
  if (!src) return false;
  if (src.startsWith('cid:') || src.startsWith('data:')) return true;
  try {
    return new URL(src, window.location.href).origin === window.location.origin;
  } catch {
    return false;
  }
}

/**
 * Remove `<img>` elements whose `src` is not herold-origin/cid:/data: from
 * a pasted HTML fragment (issue #242). Used as `transformPastedHTML` so a
 * clipboard flavor carrying only a foreign image URL (no raw image bytes)
 * never becomes a broken image node instead of being silently dropped.
 */
export function stripForeignImagesFromPastedHtml(html: string): string {
  if (!html.includes('<img')) return html;
  const wrapper = document.createElement('div');
  wrapper.innerHTML = html;
  wrapper.querySelectorAll('img[src]').forEach((img) => {
    if (!isAllowedPastedImageSrc(img.getAttribute('src') ?? '')) {
      img.remove();
    }
  });
  return wrapper.innerHTML;
}

/**
 * Extract raw image `File`s from a paste/drop `DataTransfer` (issue #242).
 * Clipboard sources that put both a `text/html` flavor (e.g. a foreign
 * `<img src>`) and the raw image bytes on the clipboard — Google Photos
 * among them — must have the raw bytes win: this is what lets the paste
 * handler route the image through the same upload-and-`cid:` flow as
 * drag-drop instead of falling through to the HTML parse.
 */
export function extractPastedImageFiles(dataTransfer: DataTransfer | null): File[] {
  if (!dataTransfer) return [];
  const files: File[] = [];
  if (dataTransfer.files && dataTransfer.files.length > 0) {
    for (const file of Array.from(dataTransfer.files)) {
      if (file.type.startsWith('image/')) files.push(file);
    }
  }
  if (files.length === 0 && dataTransfer.items) {
    for (const item of Array.from(dataTransfer.items)) {
      if (item.kind === 'file' && item.type.startsWith('image/')) {
        const file = item.getAsFile();
        if (file) files.push(file);
      }
    }
  }
  return files;
}

export function createComposeEditor(
  target: HTMLElement,
  options: {
    initialHtml: string;
    onChange: (state: EditorStateType) => void;
    /** Called with the src of each image node removed from the doc (issue #83). */
    onImageRemoved?: (src: string) => void;
    /**
     * Called with the raw image File(s) found on a paste's clipboard data
     * (issue #242). The caller routes them through the same upload-and-cid
     * flow used for drag-dropped files; the paste handler suppresses
     * ProseMirror's default HTML-flavor paste whenever this fires so a
     * foreign clipboard URL never wins over the raw bytes.
     */
    onImagePaste?: (files: File[]) => void;
  },
): EditorView {
  const doc = htmlToDoc(options.initialHtml);
  // Read the enter-mode preference at construction time so the keymap
  // reflects whatever the user had set when the compose window opened.
  // Changing the setting while the window is open takes effect on next open.
  const state = EditorState.create({
    schema: composeSchema,
    doc,
    plugins: [history(), composeKeymap(settings.composerEnterMode), keymap(baseKeymap)],
  });
  const view = new EditorView(target, {
    state,
    dispatchTransaction(tr: Transaction) {
      const prev = view.state;
      const next = prev.apply(tr);
      view.updateState(next);
      options.onChange(next);
      // Detect image removals when doc changed and a removal callback exists.
      if (options.onImageRemoved && tr.docChanged) {
        const prevSrcs = collectImageSrcs(prev.doc);
        const nextSrcs = collectImageSrcs(next.doc);
        for (const src of prevSrcs) {
          if (!nextSrcs.has(src)) {
            options.onImageRemoved(src);
          }
        }
      }
    },
    handleDOMEvents: {
      // Raw image bytes on the clipboard (issue #242) take priority over
      // any accompanying text/html flavor: extract them here, before
      // ProseMirror's own clipboard pipeline runs, and hand them to the
      // caller's upload flow. Returning true (after preventDefault) skips
      // ProseMirror's default paste handling entirely, so the foreign
      // <img src> in the HTML flavor never gets a chance to be parsed.
      paste(_view, event: ClipboardEvent) {
        const files = extractPastedImageFiles(event.clipboardData);
        if (files.length === 0 || !options.onImagePaste) return false;
        event.preventDefault();
        options.onImagePaste(files);
        return true;
      },
    },
    // No raw image bytes on the clipboard: fall back to ProseMirror's
    // default HTML paste, but strip any <img> that isn't herold-origin so
    // a foreign URL (e.g. Google Photos serving only a link, no bytes)
    // does not survive as a broken image node (issue #242).
    transformPastedHTML: stripForeignImagesFromPastedHtml,
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
 * Replace the `src` attribute of every image node matching `oldSrc` with
 * `newSrc`, in place (issue #243). Used to swap a large inline image's
 * full-resolution blob: URL for a downscaled proxy once one has been
 * generated, without disturbing the node's position, alt text, or the
 * surrounding document.
 */
export function replaceImageSrc(
  view: EditorView | null,
  oldSrc: string,
  newSrc: string,
): void {
  if (!view) return;
  const { doc, tr } = view.state;
  let transaction = tr;
  let changed = false;
  doc.descendants((node, pos) => {
    if (node.type.name === 'image' && node.attrs.src === oldSrc) {
      transaction = transaction.setNodeAttribute(pos, 'src', newSrc);
      changed = true;
    }
  });
  if (changed) view.dispatch(transaction);
}

/**
 * Insert an HTML fragment at the end of the document (before the
 * signature delimiter when present). Used by the share-link offload path
 * to inject the download-link block directly into the live ProseMirror
 * document so the resulting `onUpdate` propagates the new HTML back into
 * `compose.body` naturally (REQ-ATT-63).
 *
 * The HTML is parsed into ProseMirror nodes via the compose schema's
 * DOMParser. The insertion point is the end of the document. If the
 * last top-level block is the signature-delimiter paragraph (`<p>-- </p>`),
 * the block is inserted before it; otherwise it is appended.
 */
export function insertHtmlBlockAtEnd(view: EditorView | null, html: string): void {
  if (!view) return;
  const trimmed = html.trim();
  if (!trimmed) return;
  const wrapper = document.createElement('div');
  wrapper.innerHTML = trimmed;
  // Parse the wrapper as a full document and take the content fragment,
  // which is a sequence of block nodes ready to be inserted.
  const parsedDoc = PMDOMParser.fromSchema(composeSchema).parse(wrapper);
  const fragment = parsedDoc.content;
  const { doc, tr } = view.state;
  // Find the insertion point: before the first signature-delimiter paragraph
  // (content "-- " i.e. trimmed "--") when present; otherwise append.
  let insertPos = doc.content.size;
  let foundSig = false;
  doc.forEach((node, offset) => {
    if (
      !foundSig &&
      node.type.name === 'paragraph' &&
      node.textContent.trim() === '--'
    ) {
      insertPos = offset;
      foundSig = true;
    }
  });
  const transaction = tr.insert(insertPos, fragment);
  view.dispatch(transaction);
}

/**
 * Remove all top-level block nodes that contain a link whose `href`
 * matches the given URL. Used by the share-link offload path to retract
 * the download-link block from the live ProseMirror document (REQ-ATT-64).
 *
 * ProseMirror's DOMParser does not preserve arbitrary `data-*` attributes
 * (the compose schema's paragraph spec does not declare them). We therefore
 * match share-link blocks by scanning for nodes that contain the share URL
 * as a link mark — each share URL is unique within a compose session so
 * this is an unambiguous identifier.
 */
export function removeHtmlBlockByShareUrl(
  view: EditorView | null,
  shareUrl: string,
): void {
  if (!view) return;
  const { doc, tr } = view.state;
  const linkMarkType = composeSchema.marks.link;
  if (!linkMarkType) return;
  const toDelete: Array<{ from: number; to: number }> = [];
  doc.forEach((node, offset) => {
    // Walk all inline content of this block looking for a link with the URL.
    let found = false;
    node.descendants((inlineNode) => {
      if (found) return false;
      const linkMark = inlineNode.marks.find(
        (m) => m.type === linkMarkType && (m.attrs.href as string) === shareUrl,
      );
      if (linkMark) found = true;
      return true;
    });
    if (found) {
      toDelete.push({ from: offset, to: offset + node.nodeSize });
    }
  });
  if (toDelete.length === 0) return;
  // Apply in reverse so earlier positions stay valid after each delete.
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
