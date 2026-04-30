<script lang="ts">
  import type { EditorView } from 'prosemirror-view';
  import {
    applyBlockquote,
    applyBold,
    applyBulletList,
    applyImage,
    applyItalic,
    applyLink,
    applyOrderedList,
    applyUnderline,
    removeLink,
    type ActiveState,
  } from './editor';
  import { prompt } from '../dialog/prompt.svelte';
  import { compose } from './compose.svelte';
  import { toast } from '../toast/toast.svelte';
  import ImageIcon from '../icons/ImageIcon.svelte';

  interface Props {
    view: EditorView | null;
    active: ActiveState;
  }
  let { view, active }: Props = $props();

  let imageInput = $state<HTMLInputElement | null>(null);

  async function promptLink(): Promise<void> {
    if (!view) return;
    if (active.link) {
      removeLink(view);
      return;
    }
    const url = await prompt.ask({
      title: 'Insert link',
      label: 'URL',
      placeholder: 'https://example.com',
      confirmLabel: 'Insert',
    });
    if (!url) {
      view.focus();
      return;
    }
    applyLink(view, url);
  }

  function pickImage(): void {
    imageInput?.click();
  }

  async function onImagePicked(e: Event): Promise<void> {
    const input = e.currentTarget as HTMLInputElement;
    const files = Array.from(input.files ?? []);
    input.value = '';
    if (files.length === 0 || !view) return;
    const images = files.filter((f) => f.type.startsWith('image/'));
    if (images.length === 0) {
      toast.show({
        message: 'Pick image files (PNG, JPEG, GIF, WebP).',
        kind: 'error',
        timeoutMs: 4000,
      });
      return;
    }
    // Insert each picked image sequentially so the editor's cursor
    // advances between insertions and every image receives its own
    // <img> node + unique cid. A Promise.all would interleave
    // addInlineImage calls and applyImage dispatches in unpredictable
    // order.
    for (const file of images) {
      const result = await compose.addInlineImage(file);
      if (!result) {
        toast.show({
          message: `Image upload failed: ${file.name}`,
          kind: 'error',
          timeoutMs: 4000,
        });
        continue;
      }
      // Use the blob: URL for in-editor preview; persistDraft and
      // send() rewrite it to cid:<cid> before the message goes to JMAP.
      applyImage(view, result.objectURL, file.name);
    }
  }
</script>

<div class="toolbar" role="toolbar" aria-label="Formatting">
  <button
    type="button"
    class="tool"
    class:on={active.strong}
    aria-pressed={active.strong}
    aria-label="Bold"
    title="Bold (Mod+B)"
    onclick={() => applyBold(view)}
  >
    <span class="glyph"><b>B</b></span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.em}
    aria-pressed={active.em}
    aria-label="Italic"
    title="Italic (Mod+I)"
    onclick={() => applyItalic(view)}
  >
    <span class="glyph"><i>I</i></span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.underline}
    aria-pressed={active.underline}
    aria-label="Underline"
    title="Underline (Mod+U)"
    onclick={() => applyUnderline(view)}
  >
    <span class="glyph"><u>U</u></span>
  </button>

  <span class="sep" aria-hidden="true"></span>

  <button
    type="button"
    class="tool"
    class:on={active.bulletList}
    aria-pressed={active.bulletList}
    aria-label="Bulleted list"
    title="Bulleted list"
    onclick={() => applyBulletList(view)}
  >
    <span class="glyph">• —</span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.orderedList}
    aria-pressed={active.orderedList}
    aria-label="Numbered list"
    title="Numbered list"
    onclick={() => applyOrderedList(view)}
  >
    <span class="glyph">1.</span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.blockquote}
    aria-pressed={active.blockquote}
    aria-label="Blockquote"
    title="Blockquote"
    onclick={() => applyBlockquote(view)}
  >
    <span class="glyph">”</span>
  </button>

  <span class="sep" aria-hidden="true"></span>

  <button
    type="button"
    class="tool"
    class:on={active.link}
    aria-pressed={active.link}
    aria-label={active.link ? 'Remove link' : 'Add link'}
    title="Link (Mod+K)"
    onclick={promptLink}
  >
    <span class="glyph">⎘</span>
  </button>

  <button
    type="button"
    class="tool"
    aria-label="Insert image"
    title="Insert image"
    onclick={pickImage}
  >
    <ImageIcon size={18} />
  </button>
  <input
    bind:this={imageInput}
    type="file"
    accept="image/*"
    multiple
    hidden
    onchange={onImagePicked}
  />
</div>

<style>
  .toolbar {
    display: flex;
    align-items: center;
    gap: var(--spacing-01);
    padding: var(--spacing-02);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }
  .tool {
    width: 32px;
    height: 32px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .tool:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .tool.on {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .sep {
    width: 1px;
    height: 20px;
    background: var(--border-subtle-01);
    margin: 0 var(--spacing-02);
  }
  .glyph {
    line-height: 1;
  }
</style>
