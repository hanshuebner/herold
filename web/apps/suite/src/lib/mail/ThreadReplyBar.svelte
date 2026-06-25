<script lang="ts">
  /**
   * Always-visible Reply / Reply All / Forward strip pinned to the
   * bottom of the thread reader. Operates on the most-recent email in
   * the thread (the one a reply would normally target). Reply-All only
   * appears when the targeted email has more than one recipient (any
   * second To address or any Cc) — otherwise it is identical to Reply
   * and just clutters the bar.
   *
   * Per re #98 the bar renders its CTAs with the same visual language
   * as the ThreadToolbar (transparent pill, icon + label). Previously
   * the Reply pill was solid-coloured "primary" while the toolbar used
   * ghost pills, which made the two action surfaces feel disconnected.
   *
   * Per re #38 clicking Reply / Reply All / Forward opens the compose
   * inline inside ThreadReader rather than as a floating modal. The bar
   * hides itself while the inline composer is visible; the pop-out
   * control in the inline composer restores the floating window.
   *
   * The compose handlers (compose.openReplyAll especially) preserve
   * To/Cc per REQ-MAIL-31..32 and the spec for reply-all on own
   * messages — that logic lives in compose.svelte.ts.
   */
  import { compose } from '../compose/compose.svelte';
  import { t } from '../i18n/i18n.svelte';
  import ReplyIcon from '../icons/ReplyIcon.svelte';
  import ReplyAllIcon from '../icons/ReplyAllIcon.svelte';
  import ForwardIcon from '../icons/ForwardIcon.svelte';
  import type { Email } from './types';

  interface Props {
    /** The email a reply / forward should target — typically the
     *  most recent email in the thread. */
    target: Email;
  }
  let { target }: Props = $props();

  let hasMultipleRecipients = $derived(
    (target.to?.length ?? 0) > 1 || (target.cc?.length ?? 0) > 0,
  );

  // Hide while the inline composer is open. When the user pops out to the
  // floating window, compose.inlineMode becomes false and this bar reappears.
  let isInlineOpen = $derived(compose.isOpen && compose.inlineMode);

  // Set inlineMode BEFORE calling openReply so ComposeWindow never renders
  // the floating modal (prevents a one-frame flash on slow identity loads).
  // If the beforeOpenHook blocks the open, status stays idle and the inline
  // composer doesn't show — inlineMode resets on the next close() call.
  async function reply(): Promise<void> {
    compose.inlineMode = true;
    await compose.openReply(target);
    if (!compose.isOpen) compose.inlineMode = false;
  }
  function replyAll(): void {
    compose.inlineMode = true;
    compose.openReplyAll(target);
    if (!compose.isOpen) compose.inlineMode = false;
  }
  function forward(): void {
    compose.inlineMode = true;
    compose.openForward(target);
    if (!compose.isOpen) compose.inlineMode = false;
  }
</script>

{#if !isInlineOpen}
  <div class="reply-bar" role="toolbar" aria-label={t('msg.reply')}>
    <button
      type="button"
      class="action-btn"
      aria-label={t('msg.reply')}
      title={t('msg.reply')}
      onclick={() => void reply()}
    >
      <ReplyIcon size={16} />
      <span class="btn-label">{t('msg.reply')}</span>
    </button>
    {#if hasMultipleRecipients}
      <button
        type="button"
        class="action-btn"
        aria-label={t('msg.replyAll')}
        title={t('msg.replyAll')}
        onclick={replyAll}
      >
        <ReplyAllIcon size={16} />
        <span class="btn-label">{t('msg.replyAll')}</span>
      </button>
    {/if}
    <button
      type="button"
      class="action-btn"
      aria-label={t('msg.forward')}
      title={t('msg.forward')}
      onclick={forward}
    >
      <ForwardIcon size={16} />
      <span class="btn-label">{t('msg.forward')}</span>
    </button>
  </div>
{/if}

<style>
  .reply-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--background);
    border-top: 1px solid var(--border-subtle-01);
    flex-shrink: 0;
    flex-wrap: wrap;
  }

  /* Same visual language as ThreadToolbar .action-btn so the two action
     surfaces read as one cohesive set (re #98, re #2). */
  .action-btn {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    background: transparent;
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: 32px;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .action-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .btn-label {
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }

  @media print {
    .reply-bar {
      display: none;
    }
  }
</style>
