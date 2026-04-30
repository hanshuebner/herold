<script lang="ts">
  /**
   * Message list pane for an open conversation.
   *
   * Renders messages oldest-first. Inline images scale to the bubble width
   * (max-height 320px) per REQ-CHAT-22. Reactions strip below each message per REQ-CHAT-30..33.
   * Read receipts shown for DMs per REQ-CHAT-40.
   *
   * Read tracking is focus-driven: when the recipient's cursor is in the
   * chat compose for THIS conversation, the read pointer advances to the
   * latest message — both on focus itself, and whenever a new message
   * arrives while focus is held. Bare scrolling, hover, or focus on a
   * different conversation's compose do NOT advance the pointer.
   */

  import { untrack } from 'svelte';
  import { chat } from './store.svelte';
  import { auth } from '../auth/auth.svelte';
  import { chatTimestampGroupingSeconds } from '../auth/capabilities';
  import EmojiPicker from '../mail/EmojiPicker.svelte';
  import ImageLightbox from './ImageLightbox.svelte';
  import type { Message, Conversation, LinkPreview } from './types';

  interface Props {
    conversationId: string;
    conversation: Conversation;
    /**
     * External message list.  When supplied the component reads from this
     * array instead of chat.messages.  Used by overlay windows so each
     * window has its own message cache independent of the main pane.
     */
    externalMessages?: Message[];
    externalStatus?: 'idle' | 'loading' | 'ready' | 'error';
    externalHasMore?: boolean;
    onLoadMore?: (conversationId: string) => void;
  }
  let {
    conversationId,
    conversation,
    externalMessages,
    externalStatus,
    externalHasMore,
    onLoadMore,
  }: Props = $props();

  // Read from external sources when provided, fall back to the store.
  let effectiveMessages = $derived(externalMessages ?? chat.messages);
  let effectiveStatus = $derived(externalStatus ?? chat.messagesStatus);
  let effectiveHasMore = $derived(externalHasMore ?? chat.hasMoreMessages);

  let scrollEl = $state<HTMLDivElement | null>(null);
  let showPickerFor = $state<string | null>(null);
  let lightboxSrc = $state<string | null>(null);

  /**
   * The message id after which the "New" divider is shown. Set once on
   * conversation open from myMembership.lastReadMessageId; cleared when
   * markRead advances the read pointer past the first unread message so
   * the divider disappears naturally as the user reads.
   *
   * Anchored at open — new messages arriving while the user is reading
   * do not move the divider.
   */
  let newDividerAfterMessageId = $state<string | null>(null);

  /**
   * Delegated click handler for inline images rendered via {@html}.
   * Since the image nodes are inserted by the browser parser we cannot
   * attach svelte onclick directives; instead we listen on the .body
   * wrapper and check whether the event landed on an <img>.
   */
  function handleBodyClick(ev: MouseEvent): void {
    if (ev.target instanceof HTMLImageElement) {
      lightboxSrc = ev.target.src;
    }
  }

  // Auto-scroll to bottom when new messages arrive (while at bottom).
  let wasAtBottom = true;

  /** Imperatively scroll the list to the bottom. */
  function scrollToBottom(): void {
    if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
  }

  // Scroll when effective messages change (conditional on wasAtBottom).
  $effect(() => {
    const _messages = effectiveMessages;
    untrack(() => {
      if (!scrollEl) return;
      if (wasAtBottom) {
        requestAnimationFrame(scrollToBottom);
      }
    });
  });

  // Force-scroll unconditionally when the user sends a message (Bug B).
  $effect(() => {
    const _signal = chat.scrollToBottomSignal;
    untrack(() => {
      if (!scrollEl) return;
      wasAtBottom = true;
      requestAnimationFrame(scrollToBottom);
    });
  });

  /**
   * Event-delegated handler for <img> load/error events inside the
   * scroll container. Re-scrolls to the bottom if the user has not
   * scrolled away, so a conversation that ends with an image lands at
   * the correct scroll position once the image dimensions are known
   * (Bug A).
   */
  function handleContainerImgLoad(): void {
    if (wasAtBottom) {
      requestAnimationFrame(scrollToBottom);
    }
  }

  function handleScroll(): void {
    if (!scrollEl) return;
    const { scrollTop, scrollHeight, clientHeight } = scrollEl;
    wasAtBottom = scrollHeight - scrollTop - clientHeight < 40;

    // Paginate: load more when scrolled near top.
    if (scrollTop < 80 && effectiveHasMore) {
      if (onLoadMore) {
        onLoadMore(conversationId);
      } else {
        void chat.loadMoreMessages(conversationId);
      }
    }
  }

  // Reset the divider whenever the conversation changes so a fresh open
  // always recomputes from the new conversation's read pointer.
  $effect(() => {
    const _cid = conversationId;
    untrack(() => {
      newDividerAfterMessageId = null;
    });
  });

  // Set the "New" divider position once the message list is ready (Bug C).
  // The divider is anchored at the last-read message at open time; it does
  // not move as new messages arrive while the user is reading.
  // conversationId is read outside untrack so this effect re-fires when the
  // conversation switches, allowing the reset effect above to clear the old
  // value before this one sets the new one.
  $effect(() => {
    const status = effectiveStatus;
    const msgs = effectiveMessages;
    // Read conversationId here (outside untrack) so this effect re-fires
    // when the user switches conversations.
    const _cid = conversationId;
    untrack(() => {
      if (status !== 'ready') return;
      // Only set once per conversation open (guard against re-fires from
      // incoming messages extending effectiveMessages while status stays ready).
      if (newDividerAfterMessageId !== null) return;
      const lastRead = conversation.myMembership?.lastReadMessageId;
      if (!lastRead) return;
      // Only show the divider when there is at least one unread message
      // after the read pointer.
      const lastReadIdx = msgs.findIndex((m) => m.id === lastRead);
      if (lastReadIdx !== -1 && lastReadIdx < msgs.length - 1) {
        newDividerAfterMessageId = lastRead;
      }
    });
  });

  // Clear the divider once markRead has advanced past the first unread
  // message — i.e. once the conversation's read pointer has moved beyond
  // where the divider is anchored.
  $effect(() => {
    const lastRead = conversation.myMembership?.lastReadMessageId;
    const divider = newDividerAfterMessageId;
    untrack(() => {
      if (!divider || !lastRead) return;
      const msgs = effectiveMessages;
      const dividerIdx = msgs.findIndex((m) => m.id === divider);
      const lastReadIdx = msgs.findIndex((m) => m.id === lastRead);
      // If the read pointer has advanced past the divider anchor, hide it.
      if (lastReadIdx > dividerIdx) {
        newDividerAfterMessageId = null;
      }
    });
  });

  // Focus-gated mark-read. Re-runs on three triggers:
  //   - focus enters this conversation's compose (chat.focusedConversationId)
  //   - a new message arrives in this conversation while focused
  //   - the conversation transitions from loading to ready while focused
  // The store's markRead is idempotent against the read pointer it
  // already has, so re-firing on every messages-array mutation is
  // cheap and avoids a debounce timer.
  $effect(() => {
    const msgs = effectiveMessages;
    const status = effectiveStatus;
    const focused = chat.focusedConversationId;
    untrack(() => {
      if (status !== 'ready' || msgs.length === 0) return;
      if (focused !== conversationId) return;
      const last = msgs[msgs.length - 1];
      if (!last) return;
      void chat.markRead(conversationId, last.id);
    });
  });

  function formatTime(isoDate: string): string {
    const d = new Date(isoDate);
    return d.toLocaleTimeString(undefined, {
      hour: '2-digit',
      minute: '2-digit',
    });
  }

  function formatDate(isoDate: string): string {
    const d = new Date(isoDate);
    const today = new Date();
    const yesterday = new Date(today);
    yesterday.setDate(yesterday.getDate() - 1);

    if (d.toDateString() === today.toDateString()) return 'Today';
    if (d.toDateString() === yesterday.toDateString()) return 'Yesterday';
    return d.toLocaleDateString(undefined, {
      month: 'short',
      day: 'numeric',
      year:
        d.getFullYear() !== today.getFullYear() ? 'numeric' : undefined,
    });
  }

  /**
   * Group messages by date for date-divider rendering. Each entry also
   * carries a `showTimestamp` flag computed from the gap to the
   * previous message in the same group: if the gap is at or below the
   * server-supplied threshold we suppress the timestamp under the
   * second message, matching the iMessage / Slack convention. The
   * threshold is configurable via system.toml chat
   * message_timestamp_grouping_seconds (default 120).
   */
  let groupingThresholdMs = $derived(chatTimestampGroupingSeconds() * 1000);
  let grouped = $derived.by(() => {
    const result: Array<{
      date: string;
      messages: Array<{ msg: Message; showTimestamp: boolean }>;
    }> = [];
    let currentDate = '';
    let prevMsAtDate = 0;
    const threshold = groupingThresholdMs;
    for (const msg of effectiveMessages) {
      const ms = new Date(msg.createdAt).getTime();
      const d = new Date(msg.createdAt).toDateString();
      let showTimestamp: boolean;
      if (d !== currentDate) {
        currentDate = d;
        showTimestamp = true; // first message in a date group: always show
        result.push({ date: msg.createdAt, messages: [{ msg, showTimestamp }] });
      } else {
        showTimestamp = threshold === 0 || ms - prevMsAtDate > threshold;
        result[result.length - 1]!.messages.push({ msg, showTimestamp });
      }
      prevMsAtDate = ms;
    }
    return result;
  });

  function isMine(senderPrincipalId: string): boolean {
    return senderPrincipalId === auth.principalId;
  }

  /**
   * Resolve a senderPrincipalId to a display name. The server populates
   * conversation.members[i].displayName for every participant, so any
   * sender — DM peer or Space contributor — can be labelled by name
   * without leaking the principal id (REQ-CHAT-15).
   */
  function senderName(senderPrincipalId: string): string {
    if (isMine(senderPrincipalId)) return 'You';
    const member = conversation.members.find(
      (m) => m.principalId === senderPrincipalId,
    );
    if (member?.displayName) return member.displayName;
    if (conversation.kind === 'dm') return conversation.name;
    return 'Member';
  }

  function handleToggleReaction(messageId: string, emoji: string): void {
    void chat.toggleReaction(messageId, emoji, auth.principalId ?? '');
  }

  function hideImage(ev: Event): void {
    if (ev.target instanceof HTMLImageElement) {
      ev.target.style.display = 'none';
    }
  }

  function handleAddReaction(messageId: string, emoji: string): void {
    showPickerFor = null;
    void chat.toggleReaction(messageId, emoji, auth.principalId ?? '');
  }

  // DM read receipt: find the other participant's lastReadMessageId.
  // The Conversation/get response only includes the requester's own
  // myMembership (other members' read pointers are suppressed in the
  // Members[] projection per REQ-CHAT-32 / 33), so this falls back to
  // the chat.memberships map populated by Membership/changes pushes.
  // For an unsynced overlay the indicator is simply absent.
  let otherReadThrough = $derived.by(() => {
    if (conversation.kind !== 'dm') return null;
    const mems = chat.memberships.get(conversationId) ?? [];
    const other = mems.find((m) => m.principalId !== auth.principalId);
    return other?.lastReadMessageId ?? null;
  });

  // Typing indicator text.
  // The typers set contains principalIds; we must not render them (REQ-CHAT-15).
  // For a DM the other participant is identified by conversation.name.
  // For Spaces, use the count only — we have no display name resolution yet.
  let typingText = $derived.by(() => {
    const typerCount = Array.from(chat.typing.get(conversationId) ?? []).filter(
      (id) => id !== auth.principalId,
    ).length;
    if (typerCount === 0) return null;
    if (conversation.kind === 'dm') return `${conversation.name} is typing…`;
    if (typerCount === 1) return 'Someone is typing…';
    return `${typerCount} people are typing…`;
  });
