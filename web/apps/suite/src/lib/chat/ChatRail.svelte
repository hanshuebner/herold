<script lang="ts">
  /**
   * Right-edge chat rail — persistent strip showing recent conversations.
   *
   * Visible on all routes except the fullscreen /chat/* route (the caller,
   * App.svelte, is responsible for that gate).  Hidden on phone breakpoints
   * (<768px) via CSS media query.
   *
   * Behaviour:
   *   - Collapsed (default width ~64px): shows avatar + unread badge for
   *     each conversation.  Expand toggle button at top.
   *   - Expanded (~280px): shows avatar, name, presence dot (DMs), unread
   *     badge.  Same toggle closes it.
   *   - Click any item -> opens the conversation in a floating overlay
   *     (does NOT navigate to /chat/*).
   *   - Sorted by latest activity (pinned first, then lastMessageAt desc),
   *     matching the existing chat store order.
   *
   * REQ-CHAT-* (conversation list), REQ-UI-MOB (hidden on phone),
   * REQ-CHAT-62 (presence dot).
   */

  import { chat } from './store.svelte';
  import { chatOverlay } from './overlay-store.svelte';
  import { auth } from '../auth/auth.svelte';
  import type { Conversation } from './types';

  interface Props {
    /** Extra class for testing or positioning overrides. */
    class?: string;
  }
  let { class: extraClass = '' }: Props = $props();

  let expanded = $state(false);

  // Publish the current rail width as a CSS custom property on :root so
  // ChatOverlayHost (position:fixed) can read it without sharing a DOM
  // subtree.  The property transitions in sync with the rail's own width
  // transition; ChatOverlayHost reads it via calc(var(--chat-rail-width,64px)+16px).
  $effect(() => {
    const width = expanded ? '280px' : '64px';
    document.documentElement.style.setProperty('--chat-rail-width', width);
    return () => {
      document.documentElement.style.removeProperty('--chat-rail-width');
    };
  });

  // All conversations sorted by the store's established ordering.
  let conversations = $derived(
    chat.conversationIds
      .map((id) => chat.conversations.get(id)!)
      .filter((c): c is Conversation => !!c),
  );

  function presenceClass(conv: Conversation): string {
    if (conv.type !== 'dm') return '';
    const other = conv.members.find((m) => m.principalId !== auth.principalId);
    if (!other) return 'offline';
    const p = chat.presence.get(other.principalId);
    if (p === 'online') return 'online';
    if (p === 'away') return 'away';
    return 'offline';
  }

  function dmOtherId(conv: Conversation): string | null {
    if (conv.type !== 'dm') return null;
    return conv.members.find((m) => m.principalId !== auth.principalId)?.principalId ?? null;
  }

  function handleSelect(conv: Conversation): void {
    chatOverlay.openWindow(conv.id);
  }

  function handleKeydown(ev: KeyboardEvent, conv: Conversation): void {
    if (ev.key === 'Enter' || ev.key === ' ') {
      ev.preventDefault();
      handleSelect(conv);
    }
  }
</script>

<aside
  class="chat-rail {extraClass}"
  class:expanded
  aria-label="Chat"
