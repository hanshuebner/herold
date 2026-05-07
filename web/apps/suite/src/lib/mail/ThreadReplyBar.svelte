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

  function reply(): void {
    void compose.openReply(target);
  }
  function replyAll(): void {
    compose.openReplyAll(target);
  }
  function forward(): void {
    compose.openForward(target);
  }
</script>

<div class="reply-bar" role="toolbar" aria-label={t('msg.reply')}>
  <button type="button" class="action-btn" aria-label={t('msg.reply')} title={t('msg.reply')} onclick={reply}>
    <ReplyIcon size={16} />
    <span class="btn-label">{t('msg.reply')}</span>
  </button>
  {#if hasMultipleRecipients}
    <button type="button" class="action-btn" aria-label={t('msg.replyAll')} title={t('msg.replyAll')} onclick={replyAll}>
      <ReplyAllIcon size={16} />
      <span class="btn-label">{t('msg.replyAll')}</span>
    </button>
  {/if}
  <button type="button" class="action-btn" aria-label={t('msg.forward')} title={t('msg.forward')} onclick={forward}>
    <ForwardIcon size={16} />
    <span class="btn-label">{t('msg.forward')}</span>
  </button>
</div>

<style>
  /* Mirror ThreadToolbar's container styling so the bottom CTAs feel
     like a peer of the top toolbar (re #98). */
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

  /* Same pill shape and colours as ThreadToolbar's .action-btn.
     Kept in sync deliberately — these two surfaces should read as one
     family of buttons, not two competing visual systems. */
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
