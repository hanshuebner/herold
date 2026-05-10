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
  import { t } from '../i18n/i18n.svelte';
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
      title: t('composeToolbar.linkPrompt.title'),
      label: 'URL',
      placeholder: 'https://example.com',
      confirmLabel: t('composeToolbar.linkPrompt.confirm'),
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
        message: t('composeToolbar.imageBadType'),
        kind: 'error',
        timeoutMs: 4000,
      });
      return;
    }
    // Insert each picked image sequentially using the two-phase approach
    // (startInlineImage + uploadInlineImage) so the thumbnail appears in
    // the editor immediately before the JMAP round-trip begins (issue #83).
    // Serial insertion keeps ProseMirror cursor positions predictable.
    const pending: Array<{ key: string; objectURL: string; file: File }> = [];
    for (const file of images) {
      const started = compose.startInlineImage(file);
      if (!started) {
        toast.show({
          message: t('composeToolbar.imageUploadFailed', { name: file.name }),
          kind: 'error',
          timeoutMs: 4000,
        });
        continue;
      }
      // Insert immediately so the editor shows the thumbnail at once.
      // The blob: URL is rewritten to cid: on send/save.
      applyImage(view, started.objectURL, file.name);
      pending.push({ key: started.key, objectURL: started.objectURL, file });
    }
    // Run uploads in parallel; retract placeholder on failure.
    await Promise.all(
      pending.map(async ({ key, objectURL, file: f }) => {
        const errMsg = await compose.uploadInlineImage(key, f);
        if (errMsg) {
          toast.show({
            message: t('composeToolbar.imageUploadFailed', { name: f.name }),
            kind: 'error',
            timeoutMs: 4000,
          });
          // Remove the in-editor placeholder so the body stays consistent.
          const { removeImageBySrc } = await import('./editor');
          removeImageBySrc(view, objectURL);
        }
      }),
    );
  }
</script>

<div class="toolbar" role="toolbar" aria-label={t('composeToolbar.aria')}>
  <button
    type="button"
    class="tool"
    class:on={active.strong}
    aria-pressed={active.strong}
    aria-label={t('composeToolbar.bold')}
    title={`${t('composeToolbar.bold')} (Mod+B)`}
    onclick={() => applyBold(view)}
  >
    <span class="glyph"><b>B</b></span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.em}
    aria-pressed={active.em}
    aria-label={t('composeToolbar.italic')}
    title={`${t('composeToolbar.italic')} (Mod+I)`}
    onclick={() => applyItalic(view)}
  >
    <span class="glyph"><i>I</i></span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.underline}
    aria-pressed={active.underline}
    aria-label={t('composeToolbar.underline')}
    title={`${t('composeToolbar.underline')} (Mod+U)`}
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
    aria-label={t('composeToolbar.bulletList')}
    title={t('composeToolbar.bulletList')}
    onclick={() => applyBulletList(view)}
  >
    <span class="glyph">• —</span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.orderedList}
    aria-pressed={active.orderedList}
    aria-label={t('composeToolbar.orderedList')}
    title={t('composeToolbar.orderedList')}
    onclick={() => applyOrderedList(view)}
  >
    <span class="glyph">1.</span>
  </button>
  <button
    type="button"
    class="tool"
    class:on={active.blockquote}
    aria-pressed={active.blockquote}
    aria-label={t('composeToolbar.blockquote')}
    title={t('composeToolbar.blockquote')}
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
    aria-label={active.link ? t('composeToolbar.linkRemove') : t('composeToolbar.linkAdd')}
    title={`${t('composeToolbar.linkAdd')} (Mod+K)`}
    onclick={promptLink}
  >
    <span class="glyph">⎘</span>
  </button>

  <button
    type="button"
    class="tool"
    aria-label={t('composeToolbar.image')}
    title={t('composeToolbar.image')}
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