>
  <button
    type="button"
    class="toggle-btn"
    aria-label={expanded ? 'Collapse chat rail' : 'Expand chat rail'}
    aria-expanded={expanded}
    onclick={() => (expanded = !expanded)}
  >
    <span class="toggle-icon" aria-hidden="true">{expanded ? '»' : '«'}</span>
  </button>

  {#if chat.conversationsStatus === 'loading'}
    <div class="rail-loading" aria-label="Loading conversations">
      <span class="loading-dot"></span>
      <span class="loading-dot"></span>
      <span class="loading-dot"></span>
    </div>
  {:else}
    <ul class="conv-list" role="list" aria-label="Recent conversations">
      {#each conversations as conv (conv.id)}
        {@const otherId = dmOtherId(conv)}
        {@const pc = presenceClass(conv)}
        <li>
          <button
            type="button"
            class="conv-item"
            class:active={chatOverlay.isOpen(conv.id)}
            aria-label="{conv.name}{conv.unreadCount > 0 && !conv.muted ? `, ${conv.unreadCount} unread` : ''}{conv.type === 'dm' && otherId ? `, ${pc}` : ''}"
            title={conv.name}
            onclick={() => handleSelect(conv)}
            onkeydown={(ev) => handleKeydown(ev, conv)}
          >
            <span class="avatar-wrap" aria-hidden="true">
              <span class="avatar" class:space={conv.type === 'space'}>
                {conv.type === 'space' ? '#' : conv.name.charAt(0).toUpperCase()}
              </span>
              {#if conv.type === 'dm' && otherId}
                <span class="presence-dot {pc}"></span>
              {/if}
            </span>

            {#if expanded}
              <span class="conv-label">
                <span class="conv-name">{conv.name}</span>
                {#if conv.lastMessagePreview}
                  <span class="preview">{conv.lastMessagePreview}</span>
                {/if}
              </span>
            {/if}

            {#if conv.unreadCount > 0 && !conv.muted}
              <span class="badge" aria-hidden="true">
                {conv.unreadCount > 99 ? '99+' : conv.unreadCount}
              </span>
            {/if}
          </button>
        </li>
      {/each}

      {#if conversations.length === 0 && chat.conversationsStatus === 'ready'}
        <li class="empty">
          {#if expanded}
            <span>No conversations yet</span>
          {:else}
            <span aria-hidden="true">–</span>
          {/if}
        </li>
      {/if}
    </ul>
  {/if}
</aside>

<style>
  .chat-rail {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    width: 64px;
    flex-shrink: 0;
    border-left: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
    overflow: hidden;
    transition: width var(--duration-moderate-01) var(--easing-productive-enter);
    /* Ensure overlays (z-index 400..500 range) render on top. */
    position: relative;
    z-index: 1;
  }

  .chat-rail.expanded {
    width: 280px;
  }

  /* Hidden on phone breakpoints per REQ-MOB / docs/design/web/requirements/24. */
  @media (max-width: 767px) {
    .chat-rail {
      display: none;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .chat-rail {
      transition: none;
    }
  }

  .toggle-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: var(--spacing-08);
    padding: 0;
    flex-shrink: 0;
    color: var(--text-helper);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .toggle-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .toggle-icon {
    font-size: var(--type-body-compact-01-size);
    font-family: var(--font-mono);
    line-height: 1;
  }

  .rail-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-01);
    padding: var(--spacing-04);
  }

  .loading-dot {
    width: 4px;
    height: 4px;
    border-radius: var(--radius-pill);
    background: var(--text-helper);
    opacity: 0.6;
  }

  .conv-list {
    list-style: none;
    margin: 0;
    padding: var(--spacing-02) 0;
    overflow-y: auto;
    flex: 1;
  }

  .conv-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    width: 100%;
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    color: var(--text-secondary);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    position: relative;
    text-align: left;
  }

  .conv-item:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .conv-item.active {
    background: color-mix(in srgb, var(--interactive) 12%, transparent);
    color: var(--text-primary);
  }

  .avatar-wrap {
    position: relative;
    flex-shrink: 0;
  }

  .avatar {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    border-radius: var(--radius-pill);
    background: var(--interactive);
    color: var(--text-on-color);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
  }

  .avatar.space {
    background: var(--layer-03);
    color: var(--text-secondary);
  }

  .presence-dot {
    position: absolute;
    bottom: 0;
    right: 0;
    width: 10px;
    height: 10px;
    border-radius: var(--radius-pill);
    border: 2px solid var(--layer-01);
  }

  .presence-dot.online {
    background: var(--support-success);
  }

  .presence-dot.away {
    background: var(--support-warning);
  }

  .presence-dot.offline {
    background: var(--border-subtle-01);
  }

  .conv-label {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
    overflow: hidden;
  }

  .conv-name {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--type-body-compact-01-size);
    line-height: 1.3;
  }

  .preview {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
  }

  .badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 18px;
    height: 18px;
    padding: 0 var(--spacing-01);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    flex-shrink: 0;
    /* When collapsed the badge sits over the avatar's bottom-right. */
  }

  .chat-rail:not(.expanded) .badge {
    position: absolute;
    top: var(--spacing-01);
    right: var(--spacing-01);
    min-width: 14px;
    height: 14px;
    font-size: 9px;
  }

  .empty {
    padding: var(--spacing-03);
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    font-style: italic;
    text-align: center;
  }
</style>
