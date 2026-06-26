<script lang="ts">
  /**
   * "Show original" modal (REQ-MAIL-142). Fetches the email's raw RFC 5322
   * blob and renders it in a monospace pre block with a copy-to-clipboard
   * affordance. Read-only; useful for debugging delivery and authentication.
   *
   * Closes on Escape, backdrop click, or the explicit Close button.
   */
  import { t } from '../i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import { untrack } from 'svelte';

  interface Props {
    open: boolean;
    /** Same-origin URL pointing at the JMAP blob endpoint for this email. */
    sourceUrl: string | null;
    /**
     * Suggested filename for the .eml download (REQ-MAIL-142a). Derived by
     * the caller from the email subject via emlDownloadFilename(). When null
     * the Download button is still functional; the browser falls back to
     * 'message.eml'.
     */
    filename?: string | null;
    onClose: () => void;
  }
  let { open, sourceUrl, filename = null, onClose }: Props = $props();

  type LoadState =
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'ready'; text: string }
    | { kind: 'error'; message: string };

  // Variable named `loadState` rather than `state` to avoid svelte-check
  // misreading `state.kind` as Svelte 4 store auto-subscribe (`$state.kind`).
  let loadState: LoadState = $state({ kind: 'idle' });
  let lastFetchedUrl: string | null = $state(null);
  let copied = $state(false);
  let copyResetTimer: ReturnType<typeof setTimeout> | null = null;

  // Trigger fetch when the modal becomes visible against a new URL. The
  // effect's tracked deps are `open` and `sourceUrl`; the writes to
  // `loadState` / `lastFetchedUrl` go inside `untrack` to avoid the
  // Svelte 5 effect-update-depth loop (see web/CLAUDE.md). Re-using the
  // modal for a second email re-fetches.
  $effect(() => {
    if (!open) return;
    const url = sourceUrl;
    untrack(() => {
      if (!url) {
        loadState = { kind: 'error', message: t('msg.rawSource.error') };
        return;
      }
      if (lastFetchedUrl === url && loadState.kind === 'ready') return;
      lastFetchedUrl = url;
      loadState = { kind: 'loading' };
      void load(url);
    });
  });

  async function load(url: string): Promise<void> {
    try {
      const res = await fetch(url, { credentials: 'include' });
      if (!res.ok) {
        loadState = { kind: 'error', message: `HTTP ${res.status}` };
        return;
      }
      const text = await res.text();
      loadState = { kind: 'ready', text };
    } catch (err) {
      loadState = {
        kind: 'error',
        message: err instanceof Error ? err.message : String(err),
      };
    }
  }

  /**
   * Save the loaded source as a .eml file. Creates a Blob from the already-
   * fetched text (no second network request), wires a temporary <a download>
   * element, clicks it, then revokes the object URL immediately so the
   * browser can GC the Blob. The clipboard is not involved — this path
   * works for large messages that the Clipboard API would reject.
   */
  function downloadEml(): void {
    if (loadState.kind !== 'ready') return;
    const blob = new Blob([loadState.text], { type: 'message/rfc822' });
    const url = URL.createObjectURL(blob);
    try {
      const a = document.createElement('a');
      a.href = url;
      a.download = filename ?? 'message.eml';
      a.style.display = 'none';
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
    } finally {
      URL.revokeObjectURL(url);
    }
  }

  async function copyToClipboard(): Promise<void> {
    if (loadState.kind !== 'ready') return;
    try {
      await navigator.clipboard.writeText(loadState.text);
      copied = true;
      if (copyResetTimer) clearTimeout(copyResetTimer);
      copyResetTimer = setTimeout(() => {
        copied = false;
        copyResetTimer = null;
      }, 1500);
    } catch {
      // Clipboard write can fail in non-secure contexts or when permission
      // is denied; in that case just no-op (the text is on-screen anyway).
    }
  }

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.stopPropagation();
      onClose();
    }
  }
</script>

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="backdrop" aria-hidden="true" onclick={onClose}></div>
  <div class="modal-wrapper">
    <div
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="rsm-title"
      tabindex="-1"
      onkeydown={handleKeydown}
    >
      <header class="header">
        <h2 id="rsm-title" class="title">{t('msg.rawSource.title')}</h2>
        <div class="header-actions">
          <Button variant="secondary" onclick={downloadEml} disabled={loadState.kind !== 'ready'}>
            {t('msg.rawSource.download')}
          </Button>
          <Button variant="secondary" onclick={copyToClipboard} disabled={loadState.kind !== 'ready'}>
            {copied ? t('msg.rawSource.copied') : t('msg.rawSource.copy')}
          </Button>
          <Button variant="ghost" onclick={onClose} ariaLabel={t('msg.rawSource.close')}>
            {t('msg.rawSource.close')}
          </Button>
        </div>
      </header>

      <div class="body">
        {#if loadState.kind === 'loading'}
          <p class="status">{t('msg.rawSource.loading')}</p>
        {:else if loadState.kind === 'error'}
          <p class="status status-error" role="alert">
            {t('msg.rawSource.error')}: {loadState.message}
          </p>
        {:else if loadState.kind === 'ready'}
          <pre class="source" data-testid="raw-source">{loadState.text}</pre>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    z-index: 299;
    cursor: default;
  }

  .modal-wrapper {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 300;
    pointer-events: none;
    padding: var(--spacing-05);
  }

  .modal {
    pointer-events: auto;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-05);
    max-width: 960px;
    width: 100%;
    max-height: calc(100vh - 2 * var(--spacing-05));
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
  }

  .title {
    margin: 0;
    font-size: var(--type-heading-02-size);
    font-weight: var(--type-heading-02-weight);
    line-height: var(--type-heading-02-line);
    color: var(--text-primary);
  }

  .header-actions {
    display: flex;
    gap: var(--spacing-03);
  }

  .body {
    flex: 1;
    overflow: auto;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .source {
    margin: 0;
    padding: var(--spacing-04);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    line-height: var(--type-code-01-line);
    color: var(--text-primary);
    white-space: pre;
    overflow: auto;
  }

  .status {
    margin: 0;
    padding: var(--spacing-04);
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
  }

  .status-error {
    color: var(--support-error);
  }
</style>
