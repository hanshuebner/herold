<script lang="ts">
  /**
   * Mailing-list chip (REQ-LIST-10..12, 20..22).
   *
   * Rendered per-message (not per-thread) beside the sender: a message
   * that has no `List-ID` header renders no chip at all, so a thread
   * mixing list and non-list messages (forwarded-out-of-the-list case,
   * REQ-LIST-12) shows the chip only on the messages that actually
   * carry the header.
   *
   * Hovering (and, for keyboard/touch users, focusing) reveals a
   * popover with the available `List-*` actions; an action is hidden
   * entirely when its backing header is absent (REQ-LIST-20/21/22).
   */
  import { compose } from '../compose/compose.svelte';
  import { toast } from '../toast/toast.svelte';
  import { t } from '../i18n/i18n.svelte';
  import type { Email } from './types';
  import {
    parseListId,
    parseAngleBracketUrls,
    pickPreferredAction,
    parseListPostAddress,
    parseMailtoUri,
    type ListAction,
  } from './list-headers';

  interface Props {
    email: Email;
  }
  let { email }: Props = $props();

  let listInfo = $derived(parseListId(email['header:List-ID:asText']));

  let archiveAction = $derived.by<ListAction | null>(() => {
    const urls = parseAngleBracketUrls(email['header:List-Archive:asText']);
    if (urls.length === 0) return null;
    return pickPreferredAction(urls, { allowMailto: false });
  });

  let helpAction = $derived.by<ListAction | null>(() => {
    const urls = parseAngleBracketUrls(email['header:List-Help:asText']);
    if (urls.length === 0) return null;
    return pickPreferredAction(urls, { allowMailto: true });
  });

  let postAddress = $derived(parseListPostAddress(email['header:List-Post:asText']));

  let open = $state(false);
  let closeTimer: ReturnType<typeof setTimeout> | null = null;

  function show(): void {
    if (closeTimer) {
      clearTimeout(closeTimer);
      closeTimer = null;
    }
    open = true;
  }

  function scheduleHide(): void {
    if (closeTimer) clearTimeout(closeTimer);
    closeTimer = setTimeout(() => {
      open = false;
      closeTimer = null;
    }, 150);
  }

  function warnCleartext(): void {
    toast.show({ message: t('mailingList.cleartextWarning'), kind: 'info' });
  }

  function runAction(action: ListAction | null, opts: { allowCompose: boolean }): void {
    if (!action) return;
    if (action.kind === 'http-only') {
      warnCleartext();
      return;
    }
    if (action.kind === 'https') {
      window.open(action.url, '_blank', 'noopener,noreferrer');
      return;
    }
    if (action.kind === 'mailto' && opts.allowCompose) {
      const fields = parseMailtoUri(action.url);
      compose.openWith({
        to: fields.to,
        subject: fields.subject,
        body: fields.body,
        skipSignature: true,
      });
    }
  }

  function viewArchive(): void {
    runAction(archiveAction, { allowCompose: false });
    open = false;
  }

  function getHelp(): void {
    runAction(helpAction, { allowCompose: true });
    open = false;
  }

  async function replyToList(): Promise<void> {
    open = false;
    if (!postAddress) return;
    compose.inlineMode = true;
    await compose.openReplyToList(email, postAddress);
    if (!compose.isOpen) compose.inlineMode = false;
  }
</script>

{#if listInfo}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <span
    class="list-chip-anchor"
    data-testid="list-chip-anchor"
    onclick={(e) => e.stopPropagation()}
    onkeydown={(e) => e.stopPropagation()}
    onmouseenter={show}
    onmouseleave={scheduleHide}
    onfocusin={show}
    onfocusout={scheduleHide}
    role="presentation"
  >
    <button
      type="button"
      class="list-chip"
      aria-haspopup="true"
      aria-expanded={open}
      title={listInfo.label}
    >
      {listInfo.label}
    </button>
    {#if open}
      <div class="list-popover" role="menu">
        {#if archiveAction}
          <button type="button" role="menuitem" onclick={viewArchive}>
            {t('mailingList.action.viewArchive')}
          </button>
        {/if}
        {#if helpAction}
          <button type="button" role="menuitem" onclick={getHelp}>
            {t('mailingList.action.getHelp')}
          </button>
        {/if}
        {#if postAddress}
          <button type="button" role="menuitem" onclick={() => void replyToList()}>
            {t('mailingList.action.replyToList')}
          </button>
        {/if}
        {#if !archiveAction && !helpAction && !postAddress}
          <span class="no-actions">{t('mailingList.noActions')}</span>
        {/if}
      </div>
    {/if}
  </span>
{/if}

<style>
  .list-chip-anchor {
    position: relative;
    display: inline-flex;
    align-items: center;
  }

  /* REQ-LIST-10: --support-info background, small chip beside the sender. */
  .list-chip {
    display: inline-flex;
    align-items: center;
    max-width: 160px;
    padding: 1px var(--spacing-03);
    background: var(--support-info);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .list-popover {
    position: absolute;
    top: calc(100% + var(--spacing-02));
    left: 0;
    z-index: 200;
    display: flex;
    flex-direction: column;
    min-width: 180px;
    padding: var(--spacing-02);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25);
  }
  .list-popover button {
    text-align: left;
    padding: var(--spacing-02) var(--spacing-03);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }
  .list-popover button:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .no-actions {
    padding: var(--spacing-02) var(--spacing-03);
    color: var(--text-placeholder);
    font-size: var(--type-body-compact-01-size);
    font-style: italic;
  }

  @media print {
    .list-chip-anchor {
      display: none;
    }
  }
</style>
