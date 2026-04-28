<script lang="ts">
  /**
   * Floating chat overlay window — Gmail-style.
   *
   * Shows one open conversation anchored to the bottom edge of the
   * viewport.  Multiple windows stack horizontally from the right via
   * the parent ChatOverlayHost.
   *
   * Behaviour:
   *   - Expanded: title bar + message list + compose.
   *   - Minimized: title bar only (~32px height).
   *   - Pressing Escape from within a focused overlay closes it.
   *   - Uses the overlay message cache (chat.overlayMessages) so the
   *     main chat pane's message list is not disturbed.
   *
   * REQ-CHAT-20..27 (compose), REQ-CHAT-40 (read receipts),
   * REQ-CHAT-52 (typing auto-expire), REQ-CHAT-62 (presence dot).
   */

  import { untrack } from 'svelte';
  import { chat } from './store.svelte';
  import { chatOverlay } from './overlay-store.svelte';
  import { auth } from '../auth/auth.svelte';
  import MessageList from './MessageList.svelte';
  import ChatCompose from './ChatCompose.svelte';
  import type { Conversation } from './types';

  interface Props {
    windowKey: string;
    conversationId: string;
    minimized: boolean;
  }
  let { windowKey, conversationId, minimized }: Props = $props();

  let conversation = $derived<Conversation | null>(
    chat.conversations.get(conversationId) ?? null,
  );

  // Overlay-local message cache entry.
  let overlayEntry = $derived(chat.overlayMessages.get(conversationId));
  let overlayMessages = $derived(overlayEntry?.messages ?? []);
  let overlayStatus = $derived(overlayEntry?.status ?? 'idle');
  let overlayHasMore = $derived(overlayEntry?.hasMore ?? false);

  // Load messages when this window first opens (or when conversationId changes).
  $effect(() => {
    const cid = conversationId;
    untrack(() => {
      if (!overlayEntry || overlayEntry.status === 'idle') {
        void chat.loadOverlayMessages(cid);
      }
    });
  });

  function handleClose(): void {
    chat.closeOverlayMessages(conversationId);
    chatOverlay.closeWindow(windowKey);
  }

  function handleMinimize(): void {
    chatOverlay.minimizeWindow(windowKey);
  }

  function handleTitleClick(): void {
    if (minimized) {
      chatOverlay.expandWindow(windowKey);
    }
  }

  function handleKeydown(ev: KeyboardEvent): void {
    if (ev.key === 'Escape') {
      ev.stopPropagation();
      handleClose();
    }
  }

  // Presence dot for DM
  function presenceClass(): string {
    if (!conversation || conversation.type !== 'dm') return '';
    const other = conversation.members.find(
      (m) => m.principalId !== auth.principalId,
    );
    if (!other) return 'offline';
    const p = chat.presence.get(other.principalId);
    if (p === 'online') return 'online';
    if (p === 'away') return 'away';
    return 'offline';
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section
  class="overlay-window"
  class:minimized
  aria-label="Chat: {conversation?.name ?? conversationId}"
  onkeydown={handleKeydown}
>
  <div
    class="title-bar"
    role="button"
    tabindex={minimized ? 0 : -1}
    aria-label={minimized ? `Expand chat with ${conversation?.name ?? conversationId}` : undefined}
    aria-expanded={!minimized}
    onclick={handleTitleClick}
    onkeydown={(ev) => {
      if (minimized && (ev.key === 'Enter' || ev.key === ' ')) {
        ev.preventDefault();
        handleTitleClick();
      }
    }}
  >
    <span class="title-content">
      {#if conversation?.type === 'dm'}
        <span class="presence-dot {presenceClass()}" aria-hidden="true"></span>
      {:else}
        <span class="space-icon" aria-hidden="true">#</span>
      {/if}
      <span class="title-name">{conversation?.name ?? conversationId}</span>
      {#if (conversation?.unreadCount ?? 0) > 0 && !(conversation?.muted)}
        <span class="unread-badge" aria-label="{conversation!.unreadCount} unread">
          {conversation!.unreadCount > 99 ? '99+' : conversation!.unreadCount}
        </span>
      {/if}
    </span>

    <span class="title-actions">
      <button
        type="button"
        class="icon-btn"
        aria-label={minimized ? 'Expand' : 'Minimize'}
        title={minimized ? 'Expand' : 'Minimize'}
        tabindex="0"
        onclick={(ev) => {
          ev.stopPropagation();
          chatOverlay.toggleMinimize(windowKey);
        }}
      >
        {minimized ? '+' : '&#x2013;'}
      </button>
      <button
        type="button"
        class="icon-btn"
        aria-label="Close"
        title="Close"
        tabindex="0"
        onclick={(ev) => {
          ev.stopPropagation();
          handleClose();
        }}
      >
        &#x00D7;
      </button>
    </span>
  </div>

  {#if !minimized && conversation}
    <div class="window-body" aria-live="polite" aria-atomic="false">
      <MessageList
        {conversationId}
        {conversation}
        externalMessages={overlayMessages}
        externalStatus={overlayStatus}
        externalHasMore={overlayHasMore}
        onLoadMore={(cid) => void chat.loadMoreOverlayMessages(cid)}
      />
    </div>

    <ChatCompose {conversationId} autofocus={false} />
  {/if}
</section>

<style>
  .overlay-window {
    display: flex;
    flex-direction: column;
    width: 320px;
    max-height: 480px;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-bottom: none;
    border-radius: var(--radius-lg) var(--radius-lg) 0 0;
    box-shadow: 0 4px 24px rgba(0, 0, 0, 0.2);
    overflow: hidden;
    transition: max-height var(--duration-moderate-01) var(--easing-productive-enter);
  }

  .overlay-window.minimized {
    max-height: 40px;
  }

  @media (prefers-reduced-motion: reduce) {
    .overlay-window {
      transition: none;
    }
  }

  .title-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 var(--spacing-03) 0 var(--spacing-04);
    height: 40px;
    flex-shrink: 0;
    background: var(--interactive);
    color: var(--text-on-color);
    cursor: default;
    user-select: none;
    border-radius: var(--radius-lg) var(--radius-lg) 0 0;
  }

  .overlay-window.minimized .title-bar {
    cursor: pointer;
    border-radius: var(--radius-lg) var(--radius-lg) 0 0;
  }

  .overlay-window.minimized .title-bar:focus-visible {
    outline: 2px solid var(--focus-inverse);
    outline-offset: -2px;
  }

  .title-content {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    min-width: 0;
    flex: 1;
    overflow: hidden;
  }

  .title-name {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
  }

  .presence-dot {
    width: 8px;
    height: 8px;
    border-radius: var(--radius-pill);
    flex-shrink: 0;
  }

  .presence-dot.online {
    background: var(--support-success);
  }

  .presence-dot.away {
    background: var(--support-warning);
  }

  .presence-dot.offline {
    background: rgba(255, 255, 255, 0.4);
  }

  .space-icon {
    font-size: var(--type-body-compact-01-size);
    opacity: 0.7;
  }

  .unread-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 16px;
    height: 16px;
    padding: 0 var(--spacing-01);
    background: rgba(255, 255, 255, 0.3);
    border-radius: var(--radius-pill);
    font-size: 10px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    flex-shrink: 0;
  }

  .title-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-01);
    flex-shrink: 0;
  }

  .icon-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: var(--radius-md);
    color: var(--text-on-color);
    font-size: 16px;
    line-height: 1;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .icon-btn:hover {
    background: rgba(0, 0, 0, 0.15);
  }

  .icon-btn:focus-visible {
    outline: 2px solid var(--focus-inverse);
    outline-offset: 1px;
  }

  .window-body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
</style>
