<script lang="ts">
  import 'prosemirror-view/style/prosemirror.css';
  import {
    computeActive,
    createComposeEditor,
    docToHtml,
    docToText,
    EMPTY_ACTIVE,
    type ActiveState,
  } from './editor';
  import type { EditorView } from 'prosemirror-view';

  interface Props {
    initialHtml?: string;
    onUpdate?: (html: string, text: string) => void;
    onActiveChange?: (active: ActiveState) => void;
    onView?: (view: EditorView | null) => void;
    /** Called when the user removes an image from the editor (issue #83). */
    onImageRemoved?: (src: string) => void;
    /** Called with raw image bytes found on a paste's clipboard (issue #242). */
    onImagePaste?: (files: File[]) => void;
    autofocus?: boolean;
    /**
     * Set of blob: objectURLs currently in the 'uploading' state (issue #83).
     * The editor renders those image nodes with a greyed-out overlay so the
     * user knows the upload is still in progress.
     */
    uploadingSrcs?: ReadonlySet<string>;
    /**
     * Fold the auto-inserted reply/forward quote behind a toggle on mount
     * (issue #253). Only meaningful at the initial mount of a fresh
     * reply/forward — see the `collapseQuote` doc on `createComposeEditor`.
     */
    collapseQuote?: boolean;
  }

  let {
    initialHtml = '',
    onUpdate,
    onActiveChange,
    onView,
    onImageRemoved,
    onImagePaste,
    autofocus = false,
    uploadingSrcs = new Set<string>(),
    collapseQuote = false,
  }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);
  let currentView = $state<EditorView | null>(null);

  $effect(() => {
    if (!host) return;
    const view = createComposeEditor(host, {
      initialHtml,
      onChange: (state, docChanged) => {
        // Only re-serialize the document into the body string when it
        // actually changed. Selection-only transactions (clicking to
        // position the cursor) and the quote-fold toggle's metadata-only
        // transaction (issue #253) must not reassign compose.body -- see
        // the onChange doc comment on createComposeEditor.
        if (docChanged) {
          onUpdate?.(docToHtml(state.doc), docToText(state.doc));
        }
        onActiveChange?.(computeActive(state));
      },
      onImageRemoved,
      onImagePaste,
      collapseQuote,
    });
    currentView = view;
    onView?.(view);
    onActiveChange?.(EMPTY_ACTIVE);
    if (autofocus) {
      requestAnimationFrame(() => view.focus());
    }
    return () => {
      currentView = null;
      onView?.(null);
      view.destroy();
    };
  });

  /**
   * Apply/remove the 'img-uploading' CSS class on image elements in the
   * ProseMirror editor DOM when their upload status changes (issue #83).
   * We operate directly on the live DOM because ProseMirror does not
   * re-render nodes unless the document changes; this is a visual-only
   * overlay that does not affect the document model.
   */
  $effect(() => {
    const view = currentView;
    if (!view) return;
    const srcs = uploadingSrcs;
    const editorDom = view.dom;
    const imgs = editorDom.querySelectorAll<HTMLImageElement>('img');
    for (const img of imgs) {
      if (srcs.has(img.src)) {
        img.classList.add('img-uploading');
      } else {
        img.classList.remove('img-uploading');
      }
    }
  });
</script>

<div bind:this={host} class="rich-editor"></div>

<style>
  .rich-editor {
    width: 100%;
    min-height: 240px;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    overflow: auto;
  }
  .rich-editor :global(.ProseMirror) {
    min-height: 200px;
    padding: var(--spacing-04);
    outline: none;
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
  }
  .rich-editor:focus-within {
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }
  .rich-editor :global(.ProseMirror p) {
    margin: 0 0 var(--spacing-03);
  }
  .rich-editor :global(.ProseMirror p:last-child) {
    margin-bottom: 0;
  }
  .rich-editor :global(.ProseMirror ul),
  .rich-editor :global(.ProseMirror ol) {
    padding-left: var(--spacing-06);
    margin: 0 0 var(--spacing-03);
  }
  .rich-editor :global(.ProseMirror blockquote) {
    border-left: 3px solid var(--border-strong-01);
    margin: 0 0 var(--spacing-03);
    padding: 0 var(--spacing-04);
    color: var(--text-secondary);
  }
  /* Reply/forward quote fold (issue #253). `.cq-folded` is a view-level
     decoration applied by quoteFoldPlugin to the blockquote AND, when one
     immediately precedes it, the "On {date}, {sender} wrote:" / "Forwarded
     message" introducer paragraph -- both fold away together. It never
     touches the ProseMirror document, so the quoted content stays fully
     present in docToHtml()/docToText() output regardless of this
     display:none. */
  .rich-editor :global(.ProseMirror :is(p, blockquote).cq-folded) {
    display: none;
  }
  .rich-editor :global(.cq-fold-toggle) {
    display: inline-flex;
    align-items: center;
    margin: 0 0 var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    cursor: pointer;
  }
  .rich-editor :global(.cq-fold-toggle:hover) {
    background: var(--layer-hover-01);
  }
  .rich-editor :global(.cq-fold-toggle:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }
  .rich-editor :global(.ProseMirror code) {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    background: var(--layer-02);
    padding: 0 var(--spacing-02);
    border-radius: var(--radius-sm);
  }
  .rich-editor :global(.ProseMirror a) {
    color: var(--interactive);
  }
  .rich-editor :global(.ProseMirror u) {
    text-decoration: underline;
  }
  /* REQ-MAIL-24: cap inline image preview at the editor column width.
     The full-resolution bytes still ship in the outbound MIME — this is
     a pure CSS visual cap so a 4032×3024 phone photo does not blow up
     the compose pane. height:auto preserves aspect ratio. */
  .rich-editor :global(.ProseMirror img) {
    max-width: 100%;
    height: auto;
  }

  /* Upload progress overlay for inline images (issue #83).
     img-uploading is applied imperatively via the $effect above while
     the corresponding ComposeAttachment.status === 'uploading'.
     The image fades to greyscale so the user sees that upload is in flight. */
  .rich-editor :global(.ProseMirror img.img-uploading) {
    opacity: 0.4;
    filter: grayscale(60%);
  }
</style>
