<script lang="ts">
  /**
   * Sidebar chats section — renders up to 8 recent conversations with
   * avatar, presence dot (DMs), name, and unread badge.  Clicking a row
   * opens the conversation as a floating overlay.
   *
   * Gated on the chat capability at the call site; this component always
   * renders its content when mounted.
   *
   * REQ-CHAT-* (conversation list), REQ-CHAT-62 (presence dot).
   */

  import { chat } from './store.svelte';
  import { chatOverlay } from './overlay-store.svelte';
  import { newChatPicker } from './new-chat-picker.svelte';
  import { auth } from '../auth/auth.svelte';
  import { t } from '../i18n/i18n.svelte';
  import type { Conversation } from './types';

  const MAX = 8;

  let conversations = $derived(
    chat.conversationIds
      .slice(0, MAX)
      .map((id) => chat.conversations.get(id)!)
      .filter((c): c is Conversation => !!c),
  );

  function presenceClass(conv: Conversation): string {
    if (conv.kind !== 'dm') return '';
    const other = conv.members.find((m) => m.principalId !== auth.principalId);
    if (!other) return 'offline';
    const p = chat.presence.get(other.principalId);
    if (p === 'online') return 'online';
    if (p === 'away') return 'away';
    return 'offline';
  }

  function handleNewChat(): void {
    newChatPicker.open({ mode: 'dm' });
  }
</script>

<div class="chats-section">
  <div class="chats-header">
    <h3>{t('sidebar.chats')}</h3>
    <button
      type="button"
      class="new-chat-btn"
      aria-label={t('sidebar.newChat')}
      title={t('sidebar.newChat')}
      onclick={handleNewChat}
    >+</button>
  </div>

  {#if chat.conversationsStatus === 'loading'}
    <div class="chats-loading" aria-label="Loading conversations">
      <span class="loading-dot"></span>
      <span class="loading-dot"></span>
      <span class="loading-dot"></span>
    </div>
  {:else if chat.conversationsStatus === 'ready' && conversations.length === 0}
    <p class="chats-empty">No conversations yet</p>
  {:else}
    <ul class="conv-list">
      {#each conversations as conv (conv.id)}
        {@const pc = presenceClass(conv)}
        <li>
          <button
            type="button"
            class="conv-item"
            class:active={chatOverlay.isOpen(conv.id)}
            aria-label="{conv.name}{conv.unreadCount > 0 && !conv.muted ? `, ${conv.unreadCount} unread` : ''}"
            title={conv.name}
            onclick={() => chatOverlay.openWindow(conv.id)}
          >
            <span class="avatar-wrap" aria-hidden="true">
              <span class="avatar" class:space={conv.kind === 'space'}>
                {conv.kind === 'space' ? '#' : conv.name.charAt(0).toUpperCase()}
              </span>
              {#if conv.kind === 'dm'}
                <span class="presence-dot {pc}"></span>
              {/if}
            </span>
            <span class="conv-name">{conv.name}</span>
            {#if conv.unreadCount > 0 && !conv.muted}
              <span class="badge" aria-label="{conv.unreadCount} unread">
                {conv.unreadCount > 99 ? '99+' : conv.unreadCount}
              </span>
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .chats-section {
    margin-top: auto;
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-03);
    flex-shrink: 0;
  }
  .chats-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 var(--spacing-04);
    margin-bottom: var(--spacing-02);
  }
  h3 {
    font-size: var(--type-heading-compact-01-size);
    line-height: var(--type-heading-compact-01-line);
    font-weight: var(--type-heading-compact-01-weight);
    color: var(--text-helper);
    margin: 0;
  }
  .new-chat-btn {
    width: 24px;
    height: 24px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-md);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .new-chat-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .chats-loading {
    display: flex;
    align-items: center;
    gap: var(--spacing-01);
    padding: var(--spacing-03) var(--spacing-04);
  }
  .loading-dot {
    width: 4px;
    height: 4px;
    border-radius: var(--radius-pill);
    background: var(--text-helper);
    opacity: 0.6;
  }
  .chats-empty {
    padding: var(--spacing-02) var(--spacing-04);
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    font-style: italic;
    margin: 0;
  }
  .conv-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .conv-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    width: 100%;
    padding: var(--spacing-02) var(--spacing-04);
    min-height: var(--touch-min);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
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
    width: 28px;
    height: 28px;
    border-radius: var(--radius-pill);
    background: var(--interactive);
    color: var(--text-on-color);
    font-size: var(--type-helper-text-01-size);
    font-weight: 600;
    flex-shrink: 0;
  }
  .avatar.space {
    background: var(--layer-03);
    color: var(--text-secondary);
  }
  .presence-dot {
    position: absolute;
    bottom: 0;
    right: 0;
    width: 8px;
    height: 8px;
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
  .conv-name {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--type-body-compact-01-size);
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
  }
</style>