</script>

<div
  class="message-list"
  bind:this={scrollEl}
  onscroll={handleScroll}
  onload_capture={(ev) => { if (ev.target instanceof HTMLImageElement) handleContainerImgLoad(); }}
  onerror_capture={(ev) => { if (ev.target instanceof HTMLImageElement) handleContainerImgLoad(); }}
>
  {#if effectiveStatus === 'loading'}
    <p class="loading">Loading messages…</p>
  {:else if effectiveStatus === 'error'}
    <p class="error">Failed to load messages</p>
  {:else}
    {#if effectiveHasMore}
      <button
        type="button"
        class="load-more"
        onclick={() => {
          if (onLoadMore) {
            onLoadMore(conversationId);
          } else {
            void chat.loadMoreMessages(conversationId);
          }
        }}
      >
        Load earlier messages
      </button>
    {/if}

    {#each grouped as group (group.date)}
      <div class="date-divider" aria-label={formatDate(group.date)}>
        <span>{formatDate(group.date)}</span>
      </div>

      {#each group.messages as entry (entry.msg.id)}
        {@const msg = entry.msg}
        <div
          class="message"
          class:mine={isMine(msg.senderPrincipalId)}
          class:system={msg.type === 'system'}
          id="msg-{msg.id}"
        >
          {#if msg.type === 'system'}
            <p class="system-text">{msg.body.text}</p>
          {:else}
            <div class="bubble-row" class:mine={isMine(msg.senderPrincipalId)}>
              {#if !isMine(msg.senderPrincipalId)}
                <span class="avatar" aria-hidden="true">
                  {senderName(msg.senderPrincipalId).charAt(0).toUpperCase()}
                </span>
              {/if}

              <div class="bubble" class:mine={isMine(msg.senderPrincipalId)}>
                {#if !isMine(msg.senderPrincipalId)}
                  <span class="sender-name">{senderName(msg.senderPrincipalId)}</span>
                {/if}

                {#if msg.deleted}
                  <em class="deleted">message deleted</em>
                {:else}
                  <!-- eslint-disable-next-line svelte/no-at-html-tags -->
                  <!-- svelte-ignore a11y_click_events_have_key_events -->
                  <div class="body" role="presentation" onclick={handleBodyClick}>{@html msg.body.html}</div>

                  {#if msg.linkPreviews && msg.linkPreviews.length > 0}
                    <div class="link-previews">
                      {#each msg.linkPreviews as preview (preview.url)}
                        {@const href = preview.canonicalUrl ?? preview.url}
                        <a
                          class="link-preview-card"
                          {href}
                          target="_blank"
                          rel="noopener noreferrer"
                          aria-label={preview.title ?? preview.siteName ?? href}
                        >
                          <div class="link-preview-text">
                            {#if preview.title}
                              <span class="link-preview-title">{preview.title}</span>
                            {/if}
                            {#if preview.description}
                              <span class="link-preview-description">{preview.description}</span>
                            {/if}
                            {#if preview.siteName}
                              <span class="link-preview-site">{preview.siteName}</span>
                            {:else}
                              <span class="link-preview-site">{new URL(href).hostname}</span>
                            {/if}
                          </div>
                          {#if preview.imageUrl}
                            <img
                              class="link-preview-thumb"
                              src={preview.imageUrl}
                              alt={preview.title ?? ''}
                              onerror={hideImage}
                            />
                          {/if}
                        </a>
                      {/each}
                    </div>
                  {/if}
                {/if}

                <div class="meta">
                  {#if entry.showTimestamp}
                    <span class="time" title={new Date(msg.createdAt).toLocaleString()}>
                      {formatTime(msg.createdAt)}
                    </span>
                  {/if}
                  {#if msg.editedAt}
                    <span class="edited" title="Edited: {new Date(msg.editedAt).toLocaleString()}">(edited)</span>
                  {/if}
                  {#if conversation.kind === 'dm' && isMine(msg.senderPrincipalId) && otherReadThrough === msg.id}
                    <span class="read-receipt">Read</span>
                  {/if}
                </div>

                <!-- Reactions -->
                {#if Object.keys(msg.reactions).length > 0}
                  <div class="reactions" aria-label="Reactions">
                    {#each Object.entries(msg.reactions).filter(([, rs]) => rs.length > 0) as [emoji, reactors] (emoji)}
                      {@const myReaction = auth.principalId ? reactors.includes(auth.principalId) : false}
                      <button
                        type="button"
                        class="reaction-chip"
                        class:mine={myReaction}
                        title="{reactors.length} reaction{reactors.length === 1 ? '' : 's'}"
                        aria-label="{emoji} {reactors.length} reaction{reactors.length === 1 ? '' : 's'}{myReaction ? ' (click to remove)' : ''}"
                        onclick={() => handleToggleReaction(msg.id, emoji)}
                      >
                        <span class="chip-emoji" aria-hidden="true">{emoji}</span>
                        <span class="chip-count">{reactors.length}</span>
                      </button>
                    {/each}
                  </div>
                {/if}

                <!-- React button (hover) -->
                {#if !msg.deleted}
                  <div class="message-actions">
                    <button
                      type="button"
                      class="react-btn"
                      aria-label="Add reaction"
                      onclick={() => {
                        showPickerFor = showPickerFor === msg.id ? null : msg.id;
                      }}
                    >
                      +
                    </button>
                    {#if showPickerFor === msg.id}
                      <div class="picker-anchor">
                        <EmojiPicker
                          onSelect={(emoji) => handleAddReaction(msg.id, emoji)}
                          onClose={() => { showPickerFor = null; }}
                        />
                      </div>
                    {/if}
                  </div>
                {/if}
              </div>
            </div>
          {/if}
        </div>
        {#if newDividerAfterMessageId === msg.id}
          <div class="new-divider" role="separator" aria-label="New messages">
            <span class="new-divider-label">New</span>
          </div>
        {/if}
      {/each}
    {/each}

    {#if typingText}
      <p class="typing-indicator" aria-live="polite">{typingText}</p>
    {/if}
  {/if}
</div>

{#if lightboxSrc}
  <ImageLightbox
    src={lightboxSrc}
    onClose={() => { lightboxSrc = null; }}
  />
{/if}

<style>
  .message-list {
    flex: 1;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    padding: var(--spacing-04);
    gap: var(--spacing-01);
    min-height: 0;
  }

  .loading,
  .error {
    margin: auto;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    font-style: italic;
  }

  .error {
    color: var(--support-error);
  }

  .load-more {
    align-self: center;
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-04);
  }

  .load-more:hover {
    background: var(--layer-02);
  }

  .date-divider {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    margin: var(--spacing-04) 0;
  }

  .date-divider::before,
  .date-divider::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border-subtle-01);
  }

  .date-divider span {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    white-space: nowrap;
  }

  /* "New" unread divider — rendered between last-read and first-unread message */
  .new-divider {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    margin: var(--spacing-03) 0;
  }

  .new-divider::before,
  .new-divider::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--support-error);
    opacity: 0.5;
  }

  .new-divider-label {
    font-size: var(--type-helper-text-01-size);
    font-weight: 600;
    color: var(--support-error);
    white-space: nowrap;
    padding: 0 var(--spacing-02);
    border: 1px solid var(--support-error);
    border-radius: var(--radius-pill);
    line-height: 1.6;
    opacity: 0.8;
  }

  .message {
    display: flex;
    flex-direction: column;
  }

  .system {
    align-items: center;
  }

  .system-text {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    font-style: italic;
    margin: var(--spacing-02) 0;
    text-align: center;
  }

  .bubble-row {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-02);
    margin-bottom: var(--spacing-02);
  }

  .bubble-row.mine {
    flex-direction: row-reverse;
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

  .bubble {
    max-width: 75%;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    position: relative;
  }

  .bubble:hover .message-actions {
    opacity: 1;
  }

  .sender-name {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    font-weight: 600;
    padding: 0 var(--spacing-03);
  }

  .body {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-03) var(--spacing-04);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    word-break: break-word;
  }

  .bubble.mine .body {
    background: color-mix(in srgb, var(--interactive) 18%, transparent);
    border-color: var(--interactive);
  }

  .body :global(img) {
    max-width: 100%;
    max-height: 320px;
    height: auto;
    width: auto;
    border-radius: var(--radius-sm);
    display: block;
    margin: var(--spacing-02) 0;
    cursor: zoom-in;
    background: var(--layer-02);
  }

  .body :global(p) {
    margin: 0;
  }

  .body :global(p + p) {
    margin-top: var(--spacing-02);
  }

  .body :global(code) {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    background: var(--layer-02);
    padding: 0 var(--spacing-01);
    border-radius: var(--radius-sm);
  }

  .body :global(pre) {
    background: var(--layer-02);
    padding: var(--spacing-03);
    border-radius: var(--radius-sm);
    overflow-x: auto;
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    margin: var(--spacing-02) 0;
  }

  .deleted {
    color: var(--text-helper);
    padding: var(--spacing-03) var(--spacing-04);
  }

  .meta {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: 0 var(--spacing-03);
  }

  .time {
    font-size: 11px;
    color: var(--text-helper);
  }

  .edited {
    font-size: 11px;
    color: var(--text-helper);
    font-style: italic;
  }

  .read-receipt {
    font-size: 11px;
    color: var(--interactive);
  }

  .reactions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-01);
    padding: 0 var(--spacing-02);
  }

  .reaction-chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-01);
    padding: var(--spacing-01) var(--spacing-02);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    line-height: 1;
    min-height: 24px;
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .reaction-chip:hover {
    background: var(--layer-02);
    border-color: var(--border-strong-01);
  }

  .reaction-chip.mine {
    background: color-mix(in srgb, var(--interactive) 18%, transparent);
    border-color: var(--interactive);
    color: var(--interactive);
  }

  .reaction-chip.mine:hover {
    background: color-mix(in srgb, var(--interactive) 28%, transparent);
  }

  .chip-emoji {
    font-size: 14px;
    line-height: 1;
  }

  .chip-count {
    font-variant-numeric: tabular-nums;
    font-size: 12px;
  }

  .message-actions {
    position: absolute;
    top: -12px;
    right: var(--spacing-02);
    opacity: 0;
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
    display: flex;
    gap: var(--spacing-01);
  }

  .bubble.mine .message-actions {
    right: auto;
    left: var(--spacing-02);
  }

  .react-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: var(--radius-pill);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    color: var(--text-helper);
    font-size: 14px;
    line-height: 1;
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .react-btn:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .picker-anchor {
    position: absolute;
    top: 28px;
    right: 0;
    z-index: 100;
  }

  .typing-indicator {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    font-style: italic;
    padding: var(--spacing-02) var(--spacing-04);
    margin: 0;
    min-height: 24px;
  }

  /* Link preview cards */
  .link-previews {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    margin-top: var(--spacing-02);
  }

  .link-preview-card {
    display: flex;
    flex-direction: row;
    align-items: stretch;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-left: 3px solid var(--interactive);
    border-radius: var(--radius-md);
    overflow: hidden;
    text-decoration: none;
    color: inherit;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
                border-color var(--duration-fast-02) var(--easing-productive-enter);
    max-width: 100%;
  }

  .link-preview-card:hover {
    background: var(--layer-02);
    border-left-color: color-mix(in srgb, var(--interactive) 70%, var(--text-primary));
  }

  .link-preview-text {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    padding: var(--spacing-03) var(--spacing-04);
    min-width: 0;
  }

  .link-preview-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    line-height: var(--type-body-compact-01-line);
  }

  .link-preview-description {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-secondary);
    line-height: var(--type-helper-text-01-line);
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .link-preview-site {
    font-size: 11px;
    color: var(--text-helper);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .link-preview-thumb {
    width: 80px;
    height: 80px;
    object-fit: cover;
    flex-shrink: 0;
    background: var(--layer-02);
  }
</style>
