<script lang="ts">
  import { router } from '../lib/router/router.svelte';
  import { mail } from '../lib/mail/store.svelte';
  import { keyboard } from '../lib/keyboard/engine.svelte';
  import { compose } from '../lib/compose/compose.svelte';
  import ThreadReader from '../lib/mail/ThreadReader.svelte';
  import type { Email } from '../lib/mail/types';

  let threadId = $derived(router.parts[1] === 'thread' ? router.parts[2] : undefined);
  let label = $derived(router.parts[1] === 'label' ? router.parts[2] : undefined);
  let searchQuery = $derived(
    router.parts[1] === 'search' ? decodeURIComponent(router.parts[2] ?? '') : undefined,
  );
  let isInboxRoute = $derived(router.matches('mail') && !router.parts[1]);
  let isSearchRoute = $derived(router.matches('mail', 'search'));

  // Kick off the inbox load when the inbox route is shown.
  $effect(() => {
    if (isInboxRoute) {
      void mail.loadInbox();
    }
  });

  // Run the search whenever the search route's query changes.
  $effect(() => {
    if (isSearchRoute && searchQuery !== undefined) {
      void mail.runSearch(searchQuery);
    }
  });

  // Search-view keyboard layer.
  $effect(() => {
    if (!isSearchRoute) return;

    const focusedSearchId = (): string | null => {
      const idx = mail.searchFocusedIndex;
      if (idx < 0) return null;
      return mail.searchEmailIds[idx] ?? null;
    };

    const pop = keyboard.pushLayer([
      {
        key: 'j',
        description: 'Next result',
        action: () => mail.focusSearchNext(),
      },
      {
        key: 'k',
        description: 'Previous result',
        action: () => mail.focusSearchPrev(),
      },
      {
        key: 'Enter',
        description: 'Open focused thread',
        action: () => {
          const tid = mail.focusedSearchThreadId();
          if (tid) router.navigate(`/mail/thread/${encodeURIComponent(tid)}`);
        },
      },
      {
        key: 'Escape',
        description: 'Clear search',
        action: () => router.navigate('/mail'),
      },
      {
        key: 'e',
        description: 'Archive',
        action: () => {
          const id = focusedSearchId();
          if (id) void mail.archiveEmail(id);
        },
      },
      {
        key: 's',
        description: 'Star / unstar',
        action: () => {
          const id = focusedSearchId();
          if (id) void mail.toggleFlagged(id);
        },
      },
    ]);
    return pop;
  });

  // Inbox-view keyboard bindings are pushed only while the inbox is showing.
  $effect(() => {
    if (!isInboxRoute) return;

    const focusedEmailId = (): string | null => {
      const idx = mail.inboxFocusedIndex;
      if (idx < 0) return null;
      return mail.inboxEmailIds[idx] ?? null;
    };

    const pop = keyboard.pushLayer([
      {
        key: 'j',
        description: 'Next thread',
        action: () => mail.focusInboxNext(),
      },
      {
        key: 'k',
        description: 'Previous thread',
        action: () => mail.focusInboxPrev(),
      },
      {
        key: 'Enter',
        description: 'Open focused thread',
        action: () => {
          const tid = mail.focusedInboxThreadId();
          if (tid) router.navigate(`/mail/thread/${encodeURIComponent(tid)}`);
        },
      },
      {
        key: 'Escape',
        description: 'Clear focus',
        action: () => {
          mail.inboxFocusedIndex = -1;
        },
      },
      {
        key: 'e',
        description: 'Archive',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.archiveEmail(id);
        },
      },
      {
        key: '#',
        description: 'Delete',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.deleteEmail(id);
        },
      },
      {
        key: 's',
        description: 'Star / unstar',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.toggleFlagged(id);
        },
      },
      {
        key: 'I',
        description: 'Mark as read',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.setSeen(id, true);
        },
      },
      {
        key: 'U',
        description: 'Mark as unread',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.setSeen(id, false);
        },
      },
    ]);
    return pop;
  });

  // Thread-view bindings.
  $effect(() => {
    if (!threadId) return;
    const tid = threadId;
    const replyTarget = (): Email | null => {
      const emails = mail.threadEmails(tid);
      return emails[emails.length - 1] ?? null;
    };
    const pop = keyboard.pushLayer([
      {
        key: 'Escape',
        description: 'Back to inbox',
        action: () => router.navigate('/mail'),
      },
      {
        key: 'r',
        description: 'Reply',
        action: () => {
          const e = replyTarget();
          if (e) compose.openReply(e);
        },
      },
      {
        key: 'f',
        description: 'Forward',
        action: () => {
          const e = replyTarget();
          if (e) compose.openForward(e);
        },
      },
      {
        key: 'u',
        description: 'Back to inbox',
        action: () => router.navigate('/mail'),
      },
    ]);
    return pop;
  });

  // Scroll the focused row into view whenever the index changes.
  let listEl = $state<HTMLUListElement | null>(null);
  $effect(() => {
    if (!isInboxRoute) return;
    const idx = mail.inboxFocusedIndex;
    if (idx < 0 || !listEl) return;
    const row = listEl.querySelector<HTMLElement>(`[data-row-index="${idx}"]`);
    row?.scrollIntoView({ block: 'nearest' });
  });

  function senderLabel(email: Email): string {
    const a = email.from?.[0];
    if (!a) return '(no sender)';
    return a.name?.trim() || a.email;
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    const now = new Date();
    const sameYear = d.getFullYear() === now.getFullYear();
    const opts: Intl.DateTimeFormatOptions = sameYear
      ? { month: 'short', day: 'numeric' }
      : { month: 'short', day: 'numeric', year: 'numeric' };
    return d.toLocaleDateString(undefined, opts);
  }

  function isUnread(email: Email): boolean {
    return !email.keywords.$seen;
  }

  function isFlagged(email: Email): boolean {
    return Boolean(email.keywords.$flagged);
  }

  function openThread(email: Email): void {
    router.navigate(`/mail/thread/${encodeURIComponent(email.threadId)}`);
  }
