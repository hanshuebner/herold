<script lang="ts">
  /**
   * Mailing-list chip (REQ-LIST-02, 10..12, 20..22).
   *
   * Rendered per-message (not per-thread) beside the sender: a message
   * that has no `List-ID` header renders no chip at all, so a thread
   * mixing list and non-list messages (forwarded-out-of-the-list case,
   * REQ-LIST-12) shows the chip only on the messages that actually
   * carry the header.
   *
   * The chip label is the `List-ID` description when the header
   * carries one; otherwise it derives a readable name from the
   * sender's display name or email domain, falling back to a generic
   * "Newsletter" / "Mailingliste" label -- the raw campaign/list token
   * is never shown as the label (issue #415), only in the popover and
   * the raw-headers ("Show original") view.
   *
   * Hovering, focusing, clicking, and keyboard-activating (Enter/Space)
   * the chip button all reveal a popover with the available `List-*`
   * actions; an action is hidden entirely when its backing header is
   * absent (REQ-LIST-20/21/22).
   */
  import { compose } from '../compose/compose.svelte';
  import { toast } from '../toast/toast.svelte';
  import { t } from '../i18n/i18n.svelte';
  import type { Email } from './types';
  import {
    parseListId,
    deriveListLabelFromSender,
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

  let chipLabel = $derived.by<string>(() => {
    if (!listInfo) return '';
    if (listInfo.description) return listInfo.description;
    return deriveListLabelFromSender(email.from?.[0]) ?? t('mailingList.genericLabel');
  });

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

  /**
   * The popover renders through a `document.body` portal (issue #457)
   * rather than as a normal descendant of the chip anchor: the anchor
   * sits inside MessageAccordion's `.from` sender row, which is one
   * line tall with `overflow: hidden` so the chip's own label can
   * ellipsis (issue #415). A popover opening below that one-line row
   * is clipped away by the same rule that makes the label elide, so
   * removing the clip is not an option -- the popover instead escapes
   * the clipping ancestor entirely and is positioned `fixed` from the
   * button's measured rect.
   *
   * A `position: fixed` portal has no layout relationship to the
   * button the way the previous `position: absolute` child did, so its
   * position has to be re-measured on every scroll and resize tick
   * while it is open, and once more after it renders (a no-actions
   * popover is narrower than one with actions, which can shift the
   * right-edge clamp).
   */
  const POPOVER_GAP = 4; // --spacing-02
  const VIEWPORT_MARGIN = 4;

  let buttonEl = $state<HTMLButtonElement | null>(null);
  let popoverEl = $state<HTMLDivElement | null>(null);
  let position = $state<{ top: number; left: number }>({ top: 0, left: 0 });

  function layoutPopover(): void {
    if (!buttonEl) return;
    const rect = buttonEl.getBoundingClientRect();
    const width = popoverEl?.offsetWidth ?? 180;
    const left = Math.max(
      VIEWPORT_MARGIN,
      Math.min(window.innerWidth - width - VIEWPORT_MARGIN, rect.left),
    );
    position = { top: rect.bottom + POPOVER_GAP, left };
  }

  $effect(() => {
    if (!open) return;
    layoutPopover();
    const onViewportChange = (): void => layoutPopover();
    const ro = new ResizeObserver(onViewportChange);
    if (popoverEl) ro.observe(popoverEl);
    window.addEventListener('scroll', onViewportChange, true);
    window.addEventListener('resize', onViewportChange);
    return () => {
      ro.disconnect();
      window.removeEventListener('scroll', onViewportChange, true);
      window.removeEventListener('resize', onViewportChange);
    };
  });

  /** Moves `node` to `document.body` on mount and detaches it on destroy. */
  function portal(node: HTMLElement): { destroy(): void } {
    document.body.appendChild(node);
    return {
      destroy(): void {
        node.remove();
      },
    };
  }

  /**
   * Click and keyboard (Enter/Space) activation both open the popover
   * explicitly (issue #415) rather than toggling it: a real mouse click
   * is preceded by `mouseenter`, which has already opened it via
   * `show()`, so a toggle would immediately close what hover just
   * opened. Closing stays the job of `scheduleHide` (hover/focus out)
   * and the action buttons below.
   */
  function openOnActivation(): void {
    show();
  }

  /**
   * A native `<button>` fires `click` on Enter/Space in a real browser,
   * so this only needs to stop the default (avoids a page scroll on
   * Space) -- also makes the behaviour deterministic under jsdom, which
   * does not implement that default action for a plain `<button>`.
   */
  function handleButtonKeydown(e: KeyboardEvent): void {
    if (e.key === 'Enter' || e.key === ' ' || e.key === 'Spacebar') {
      e.preventDefault();
      openOnActivation();
    }
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
      bind:this={buttonEl}
      type="button"
      class="list-chip"
      aria-haspopup="true"
      aria-expanded={open}
      title={t('mailingList.chipTooltip')}
      onclick={openOnActivation}
      onkeydown={handleButtonKeydown}
    >
      <svg
        class="list-chip-icon"
        aria-hidden="true"
        viewBox="0 0 24 24"
        width="12"
        height="12"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <rect x="3" y="5" width="18" height="14" rx="2" />
        <path d="M3 7l9 7 9-7" />
      </svg>
      <span class="list-chip-label">{chipLabel}</span>
      <svg
        class="list-chip-caret"
        aria-hidden="true"
        viewBox="0 0 24 24"
        width="10"
        height="10"
        fill="none"
        stroke="currentColor"
        stroke-width="2.4"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <path d="M6 9l6 6 6-6" />
      </svg>
    </button>
    {#if open}
      <div
        class="list-popover"
        role="menu"
        use:portal
        bind:this={popoverEl}
        style:top="{position.top}px"
        style:left="{position.left}px"
        onmouseenter={show}
        onmouseleave={scheduleHide}
        onfocusin={show}
        onfocusout={scheduleHide}
      >
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
        <span class="list-popover-id" data-testid="list-chip-raw-id">
          {t('mailingList.rawId', { id: listInfo.id })}
        </span>
      </div>
    {/if}
  </span>
{/if}

<style>
  .list-chip-anchor {
    position: relative;
    display: inline-flex;
    align-items: center;
    /*
     * The anchor sits inside MessageAccordion's `.from` flex row
     * alongside the sender name/email, which is itself `overflow:
     * hidden`. Without `min-width: 0` a flex item's automatic minimum
     * width is its max-content size, so the browser never shrinks the
     * chip -- it overflows `.from` and gets hard-clipped instead of
     * eliding via the chip's own ellipsis (issue #415: a long derived
     * sender-domain label exposed this).
     */
    min-width: 0;
  }

  /*
   * REQ-LIST-10: --support-info background, small chip beside the
   * sender. The list icon plus trailing caret are the visible
   * affordance that the chip opens a popover of list actions
   * (issue #415) -- not just the coloured background.
   */
  .list-chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    min-width: 0;
    max-width: 200px;
    padding: 1px var(--spacing-03);
    background: var(--support-info);
    color: var(--text-on-color);
    border: none;
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    white-space: nowrap;
    cursor: pointer;
  }
  .list-chip-icon,
  .list-chip-caret {
    flex: 0 0 auto;
  }
  .list-chip-caret {
    opacity: 0.8;
  }
  .list-chip-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /*
   * `position: fixed` and no `top`/`left` here -- both are set inline
   * from the button's measured rect (issue #457), since this element
   * is portalled to `document.body` and has no layout relationship to
   * the anchor a `position: absolute` child would have had.
   */
  .list-popover {
    position: fixed;
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
  /* REQ-LIST-02: the raw List-ID identifier stays available here even
     when the chip label is a derived/generic name. */
  .list-popover-id {
    margin-top: var(--spacing-01);
    padding: var(--spacing-02) var(--spacing-03);
    border-top: 1px solid var(--border-subtle-01);
    color: var(--text-placeholder);
    font-size: var(--type-caption-01-size, var(--type-body-compact-01-size));
    word-break: break-all;
  }

  @media print {
    .list-chip-anchor {
      display: none;
    }
  }
</style>
