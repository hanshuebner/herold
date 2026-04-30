<script lang="ts">
  /**
   * Conversation list sidebar for the chat panel.
   *
   * Shows DMs and Spaces sorted by last activity (pinned first per REQ-CHAT-06).
   * Unread count badge per REQ-CHAT-90. Presence dot per REQ-CHAT-62.
   */

  import { chat } from './store.svelte';
  import { auth } from '../auth/auth.svelte';
  import Avatar from '../avatar/Avatar.svelte';
  import type { Conversation } from './types';

  interface Props {
    onSelect: (id: string) => void;
    activeId?: string;
  }
  let { onSelect, activeId }: Props = $props();

  let dms = $derived(
    chat.conversationIds
      .map((id) => chat.conversations.get(id)!)
      .filter((c): c is Conversation => !!c && c.kind === 'dm'),
  );
  let spaces = $derived(
    chat.conversationIds
      .map((id) => chat.conversations.get(id)!)
      .filter((c): c is Conversation => !!c && c.kind === 'space'),
  );

  function presenceClass(principalId: string): string {
    const p = chat.presence.get(principalId);
    if (p === 'online') return 'online';
    if (p === 'away') return 'away';
    return 'offline';
  }

  /**
   * For a DM, find the other participant's principalId (not ourselves).
   * Falls back to null if membership data isn't loaded yet.
   */
  function dmPrincipalId(conv: Conversation): string | null {
    const mems = conv.members;
    const other = mems.find((m) => m.principalId !== auth.principalId);
    return other?.principalId ?? null;
  }

  /** Email of the OTHER DM member, used as the avatar resolver key. */
  function otherEmail(conv: Conversation): string {
    if (conv.kind !== 'dm') return '';
    const other = conv.members.find((m) => m.principalId !== auth.principalId);
    return other?.email ?? '';
  }
</script>

<nav class="conv-list" aria-label="Conversations">
  {#if chat.conversationsStatus === 'loading'}
    <p class="loading">Loading conversations…</p>
  {:else if chat.conversationsStatus === 'error'}
    <p class="error">Failed to load conversations</p>
  {:else}
    {#if dms.length > 0}
      <h3 class="group-label">Direct messages</h3>
      <ul class="list">
        {#each dms as conv (conv.id)}
          {@const otherId = dmPrincipalId(conv)}
          <li>
            <button
              type="button"
              class="conv-row"
              class:active={conv.id === activeId}
              onclick={() => onSelect(conv.id)}
              aria-current={conv.id === activeId ? 'true' : undefined}
            >
              <span class="avatar-wrap" aria-hidden="true">
                <Avatar
                  email={otherEmail(conv)}
                  fallbackInitial={conv.name.charAt(0).toUpperCase()}
                  size={32}
                />
                {#if otherId}
                  <span
                    class="presence-dot {presenceClass(otherId)}"
                    aria-label="Presence: {presenceClass(otherId)}"
                  ></span>
                {/if}
              </span>
              <span class="conv-info">
                <span class="conv-name">{conv.name}</span>
                {#if conv.lastMessagePreview}
                  <span class="preview">{conv.lastMessagePreview}</span>
                {/if}
              </span>
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

    {#if spaces.length > 0}
      <h3 class="group-label">Spaces</h3>
      <ul class="list">
        {#each spaces as conv (conv.id)}
          <li>
            <button
              type="button"
              class="conv-row"
              class:active={conv.id === activeId}
              onclick={() => onSelect(conv.id)}
              aria-current={conv.id === activeId ? 'true' : undefined}
            >
              <span class="avatar-wrap" aria-hidden="true">
                <span class="avatar space">#</span>
              </span>
              <span class="conv-info">
                <span class="conv-name">{conv.name}</span>
                {#if conv.lastMessagePreview}
                  <span class="preview">{conv.lastMessagePreview}</span>
                {/if}
              </span>
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

    {#if dms.length === 0 && spaces.length === 0 && chat.conversationsStatus === 'ready'}
      <p class="empty">No conversations yet</p>
    {/if}
  {/if}
</nav>

<style>
  .conv-list {
    display: flex;
    flex-direction: column;
    padding: var(--spacing-03) 0;
    overflow-y: auto;
    height: 100%;
  }

  .group-label {
    font-size: var(--type-helper-text-01-size);
    line-height: var(--type-helper-text-01-line);
    font-weight: 600;
    color: var(--text-helper);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: var(--spacing-04) var(--spacing-04) var(--spacing-02);
    margin: 0;
  }

  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .conv-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    width: 100%;
    padding: var(--spacing-03) var(--spacing-04);
    border-radius: 0;
    text-align: left;
    color: var(--text-secondary);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .conv-row:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .conv-row.active {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 600;
  }

  .avatar-wrap {
    position: relative;
    flex-shrink: 0;
  }

  .avatar {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
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
    bottom: -2px;
    right: -2px;
    width: 14px;
    height: 14px;
    border-radius: var(--radius-pill);
    border: 2px solid var(--background);
  }

  .presence-dot.online {
    background: var(--presence-online);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--presence-online) 70%, transparent);
  }

  .presence-dot.away {
    background: var(--presence-away);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--presence-away) 70%, transparent);
  }

  .presence-dot.offline {
    background: var(--presence-offline);
  }

  .conv-info {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .conv-name {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--type-body-compact-01-size);
  }

  .preview {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    font-weight: 400;
  }

  .badge {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 18px;
    height: 18px;
    padding: 0 var(--spacing-02);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .loading,
  .error,
  .empty {
    padding: var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    font-style: italic;
    margin: 0;
  }

  .error {
    color: var(--support-error);
  }
</style>