</script>

<div class="mail">
  {#if threadId}
    <div class="thread-frame">
      <header class="thread-frame-bar">
        <button type="button" class="back" onclick={() => router.navigate('/mail')}>
          ← Back to inbox
        </button>
      </header>
      <ThreadReader {threadId} />
    </div>
  {:else if isSearchRoute}
    <header class="list-header">
      <h1>
        Search: <span class="query-echo">{searchQuery || '(empty)'}</span>
      </h1>
      <button type="button" class="back" onclick={() => router.navigate('/mail')}>
        ← Back to inbox
      </button>
    </header>

    {#if mail.searchLoadStatus === 'idle' || mail.searchLoadStatus === 'loading'}
      <div class="state">Searching…</div>
    {:else if mail.searchLoadStatus === 'error'}
      <div class="state error">
        <p>Search failed.</p>
        {#if mail.searchError}<p class="detail">{mail.searchError}</p>{/if}
        <button type="button" onclick={() => mail.runSearch(mail.searchQuery)}>
          Retry
        </button>
      </div>
    {:else if mail.searchEmails.length === 0}
      <div class="state">No matches.</div>
    {:else}
      <ul class="thread-list" role="listbox" aria-label="Search results">
        {#each mail.searchEmails as email, i (email.id)}
          <li
            class="thread-row"
            class:unread={isUnread(email)}
            class:focused={mail.searchFocusedIndex === i}
            data-row-index={i}
          >
            <button
              type="button"
              role="option"
              aria-selected={mail.searchFocusedIndex === i}
              onclick={() => {
                mail.searchFocusedIndex = i;
                openThread(email);
              }}
            >
              <span class="star" class:flagged={isFlagged(email)} aria-hidden="true">★</span>
              <span class="from">{senderLabel(email)}</span>
              <span class="subject-and-preview">
                <span class="subject">{email.subject || '(no subject)'}</span>
                <span class="preview"> — {email.preview}</span>
              </span>
              {#if email.hasAttachment}
                <span class="attachment" aria-label="Has attachment">📎</span>
              {/if}
              <span class="date">{formatDate(email.receivedAt)}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  {:else if label}
    <header>
      <h1>Label: {label}</h1>
      <p class="lead">Label-view querying arrives after inbox.</p>
    </header>
  {:else}
    <header class="list-header">
      <h1>Inbox</h1>
      <button
        type="button"
        class="refresh"
        aria-label="Refresh"
        onclick={() => mail.refreshInbox()}
        disabled={mail.inboxLoadStatus === 'loading'}
      >
        ↻
      </button>
    </header>

    {#if mail.inboxLoadStatus === 'idle' || mail.inboxLoadStatus === 'loading'}
      <div class="state">Loading…</div>
    {:else if mail.inboxLoadStatus === 'error'}
      <div class="state error">
        <p>Couldn't load inbox.</p>
        {#if mail.inboxError}<p class="detail">{mail.inboxError}</p>{/if}
        <button type="button" onclick={() => mail.loadInbox()}>Retry</button>
      </div>
    {:else if mail.inboxEmails.length === 0}
      <div class="state">Inbox is empty.</div>
    {:else}
      <ul
        class="thread-list"
        role="listbox"
        aria-label="Inbox threads"
        bind:this={listEl}
      >
        {#each mail.inboxEmails as email, i (email.id)}
          <li
            class="thread-row"
            class:unread={isUnread(email)}
            class:focused={mail.inboxFocusedIndex === i}
            data-row-index={i}
          >
            <button
              type="button"
              role="option"
              aria-selected={mail.inboxFocusedIndex === i}
              onclick={() => {
                mail.inboxFocusedIndex = i;
                openThread(email);
              }}
            >
              <span class="star" class:flagged={isFlagged(email)} aria-hidden="true">★</span>
              <span class="from">{senderLabel(email)}</span>
              <span class="subject-and-preview">
                <span class="subject">{email.subject || '(no subject)'}</span>
                <span class="preview"> — {email.preview}</span>
              </span>
              {#if email.hasAttachment}
                <span class="attachment" aria-label="Has attachment">📎</span>
              {/if}
              <span class="date">{formatDate(email.receivedAt)}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .mail {
    height: 100%;
    overflow: auto;
    background: var(--background);
  }

  .thread-frame {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .thread-frame-bar {
    flex: 0 0 auto;
    padding: var(--spacing-03) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  .thread-frame :global(.thread-reader) {
    flex: 1;
    min-height: 0;
  }

  header {
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  .list-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }
  h1 {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    margin: 0;
    flex: 1;
  }
  .lead {
    margin: var(--spacing-02) 0 0;
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .query-echo {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    color: var(--text-primary);
    background: var(--layer-02);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    font-weight: 500;
  }

  .refresh,
  .back {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .refresh {
    width: 36px;
    padding: var(--spacing-02);
    text-align: center;
    font-size: 16px;
  }
  .refresh:hover:not(:disabled),
  .back:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .refresh:disabled {
    opacity: 0.4;
    cursor: progress;
  }

  .state {
    padding: var(--spacing-07) var(--spacing-05);
    text-align: center;
    color: var(--text-secondary);
  }
  .state.error {
    color: var(--support-error);
  }
  .state .detail {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-helper);
    margin: var(--spacing-03) 0;
  }
  .state button {
    margin-top: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
  }

  .thread-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .thread-row {
    border-bottom: 1px solid var(--border-subtle-01);
    border-left: 3px solid transparent;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .thread-row.focused {
    border-left-color: var(--interactive);
    background: var(--layer-01);
  }
  .thread-row button {
    display: grid;
    grid-template-columns: 24px 14ch 1fr auto auto;
    gap: var(--spacing-04);
    align-items: center;
    width: 100%;
    padding: var(--spacing-03) var(--spacing-05);
    color: var(--text-secondary);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .thread-row button:hover {
    background: var(--layer-01);
  }
  .thread-row.unread button {
    color: var(--text-primary);
  }

  .star {
    color: var(--text-helper);
    font-size: 16px;
    line-height: 1;
  }
  .star.flagged {
    color: var(--support-warning);
  }

  .from {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .thread-row.unread .from {
    font-weight: 600;
  }

  .subject-and-preview {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .subject {
    color: var(--text-primary);
  }
  .thread-row.unread .subject {
    font-weight: 600;
  }
  .preview {
    color: var(--text-secondary);
  }

  .attachment {
    color: var(--text-helper);
  }
  .date {
    color: var(--text-helper);
    font-variant-numeric: tabular-nums;
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }
  .thread-row.unread .date {
    color: var(--text-secondary);
    font-weight: 600;
  }
</style>
