<script lang="ts">
  import { mail } from './store.svelte';
  import { t } from '../i18n/i18n.svelte';
  import type { Email } from './types';

  interface Props {
    threadId: string;
    /**
     * Invoked when the user clicks "Show new reply". Receives the latest
     * arrival's email id so ThreadReader can scroll/expand it. The banner
     * is always cleared from `pendingArrivals` after the click; callers
     * decide what (if anything) to focus next.
     */
    onShow: (emailId: string) => void;
  }

  let { threadId, onShow }: Props = $props();

  let arrivals = $derived(mail.pendingArrivalsForThread(threadId));
  let latest = $derived<Email | undefined>(arrivals[arrivals.length - 1]);
  let extra = $derived(arrivals.length - 1);

  function senderLabel(email: Email | undefined): string {
    if (!email) return '';
    const from = email.from?.[0];
    if (!from) return '';
    return from.name?.trim() || from.email;
  }

  function previewText(email: Email | undefined): string {
    if (!email) return '';
    return email.preview?.trim() ?? '';
  }

  function dismiss(): void {
    mail.dismissPendingArrivals(threadId);
  }

  function show(): void {
    if (!latest) {
      dismiss();
      return;
    }
    const id = latest.id;
    dismiss();
    onShow(id);
  }
</script>

{#if arrivals.length > 0 && latest}
  <div class="new-reply-banner" role="status" aria-label={t('mail.threadReader.newReply.aria')}>
    <div class="text">
      <strong class="heading">
        {#if arrivals.length === 1}
          {t('mail.threadReader.newReply.one.heading', { sender: senderLabel(latest) })}
        {:else}
          {t('mail.threadReader.newReply.many.heading', { count: arrivals.length })}
        {/if}
      </strong>
      {#if previewText(latest)}
        <span class="preview">{previewText(latest)}</span>
      {/if}
      {#if extra > 0}
        <span class="more">{t('mail.threadReader.newReply.more', { count: extra })}</span>
      {/if}
    </div>
    <div class="actions">
      <button type="button" class="primary" onclick={show}>
        {t('mail.threadReader.newReply.show')}
      </button>
      <button type="button" class="secondary" onclick={dismiss}>
        {t('mail.threadReader.newReply.dismiss')}
      </button>
    </div>
  </div>
{/if}

<style>
  /* Inline non-modal banner inserted into the thread reader above the
     first freshly-arrived message (issue #118). Calm light-blue chrome,
     not a warning; pulses a thin border for the first 2s to draw the
     eye, then settles. Persists until the user dismisses or clicks
     Show new reply. */
  .new-reply-banner {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-05);
    margin: var(--spacing-04) var(--spacing-05) 0;
    background: var(--layer-01);
    border: 1px solid var(--interactive);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    animation: new-reply-pulse 800ms ease-out 0s 2;
  }
  .text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .heading {
    color: var(--text-primary);
    font-weight: 600;
  }
  .preview {
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    word-break: break-word;
  }
  .more {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .actions {
    display: flex;
    flex-shrink: 0;
    align-items: center;
    gap: var(--spacing-03);
  }
  .primary {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .primary:hover {
    background: var(--interactive-hover, var(--interactive));
    filter: brightness(0.95);
  }
  .secondary {
    padding: var(--spacing-02) var(--spacing-03);
    background: transparent;
    color: var(--text-secondary);
    border-radius: var(--radius-md);
    font-weight: 500;
    min-height: var(--touch-min);
  }
  .secondary:hover {
    color: var(--text-primary);
    background: var(--layer-02);
  }

  @keyframes new-reply-pulse {
    0% {
      box-shadow: 0 0 0 0 var(--interactive);
    }
    70% {
      box-shadow: 0 0 0 6px transparent;
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .new-reply-banner {
      animation: none;
    }
  }

  @media (max-width: 560px) {
    .new-reply-banner {
      flex-direction: column;
      align-items: stretch;
    }
    .actions {
      justify-content: flex-end;
    }
  }
</style>
