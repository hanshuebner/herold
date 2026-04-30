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
    autofocus?: boolean;
  }

  let {
    initialHtml = '',
    onUpdate,
    onActiveChange,
    onView,
    autofocus = false,
  }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);

  $effect(() => {
    if (!host) return;
    const view = createComposeEditor(host, {
      initialHtml,
      onChange: (state) => {
        onUpdate?.(docToHtml(state.doc), docToText(state.doc));
        onActiveChange?.(computeActive(state));
      },
    });
    onView?.(view);
    onActiveChange?.(EMPTY_ACTIVE);
    if (autofocus) {
      requestAnimationFrame(() => view.focus());
    }
    return () => {
      onView?.(null);
      view.destroy();
    };
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
</style>
