<script lang="ts">
  /**
   * Always-visible Reply / Reply All / Forward strip pinned to the
   * bottom of the thread reader. Operates on the most-recent email in
   * the thread (the one a reply would normally target). Reply-All only
   * appears when the targeted email has more than one recipient (any
   * second To address or any Cc) — otherwise it is identical to Reply
   * and just clutters the bar.
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
  <button type="button" class="action primary" onclick={reply}>
    <ReplyIcon size={18} />
    <span>{t('msg.reply')}</span>
  </button>
  {#if hasMultipleRecipients}
    <button type="button" class="action" onclick={replyAll}>
      <ReplyAllIcon size={18} />
      <span>{t('msg.replyAll')}</span>
    </button>
  {/if}
  <button type="button" class="action" onclick={forward}>
    <ForwardIcon size={18} />
    <span>{t('msg.forward')}</span>
  </button>
</div>

<style>
  .reply-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-04) var(--spacing-05);
    background: var(--background);
    border-top: 1px solid var(--border-subtle-01);
    flex-shrink: 0;
  }
  .action {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
    border: 1px solid var(--border-subtle-02);
    color: var(--text-primary);
    background: transparent;
    font-weight: 500;
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .action:hover {
    background: var(--layer-02);
  }
  .action.primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border-color: var(--interactive);
  }
  .action.primary:hover {
    filter: brightness(1.05);
  }

  @media print {
    .reply-bar {
      display: none;
    }
  }
</style>
